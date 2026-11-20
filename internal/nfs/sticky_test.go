package nfs

// Clean-room wire test for #94, "The sticky bit on a directory is not
// honoured", written without reading the implementation, from ADR 0026
// (docs/adr/0026-in-a-sticky-directory-only-an-owner-removes-or-renames-an-entry.md)
// §1 ("What a client can observe"), §3 (the refusal is vfs.ErrPerm, which
// statusOf passes to the client as NFS3ERR_PERM, 1) and §8 (K10, and the
// encodings of MKDIR, REMOVE, RMDIR and RENAME as internal/nfs writes and
// reads them at 273f010), and from ADR 0007 §5 (sattr3, fattr3, wcc_data,
// post_op_fh3, and the end of a reply).
//
// Covered, with the name ADR 0026 §8 pins: K10,
// TestNFSAStickyDirectoryRefusesWithNFS3ERRPERM (#94's second done-when, "The
// error code is decided").
//
// It drives the server over a real TCP socket with startServer, as
// owner_access_test.go and max_file_size_test.go do, and reuses startServer,
// dial, mountRoot, rpcClient and its uid and gid fields, nfsCreate,
// skipWccData and skipPostOpAttr (integration_test.go); nmfsLookup,
// nmfsWantEnd, nmfsOK and nmfsNoEnt (max_file_size_test.go); and oaGetAttr
// (owner_access_test.go). Of unexported non-test names it uses the procedure
// numbers procMkdir, procRemove, procRmdir and procRename, and dontChange.
// Helpers and constants of this file's own are prefixed stk. It builds on the
// tree of 273f010 alone, so that the session can take its red run there.
//
// startServer's root directory is owned by 501:20, which is what dial's client
// sends, and every bucket is under the test's t.TempDir(). No test here
// sleeps, polls or runs in parallel; each client is its own connection, and a
// client sends the uid and gid its fields hold.

import (
	"fmt"
	"testing"

	"strata/internal/xdr"
)

// The nfsstat3 values this test expects beyond nmfsOK and nmfsNoEnt (RFC 1813
// §2.6, as ADR 0026 §8 gives them).
const stkPerm uint32 = 1

const stkAcces uint32 = 13

// stkMkdir sends a MKDIR of name in dir whose sattr3 sets the mode alone, and
// returns its status and, for NFS3_OK, the new directory's handle. The
// sattr3 is set_mode3 TRUE with mode, set_uid3, set_gid3 and set_size3 FALSE,
// and both times DONT_CHANGE. The reply is the status, then for NFS3_OK a
// post_op_fh3, a post_op_attr and one wcc_data, and for any other status one
// wcc_data; it must end there (ADR 0026 §8; ADR 0007 §5).
func stkMkdir(t *testing.T, c *rpcClient, dir []byte, name string, mode uint32) (uint32, []byte) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procMkdir, func(w *xdr.Writer) {
		w.Opaque(dir)
		w.String(name)
		w.Bool(true)
		w.Uint32(mode)
		w.Bool(false)
		w.Bool(false)
		w.Bool(false)
		w.Uint32(dontChange)
		w.Uint32(dontChange)
	})
	st := r.Uint32()
	var fh []byte
	if st == nmfsOK {
		if r.Bool() {
			fh = r.Opaque()
		}
		skipPostOpAttr(r)
	}
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a MKDIR of %s, status %d, which is a post_op_fh3, a post_op_attr "+
		"and one wcc_data for NFS3_OK and one wcc_data otherwise (ADR 0026 §8)", name, st))
	return st, fh
}

// stkUnlink sends a REMOVE or an RMDIR, proc, of name in dir, and returns its
// status. The reply is the status and one wcc_data, whatever the status, and
// must end there (ADR 0026 §8; ADR 0007 §5).
func stkUnlink(t *testing.T, c *rpcClient, proc uint32, dir []byte, name, what string) uint32 {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, proc, func(w *xdr.Writer) {
		w.Opaque(dir)
		w.String(name)
	})
	st := r.Uint32()
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to %s, status %d, which is the status and one wcc_data, whatever "+
		"the status (ADR 0026 §8)", what, st))
	return st
}

// stkRename sends a RENAME of fromName in fromDir to toName in toDir, and
// returns its status. The reply is the status and two wcc_data, whatever the
// status, and must end there (ADR 0026 §8; ADR 0007 §5).
func stkRename(t *testing.T, c *rpcClient, fromDir []byte, fromName string, toDir []byte, toName, what string) uint32 {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procRename, func(w *xdr.Writer) {
		w.Opaque(fromDir)
		w.String(fromName)
		w.Opaque(toDir)
		w.String(toName)
	})
	st := r.Uint32()
	skipWccData(r)
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to %s, status %d, which is the status and two wcc_data, whatever "+
		"the status (ADR 0026 §8)", what, st))
	return st
}

// stkClient dials addr as a client that sends uid and gid.
func stkClient(t *testing.T, addr string, uid, gid uint32) *rpcClient {
	t.Helper()
	c := dial(t, addr)
	c.uid, c.gid = uid, gid
	return c
}

// TestNFSAStickyDirectoryRefusesWithNFS3ERRPERM pins ADR 0026 §8's K10 (§1,
// "What a client can observe"; §3; #94's second done-when). The owner, uid
// 501, makes tmp in the root with a MKDIR whose sattr3 sets mode 0o1777, and
// GETATTR reports mode 0o1777; it also creates k in the root. A client sending
// 502:30 creates a and a2 in tmp and makes the directory da there. A client
// sending 503:20 creates b in tmp. Then, from 503:20:
//
//   - REMOVE of tmp/a, RMDIR of tmp/da, RENAME of tmp/a to tmp/z, and RENAME
//     of tmp/b onto tmp/a each answer status 1, NFS3ERR_PERM, with one
//     wcc_data, or two for RENAME, and nothing after;
//   - LOOKUP of a and of da in tmp then answers NFS3_OK, and of z
//     NFS3ERR_NOENT;
//   - REMOVE of k in the root, whose mode 0o755 gives the group no write,
//     answers status 13, NFS3ERR_ACCES: the two refusals differ.
//
// Then REMOVE of tmp/a from 502:30, the entry's owner, RMDIR of tmp/da from
// 501:20, the directory's owner, and REMOVE of tmp/a2 from a client sending
// uid 0 each answer NFS3_OK.
func TestNFSAStickyDirectoryRefusesWithNFS3ERRPERM(t *testing.T) {
	addr, _, _ := startServer(t)
	owner := dial(t, addr)
	root := mountRoot(t, owner)

	// Setup, as the owner of the root.
	st, tmp := stkMkdir(t, owner, root, "tmp", 0o1777)
	if st != nmfsOK || tmp == nil {
		t.Fatalf("setup: the owner's MKDIR of tmp in the root with mode 0o1777 = status %d, handle %v, want "+
			"NFS3_OK with a handle", st, tmp != nil)
	}
	if mode, _ := oaGetAttr(t, owner, tmp, "tmp"); mode != 0o1777 {
		t.Fatalf("setup: GETATTR of tmp = mode %#o, want 0o1777: a mode given to MKDIR is applied masked to "+
			"0o7777, and GETATTR reports it (ADR 0026, What the code has; §8)", mode)
	}
	nfsCreate(t, owner, root, "k")

	other := stkClient(t, addr, 502, 30)
	nfsCreate(t, other, tmp, "a")
	nfsCreate(t, other, tmp, "a2")
	if st, da := stkMkdir(t, other, tmp, "da", 0o755); st != nmfsOK || da == nil {
		t.Fatalf("setup: 502:30's MKDIR of da in tmp, whose mode 0o1777 gives it write and search, = status %d, "+
			"handle %v, want NFS3_OK with a handle", st, da != nil)
	}

	member := stkClient(t, addr, 503, 20)
	nfsCreate(t, member, tmp, "b")

	// From 503:20, whom tmp's mode gives write and search, and who is neither
	// root, nor tmp's owner, nor the owner of a or da.
	const perm = "in a directory whose mode has 0o1000 set, a caller with write and search there who is neither " +
		"root, nor the directory's owner, nor the owner of the entry, is refused with vfs.ErrPerm, which the " +
		"server answers NFS3ERR_PERM, 1, with the reply's usual form (ADR 0026 §1, What a client can observe, " +
		"and §3)"
	refused := []struct {
		what string
		st   uint32
	}{
		{"503:20's REMOVE of tmp/a", stkUnlink(t, member, procRemove, tmp, "a", "503:20's REMOVE of tmp/a")},
		{"503:20's RMDIR of tmp/da", stkUnlink(t, member, procRmdir, tmp, "da", "503:20's RMDIR of tmp/da")},
		{"503:20's RENAME of tmp/a to tmp/z", stkRename(t, member, tmp, "a", tmp, "z",
			"503:20's RENAME of tmp/a to tmp/z")},
		{"503:20's RENAME of tmp/b, its own, onto tmp/a", stkRename(t, member, tmp, "b", tmp, "a",
			"503:20's RENAME of tmp/b onto tmp/a")},
	}
	for _, r := range refused {
		if r.st != stkPerm {
			t.Errorf("%s = status %d, want %d, NFS3ERR_PERM: %s", r.what, r.st, stkPerm, perm)
		}
	}

	for _, name := range []string{"a", "da"} {
		if st, _ := nmfsLookup(t, owner, tmp, name); st != nmfsOK {
			t.Errorf("after 503:20's refused calls, LOOKUP of %s in tmp = status %d, want NFS3_OK: a refused call "+
				"takes no name away (ADR 0026 §4)", name, st)
		}
	}
	if st, _ := nmfsLookup(t, owner, tmp, "z"); st != nmfsNoEnt {
		t.Errorf("after 503:20's refused RENAME of tmp/a to tmp/z, LOOKUP of z in tmp = status %d, want %d, "+
			"NFS3ERR_NOENT: a refused call makes no name (ADR 0026 §4)", st, nmfsNoEnt)
	}

	if st := stkUnlink(t, member, procRemove, root, "k", "503:20's REMOVE of k in the root"); st != stkAcces {
		t.Errorf("503:20's REMOVE of k in the root, of mode 0o755, = status %d, want %d, NFS3ERR_ACCES: a caller "+
			"the directory's mode denies write is refused with vfs.ErrAcces, which differs from the sticky "+
			"refusal (ADR 0026 §1, line 2, and §3)", st, stkAcces)
	}

	// The owners and root.
	if st := stkUnlink(t, other, procRemove, tmp, "a", "502:30's REMOVE of tmp/a"); st != nmfsOK {
		t.Errorf("502:30's REMOVE of tmp/a, its own entry, = status %d, want NFS3_OK: the entry's owner may "+
			"remove it (ADR 0026 §1)", st)
	}
	if st := stkUnlink(t, owner, procRmdir, tmp, "da", "501:20's RMDIR of tmp/da"); st != nmfsOK {
		t.Errorf("501:20's RMDIR of tmp/da, the empty directory 502:30 made in 501's tmp, = status %d, want "+
			"NFS3_OK: the directory's owner may remove any entry of it (ADR 0026 §1)", st)
	}
	rootClient := stkClient(t, addr, 0, 0)
	if st := stkUnlink(t, rootClient, procRemove, tmp, "a2", "uid 0's REMOVE of tmp/a2"); st != nmfsOK {
		t.Errorf("uid 0's REMOVE of tmp/a2, 502:30's entry, = status %d, want NFS3_OK: root may remove any "+
			"entry (ADR 0026 §1)", st)
	}
}
