package nfs

// Clean-room wire test for ADR 0025, "A regular file's owner may read, write
// and truncate it whatever its mode, execute permission is enough to read it,
// and ACCESS reports the mode alone"
// (docs/adr/0025-a-files-owner-may-read-and-write-it-whatever-its-mode.md),
// as revised on 2026-11-15. It is A10 of that ADR's §6, written from §1 ("What
// a client can observe"), §4 ("One path asks no ACCESS first") and §6, and from
// ADR 0007 §5 (the encodings of sattr3, fattr3, SETATTR, CREATE and their
// replies) and its Assumption 9, without reading the implementation.
//
// The path: a client that takes a name to be absent opens it by sending an
// UNCHECKED CREATE, and asks no ACCESS first. If the name is the caller's own
// regular file, the server returns it and then applies the CREATE's size as a
// size change by the caller, ignoring a refusal of it and answering NFS3_OK
// either way (ADR 0007 §5 and Assumption 9). Since ADR 0025 §1 a regular
// file's owner may truncate and write it whatever its mode, so that size
// truncates the owner's read-only file and the owner's WRITE then lands; a
// caller the mode denies gains nothing.
//
// It drives the server over a real TCP socket with startServer, as
// max_file_size_test.go does: no sleeps, no COMMIT, and no ACCESS anywhere,
// since the absence of ACCESS is the path under test. startServer's root
// directory is owned by 501:20, which is what dial's client sends.

import (
	"bytes"
	"fmt"
	"testing"

	"strata/internal/xdr"
)

// oaSetMode sends a SETATTR of fh that sets the mode alone, with no guard, and
// returns its status. The sattr3 is set_mode3 TRUE with mode, set_uid3,
// set_gid3 and set_size3 FALSE, and both times DONT_CHANGE; the sattrguard3's
// check is FALSE. SETATTR3resok and SETATTR3resfail each carry one wcc_data,
// which it decodes, and the reply must end there (ADR 0007 §5).
func oaSetMode(t *testing.T, c *rpcClient, fh []byte, mode uint32, what string) uint32 {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, nmfsProcSetAttr, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Bool(true)
		w.Uint32(mode)
		w.Bool(false)
		w.Bool(false)
		w.Bool(false)
		w.Uint32(dontChange)
		w.Uint32(dontChange)
		w.Bool(false)
	})
	st := r.Uint32()
	skipWccData(r)
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to a SETATTR of %s's mode to %#o, status %d, whose "+
		"SETATTR3resok or SETATTR3resfail is one wcc_data", what, mode, st))
	return st
}

// oaGetAttr sends a GETATTR of fh, which must succeed, and returns the mode and
// the size it reports. GETATTR3resok is a bare fattr3, which begins type, mode,
// nlink, uid and gid as uint32s and then size as a uint64; the whole of it is
// decoded, and the reply must end there (ADR 0007 §5).
func oaGetAttr(t *testing.T, c *rpcClient, fh []byte, what string) (mode uint32, size uint64) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procGetAttr, func(w *xdr.Writer) { w.Opaque(fh) })
	if st := r.Uint32(); st != nmfsOK {
		t.Fatalf("GETATTR of %s status = %d, want NFS3_OK", what, st)
	}
	r.Uint32() // type
	mode = r.Uint32()
	r.Uint32() // nlink
	r.Uint32() // uid
	r.Uint32() // gid
	size = r.Uint64()
	r.Uint64() // used
	r.Uint32() // rdev, specdata1
	r.Uint32() // rdev, specdata2
	r.Uint64() // fsid
	r.Uint64() // fileid
	for i := 0; i < 6; i++ {
		r.Uint32() // atime, mtime and ctime, seconds and nanoseconds each
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the GETATTR reply for %s, whose GETATTR3resok is one fattr3", what))
	return mode, size
}

// TestNFSUncheckedCreateWithASizeTruncatesItsOwnersReadOnlyFile pins ADR 0025
// §6's A10 (§1, "What a client can observe"; §4, "One path asks no ACCESS
// first"). The owner, uid 501, creates ro.bin with a GUARDED CREATE, writes 100
// bytes to it, and with SETATTR sets its mode to 0o444 and the root
// directory's to 0o777, so that the setup does not rest on the override and
// another caller may add a name in the root. Then:
//
//   - a client sending uid 502 and gid 30 sends an UNCHECKED CREATE of ro.bin
//     with size 0. Its status is not pinned, since the handler answers NFS3_OK
//     and drops a refused size (ADR 0007, Assumption 9); GETATTR still reports
//     size 100 and mode 0o444, since the size is refused to a caller the mode
//     denies;
//   - the owner sends the same CREATE. It answers NFS3_OK with attributes whose
//     size is 0, and GETATTR of the handle the first CREATE returned reports
//     size 0 and mode 0o444;
//   - the owner's WRITE of 10 bytes at 0 to that handle answers NFS3_OK with
//     count 10, and GETATTR reports size 10 and mode 0o444.
//
// No call the test makes is an ACCESS, which is the path.
func TestNFSUncheckedCreateWithASizeTruncatesItsOwnersReadOnlyFile(t *testing.T) {
	addr, _, _ := startServer(t)
	c := dial(t, addr)
	root := mountRoot(t, c)

	// Setup, as the owner: none of it rests on the override.
	fh := nfsCreate(t, c, root, "ro.bin")
	data := bytes.Repeat([]byte{0xa5}, 100)
	if rep := nmfsWrite(t, c, fh, 0, data); rep.status != nmfsOK || rep.count != 100 {
		t.Fatalf("setup: the owner's WRITE of 100 bytes at 0 to ro.bin, of mode 0o644, = status %d, "+
			"count %d, want NFS3_OK with count 100", rep.status, rep.count)
	}
	if st := oaSetMode(t, c, fh, 0o444, "ro.bin"); st != nmfsOK {
		t.Fatalf("setup: the owner's SETATTR of ro.bin's mode to 0o444 status = %d, want NFS3_OK", st)
	}
	if st := oaSetMode(t, c, root, 0o777, "the root"); st != nmfsOK {
		t.Fatalf("setup: the owner's SETATTR of the root's mode to 0o777 status = %d, want NFS3_OK", st)
	}
	if mode, _ := oaGetAttr(t, c, root, "the root after the setup"); mode != 0o777 {
		t.Fatalf("setup: GETATTR of the root = mode %#o, want 0o777, so that another caller may add "+
			"a name in it", mode)
	}
	if mode, size := oaGetAttr(t, c, fh, "ro.bin after the setup"); mode != 0o444 || size != 100 {
		t.Fatalf("setup: GETATTR of ro.bin = mode %#o, size %d, want mode 0o444, size 100", mode, size)
	}

	// Another caller, whom the mode denies write: the size is refused to it.
	c2 := dial(t, addr)
	c2.uid, c2.gid = 502, 30
	if rep := nmfsCreate(t, c2, root, "other.bin", nmfsGuarded, 0); rep.status != nmfsOK {
		t.Fatalf("setup: a GUARDED CREATE of the fresh name other.bin in the root, of mode 0o777, by "+
			"502:30 status = %d, want NFS3_OK: without it, 502's UNCHECKED CREATE of ro.bin could "+
			"be refused at the directory and never reach the size", rep.status)
	}
	rep2 := nmfsCreate(t, c2, root, "ro.bin", nmfsUnchecked, 0)
	t.Logf("UNCHECKED CREATE of ro.bin with size 0 by 502:30: status %d, attributes %v, size %d "+
		"(the status is not pinned: ADR 0007, Assumption 9)", rep2.status, rep2.hasAttr, rep2.size)
	if mode, size := oaGetAttr(t, c, fh, "ro.bin after 502's CREATE"); mode != 0o444 || size != 100 {
		t.Errorf("after an UNCHECKED CREATE of ro.bin with size 0 by 502:30, neither its owner nor in "+
			"its group, GETATTR of ro.bin = mode %#o, size %d, want mode 0o444, size 100: the size is "+
			"refused to a caller the mode denies, and the override is the owner's alone (ADR 0025 §1, "+
			"§6 A10)", mode, size)
	}

	// The owner: the CREATE's size truncates its own read-only file.
	rep := nmfsCreate(t, c, root, "ro.bin", nmfsUnchecked, 0)
	if rep.status != nmfsOK {
		t.Errorf("the owner's UNCHECKED CREATE of its existing ro.bin, of mode 0o444, with size 0 "+
			"status = %d, want NFS3_OK (ADR 0025 §1, §6 A10; ADR 0007 §5)", rep.status)
	} else if !rep.hasAttr || rep.size != 0 {
		t.Errorf("the owner's UNCHECKED CREATE of its existing ro.bin, of mode 0o444, with size 0: "+
			"obj_attributes present %v, size %d, want attributes with size 0: the size is applied "+
			"as a size change by the file's owner, which the owner override allows (ADR 0025 §1, "+
			"§4, §6 A10)", rep.hasAttr, rep.size)
	}
	if mode, size := oaGetAttr(t, c, fh, "ro.bin after its owner's CREATE"); mode != 0o444 || size != 0 {
		t.Errorf("after its owner's UNCHECKED CREATE of ro.bin with size 0, GETATTR of the handle "+
			"the first CREATE returned = mode %#o, size %d, want mode 0o444, size 0: the size "+
			"truncates the owner's read-only file, and the mode is as it was (ADR 0025 §1, §4, "+
			"§6 A10)", mode, size)
	}

	// The owner's WRITE through the same handle, as the descriptor a client
	// holds after the creating open would send it.
	wrep := nmfsWrite(t, c, fh, 0, data[:10])
	if wrep.status != nmfsOK || wrep.count != 10 {
		t.Errorf("the owner's WRITE of 10 bytes at 0 to ro.bin, of mode 0o444, = status %d, count %d, "+
			"want NFS3_OK with count 10: the owner of a regular file may write it whatever its mode "+
			"(ADR 0025 §1, §4, §6 A10)", wrep.status, wrep.count)
	}
	if mode, size := oaGetAttr(t, c, fh, "ro.bin after its owner's WRITE"); mode != 0o444 || size != 10 {
		t.Errorf("after its owner's WRITE of 10 bytes at 0, GETATTR of ro.bin = mode %#o, size %d, "+
			"want mode 0o444, size 10 (ADR 0025 §1, §6 A10)", mode, size)
	}
}
