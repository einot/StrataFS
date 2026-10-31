package blobfs

// Clean-room test for ADR 0024
// (docs/adr/0024-every-namespace-change-is-marked-dirty-and-a-refused-setattr-makes-none.md)
// section 1's paragraph on bufferWrite's tail block, written without reading
// the implementation, from ADR 0024 sections 1 and 7 and Assumption 25, ADR
// 0003 section 2 (site 2) and its "If the inode goes away", the facts and
// harness of ADR 0023 section 11, the safety rules of ADR 0011 section 7, and
// internal/vfs/vfs.go.
//
// ADR 0024 section 1: markDirty is called exactly where a change is made. A
// Write's tail block, holding the file's openFile.mu and FS.mu for writing,
// resolves the handle again; if the inode is gone, unlinked while the write
// buffered, it drops the openFile's buffers and releases their charge, which
// changes no inode, entry, attribute, size or chunk list, and so marks
// nothing. A Sync made between the unlink and that block commits the removal
// and clears FS.dirty; the block must not set it again, or the next Sync
// commits for nothing.
//
// Covered, with the name section 7 pins:
//   - B1, TestAWriteThatLandsAfterAnUnlinkMarksNothing.
//
// The harness: a bucket under the test's t.TempDir(), behind ADR 0023 section
// 11's uc43Store, which records every store call; the mount under test is over
// a bpStore (backpressure_test.go) that wraps the uc43Store, so that a Get of a
// chunk can be held at a gate while the uc43Store still records it once it
// passes. The file is written by a first mount that is then dropped, so that
// the mount under test has a cold cache and its Write fetches the chunk.
//
// The safety rules (ADR 0023 section 11; ADR 0011 section 7): every New and
// every FS call, Root() included, runs in uc43Go, which recovers a panic, and
// is waited for under uc43Bound, 10 s; the gate is a newBTGate, opened by a
// cleanup that runs before the harness's own cleanups release its holds,
// cancel the test's context, which the held Get also selects on, and wait for
// every call the test started; nothing sleeps or polls, and nothing is
// parallel; FS.dirty and FS.changes are read only holding FS.mu for reading,
// inside uc43Go (sa24Read), and no FS field is written.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"strata/internal/vfs"
)

// TestAWriteThatLandsAfterAnUnlinkMarksNothing is B1 of ADR 0024 section 7,
// and pins section 1's rule for bufferWrite's tail block: a Write whose file
// is unlinked while it buffers changes nothing in the namespace when it lands,
// and marks nothing (Assumption 25; ADR 0003 section 2, site 2).
//
// A first mount on the harness's bucket: testCaller creates f.bin, writes 4096
// bytes p at 0, vfs.Unstable, and Syncs; that mount is dropped and not used
// again. Then New over a bpStore wrapping the uc43Store, so that the mount's
// cache is empty, with setGet armed with a bpHook whose key is empty, so that
// it holds every Get of a chunk, with entered and a gate. In a goroutine under
// the bound, testCaller's Write of 10 bytes at 100 of f.bin, vfs.Unstable,
// which covers part of the stored chunk and so fetches it; the test waits for
// entered. While the Get is held: testCaller's Remove of f.bin returns nil;
// Sync returns nil, committing the removal, without waiting for the Write,
// since the Remove dropped the file's FS.open entry; FS.dirty is false and
// FS.changes is read, c2. The gate is opened; the Write returns 10 and nil
// under the bound (ADR 0003, "If the inode goes away"). Then FS.dirty is false
// and FS.changes is c2; the harness's record is marked; Sync returns nil and
// the harness recorded no call since the mark; and a remount of the inner
// store answers vfs.ErrNoEnt to Lookup of f.bin.
//
// Red at 369cbad and at 2ef1eb4, where the tail block marks on that branch:
// FS.dirty is true after the Write, FS.changes is above c2, and the last Sync
// records a Put under snapshotPrefix and a PutIfMatch of rootKey for a commit
// that changes nothing.
func TestAWriteThatLandsAfterAnUnlinkMarksNothing(t *testing.T) {
	const where = "B1, ADR 0024 sections 1 and 7; Assumption 25"
	const name = "f.bin"
	e := uc43Setup(t)
	ctx := e.ctx

	p := make([]byte, 4096)
	for i := range p {
		p[i] = byte(i*11 + 5)
	}
	func() {
		// The first mount lives only in this scope, so that nothing uses it
		// after it is dropped.
		first := e.newFS(where + ", the first mount")
		h := e.create(first, e.root(first, where), name, where)
		e.write(first, h, 0, p, fmt.Sprintf("Write of %d bytes at 0 of %q on the first mount, vfs.Unstable",
			len(p), name), where)
		if err := e.sync(first, fmt.Sprintf("the Sync that commits %q on the first mount", name), where); err != nil {
			t.Fatalf("setup: the Sync that commits %q on the first mount = %v, want nil against a healthy store (%s)",
				name, err, where)
		}
	}()

	// The mount under test, over a bpStore wrapping the harness.
	bps := &bpStore{Store: e.h}
	nr := uc43Go(e, func() uc43R[*FS] {
		fs, err := New(ctx, uc43Config(bps))
		return uc43R[*FS]{fs, err}
	}).wait(t, "New over a bpStore wrapping the harness", where)
	if nr.err != nil || nr.v == nil {
		t.Fatalf("setup: New over a bpStore wrapping the harness = (%v, %v), want a mount (%s)", nr.v, nr.err, where)
	}
	fs := nr.v
	root := e.root(fs, where)
	l := e.lookup(fs, root, name, where)
	if l.err != nil {
		t.Fatalf("setup: Lookup of %q on the mount under test = %v, want success (%s)", name, l.err, where)
	}
	h := l.h

	gate := newBTGate(t)
	hook := &bpHook{key: "", entered: make(chan struct{}), gate: gate.ch}
	bps.setGet(hook)

	data := []byte("0123456789")
	what := fmt.Sprintf("testCaller's Write of %d bytes at 100 of %q, vfs.Unstable", len(data), name)
	w := uc43Go(e, func() uc43R[uint32] {
		n, _, err := fs.Write(ctx, testCaller, h, 100, data, vfs.Unstable)
		return uc43R[uint32]{n, err}
	})
	select {
	case <-hook.entered:
	case <-w.done:
		t.Fatalf("setup: %s returned (%d, %v; panicked %v with %v) without a Get of a chunk, so the test could not "+
			"hold its fetch: on a mount with a cold cache a Write that covers part of a stored chunk loads it with "+
			"one Get under %q (ADR 0024 section 7, the facts; %s)", what, w.val.v, w.val.err, w.panicked, w.pval,
			chunkPrefix, where)
	case <-time.After(uc43Bound):
		t.Fatalf("setup: %s has not made a Get of a chunk after %v (%s)", what, uc43Bound, where)
	}

	// While the Get is held.
	rwhat := fmt.Sprintf("testCaller's Remove of %q, while %s is held in its chunk's Get", name, what)
	if err := uc43Go(e, func() error { return fs.Remove(ctx, testCaller, root, name) }).wait(t, rwhat,
		where); err != nil {
		t.Fatalf("%s = %v, want nil: testCaller owns the root directory (%s)", rwhat, err, where)
	}
	if err := e.sync(fs, "the Sync after the Remove, while the Write is held", where); err != nil {
		t.Fatalf("the Sync after the Remove, while %s is held, = %v, want nil: it commits the removal, and does not "+
			"wait for the Write, since the Remove dropped the file's FS.open entry (ADR 0024 section 7, the facts; "+
			"%s)", what, err, where)
	}
	if w.finished() {
		t.Errorf("test bug: %s had returned before the gate was opened, so it did not land after the unlink (%s)",
			what, where)
	}
	f := sa24Read(e, fs, "reading FS.dirty and FS.changes after the Sync that commits the removal", where)
	if f.dirty {
		t.Fatalf("setup: FS.dirty is true after a Sync that returned nil, with only the held Write running, want "+
			"false: the Sync committed the removal (ADR 0023 section 3; ADR 0024 section 7, B1; %s)", where)
	}
	c2 := f.changes

	gate.open()
	wr := w.wait(t, what+", once the gate was opened", where)
	if wr.err != nil || wr.v != uint32(len(data)) {
		t.Errorf("%s = (%d, %v), want (%d, nil): a Write whose file is removed while it buffers returns its full "+
			"length and nil (ADR 0003, \"If the inode goes away\"; ADR 0024 section 7, the facts; %s)", what, wr.v,
			wr.err, len(data), where)
	}

	after := "after the Write landed on the unlinked file"
	f = sa24Read(e, fs, "reading FS.dirty and FS.changes "+after, where)
	if f.dirty {
		t.Errorf("%s, FS.dirty is true, want false: the Write's tail block found the inode gone and changed "+
			"nothing, so it marks nothing (ADR 0024 section 1; Assumption 25; %s)", after, where)
	}
	if f.changes != c2 {
		t.Errorf("%s, FS.changes is %d, want %d, its value after the Sync that committed the removal: the tail "+
			"block changed nothing (ADR 0024 section 1; Assumption 25; %s)", after, f.changes, c2, where)
	}
	mark := len(e.h.records())
	if err := e.sync(fs, "the Sync "+after, where); err != nil {
		t.Errorf("the Sync %s = %v, want nil (ADR 0024 section 7, B1; %s)", after, err, where)
	}
	if calls := e.h.records()[mark:]; len(calls) != 0 {
		snaps, swaps := len(uc43Filter(calls, uc43IsSnapPut)), len(uc43Filter(calls, uc43IsRootSwap))
		t.Errorf("the Sync %s: the harness recorded %v (%d Puts under %q, %d PutIfMatch calls of %q), want no "+
			"call: nothing changed the namespace since the last commit, so a Sync with nothing dirty makes no "+
			"store call (ADR 0024 section 1; ADR 0023 section 3; %s)", after, calls, snaps, snapshotPrefix, swaps,
			rootKey, where)
	}

	rwhere := where + ", the remount"
	if rm := e.remount(rwhere); rm != nil {
		if rl := e.lookup(rm, e.root(rm, rwhere), name, rwhere); !errors.Is(rl.err, vfs.ErrNoEnt) {
			t.Errorf("Lookup of %q on a remount of the inner store = %v, want vfs.ErrNoEnt: the removal was "+
				"committed (ADR 0024 section 7, B1; %s)", name, rl.err, rwhere)
		}
	}
}
