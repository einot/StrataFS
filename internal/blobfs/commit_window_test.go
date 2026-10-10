package blobfs

// Clean-room tests for ADR 0012, "A commit stores a truncate whole or not at
// all" (docs/adr/0012-a-commit-stores-a-truncate-whole-or-not-at-all.md). This
// file is the test for #64. It is written from that ADR, above all from §9, its
// test surface: items 1 to 8, its terms, how a test reaches each case, and its
// safety rules. It also draws on ADR 0006 §5, §7 and §8 (what a flush does with
// a pending trim); ADR 0003 §4's test surface (FS.mu and FS.open); ADR 0004
// §5 (a chunk's object key is chunkPrefix followed by the lowercase hex
// SHA-256 of its bytes); ADR 0005 §4 and §7 (a file has no FS.open entry until
// it is written or truncated on the mount, and Read adds none); ADR 0007 §7
// (ErrFBig); ADR 0011 §7 (the safety rules §9 adopts); and internal/vfs/vfs.go,
// without reading the implementation.
//
// Every test reaches the window the way §9 says. A is seeded at 2cs through
// another mount and the bucket is mounted afresh, so A is cold and is a file
// the first pass does not list: it has no FS.open entry when the Sync is
// called, which each test checks before the Sync starts, holding FS.mu for
// reading (bpOpen). On the mount under test, held.bin (C) is created and 100
// bytes new to the bucket are written at its offset 0, so its chunk 0 is
// exactly those bytes. Hold 1 gates the store's Put of that chunk's key, which
// the test computes as ADR 0004 §5 gives it. The call that runs the Sync is
// started, and "while held" means after that Put has been entered and before
// hold 1's gate opens. A is changed while held, hold 1 is opened, and a fresh
// mount of the bucket (wsRemountLookup on the *store.Local, with no further
// Sync of the mount under test) shows what the commit stored. Where a file is
// grown in more than one way, each way gets a fresh mount of its own. T is
// cs+1024 throughout (cwCut).
//
// They cover:
//
//   - §9 items 1 and 2, on each path that runs a Sync: Sync itself, a Commit
//     of another file, a FILE_SYNC and a DATA_SYNC Write to another file, and
//     a budget drain;
//   - item 3, a Write past the new end within the cut chunk;
//   - item 4, the dirty tail;
//   - item 5, the settle pass's upload of the shortened chunk, and a settle
//     pass that fails and so commits nothing;
//   - item 6, what returns while a Sync is held in that upload, and a truncate
//     of another file started then, which is in the commit whole or not at
//     all;
//   - item 7, a cut undone in the window by a truncate to the chunk's start;
//   - item 8, growth past the cut chunk in the window, by SetAttr and by Write.
//
// The safety rules of §9 hold throughout. Every FS call runs in a goroutine
// and is waited for under btHangBound, and goroutines never touch t. Every
// gate comes from newBTGate, so the test's cleanup opens it. Every mount under
// test has MaxDirtyBytes -1, except that of the budget-drain row, which needs a
// budget of one chunk; and every Write made while a Sync is held is UNSTABLE.
// Every bucket comes from bpLocal, under the test's temporary directory, and
// every mount that commits is made by bpNew, with SnapshotRetention -1. Run is
// never called. Nothing sleeps except for cwSettleGrace, the one fixed wait,
// which is only a limit.
//
// Nothing here reads or writes FS.commitGate or openFile.unsettled (§9, "No
// new name"). Nothing relies on whether, or for how long, a size change waits
// for the gate beyond item 1; on the order or number of a Sync's flushes; on
// whether the settle pass uploads a file's other dirty data; or on what a Sync
// does when no file is unsettled. The bytes a Write made in the window are
// never checked, since §9 leaves them unpinned.
//
// Not covered, on purpose, because §9 leaves them to review: that the gate is
// held across the whole commit phase on every path; §4's fail-safe and its
// record; that Run's committer, cmd/strata's shutdown, Commit, stable writes
// and budget drains all go through Sync; and §5's lock order.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// cwSettleGrace is how long TestCommitWindowTruncateDuringSettleIsWholeOrAbsent
// waits for its truncate of D to return before it releases the held settle
// upload. It is a limit, never a condition: a correct build always waits it
// out in full, because that truncate waits for the commit gate (ADR 0012 §2,
// §7), and a build that lets the truncate through during the commit phase ends
// the wait early and then fails on the fresh mount (§9 item 6). Waiting it out
// also makes it likely that the truncate is already waiting when the calls
// after it run, so a build whose truncate waits for the gate while holding
// FS.mu makes those calls hang and fail (§5). It is the only fixed wait in this
// file.
const cwSettleGrace = 100 * time.Millisecond

// cwA is the name of A, the cold file each test truncates while a Sync is held.
const cwA = "a.bin"

// cwD is the name of D, which TestCommitWindowTruncateDuringSettleIsWholeOrAbsent
// truncates while its Sync is held in the settle upload.
const cwD = "d.bin"

// cwG is the name of G, which TestCommitWindowTruncateDuringSettleIsWholeOrAbsent
// reads while its Sync is held in the settle upload.
const cwG = "g.bin"

// cwCut is T, the size A is truncated to: cs+1024, inside A's stored chunk 1,
// so that the truncate cuts into a stored chunk (ADR 0012 §1).
const cwCut = bpCS + 1024

// cwKey returns the object key of the chunk whose bytes are b: chunkPrefix
// followed by the lowercase hex SHA-256 of b (ADR 0004 §5; ADR 0012 §9, "No
// new name").
func cwKey(b []byte) string {
	sum := sha256.Sum256(b)
	return chunkPrefix + hex.EncodeToString(sum[:])
}

// cwMount is the mount under test, over a bpStore that wraps st, and A on it.
// c, cData, gate and put are set by createHeld and arm.
type cwMount struct {
	st    *store.Local
	bps   *bpStore
	fs    *FS
	a     vfs.Handle
	aID   uint64
	aData []byte
	c     vfs.Handle
	cData []byte
	gate  *btGate
	put   *bpHook
}

// cwNewMount seeds A at 2cs in a new bucket through a mount of its own, then
// mounts the bucket afresh with the given MaxDirtyBytes, so that A is cold
// there (ADR 0012 §9, "A file the first pass does not list"; ADR 0006 §8).
func cwNewMount(t *testing.T, limit int64) *cwMount {
	t.Helper()
	st := bpLocal(t)
	aData := bpSeedFile(t, st, cwA, 2*bpCS)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, limit, quietLog())
	a, attr := bpLookup(t, fs, cwA)
	return &cwMount{st: st, bps: bps, fs: fs, a: a, aID: attr.FileID, aData: aData}
}

// cwSeed commits a file of n unique bytes with the given mode to st through a
// mount of its own, as bpSeedFile does, and returns the bytes.
func cwSeed(t *testing.T, st *store.Local, name string, n int, mode uint32) []byte {
	t.Helper()
	fs := bpNew(t, st, 0, quietLog())
	h, _ := bpCreate(t, fs, name, mode)
	data := bpUnique(n)
	bpMustWrite(t, fs, "seeding "+name, h, 0, data)
	bpMustSync(t, fs, "Sync seeding "+name)
	return data
}

// cwWantNotOpen requires the file whose inode is id to have no FS.open entry
// on fs, read holding FS.mu for reading (ADR 0003 §4).
func cwWantNotOpen(t *testing.T, fs *FS, id uint64, name, when string) {
	t.Helper()
	if of := bpOpen(t, fs, id, "FS.open of "+name+" "+when); of != nil {
		t.Fatalf("setup: %s has an FS.open entry %s, so it is not a file the first pass does not "+
			"list; nothing on this mount has written or truncated it (ADR 0012 §9, \"A file the "+
			"first pass does not list\"; ADR 0005 §4, §7)", name, when)
	}
}

// createHeld creates held.bin (C) and writes 100 bytes new to the bucket at its
// offset 0, so that its chunk 0 is exactly those bytes (ADR 0012 §9, "A Sync
// held in its first pass").
func (m *cwMount) createHeld(t *testing.T) {
	t.Helper()
	m.c, _ = bpCreate(t, m.fs, "held.bin", 0)
	m.cData = bpUnique(100)
	bpMustWrite(t, m.fs, "the Write of 100 bytes at 0 of held.bin", m.c, 0, m.cData)
}

// arm checks that A has no FS.open entry, and then arms hold 1: a gate on the
// store's Put of C's chunk key, with an entered channel.
func (m *cwMount) arm(t *testing.T) {
	t.Helper()
	cwWantNotOpen(t, m.fs, m.aID, cwA, "before the Sync starts")
	m.gate = newBTGate(t)
	m.put = &bpHook{key: cwKey(m.cData), entered: make(chan struct{}), gate: m.gate.ch}
	m.bps.setPut(m.put)
}

// cwWaitHeld waits until c, the call that runs the Sync, is held by hold 1.
func cwWaitHeld[T any](t *testing.T, m *cwMount, call string, c *bpCall[T]) {
	t.Helper()
	bpWaitEntered(t, call+" holding its Sync in the first pass, in the Put of held.bin's chunk "+
		"(ADR 0012 §9, \"A Sync held in its first pass\")", m.put.entered, c)
}

// truncateA sets A's size while a Sync is held, and requires it to return
// under the bound and succeed (ADR 0012 §9 item 1).
func (m *cwMount) truncateA(t *testing.T, size uint64, while string) {
	t.Helper()
	bpTruncate(t, m.fs, m.a, size, fmt.Sprintf("SetAttr(size %s) of a.bin %s (ADR 0012 §9 item 1: "+
		"it returns without waiting for the Sync)", ptAt(size), while))
}

// cwStartSync starts m.fs.Sync in its own goroutine.
func cwStartSync(m *cwMount) *bpCall[error] {
	return bpGo(func() error { return m.fs.Sync(context.Background()) })
}

// holdSync arms hold 1, starts a Sync, and waits until it is held in its first
// pass.
func (m *cwMount) holdSync(t *testing.T) *bpCall[error] {
	t.Helper()
	m.arm(t)
	sc := cwStartSync(m)
	cwWaitHeld(t, m, "a Sync", sc)
	return sc
}

// releaseSync opens hold 1 and requires the Sync to succeed. item names the
// part of ADR 0012 that supposes it does.
func (m *cwMount) releaseSync(t *testing.T, sc *bpCall[error], item string) {
	t.Helper()
	m.gate.open()
	if err := sc.wait(t, "the Sync, after hold 1 was opened"); err != nil {
		t.Fatalf("the Sync, after hold 1 was opened, = %v, want success against a healthy writable "+
			"store (ADR 0012 %s)", err, item)
	}
}

// cwWantZeros requires one Read of [off, end) of a file that is size bytes long
// to return zeros, with eof exactly when end is the size.
func cwWantZeros(t *testing.T, fs *FS, h vfs.Handle, off, end, size uint64, what, why string) {
	t.Helper()
	rwhat := fmt.Sprintf("%s: one Read of [%s, %s)", what, ptAt(off), ptAt(end))
	r := rcStartRead(fs, h, off, uint32(end-off)).wait(t, rwhat)
	ptWantRead(t, rwhat, r, ptZeros(int(end-off)), end == size, why)
}

// cwWantGrownZeros mounts st afresh and requires name to be cut bytes long
// there. It then grows the file to grow with a SetAttr, and requires one Read
// of [cut, grow) to be all zeros.
func cwWantGrownZeros(t *testing.T, st *store.Local, name string, cut, grow uint64, what, why string) {
	t.Helper()
	fs2, h2, attr := wsRemountLookup(t, st, name)
	if attr.Size != cut {
		t.Errorf("%s: size on a fresh mount = %s, want %s (%s)", what, ptAt(attr.Size), ptAt(cut), why)
	}
	bpTruncate(t, fs2, h2, grow, fmt.Sprintf("%s: SetAttr(size %s) on a fresh mount", what, ptAt(grow)))
	cwWantZeros(t, fs2, h2, cut, grow, grow, what+", grown to "+ptAt(grow)+" on a fresh mount", why)
}

// cwWantUngrown mounts st afresh and, with no growth there, requires name to be
// size bytes long, to hold seed[:cut] below cut, and to read zeros in
// [cut, zerosEnd). What lies at or past zerosEnd is not checked.
func cwWantUngrown(t *testing.T, st *store.Local, name string, size uint64, seed []byte, cut, zerosEnd uint64, what, why string) {
	t.Helper()
	fs2, h2, attr := wsRemountLookup(t, st, name)
	if attr.Size != size {
		t.Errorf("%s: size on a fresh mount = %s, want %s (%s)", what, ptAt(attr.Size), ptAt(size), why)
	}
	got := bpReadAll(t, fs2, h2, what+", read whole on a fresh mount")
	if uint64(len(got)) != size {
		t.Errorf("%s: a fresh mount reads %d bytes, want %d (%s)", what, len(got), size, why)
	}
	if uint64(len(got)) < zerosEnd {
		return
	}
	if d := wsDiff(got[:cut], seed[:cut], 512); d != "" {
		t.Errorf("%s: [0, %s) on a fresh mount, which must be the seeded bytes: %s (%s)", what,
			ptAt(cut), d, why)
	}
	if d := wsDiff(got[cut:zerosEnd], ptZeros(int(zerosEnd-cut)), 512); d != "" {
		t.Errorf("%s: [%s, %s) on a fresh mount, which must be zeros, with offsets counted from %s: "+
			"%s (%s)", what, ptAt(cut), ptAt(zerosEnd), ptAt(cut), d, why)
	}
}

// cwPrepX creates x.bin and writes nothing to it.
func cwPrepX(t *testing.T, m *cwMount) vfs.Handle {
	t.Helper()
	x, _ := bpCreate(t, m.fs, "x.bin", 0)
	return x
}

// cwPrepDirtyX creates x.bin and writes 10 bytes new to the bucket at its
// offset 0, so that it has dirty data of its own.
func cwPrepDirtyX(t *testing.T, m *cwMount) vfs.Handle {
	t.Helper()
	x := cwPrepX(t, m)
	bpMustWrite(t, m.fs, "the Write of 10 bytes at 0 of x.bin", x, 0, bpUnique(10))
	return x
}

// cwStartWrite returns a start function for a Write of 10 bytes at 0 of x.bin
// with the given stability.
func cwStartWrite(how vfs.Stability) func(m *cwMount, x vfs.Handle) *bpCall[bpWR] {
	return func(m *cwMount, x vfs.Handle) *bpCall[bpWR] {
		return bpStartWrite(context.Background(), m.fs, testCaller, x, 0, bpUnique(10), how)
	}
}

// cwWantDone requires a Sync or a Commit to have succeeded.
func cwWantDone(t *testing.T, what string, r bpWR) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%s = %v, want success against a healthy writable store (ADR 0012 §9 item 2 "+
			"supposes that the call that runs the Sync succeeds)", what, r.err)
	}
}

// cwWantWrite returns a check that a Write of 10 bytes with the given
// stability succeeded. ADR 0012 and vfs.FS do not say which stability a Write
// answers, so the check asks only what RFC 1813 §3.3.7 requires, as
// write_sync_test.go does: FILE_SYNC for FILE_SYNC, DATA_SYNC or FILE_SYNC for
// DATA_SYNC, and anything for UNSTABLE.
func cwWantWrite(how vfs.Stability) func(t *testing.T, what string, r bpWR) {
	return func(t *testing.T, what string, r bpWR) {
		t.Helper()
		ok := r.err == nil && r.n == 10
		want := "any stability"
		switch how {
		case vfs.FileSync:
			ok = ok && r.how == vfs.FileSync
			want = "FILE_SYNC"
		case vfs.DataSync:
			ok = ok && (r.how == vfs.DataSync || r.how == vfs.FileSync)
			want = "DATA_SYNC or FILE_SYNC"
		}
		if !ok {
			t.Fatalf("%s = %v, want (10, %s, nil): a Write that succeeds against a healthy store "+
				"accepts every byte, and RFC 1813 §3.3.7 says what it may answer (ADR 0012 §9 item 2 "+
				"supposes that the call that runs the Sync succeeds)", what, r, want)
		}
	}
}

// cwRunFlushRow is one row of TestCommitWindowTruncateDuringFlushIsCommittedWhole.
// The mount under test has MaxDirtyBytes limit. prep, if set, makes x.bin
// before held.bin is written; start starts call, the call that runs the Sync;
// check requires what call returned. grow3 adds check (d).
func cwRunFlushRow(t *testing.T, limit int64, call string, grow3 bool, prep func(t *testing.T, m *cwMount) vfs.Handle, start func(m *cwMount, x vfs.Handle) *bpCall[bpWR], check func(t *testing.T, what string, r bpWR)) {
	t.Helper()
	m := cwNewMount(t, limit)
	var x vfs.Handle
	if prep != nil {
		x = prep(t, m)
	}
	m.createHeld(t)
	m.arm(t)
	c := start(m, x)
	cwWaitHeld(t, m, call, c)
	m.truncateA(t, cwCut, "while "+call+" is held in its first pass")
	m.gate.open()
	after := call + ", after hold 1 was opened"
	check(t, after, c.wait(t, after))

	why := "ADR 0012 §9 item 2: a truncate that cut into a stored chunk of a file the first pass " +
		"did not list, made while " + call + " was held in its first pass, is committed at its " +
		"size, and the file reads zeros where it removed bytes once it is grown"
	bpWantRemount(t, m.st, cwA, m.aData[:cwCut], "(a) a.bin, truncated to T while "+call+
		" was held (ADR 0012 §9 item 2: at the truncate's size with the bytes below it unchanged)")
	cwWantGrownZeros(t, m.st, cwA, cwCut, 2*bpCS, "(b) a.bin, truncated to T while "+call+
		" was held", why)

	fs2, h2, _ := wsRemountLookup(t, m.st, cwA)
	bpMustWrite(t, fs2, "(c) an UNSTABLE Write of 10 bytes at cs+2048 of a.bin on a fresh mount",
		h2, bpCS+2048, bpUnique(10))
	cwWantZeros(t, fs2, h2, cwCut, bpCS+2048, bpCS+2058, "(c) a.bin, truncated to T while "+call+
		" was held, then written with 10 bytes at cs+2048 on a fresh mount", why)

	if grow3 {
		cwWantGrownZeros(t, m.st, cwA, cwCut, 3*bpCS, "(d) a.bin, truncated to T while "+call+
			" was held", why)
	}
}

// TestCommitWindowTruncateDuringFlushIsCommittedWhole pins ADR 0012 §9 items
// 1 and 2. In each row a Sync is held in its first pass, run by a different
// call, and A, cold and so not on the first pass's list, is truncated to T
// while it is held. The truncate must return under the bound (item 1), and once
// the call has succeeded a fresh mount must find A at T with its seeded bytes
// below T (a), and read zeros past T once A is grown: by a SetAttr to 2cs (b),
// by a Write past T within the cut chunk (c), and, in the Sync row, by a
// SetAttr past the cut chunk to 3cs (d) (item 2). The code at 48aa23a commits
// T with the whole stored chunk 1, so (b), (c) and (d) read the removed bytes
// back (ADR 0012, Context, routes 1 and 2).
//
// The budget-drain row is set up as §9 says: a budget of one chunk; x.bin
// created with nothing in it; held.bin written last, so that its write fills
// the budget; hold 1 armed; then an UNSTABLE Write to x.bin, which finds the
// budget full and drains.
func TestCommitWindowTruncateDuringFlushIsCommittedWhole(t *testing.T) {
	t.Run("Sync", func(t *testing.T) {
		cwRunFlushRow(t, -1, "a Sync", true, nil, func(m *cwMount, _ vfs.Handle) *bpCall[bpWR] {
			return bpGo(func() bpWR { return bpWR{err: m.fs.Sync(context.Background())} })
		}, cwWantDone)
	})
	t.Run("Commit", func(t *testing.T) {
		cwRunFlushRow(t, -1, "a Commit of x.bin, which has dirty data of its own", false, cwPrepDirtyX,
			func(m *cwMount, x vfs.Handle) *bpCall[bpWR] {
				return bpGo(func() bpWR { return bpWR{err: m.fs.Commit(context.Background(), x, 0, 0)} })
			}, cwWantDone)
	})
	t.Run("FILE_SYNC Write", func(t *testing.T) {
		cwRunFlushRow(t, -1, "a FILE_SYNC Write of 10 bytes at 0 of x.bin", false, cwPrepX,
			cwStartWrite(vfs.FileSync), cwWantWrite(vfs.FileSync))
	})
	t.Run("DATA_SYNC Write", func(t *testing.T) {
		cwRunFlushRow(t, -1, "a DATA_SYNC Write of 10 bytes at 0 of x.bin", false, cwPrepX,
			cwStartWrite(vfs.DataSync), cwWantWrite(vfs.DataSync))
	})
	t.Run("budget drain", func(t *testing.T) {
		cwRunFlushRow(t, bpCS, "an UNSTABLE Write of 10 bytes at 0 of x.bin, which finds the budget "+
			"full and drains", false, cwPrepX, cwStartWrite(vfs.Unstable), cwWantWrite(vfs.Unstable))
	})
}

// TestCommitWindowWriteAfterTruncateDuringFlush pins ADR 0012 §9 item 3. While
// a Sync is held in its first pass, A is truncated to T and then written with
// 10 bytes at cs+2048, past T within the cut chunk. A fresh mount must find A
// at cs+2058, with its seeded bytes below T and zeros in [T, cs+2048), with no
// growth. The code at 48aa23a commits the Write's end as the size with the
// whole stored chunk 1, so [T, cs+2048) reads the removed bytes (Context,
// route 3). The written bytes are not checked.
func TestCommitWindowWriteAfterTruncateDuringFlush(t *testing.T) {
	m := cwNewMount(t, -1)
	m.createHeld(t)
	sc := m.holdSync(t)
	m.truncateA(t, cwCut, "while a Sync is held in its first pass")
	bpMustWrite(t, m.fs, "an UNSTABLE Write of 10 bytes at cs+2048 of a.bin, past T within the cut "+
		"chunk, while the Sync is held (ADR 0012 §9 item 3)", m.a, bpCS+2048, bpUnique(10))
	m.releaseSync(t, sc, "§9 item 3")
	cwWantUngrown(t, m.st, cwA, bpCS+2058, m.aData, cwCut, bpCS+2048, "a.bin, truncated to T and "+
		"then written with 10 bytes at cs+2048 while a Sync was held", "ADR 0012 §9 item 3: a fresh "+
		"mount reads zeros between the new end and the Write's offset; §1, Rule D: the chunk at the "+
		"truncate's end holds the bytes below the end")
}

// TestCommitWindowDirtyTailTruncatedDuringFlush pins ADR 0012 §9 item 4, the
// dirty tail. While a Sync is held in its first pass, A is written with 10
// bytes at cs+100, into its stored chunk 1, and then truncated to T, inside
// that chunk. A fresh mount must find A at T, and read zeros in [T, 2cs) once
// it is grown to 2cs. The code at 48aa23a commits T with the stored chunk 1,
// not the shortened buffer (Context, route 4). The written bytes are not
// checked.
func TestCommitWindowDirtyTailTruncatedDuringFlush(t *testing.T) {
	m := cwNewMount(t, -1)
	m.createHeld(t)
	sc := m.holdSync(t)
	bpMustWrite(t, m.fs, "an UNSTABLE Write of 10 bytes at cs+100 of a.bin, into its stored chunk 1, "+
		"while the Sync is held (ADR 0012 §9 item 4)", m.a, bpCS+100, bpUnique(10))
	m.truncateA(t, cwCut, "after a Write into its chunk 1, while a Sync is held in its first pass")
	m.releaseSync(t, sc, "§9 item 4")
	cwWantGrownZeros(t, m.st, cwA, cwCut, 2*bpCS, "a.bin, written at cs+100 and then truncated to T "+
		"while a Sync was held", "ADR 0012 §9 item 4: once the file is grown by a SetAttr, a fresh "+
		"mount reads zeros from the new end to the end of that chunk")
}

// TestCommitWindowFailedSettleCommitsNothing pins ADR 0012 §9 item 5's failing
// settle pass, reached as §9 says. While a Sync is held in its first pass, A
// is truncated to T, and the store's Get of A's stored chunk 1 is then made to
// fail with an error of the test's own; this mount has not read that chunk, so
// the settle pass's flush must fetch it from the store (ADR 0006 §7). The Sync
// must return an error that errors.Is finds the injected one in, and commit
// nothing: a fresh mount finds A at 2cs with its seeded bytes. With the store
// healthy again, a second Sync of the mount under test commits the cut, and a
// fresh mount then finds A at T, reading zeros in [T, 2cs) once grown. The
// code at 48aa23a has no settle pass, so its Sync makes no Get of that chunk
// and returns nil.
func TestCommitWindowFailedSettleCommitsNothing(t *testing.T) {
	m := cwNewMount(t, -1)
	key1 := bpChunkKey(t, m.st, m.aData[bpCS:2*bpCS])
	m.createHeld(t)
	sc := m.holdSync(t)
	m.truncateA(t, cwCut, "while a Sync is held in its first pass")
	injected := errors.New("commit window test: injected failure of the Get of a.bin's stored chunk 1")
	get := &bpHook{key: key1, err: injected}
	m.bps.setGet(get)
	m.gate.open()
	err := sc.wait(t, "the Sync, after hold 1 was opened")
	calls := m.bps.callsOf(get)
	m.bps.setGet(nil)
	m.bps.setPut(nil)
	if !errors.Is(err, injected) {
		t.Fatalf("the Sync, with every Get of a.bin's chunk 1 failing, = %v, want an error for which "+
			"errors.Is finds the injected one: before it commits it must upload the shortened chunk "+
			"that applies a truncate made during its first pass, and that flush fails (ADR 0012 §9 "+
			"item 5; §2: errors from the settle pass are wrapped with %%w); it made %d Gets of that "+
			"chunk's key", err, calls)
	}
	bpWantRemount(t, m.st, cwA, m.aData, "a.bin after the Sync whose settle pass failed (ADR 0012 §9 "+
		"item 5: it commits nothing, so a fresh mount finds the file as the previous commit left it)")

	bpMustSync(t, m.fs, "a second Sync of the mount under test, with the store healthy again (ADR "+
		"0012 §9 item 5)")
	cwWantGrownZeros(t, m.st, cwA, cwCut, 2*bpCS, "a.bin, truncated to T while a Sync was held, after "+
		"a second Sync with the store healthy", "ADR 0012 §9 items 2 and 5: the cut is committed "+
		"whole once its settle flush succeeds")
}

// TestCommitWindowTruncateUndoneDuringFlushStillCommits pins ADR 0012 §9 item
// 7. While a Sync is held in its first pass, A is truncated to T, which cuts
// into its stored chunk 1, and then to cs, the start of that chunk. The Sync
// must succeed, and a fresh mount must find A at cs, reading zeros in [cs, 2cs)
// once it is grown to 2cs. The code at 48aa23a passes this.
func TestCommitWindowTruncateUndoneDuringFlushStillCommits(t *testing.T) {
	m := cwNewMount(t, -1)
	m.createHeld(t)
	sc := m.holdSync(t)
	m.truncateA(t, cwCut, "while a Sync is held in its first pass")
	m.truncateA(t, bpCS, "after a truncate to T, while a Sync is held in its first pass")
	m.releaseSync(t, sc, "§9 item 7")
	cwWantGrownZeros(t, m.st, cwA, bpCS, 2*bpCS, "a.bin, truncated to T and then to cs while a Sync "+
		"was held", "ADR 0012 §9 item 7: the Sync succeeds and a fresh mount finds the file at the "+
		"second truncate's size; §1, Rule D: with no byte the truncates removed")
}

// TestCommitWindowTruncateDuringSettleIsWholeOrAbsent pins ADR 0012 §9 items
// 5 and 6. A, D and G are seeded at 2cs through other mounts and the bucket is
// mounted afresh; nothing on that mount touches D or G before the steps below.
// On the mount, C is created as in the other tests, E is created and removed,
// so that its handle is stale, and F is written with 10 bytes.
//
//  1. Hold 1 is armed and a Sync started; while it is held, A is truncated
//     to T.
//  2. Hold 2 is armed on the Put of A's shortened chunk 1, [cs, T) of its
//     seeded bytes, under the key ADR 0004 §5 gives it. setPut replaces the
//     hook; the Put already held keeps waiting on hold 1's gate.
//  3. Hold 1 is opened, and the Sync must enter hold 2 (item 5). The code at
//     48aa23a has no settle pass, so its Sync returns first.
//  4. A truncate of D to T is started, and waited for until it returns or
//     cwSettleGrace passes.
//  5. While hold 2 is held, each call item 6 lists must return under the
//     bound with the answer it gives.
//  6. Hold 2 is opened, and the Sync and D's truncate must both succeed.
//
// A fresh mount must then find A at T, reading zeros in [T, 2cs) once grown;
// and D either at 2cs with its seeded bytes, or at T with its seeded bytes
// below T and zeros in [T, 2cs) once grown (item 6).
func TestCommitWindowTruncateDuringSettleIsWholeOrAbsent(t *testing.T) {
	st := bpLocal(t)
	aData := bpSeedFile(t, st, cwA, 2*bpCS)
	dData := cwSeed(t, st, cwD, 2*bpCS, 0o644)
	gData := bpSeedFile(t, st, cwG, 2*bpCS)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, -1, quietLog())
	a, aAttr := bpLookup(t, fs, cwA)
	d, dAttr := bpLookup(t, fs, cwD)
	g, gAttr := bpLookup(t, fs, cwG)
	m := &cwMount{st: st, bps: bps, fs: fs, a: a, aID: aAttr.FileID, aData: aData}

	m.createHeld(t)
	e, _ := bpCreate(t, fs, "e.bin", 0)
	bpRemove(t, fs, "e.bin", "Remove e.bin, so that its handle is stale")
	f, _ := bpCreate(t, fs, "f.bin", 0)
	bpMustWrite(t, fs, "the Write of 10 bytes at 0 of f.bin", f, 0, bpUnique(10))
	cwWantNotOpen(t, fs, dAttr.FileID, cwD, "before the Sync starts")
	cwWantNotOpen(t, fs, gAttr.FileID, cwG, "before the Sync starts")

	// Step 1.
	sc := m.holdSync(t)
	m.truncateA(t, cwCut, "while a Sync is held in its first pass")

	// Step 2.
	gate2 := newBTGate(t)
	put2 := &bpHook{key: cwKey(aData[bpCS:cwCut]), entered: make(chan struct{}), gate: gate2.ch}
	bps.setPut(put2)

	// Step 3.
	m.gate.open()
	select {
	case <-put2.entered:
	case <-sc.done:
		t.Fatalf("the Sync ended, returning %v (panic: %v), without uploading a.bin's shortened chunk "+
			"1, [cs, T) of its seeded bytes: the Sync committed without applying a truncate made "+
			"during its first pass (ADR 0012 §9 item 5)", sc.val, sc.pval)
	case <-time.After(btHangBound):
		t.Fatalf("the Sync neither entered the Put of a.bin's shortened chunk 1 nor returned within "+
			"%v after hold 1 was opened (ADR 0012 §9 item 5)", btHangBound)
	}

	// Step 4.
	dc := bpGo(mfsSetSize(fs, d, cwCut))
	select {
	case <-dc.done:
	case <-time.After(cwSettleGrace):
	}

	// Step 5.
	held := " while the Sync is held in its settle upload (ADR 0012 §9 item 6: it returns)"
	what := "SetAttr(size MaxFileSize()+1) of d.bin" + held
	mfsWantFBig(t, what, bpDo(t, what, func() error {
		size := fs.MaxFileSize() + 1
		_, err := fs.SetAttr(context.Background(), testCaller, d, vfs.SetAttr{Size: &size})
		return err
	}))

	what = "SetAttr(size T) of e.bin's stale handle" + held
	if err := bpDo(t, what, mfsSetSize(fs, e, cwCut)); !errors.Is(err, vfs.ErrStale) {
		t.Errorf("%s = %v, want vfs.ErrStale (ADR 0012 §9 item 6)", what, err)
	}

	what = "SetAttr(size T) of d.bin, mode 0644, by a caller who is neither its owner nor in its group" +
		held
	err := bpDo(t, what, func() error {
		size := uint64(cwCut)
		_, err := fs.SetAttr(context.Background(), rcOther, d, vfs.SetAttr{Size: &size})
		return err
	})
	if !errors.Is(err, vfs.ErrAcces) {
		t.Errorf("%s = %v, want vfs.ErrAcces (ADR 0012 §9 item 6)", what, err)
	}

	what = "SetAttr(mode 0640) of d.bin, which sets no size" + held
	err = bpDo(t, what, func() error {
		mode := uint32(0o640)
		_, err := fs.SetAttr(context.Background(), testCaller, d, vfs.SetAttr{Mode: &mode})
		return err
	})
	if err != nil {
		t.Errorf("%s = %v, want success (ADR 0012 §9 item 6)", what, err)
	}

	bpMustWrite(t, fs, "an UNSTABLE Write of 10 bytes at 0 of f.bin, with the budget disabled"+held,
		f, 0, bpUnique(10))

	what = "one Read of [0, 1024) of g.bin, which has no FS.open entry" + held
	r := rcStartRead(fs, g, 0, 1024).wait(t, what)
	ptWantRead(t, what, r, gData[:1024], false, "ADR 0012 §9 item 6: a Read of a file with no "+
		"FS.open entry returns while a Sync is held in its settle upload")

	// Step 6.
	gate2.open()
	if err := sc.wait(t, "the Sync, after hold 2 was opened"); err != nil {
		t.Errorf("the Sync, after hold 2 was opened, = %v, want success against a healthy writable "+
			"store (ADR 0012 §9 items 5 and 6)", err)
	}
	if err := dc.wait(t, "SetAttr(size T) of d.bin, started while the Sync was held in its settle "+
		"upload, after hold 2 was opened"); err != nil {
		t.Errorf("SetAttr(size T) of d.bin, started while the Sync was held in its settle upload, = "+
			"%v, want success (ADR 0012 §9 item 6: once both have returned)", err)
	}

	cwWantGrownZeros(t, st, cwA, cwCut, 2*bpCS, "a.bin, truncated to T while the Sync was held in its "+
		"first pass", "ADR 0012 §9 items 2 and 5: the Sync's settle pass applied the cut before it "+
		"committed")
	cwWantWholeOrAbsent(t, st, cwD, dData, cwCut, "d.bin, truncated to T while the Sync was held in "+
		"its settle upload")
}

// cwWantWholeOrAbsent requires a fresh mount of st to find name either as it
// was seeded, old, or at cut with old's bytes below cut and zeros in
// [cut, len(old)) once it is grown back to len(old) (ADR 0012 §9 item 6).
func cwWantWholeOrAbsent(t *testing.T, st *store.Local, name string, old []byte, cut uint64, what string) {
	t.Helper()
	fs2, h2, attr := wsRemountLookup(t, st, name)
	full := uint64(len(old))
	switch attr.Size {
	case full:
		bpWantFile(t, fs2, h2, old, what+", found at its old size on a fresh mount (ADR 0012 §9 item "+
			"6: at its old size, with its old bytes)")
	case cut:
		bpWantFile(t, fs2, h2, old[:cut], what+", found at the truncate's size on a fresh mount (ADR "+
			"0012 §9 item 6: at its new size; §1, Rule D: with the bytes below the end)")
		bpTruncate(t, fs2, h2, full, fmt.Sprintf("%s: SetAttr(size %s) on a fresh mount", what,
			ptAt(full)))
		cwWantZeros(t, fs2, h2, cut, full, full, what+", grown to "+ptAt(full)+" on a fresh mount",
			"ADR 0012 §9 item 6: at its new size, reading zeros past the new end after it is grown")
	default:
		t.Errorf("%s: size on a fresh mount = %s, want either its old size, %s, or the truncate's, %s "+
			"(ADR 0012 §9 item 6: the truncate is in the commit whole or not at all)", what,
			ptAt(attr.Size), ptAt(full), ptAt(cut))
	}
}

// cwGrowthAfterCut is one subtest of TestCommitWindowGrowthAfterTruncateDuringFlush:
// while a Sync is held in its first pass, A is truncated to T and then grown
// past the cut chunk by grow, to size.
func cwGrowthAfterCut(t *testing.T, size uint64, grow func(t *testing.T, m *cwMount)) {
	t.Helper()
	m := cwNewMount(t, -1)
	m.createHeld(t)
	sc := m.holdSync(t)
	m.truncateA(t, cwCut, "while a Sync is held in its first pass")
	grow(t, m)
	m.releaseSync(t, sc, "§9 item 8")
	cwWantUngrown(t, m.st, cwA, size, m.aData, cwCut, 2*bpCS, fmt.Sprintf("a.bin, truncated to T and "+
		"then grown to %s while a Sync was held", ptAt(size)), "ADR 0012 §9 item 8: a fresh mount "+
		"finds the file at the grown size and reads zeros from the cut to the end of the cut chunk, "+
		"with no further growth; §1, Rule D: the bytes below the cut are unchanged")
}

// TestCommitWindowGrowthAfterTruncateDuringFlush pins ADR 0012 §9 item 8.
// While a Sync is held in its first pass, A is truncated to T and then grown
// past the cut chunk: (i) by a SetAttr to 2cs+2048, or (ii) by an UNSTABLE
// Write of 10 bytes at 2cs+808, each with its own bucket and mounts. The Sync
// must succeed, and a fresh mount must find A at the grown size, with its
// seeded bytes below T and zeros in [T, 2cs), with no further growth. The
// code at 48aa23a commits the grown size with the whole chunk 1 (Context,
// route 5). This catches a mark recomputed from the size and the chunk list
// instead of kept from the cut (Assumption 9). The written bytes are not
// checked.
func TestCommitWindowGrowthAfterTruncateDuringFlush(t *testing.T) {
	t.Run("SetAttr", func(t *testing.T) {
		cwGrowthAfterCut(t, 2*bpCS+2048, func(t *testing.T, m *cwMount) {
			bpTruncate(t, m.fs, m.a, 2*bpCS+2048, "SetAttr(size 2cs+2048) of a.bin, past the cut chunk, "+
				"while the Sync is held (ADR 0012 §9 item 8)")
		})
	})
	t.Run("Write", func(t *testing.T) {
		cwGrowthAfterCut(t, 2*bpCS+818, func(t *testing.T, m *cwMount) {
			bpMustWrite(t, m.fs, "an UNSTABLE Write of 10 bytes at 2cs+808 of a.bin, past the cut "+
				"chunk, while the Sync is held (ADR 0012 §9 item 8)", m.a, 2*bpCS+808, bpUnique(10))
		})
	})
}
