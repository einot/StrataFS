package nfs

// Clean-room wire test for #69, "SETATTR with a size, and WRITE, accept a
// symlink's handle", written without reading the implementation, from ADR
// 0029 (docs/adr/0029-only-a-regular-file-is-read-written-or-given-a-size.md)
// sections 1, 2, 3 and 9 (N9, and the encodings of READLINK and READ as
// internal/nfs reads and writes them at 939f2b2), ADR 0007 section 5 (the
// encodings of SETATTR, WRITE, CREATE and GETATTR, and the end of a reply),
// ADR 0028 section 8 (SYMLINK) and ADR 0027 section 10 (ACCESS).
//
// Covered, with the name ADR 0029 section 9 pins: N9,
// TestNFSASymlinksHandleIsRefusedWithNFS3ERRINVAL. A WRITE, a READ and a
// SETATTR that sets a size, of a symlink's handle, answer NFS3ERR_INVAL, 22,
// from the owner and from another caller alike; ACCESS still reports the
// symlink's mode; and an UNCHECKED CREATE that carries a size and names the
// symlink leaves it as it was.
//
// It drives the server over a real TCP socket with startServer, as
// sticky_test.go does, and reuses startServer, dial, mountRoot, rpcClient and
// skipPostOpAttr (integration_test.go); nmfsWrite, nmfsSetSize, nmfsCreate,
// nmfsWantEnd, nmfsUnchecked and nmfsOK (max_file_size_test.go); stkClient
// (sticky_test.go); cgDial, cgMake and cgLnk (create_group_test.go); and
// accAccess (access_test.go). Of unexported non-test names it uses the
// procedure numbers procGetAttr, procRead and procSymlink. READLINK's number,
// 5, is spelled out here as nrProcReadlink, from RFC 1813 section 3.3.5 as
// ADR 0029 section 9 gives it, rather than taken from the implementation.
// Helpers of this file's own are prefixed nr.
//
// startServer's root directory is owned by 501:20, which is what dial's client
// sends. Every bucket is under the test's t.TempDir(). Nothing here sleeps,
// polls or runs in parallel; each client is its own connection, and each call
// is bounded by the 10 s deadline rpcClient.call sets. No size sent here is
// above 100, so a build that accepted every call would build no long chunk
// list.

import (
	"fmt"
	"testing"

	"strata/internal/xdr"
)

// nrTarget is the symlink's target: 14 bytes long.
const nrTarget = "dir/target.txt"

// nrInval is NFS3ERR_INVAL (RFC 1813 section 2.6; ADR 0029 section 9).
const nrInval uint32 = 22

// nrProcReadlink is READLINK's procedure number (RFC 1813 section 3.3.5; ADR
// 0029 section 9).
const nrProcReadlink uint32 = 5

// nrGetAttr sends a GETATTR of fh from c and decodes its reply to its end: the
// status and, for NFS3_OK, a fattr3, of which it returns the type and the
// size (ADR 0007 section 5; ADR 0028 section 8).
func nrGetAttr(t *testing.T, c *rpcClient, fh []byte, what string) (st, typ uint32, size uint64) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procGetAttr, func(w *xdr.Writer) { w.Opaque(fh) })
	st = r.Uint32()
	if st == nmfsOK {
		typ = r.Uint32()
		for i := 0; i < 4; i++ {
			r.Uint32() // mode, nlink, uid and gid
		}
		size = r.Uint64()
		r.Uint64() // used
		r.Uint32() // rdev
		r.Uint32()
		r.Uint64() // fsid
		r.Uint64() // fileid
		for i := 0; i < 6; i++ {
			r.Uint32() // atime, mtime and ctime, seconds and nanoseconds each
		}
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a GETATTR of %s, status %d, which is a fattr3 for NFS3_OK (ADR 0007 "+
		"section 5)", what, st))
	return st, typ, size
}

// nrReadLink sends a READLINK of fh from c and decodes its reply to its end:
// the status, a post_op_attr and, for NFS3_OK alone, the target as a string
// (ADR 0029 section 9).
func nrReadLink(t *testing.T, c *rpcClient, fh []byte, what string) (uint32, string) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, nrProcReadlink, func(w *xdr.Writer) { w.Opaque(fh) })
	st := r.Uint32()
	skipPostOpAttr(r)
	var target string
	if st == nmfsOK {
		target = r.String()
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a READLINK of %s, status %d, which is the status, a post_op_attr "+
		"and, for NFS3_OK alone, the target (ADR 0029 section 9)", what, st))
	return st, target
}

// nrRead sends a READ of count bytes at off of fh from c and decodes its reply
// to its end: the status, a post_op_attr and, for NFS3_OK, the count, eof and
// the data; for any other status, nothing after the post_op_attr (ADR 0029
// section 9; ADR 0007 section 5). It returns the status and the data.
func nrRead(t *testing.T, c *rpcClient, fh []byte, off uint64, count uint32, what string) (uint32, []byte) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procRead, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Uint64(off)
		w.Uint32(count)
	})
	st := r.Uint32()
	skipPostOpAttr(r)
	var data []byte
	if st == nmfsOK {
		r.Uint32() // count
		r.Bool()   // eof
		data = r.Opaque()
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to %s, status %d, which is READ3resok for NFS3_OK and otherwise "+
		"READ3resfail, one post_op_attr (ADR 0029 sections 2 and 9)", what, st))
	return st, data
}

// nrLinkProblems asks GETATTR and READLINK of fh from c, and describes how
// they differ from a symlink of size 14 whose target is nrTarget, or returns
// "".
func nrLinkProblems(t *testing.T, c *rpcClient, fh []byte, when string) string {
	t.Helper()
	var p string
	st, typ, size := nrGetAttr(t, c, fh, "the symlink, "+when)
	if st != nmfsOK || typ != cgLnk || size != uint64(len(nrTarget)) {
		p = fmt.Sprintf("GETATTR = (status %d, type %d, size %d), want (NFS3_OK, type %d, size %d)", st, typ, size,
			cgLnk, len(nrTarget))
	}
	rst, target := nrReadLink(t, c, fh, "the symlink, "+when)
	if rst != nmfsOK || target != nrTarget {
		if p != "" {
			p += "; "
		}
		p += fmt.Sprintf("READLINK = (status %d, %q), want (NFS3_OK, %q)", rst, target, nrTarget)
	}
	return p
}

// TestNFSASymlinksHandleIsRefusedWithNFS3ERRINVAL is N9 of ADR 0029 section 9
// (sections 1, 2 and 3).
//
// The owner, uid 501, makes ln in the root with a SYMLINK to
// "dir/target.txt"; its sattr3 sets the owner's own gid, 20, and nothing else,
// as cgMake sends it. GETATTR of its handle reports type 5 and size 14, and
// READLINK returns the target. Then, from the owner and from a client sending
// 502:30:
//   - a WRITE of 3 bytes at 0, and one at 100, answer 22, with one wcc_data
//     and nothing after;
//   - a SETATTR of size 0 answers 22, with one wcc_data and nothing after;
//   - a READ of 10 bytes at 0 answers 22, with one post_op_attr and nothing
//     after;
//   - an ACCESS with mask 0x3f answers NFS3_OK and 0x2d, R M E X.
//
// After each, GETATTR still reports type 5 and size 14 and READLINK still
// returns the target. Then the owner sends an UNCHECKED CREATE of ln that
// carries size 0. Whatever it answers, GETATTR of the symlink's handle still
// reports type 5 and size 14, and READLINK the target. The status is not
// pinned, since the handler drops the refusal (ADR 0007, Assumption 9; ADR
// 0029 section 2, "The three ways in").
func TestNFSASymlinksHandleIsRefusedWithNFS3ERRINVAL(t *testing.T) {
	const why = "a WRITE, a READ and a SETATTR that sets a size are for a regular file, and of a symlink are " +
		"answered NFS3ERR_INVAL, 22, for every caller (ADR 0029 sections 1 and 2)"
	addr, _, _ := startServer(t)
	owner := dial(t, addr)
	root := mountRoot(t, owner)

	what := fmt.Sprintf("the owner's SYMLINK of ln to %q", nrTarget)
	mk := cgMake(t, cgDial(t, addr, 501, 20, []uint32{20}, "the owner"), procSymlink, 0, root, "ln", nrTarget, -1, 20,
		what)
	if mk.status != nmfsOK || !mk.hasFH {
		t.Fatalf("setup: %s = status %d, handle present %v, want NFS3_OK with a handle (ADR 0028 section 8)", what,
			mk.status, mk.hasFH)
	}
	ln := mk.fh
	if p := nrLinkProblems(t, owner, ln, "after the SYMLINK"); p != "" {
		t.Fatalf("setup: after %s, %s: a symlink's size is its target's length (ADR 0029 section 9)", what, p)
	}

	other := stkClient(t, addr, 502, 30)
	clients := []struct {
		name string
		c    *rpcClient
	}{
		{"the owner (501:20)", owner},
		{"a client sending 502:30", other},
	}
	three := []byte{1, 2, 3}
	for _, cl := range clients {
		check := func(what string) {
			t.Helper()
			if p := nrLinkProblems(t, owner, ln, "after "+what); p != "" {
				t.Errorf("after %s, %s: a refused call changes nothing (ADR 0029 section 2)", what, p)
			}
		}

		for _, off := range []uint64{0, 100} {
			what := fmt.Sprintf("%s's WRITE of 3 bytes at %d of ln", cl.name, off)
			if rep := nmfsWrite(t, cl.c, ln, off, three); rep.status != nrInval {
				t.Errorf("%s = status %d, want %d, NFS3ERR_INVAL, with one wcc_data and nothing after: %s", what,
					rep.status, nrInval, why)
			}
			check(what)
		}

		what := fmt.Sprintf("%s's SETATTR of ln's size to 0", cl.name)
		if st := nmfsSetSize(t, cl.c, ln, 0); st != nrInval {
			t.Errorf("%s = status %d, want %d, NFS3ERR_INVAL, with one wcc_data and nothing after: %s", what, st,
				nrInval, why)
		}
		check(what)

		what = fmt.Sprintf("%s's READ of 10 bytes at 0 of ln", cl.name)
		if st, data := nrRead(t, cl.c, ln, 0, 10, what); st != nrInval {
			t.Errorf("%s = status %d with %d bytes, want %d, NFS3ERR_INVAL, with one post_op_attr and nothing "+
				"after: %s", what, st, len(data), nrInval, why)
		}
		check(what)

		what = fmt.Sprintf("%s's ACCESS of ln with mask 0x3f", cl.name)
		if st, granted := accAccess(t, cl.c, ln, 0x3f, what); st != nmfsOK || granted != 0x2d {
			t.Errorf("%s = status %d, mask %#04x, want NFS3_OK and 0x2d, R M E X: ACCESS still reports a "+
				"symlink's mode, 0o777, which gives every caller read, write and execute, and never LOOKUP or "+
				"DELETE (ADR 0029 section 3)", what, st, granted)
		}
		check(what)
	}

	what = "the owner's UNCHECKED CREATE of ln with size 0"
	rep := nmfsCreate(t, owner, root, "ln", nmfsUnchecked, 0)
	if p := nrLinkProblems(t, owner, ln, "after "+what); p != "" {
		t.Errorf("after %s, which answered status %d, %s: Create returns the symlink's handle and the size that "+
			"follows is refused, so the symlink is as it was; the status is not pinned (ADR 0029 section 2, \"The "+
			"three ways in\")", what, rep.status, p)
	}
}
