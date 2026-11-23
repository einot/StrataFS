package nfs

// Clean-room wire test for #97, "ACCESS grants what the operations then
// refuse", written without reading the implementation, from ADR 0027
// (docs/adr/0027-access-grants-only-what-the-operations-allow.md) §1 (the rule,
// and "What changes for a client"), §3 and §10 (X13, and the encoding of
// ACCESS), and from ADR 0007 §5 (sattr3, fattr3, post_op_attr, wcc_data, and
// the end of a reply).
//
// Covered, with the name ADR 0027 §10 pins: X13,
// TestNFSAccessReportsWhatTheOperationsAllow.
//
// ACCESS's arguments are the object's handle and the mask asked for, a
// uint32; its reply is the status, a post_op_attr and, for NFS3_OK alone, the
// mask granted, a uint32, and nothing after (ADR 0027 §10). The bits are
// ACCESS3_READ 0x01, LOOKUP 0x02, MODIFY 0x04, EXTEND 0x08, DELETE 0x10 and
// EXECUTE 0x20.
//
// It drives the server over a real TCP socket with startServer, as
// sticky_test.go does, and reuses startServer, dial, mountRoot, rpcClient and
// its uid and gid fields, nfsCreate and skipPostOpAttr (integration_test.go);
// nmfsCreate, nmfsLookup, nmfsWantEnd, nmfsOK, nmfsNoEnt and nmfsGuarded
// (max_file_size_test.go); oaSetMode and oaGetAttr (owner_access_test.go); and
// stkMkdir, stkClient and stkAcces (sticky_test.go). Of unexported non-test
// names it uses the procedure number procAccess. Helpers of this file's own
// are prefixed acc. It names nothing the code lacks at 4ded55e, so that it
// builds on that tree and carries the red run.
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

// accAccess sends an ACCESS of fh asking for mask, and returns its status and,
// for NFS3_OK, the mask granted. The reply is the status, a post_op_attr and,
// for NFS3_OK alone, a uint32; it must end there (ADR 0027 §10; ADR 0007 §5).
func accAccess(t *testing.T, c *rpcClient, fh []byte, mask uint32, what string) (uint32, uint32) {
	t.Helper()
	r := c.call(ProgramNFS, VersionNFS, procAccess, func(w *xdr.Writer) {
		w.Opaque(fh)
		w.Uint32(mask)
	})
	st := r.Uint32()
	skipPostOpAttr(r)
	var granted uint32
	if st == nmfsOK {
		granted = r.Uint32()
	}
	nmfsWantEnd(t, r, fmt.Sprintf("the reply to %s, status %d, which is the status, a post_op_attr and, for "+
		"NFS3_OK alone, the mask granted (ADR 0027 §10)", what, st))
	return st, granted
}

// accWantAccess requires c's ACCESS of fh, asking for mask, to answer NFS3_OK
// and want.
func accWantAccess(t *testing.T, c *rpcClient, fh []byte, name string, mask, want uint32, who, why string) {
	t.Helper()
	what := fmt.Sprintf("%s's ACCESS of %s with mask %#04x", who, name, mask)
	st, got := accAccess(t, c, fh, mask, what)
	if st != nmfsOK || got != want {
		t.Errorf("%s = status %d, mask %#04x, want NFS3_OK and %#04x: %s", what, st, got, want, why)
	}
}

// TestNFSAccessReportsWhatTheOperationsAllow pins ADR 0027 §10's X13 (§1,
// "What changes for a client"; §3). The owner, uid 501, makes in the root the
// directories rd, of mode 0o744, wo, 0o722, ok, 0o777, and none, 0o700, each
// with a MKDIR whose sattr3 sets the mode, and the file x.bin, whose mode it
// sets to 0o755 with a SETATTR. Then, from a client sending 502:30, with mask
// 0x3f unless given:
//   - ACCESS of rd answers NFS3_OK and 0x01; of wo, 0x00; of ok, 0x1f; of
//     none, 0x00; of x.bin, 0x21; of ok with mask 0x02, 0x02; and of ok with
//     mask 0x20, 0x00;
//   - LOOKUP of a name rd lacks answers 13, NFS3ERR_ACCES, and of one ok
//     lacks, 2, NFS3ERR_NOENT;
//   - a GUARDED CREATE in wo answers 13, and in ok, NFS3_OK.
//
// From the owner, ACCESS of rd answers 0x1f and of x.bin 0x2d. From a client
// sending uid 0, ACCESS of none answers 0x1f.
func TestNFSAccessReportsWhatTheOperationsAllow(t *testing.T) {
	addr, _, _ := startServer(t)
	owner := dial(t, addr)
	root := mountRoot(t, owner)

	// Setup, as the owner of the root.
	dirs := map[string][]byte{}
	for _, d := range []struct {
		name string
		mode uint32
	}{
		{"rd", 0o744},
		{"wo", 0o722},
		{"ok", 0o777},
		{"none", 0o700},
	} {
		st, fh := stkMkdir(t, owner, root, d.name, d.mode)
		if st != nmfsOK || fh == nil {
			t.Fatalf("setup: the owner's MKDIR of %s in the root with mode %#o = status %d, handle %v, want NFS3_OK "+
				"with a handle", d.name, d.mode, st, fh != nil)
		}
		if mode, _ := oaGetAttr(t, owner, fh, d.name); mode != d.mode {
			t.Fatalf("setup: GETATTR of %s = mode %#o, want %#o: a mode given to MKDIR is applied (ADR 0026 §8)",
				d.name, mode, d.mode)
		}
		dirs[d.name] = fh
	}
	xbin := nfsCreate(t, owner, root, "x.bin")
	if st := oaSetMode(t, owner, xbin, 0o755, "x.bin"); st != nmfsOK {
		t.Fatalf("setup: the owner's SETATTR of x.bin's mode to 0o755 = status %d, want NFS3_OK: OpChmod allows the "+
			"owner (ADR 0014 §5)", st)
	}
	if mode, _ := oaGetAttr(t, owner, xbin, "x.bin"); mode != 0o755 {
		t.Fatalf("setup: GETATTR of x.bin = mode %#o, want 0o755", mode)
	}

	const rule = "ACCESS grants a bit only where the operations it names are allowed: on a directory READ with " +
		"read, LOOKUP with search, and MODIFY, EXTEND and DELETE with write and search; on a file READ with read, " +
		"MODIFY and EXTEND with write, and EXECUTE with execute; never EXECUTE on a directory; and only the bits " +
		"asked for (ADR 0027 §1 and §2)"

	other := stkClient(t, addr, 502, 30)
	const o = "502:30"
	accWantAccess(t, other, dirs["rd"], "rd, of mode 0o744", 0x3f, 0x01, o, rule+": the other bits r-- give "+
		"READ alone, where at 4ded55e they gave LOOKUP too")
	accWantAccess(t, other, dirs["wo"], "wo, of mode 0o722", 0x3f, 0x00, o, rule+": the other bits -w- give "+
		"nothing, where at 4ded55e they gave MODIFY, EXTEND and DELETE")
	accWantAccess(t, other, dirs["ok"], "ok, of mode 0o777", 0x3f, 0x1f, o, rule)
	accWantAccess(t, other, dirs["none"], "none, of mode 0o700", 0x3f, 0x00, o, rule)
	accWantAccess(t, other, xbin, "x.bin, of mode 0o755", 0x3f, 0x21, o, rule)
	accWantAccess(t, other, dirs["ok"], "ok, of mode 0o777", 0x02, 0x02, o, rule)
	accWantAccess(t, other, dirs["ok"], "ok, of mode 0o777", 0x20, 0x00, o, rule)

	// The operations agree.
	if st, _ := nmfsLookup(t, other, dirs["rd"], "nope"); st != stkAcces {
		t.Errorf("502:30's LOOKUP of nope in rd, of mode 0o744, = status %d, want %d, NFS3ERR_ACCES: read without "+
			"search does not look up, which is why ACCESS grants rd no LOOKUP (ADR 0027 §1)", st, stkAcces)
	}
	if st, _ := nmfsLookup(t, other, dirs["ok"], "nope"); st != nmfsNoEnt {
		t.Errorf("502:30's LOOKUP of nope in ok, of mode 0o777, = status %d, want %d, NFS3ERR_NOENT: the lookup is "+
			"allowed and then finds no name (ADR 0027 §1)", st, nmfsNoEnt)
	}
	if rep := nmfsCreate(t, other, dirs["wo"], "new", nmfsGuarded, 0); rep.status != stkAcces {
		t.Errorf("502:30's GUARDED CREATE of new in wo, of mode 0o722, = status %d, want %d, NFS3ERR_ACCES: a "+
			"creation needs write and search, which is why ACCESS grants wo no EXTEND (ADR 0027 §1)", rep.status,
			stkAcces)
	}
	if rep := nmfsCreate(t, other, dirs["ok"], "new", nmfsGuarded, 0); rep.status != nmfsOK {
		t.Errorf("502:30's GUARDED CREATE of new in ok, of mode 0o777, = status %d, want NFS3_OK: ACCESS grants ok "+
			"EXTEND, and the creation is allowed (ADR 0027 §1 and §8)", rep.status)
	}

	accWantAccess(t, owner, dirs["rd"], "rd, of mode 0o744", 0x3f, 0x1f, "the owner, 501:20", rule+": the owner "+
		"bits rwx give all five of a directory's bits")
	accWantAccess(t, owner, xbin, "x.bin, of mode 0o755", 0x3f, 0x2d, "the owner, 501:20", rule+": the owner bits "+
		"rwx give READ, MODIFY, EXTEND and EXECUTE, and a file never LOOKUP or DELETE")

	rootClient := stkClient(t, addr, 0, 0)
	accWantAccess(t, rootClient, dirs["none"], "none, of mode 0o700", 0x3f, 0x1f, "uid 0", rule+": Permits gives "+
		"root everything on a directory, so root is granted all five of its bits on a mount that takes changes")
}
