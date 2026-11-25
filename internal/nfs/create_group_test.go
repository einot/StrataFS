package nfs

// Clean-room wire test for #98, "CREATE, MKDIR and SYMLINK apply a requested
// group from any caller", written without reading the implementation, from
// ADR 0028
// (docs/adr/0028-a-group-asked-for-at-creation-is-applied-only-for-root-or-a-member.md)
// section 1 ("What a client can observe"), section 4, and section 8 (G4, and
// the encodings of CREATE, MKDIR, SYMLINK, GETATTR, sattr3, fattr3 and an
// AUTH_SYS credential as internal/nfs reads and writes them at 344a95b).
//
// Covered, with the name ADR 0028 section 8 pins: G4,
// TestNFSAGroupAskedForAtCreationIsAppliedOnlyForRootOrAMember.
//
// It drives the server over a real TCP socket with startServer, as
// sticky_test.go does, and reuses startServer, dial, mountRoot, rpcClient and
// its readRecord, skipWccData (integration_test.go); stkMkdir and stkClient
// (sticky_test.go); and nmfsWantEnd and nmfsOK (max_file_size_test.go). Of
// unexported non-test names it uses the procedure numbers procGetAttr,
// procCreate, procMkdir and procSymlink, createUnchecked and createGuarded,
// and dontChange. rpcClient.call sends one gid in its credential's list, the
// client's own, so cgClient.call frames a call as rpcClient.call does but
// with a gid list of the test's choosing. Helpers of this file's own are
// prefixed cg. It builds on the tree of 344a95b alone, so that the session can
// take its red run there.
//
// startServer's root directory is owned by 501:20, which is what dial's client
// sends, and every bucket is under the test's t.TempDir(). Nothing here
// sleeps, polls or runs in parallel; each client is its own connection.

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"strata/internal/xdr"
)

// The ftype3 values G4 expects (ADR 0028 section 8).
const cgReg uint32 = 1

const cgDir uint32 = 2

const cgLnk uint32 = 5

// cgClient is one of G4's clients: an rpcClient, whose uid and gid its
// credential carries, with gids, the list of gids it carries.
type cgClient struct {
	rc   *rpcClient
	gids []uint32
	name string
}

// cgDial dials addr as a client that sends uid, gid and the list gids.
func cgDial(t *testing.T, addr string, uid, gid uint32, gids []uint32, name string) *cgClient {
	t.Helper()
	return &cgClient{rc: stkClient(t, addr, uid, gid), gids: gids, name: name}
}

// call sends one NFS call, proc, framed as rpcClient.call frames it, but with
// an AUTH_SYS credential whose list of gids is c.gids: a stamp, the machine's
// name, the uid and the gid, and the gids, a count and that many uint32
// (ADR 0028 section 8). It returns a reader positioned at the reply body.
func (c *cgClient) call(proc uint32, args func(*xdr.Writer)) *xdr.Reader {
	rc := c.rc
	rc.t.Helper()
	rc.xid++

	w := xdr.NewWriter()
	w.Uint32(rc.xid)
	w.Uint32(0) // CALL
	w.Uint32(2) // RPC version
	w.Uint32(ProgramNFS)
	w.Uint32(VersionNFS)
	w.Uint32(proc)

	cred := xdr.NewWriter()
	cred.Uint32(0) // stamp
	cred.String("test-client")
	cred.Uint32(rc.uid)
	cred.Uint32(rc.gid)
	cred.Uint32(uint32(len(c.gids)))
	for _, g := range c.gids {
		cred.Uint32(g)
	}
	w.Uint32(1) // AUTH_SYS
	w.Opaque(cred.Bytes())

	w.Uint32(0) // verifier flavor AUTH_NONE
	w.Uint32(0) // verifier length

	if args != nil {
		args(w)
	}

	body := w.Bytes()
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body))|0x80000000)
	rc.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := rc.conn.Write(append(hdr[:], body...)); err != nil {
		rc.t.Fatalf("write call: %v", err)
	}

	r := xdr.NewReader(rc.readRecord())
	if got := r.Uint32(); got != rc.xid {
		rc.t.Fatalf("reply xid = %d, want %d", got, rc.xid)
	}
	if got := r.Uint32(); got != 1 {
		rc.t.Fatalf("reply mtype = %d, want REPLY", got)
	}
	if got := r.Uint32(); got != 0 {
		rc.t.Fatalf("reply rejected, reply_stat = %d", got)
	}
	r.Uint32() // verifier flavor
	r.Opaque() // verifier body
	if got := r.Uint32(); got != 0 {
		rc.t.Fatalf("accept_stat = %d (not SUCCESS) for NFS proc %d", got, proc)
	}
	return r
}

// cgAttr is what G4 checks of an fattr3: its type, mode, uid and gid.
type cgAttr struct {
	typ, mode, uid, gid uint32
}

func (a cgAttr) String() string {
	return fmt.Sprintf("{type %d, mode %#o, uid %d, gid %d}", a.typ, a.mode, a.uid, a.gid)
}

// cgFattr decodes a whole fattr3: the type, the mode, the link count, the uid
// and the gid, a uint32 each; the size and the space used, a uint64 each; two
// uint32 of rdev; the fsid and the fileid, a uint64 each; and three times of
// two uint32 each (ADR 0028 section 8).
func cgFattr(r *xdr.Reader) cgAttr {
	var a cgAttr
	a.typ = r.Uint32()
	a.mode = r.Uint32()
	r.Uint32() // nlink
	a.uid = r.Uint32()
	a.gid = r.Uint32()
	r.Uint64() // size
	r.Uint64() // used
	r.Uint32() // rdev
	r.Uint32()
	r.Uint64() // fsid
	r.Uint64() // fileid
	for i := 0; i < 6; i++ {
		r.Uint32() // atime, mtime and ctime, seconds and nanoseconds each
	}
	return a
}

// cgSattr encodes a sattr3 that sets the mode, unless mode is negative, and
// the gid, and nothing else: set_mode3, then set_uid3 FALSE, set_gid3 TRUE
// with gid, set_size3 FALSE, and both times DONT_CHANGE (ADR 0028 section 8).
func cgSattr(w *xdr.Writer, mode int64, gid uint32) {
	if mode >= 0 {
		w.Bool(true)
		w.Uint32(uint32(mode))
	} else {
		w.Bool(false)
	}
	w.Bool(false)
	w.Bool(true)
	w.Uint32(gid)
	w.Bool(false)
	w.Uint32(dontChange)
	w.Uint32(dontChange)
}

// cgReply is what cgMake decodes from a CREATE, MKDIR or SYMLINK reply.
type cgReply struct {
	status  uint32
	fh      []byte
	hasFH   bool
	hasAttr bool
	attr    cgAttr
}

// cgMake sends a CREATE (proc procCreate, with createmode3 how), a MKDIR or a
// SYMLINK (to target) of name in dir, from c, whose sattr3 sets mode, unless
// it is negative, and gid; and decodes the reply to its end: the status, then
// for NFS3_OK a post_op_fh3, a post_op_attr and one wcc_data, and for any
// other status one wcc_data, and nothing after (ADR 0028 section 8).
func cgMake(t *testing.T, c *cgClient, proc, how uint32, dir []byte, name, target string, mode int64, gid uint32,
	what string) cgReply {
	t.Helper()
	r := c.call(proc, func(w *xdr.Writer) {
		w.Opaque(dir)
		w.String(name)
		if proc == procCreate {
			w.Uint32(how)
		}
		cgSattr(w, mode, gid)
		if proc == procSymlink {
			w.String(target)
		}
	})
	var rep cgReply
	rep.status = r.Uint32()
	if rep.status == nmfsOK {
		if r.Bool() {
			rep.hasFH = true
			rep.fh = r.Opaque()
		}
		if r.Bool() {
			rep.hasAttr = true
			rep.attr = cgFattr(r)
		}
	}
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to %s, status %d, which is a post_op_fh3, a post_op_attr and one "+
		"wcc_data for NFS3_OK and one wcc_data otherwise (ADR 0028 section 8)", what, rep.status))
	return rep
}

// cgGetAttr sends a GETATTR of fh from c, decodes its reply to its end, the
// status and for NFS3_OK a fattr3, and returns the status and what the
// fattr3 reports.
func cgGetAttr(t *testing.T, c *rpcClient, fh []byte, what string) (uint32, cgAttr) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procGetAttr, func(w *xdr.Writer) { w.Opaque(fh) })
	st := r.Uint32()
	var a cgAttr
	if st == nmfsOK {
		a = cgFattr(r)
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a GETATTR of %s, status %d, which is a fattr3 for NFS3_OK (ADR 0028 "+
		"section 8)", what, st))
	return st, a
}

// TestNFSAGroupAskedForAtCreationIsAppliedOnlyForRootOrAMember is G4 of ADR
// 0028 section 8 (section 1, "What a client can observe"; section 4). The
// owner, 501:20, makes pub in the root with a MKDIR of mode 0o777. Then each
// of these answers NFS3_OK with a handle and attributes in the reply that
// report the type, the mode, the uid and the gid given, as a GETATTR of the
// handle then does too, and each reply ends where its form ends.
//
// From a client sending 502:30: a GUARDED CREATE of pub/a with mode 0o2775
// and gid 20 gives 502:30 and mode 0o2775; a MKDIR of pub/da with mode 0o755
// and gid 20 gives 502:30; a SYMLINK of pub/la to a with gid 20 gives 502:30;
// and an UNCHECKED CREATE of pub/b with mode 0o644 and gid 30 gives 502:30.
// From a client sending uid 504, gid 30 and the gids 40 and 41: a GUARDED
// CREATE of pub/m with mode 0o644 and gid 41 gives 504:41, and one of pub/m2
// with gid 20 gives 504:30. From a client sending 0:0: a GUARDED CREATE of
// pub/r with mode 0o644 and gid 20 gives 0:20. And from the client sending
// 502:30, an UNCHECKED CREATE of pub/r, which exists, with gid 30, still
// reports 0:20.
func TestNFSAGroupAskedForAtCreationIsAppliedOnlyForRootOrAMember(t *testing.T) {
	addr, _, _ := startServer(t)
	owner := dial(t, addr)
	root := mountRoot(t, owner)

	st, pub := stkMkdir(t, owner, root, "pub", 0o777)
	if st != nmfsOK || pub == nil {
		t.Fatalf("setup: the owner's MKDIR of pub in the root with mode 0o777 = status %d, handle %v, want NFS3_OK "+
			"with a handle", st, pub != nil)
	}
	if st, a := cgGetAttr(t, owner, pub, "pub"); st != nmfsOK || a != (cgAttr{cgDir, 0o777, 501, 20}) {
		t.Fatalf("setup: GETATTR of pub = status %d, %s, want NFS3_OK, %s: a mode given to MKDIR is stored, and "+
			"the directory is owned by its caller (ADR 0028 section 8, the facts)", st, a,
			cgAttr{cgDir, 0o777, 501, 20})
	}

	other := cgDial(t, addr, 502, 30, []uint32{30}, "the client sending 502:30")
	multi := cgDial(t, addr, 504, 30, []uint32{40, 41}, "the client sending uid 504, gid 30 and the gids 40 and 41")
	rootC := cgDial(t, addr, 0, 0, []uint32{0}, "the client sending 0:0")

	const outside = "the caller is neither root nor in the group asked for, so the group is left unapplied and the " +
		"call answers NFS3_OK, the new object's attributes reporting the caller's gid (ADR 0028 sections 1, 4 and 5)"
	const member = "the caller is in the group asked for, so it is applied (ADR 0028 section 1)"
	rows := []struct {
		c      *cgClient
		proc   uint32
		how    uint32
		name   string
		target string
		mode   int64
		gid    uint32
		want   cgAttr
		why    string
	}{
		{other, procCreate, createGuarded, "a", "", 0o2775, 20, cgAttr{cgReg, 0o2775, 502, 30},
			outside + "; the mode asked for keeps its set-group-ID bit (section 6). This is the issue's case"},
		{other, procMkdir, 0, "da", "", 0o755, 20, cgAttr{cgDir, 0o755, 502, 30}, outside},
		{other, procSymlink, 0, "la", "a", -1, 20, cgAttr{cgLnk, 0o777, 502, 30},
			outside + "; a symlink made with no mode has 0o777 (section 8, the facts)"},
		{other, procCreate, createUnchecked, "b", "", 0o644, 30, cgAttr{cgReg, 0o644, 502, 30},
			"a caller is always in its own gid (ADR 0028 sections 1 and 5)"},
		{multi, procCreate, createGuarded, "m", "", 0o644, 41, cgAttr{cgReg, 0o644, 504, 41},
			member + ": 41 is among the gids of the client's credential"},
		{multi, procCreate, createGuarded, "m2", "", 0o644, 20, cgAttr{cgReg, 0o644, 504, 30},
			outside + "; its gid, 30, not one of its gids"},
		{rootC, procCreate, createGuarded, "r", "", 0o644, 20, cgAttr{cgReg, 0o644, 0, 20},
			"root may give a new object any group (ADR 0028 section 1)"},
		{other, procCreate, createUnchecked, "r", "", -1, 30, cgAttr{cgReg, 0o644, 0, 20},
			"an UNCHECKED CREATE of a name that exists makes nothing and applies none of its sattr3, so the file " +
				"keeps root's owner and group 20 (ADR 0028 sections 2 and 7)"},
	}
	procs := map[uint32]string{procCreate: "CREATE", procMkdir: "MKDIR", procSymlink: "SYMLINK"}
	for _, r := range rows {
		call := procs[r.proc]
		switch {
		case r.proc == procCreate && r.how == createGuarded:
			call = "GUARDED CREATE"
		case r.proc == procCreate:
			call = "UNCHECKED CREATE"
		}
		what := fmt.Sprintf("%s's %s of pub/%s", r.c.name, call, r.name)
		if r.target != "" {
			what += " to " + r.target
		}
		if r.mode >= 0 {
			what += fmt.Sprintf(" with mode %#o", r.mode)
		}
		what += fmt.Sprintf(" and gid %d", r.gid)

		rep := cgMake(t, r.c, r.proc, r.how, pub, r.name, r.target, r.mode, r.gid, what)
		if rep.status != nmfsOK {
			t.Errorf("%s = status %d, want NFS3_OK: a group the caller may not set is left unapplied, never "+
				"refused, and pub's mode 0o777 gives every caller write and search (ADR 0028 sections 1 and 4)",
				what, rep.status)
			continue
		}
		if !rep.hasFH || len(rep.fh) == 0 || !rep.hasAttr {
			t.Errorf("%s answered NFS3_OK with a handle %v (%d bytes) and attributes %v, want both present: %s",
				what, rep.hasFH, len(rep.fh), rep.hasAttr, r.why)
		}
		if rep.hasAttr && rep.attr != r.want {
			t.Errorf("%s answered NFS3_OK with attributes %s, want %s: %s", what, rep.attr, r.want, r.why)
		}
		if !rep.hasFH || len(rep.fh) == 0 {
			continue
		}
		if st, a := cgGetAttr(t, owner, rep.fh, "pub/"+r.name); st != nmfsOK || a != r.want {
			t.Errorf("after %s, GETATTR of the handle it returned = status %d, %s, want NFS3_OK, %s: %s", what, st, a,
				r.want, r.why)
		}
	}
}
