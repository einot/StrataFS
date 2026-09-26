package blobfs

// Clean-room tests for ADR 0007, "A file is at most 2^20 chunks long, and no
// call takes it further" (docs/adr/0007-maximum-file-size.md), which are the
// tests for #55 and #66. They are written from that ADR, from ADR 0003 §2, §3
// and §4 (its test surface), ADR 0005 §2, ADR 0006 §8, and
// internal/vfs/vfs.go (MaxFileSize, SetAttr, Write, Create), without reading
// the implementation. They cover:
//
//   - §1, the limit: 2^20 times the chunk size the mount uses, the adopted one
//     for an existing bucket, and inclusive;
//   - §3 and §4, SetAttr: a size above the limit fails with vfs.ErrFBig,
//     whatever the file's current size, applies none of sa, and takes no lock;
//   - §3 and §4, Write: a write of at least one byte at or past the limit is
//     refused and changes nothing, including a write whose end runs past 2^64
//     (#66); one that crosses the limit is short; an empty write is never
//     refused for its offset; a refused write takes no lock and never waits
//     on the budget, and a refused stable write syncs nothing;
//   - §3 and §4, Create: a sa.Size above the limit fails before anything is
//     created or changed, whether or not the name exists; Mkdir and Symlink
//     accept any sa.Size;
//   - §4's order: a read-only mount's vfs.ErrROFS (reached as §7's "A
//     read-only mount" says), a diverged mount's vfs.ErrStale and a bad
//     name's vfs.ErrInval come before vfs.ErrFBig, and vfs.ErrFBig comes before a
//     stale handle, a directory, a permission failure, a parent that is not a
//     directory and an existing name;
//   - §6, a file an earlier build left larger, and the record New logs for it;
//   - §7, the test surface: FS.maxFileSize, set after New and before first
//     use, stands for a build with another limit.
//
// "Touched nothing" (mfsWantUnchanged) is §4's list and §7's: GetAttr's Size,
// Used, Mode, NLink, UID, GID, MTime and CTime; DirtyBytes(); the file's
// FS.open entry, or its absence; its dirty buffers and pending trims, read
// through bpSnapshot; its contents; and ADR 0003 §2's accounting invariant.
// What a fresh mount finds is checked after a Sync once every refusal has been
// confirmed.
//
// ADR 0007 §7 says how a test stays safe against a build that compiles but
// lacks a check, which turns an over-limit request into a chunk list as long
// as the request asks for. Every test here follows it:
//   - the first over-limit request in a test uses the cheapest value, a size of
//     MaxFileSize + 1 or a write at MaxFileSize, and a wrong answer to it fails
//     the test at once (t.Fatalf); only after that do 2^63, 2^64 − 1, 2^64 − 10
//     or 2^64 − 20 follow;
//   - nothing Syncs, Commits, remounts or makes a stable write after a write
//     at or past the limit until that write is known to have been refused, and
//     every FS mounted here with mfsNew has a commit interval of an hour, so
//     its ticker never flushes on its own;
//   - a call made while the test holds FS.mu, or an openFile.mu and FS.mu, that
//     has not returned within btHangBound fails the test with the locks still
//     held (mfsRefusedUnderLocks), so that it cannot go on to build the list;
//   - few steps build 2^20 chunk references (about 24 MiB, and an 11 MiB
//     snapshot at 4 KiB chunks): a SetAttr to exactly the limit, the flush of a
//     FILE_SYNC write just below it and the fresh mount after it, and the
//     setup of the mount record.
//
// Every FS call runs under btHangBound, through the bp* helpers or bpGo, and
// goroutines never touch t.
//
// Not covered, on purpose:
//   - ErrNameTooLong before ErrFBig: no document here gives the name limit.
//   - Cloner.CloneRange (§3): nothing implements it.
//   - That truncate's and flushOpen's fills are unchanged and bounded by
//     admission (§4, Assumption 10): nothing observable distinguishes them.
//   - What the NFS server does: internal/nfs/max_file_size_test.go.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// mfsMax is MaxFileSize for every FS here whose chunk size is bpCS:
// 2^20 × 4096 = 2^32 (ADR 0007 §1).
const mfsMax uint64 = bpCS << 20

// mfsMsgLarger is the message of ADR 0007 §6's record.
const mfsMsgLarger = "files larger than the maximum file size"

// mfsRule is what a refused call must leave alone, for failure messages.
const mfsRule = "ADR 0007 §4: a refused call adds no FS.open entry, neither waits on the budget " +
	"nor charges it, and changes no size, time, mode, chunk list, dirty buffer or pending trim; " +
	"§7: afterwards the file's attributes, DirtyBytes(), its FS.open entry or its absence, its " +
	"dirty buffers and pending trims, and its contents are all as they were"

// mfsMount runs New with cfg.
func mfsMount(t *testing.T, what string, cfg Config) *FS {
	t.Helper()
	fs, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	return fs
}

// mfsNew mounts an FS on st with bpCS chunks, the default budget, pruning off,
// and a commit interval of an hour, so that nothing flushes unless a test asks
// for it (ADR 0007 §7: no flush after a far write not known to be refused).
func mfsNew(t *testing.T, st store.Store, lg *slog.Logger) *FS {
	t.Helper()
	return mfsMount(t, "New with 4096-byte chunks", Config{
		Store:             st,
		ChunkSize:         bpCS,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		CommitInterval:    time.Hour,
		Log:               lg,
	})
}

// mfsWantFBig requires err to be vfs.ErrFBig, and fails the test at once
// otherwise (ADR 0007 §7: a wrong answer to an over-limit request ends the
// test before anything larger is sent).
func mfsWantFBig(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, vfs.ErrFBig) {
		t.Fatalf("%s = %v, want vfs.ErrFBig (ADR 0007 §3, §4)", what, err)
	}
}

// mfsWantRefusedWrite requires r, one Write, to be ADR 0007 §7's refusal,
// (0, 0, vfs.ErrFBig), a zero vfs.Stability being vfs.Unstable. It fails the
// test at once otherwise, so that nothing follows a write that a build without
// the check has taken (§7).
func mfsWantRefusedWrite(t *testing.T, what string, r bpWR) {
	t.Helper()
	if !errors.Is(r.err, vfs.ErrFBig) || r.n != 0 || r.how != vfs.Unstable {
		t.Fatalf("%s = %v, want (0, UNSTABLE, vfs.ErrFBig): a write of at least one byte that "+
			"starts at or past MaxFileSize fails with ErrFBig and changes nothing (ADR 0007 §3, "+
			"§4), and a refused Write returns (0, 0, vfs.ErrFBig) (§7)", what, r)
	}
}

// mfsState is what mfsWantUnchanged compares: a file's attributes,
// DirtyBytes(), the file's FS.open entry (nil if it has none) and, if it has
// one, that entry's accounting state, and the file's contents.
type mfsState struct {
	attr  vfs.Attr
	dirty int64
	of    *openFile
	snap  bpOFSnap
	data  []byte
}

// mfsCapture reads h's state. It reads the whole file, so it is used only on
// small files.
func mfsCapture(t *testing.T, fs *FS, h vfs.Handle, what string) mfsState {
	t.Helper()
	s := mfsState{attr: bpGetAttr(t, fs, h, what+": GetAttr")}
	s.dirty = fs.DirtyBytes()
	if of := bpOpen(t, fs, s.attr.FileID, what); of != nil {
		s.of = of
		s.snap = bpSnapshot(t, of, what)
	}
	s.data = bpReadAll(t, fs, h, what+": reading the file")
	return s
}

// mfsWantUnchanged requires h's state to be before's, after a call that ADR
// 0007 says is refused, and ADR 0003 §2's accounting to hold.
func mfsWantUnchanged(t *testing.T, fs *FS, h vfs.Handle, before mfsState, what string) {
	t.Helper()
	after := mfsCapture(t, fs, h, "after "+what)
	b, a := before.attr, after.attr
	if a.Size != b.Size || a.Used != b.Used || a.Mode != b.Mode || a.NLink != b.NLink ||
		a.UID != b.UID || a.GID != b.GID || !a.MTime.Equal(b.MTime) || !a.CTime.Equal(b.CTime) {
		t.Errorf("%s changed the file's attributes: before, size %d, used %d, mode %#o, nlink %d, "+
			"uid %d, gid %d, mtime %v, ctime %v; after, size %d, used %d, mode %#o, nlink %d, "+
			"uid %d, gid %d, mtime %v, ctime %v. %s", what,
			b.Size, b.Used, b.Mode, b.NLink, b.UID, b.GID, b.MTime, b.CTime,
			a.Size, a.Used, a.Mode, a.NLink, a.UID, a.GID, a.MTime, a.CTime, mfsRule)
	}
	if after.dirty != before.dirty {
		t.Errorf("%s changed DirtyBytes() from %d to %d. %s", what, before.dirty, after.dirty, mfsRule)
	}
	switch {
	case before.of == nil && after.of != nil:
		t.Errorf("%s added an FS.open entry for inode %d, which had none. %s", what, b.FileID, mfsRule)
	case before.of != nil && after.of != before.of:
		t.Errorf("%s replaced or removed inode %d's FS.open entry. %s", what, b.FileID, mfsRule)
	case before.of != nil:
		o, s := before.snap, after.snap
		if s.bytes != o.bytes || s.pending != o.pending || !maps.Equal(s.caps, o.caps) {
			t.Errorf("%s changed inode %d's open-file state: before, of.bytes %d, of.dirty "+
				"indices %v, len(of.pendingTrim) %d; after, %d, %v, %d. %s", what, b.FileID,
				o.bytes, o.indices(), o.pending, s.bytes, s.indices(), s.pending, mfsRule)
		}
	}
	if d := wsDiff(after.data, before.data, 512); d != "" {
		t.Errorf("%s changed the file's contents: %s. %s", what, d, mfsRule)
	}
	bpCheckAccounting(t, fs, "after "+what)
}

// mfsWantNoName requires a Lookup of name in fs's root to fail with
// vfs.ErrNoEnt.
func mfsWantNoName(t *testing.T, fs *FS, name, what string) {
	t.Helper()
	err := bpDo(t, "Lookup of "+name+" "+what, func() error {
		_, _, err := fs.Lookup(context.Background(), testCaller, fs.Root(), name)
		return err
	})
	if !errors.Is(err, vfs.ErrNoEnt) {
		t.Errorf("Lookup of %s %s = %v, want vfs.ErrNoEnt", name, what, err)
	}
}

// mfsSetSize returns a call to SetAttr of h's size alone, as testCaller.
func mfsSetSize(fs *FS, h vfs.Handle, size uint64) func() error {
	return func() error {
		_, err := fs.SetAttr(context.Background(), testCaller, h, vfs.SetAttr{Size: &size})
		return err
	}
}

// mfsWriteRefusal returns a call to Write, as testCaller, whose error is the
// Write's own only when the Write returned ADR 0007 §7's refusal,
// (0, 0, vfs.ErrFBig); any other result is reported as an error that is not
// vfs.ErrFBig.
func mfsWriteRefusal(fs *FS, h vfs.Handle, off uint64, data []byte, how vfs.Stability) func() error {
	return func() error {
		n, got, err := fs.Write(context.Background(), testCaller, h, off, data, how)
		if errors.Is(err, vfs.ErrFBig) && n == 0 && got == vfs.Unstable {
			return err
		}
		return fmt.Errorf("the Write returned %v, where a refused Write returns (0, UNSTABLE, "+
			"vfs.ErrFBig)", bpWR{n, got, err})
	}
}

// mfsCall is one call that mfsRefusedUnderLocks makes.
type mfsCall struct {
	what string
	fn   func() error
}

// mfsRefusedUnderLocks takes of.mu, if of is not nil, and then FS.mu for
// writing, in ADR 0003 §3's order, and makes each call in its own goroutine
// while it holds them. Each must return vfs.ErrFBig within btHangBound: a
// refused call takes no lock, neither FS.mu nor any openFile.mu (ADR 0007 §4,
// §7). A call that has not returned fails the test with the locks still held,
// so that a build without the check cannot go on to build the chunk list the
// call asks for (§7). Otherwise the locks are released in reverse order.
func mfsRefusedUnderLocks(t *testing.T, fs *FS, of *openFile, held string, calls ...mfsCall) {
	t.Helper()
	if of != nil {
		of.mu.Lock()
	}
	fs.mu.Lock()
	unlock := func() {
		fs.mu.Unlock()
		if of != nil {
			of.mu.Unlock()
		}
	}
	for _, call := range calls {
		c := bpGo(call.fn)
		select {
		case <-c.done:
		case <-time.After(btHangBound):
			t.Fatalf("%s, made while the test holds %s, has not returned after %v: a refused call "+
				"takes no lock, neither FS.mu nor any openFile.mu, and returns while another "+
				"goroutine holds them (ADR 0007 §4, §7). The locks are left held, so that the call "+
				"cannot go on to build the chunk list it asks for (§7)", call.what, held, btHangBound)
		}
		if c.panicked {
			unlock()
			t.Fatalf("%s, made while the test holds %s, panicked with %v", call.what, held, c.pval)
		}
		if !c.returned {
			unlock()
			t.Fatalf("%s, made while the test holds %s, ended without returning (runtime.Goexit)",
				call.what, held)
		}
		if !errors.Is(c.val, vfs.ErrFBig) {
			unlock()
			t.Fatalf("%s, made while the test holds %s, = %v, want vfs.ErrFBig (ADR 0007 §3, §4)",
				call.what, held, c.val)
		}
	}
	unlock()
}

// mfsWantKind requires r to have a top-level attribute key of the given kind,
// and returns it.
func mfsWantKind(t *testing.T, r slog.Record, key string, kind slog.Kind) (slog.Value, bool) {
	t.Helper()
	v, ok := btAttr(r, key)
	switch {
	case !ok:
		t.Errorf("the %q record has no top-level attribute %q; ADR 0007 §6 gives it one, and its "+
			"attributes are top-level, not grouped", mfsMsgLarger, key)
		return v, false
	case v.Kind() != kind:
		t.Errorf("the %q record's attribute %q is of kind %v, want %v (ADR 0007 §6)",
			mfsMsgLarger, key, v.Kind(), kind)
		return v, false
	}
	return v, true
}

// TestMaxFileSizeFollowsTheChunkSize pins ADR 0007 §1, "MaxFileSize = 2^20 ×
// cs, where cs is the chunk size the mount uses: for an existing filesystem,
// the one New adopts from the bucket in loadOrInit, whatever the configuration
// asked for; for a new one, the configured chunk size", its table, and §7:
// "MaxFileSize() is 2^20 times the chunk size the mount uses", and New sets
// FS.maxFileSize, which MaxFileSize returns.
func TestMaxFileSizeFollowsTheChunkSize(t *testing.T) {
	fs := bpNew(t, bpLocal(t), 0, quietLog())
	var v vfs.FS = fs
	if got := v.MaxFileSize(); got != 1<<32 {
		t.Errorf("MaxFileSize() of a new FS with 4096-byte chunks = %d, want 2^32 = %d (ADR 0007 §1)",
			got, uint64(1<<32))
	}
	if fs.maxFileSize != fs.MaxFileSize() {
		t.Errorf("FS.maxFileSize = %d but MaxFileSize() = %d; ADR 0007 §7: MaxFileSize returns it",
			fs.maxFileSize, fs.MaxFileSize())
	}

	fs8k := mfsMount(t, "New with ChunkSize 8192 on an empty bucket", Config{
		Store:     bpLocal(t),
		ChunkSize: 8192,
		Log:       quietLog(),
	})
	if got := fs8k.MaxFileSize(); got != 1<<33 {
		t.Errorf("MaxFileSize() of a new FS with 8192-byte chunks = %d, want 2^33 = %d (ADR 0007 §1)",
			got, uint64(1<<33))
	}

	fsDefault := mfsMount(t, "New with ChunkSize 0, the default of 1 MiB, on an empty bucket", Config{
		Store:     bpLocal(t),
		ChunkSize: 0,
		Log:       quietLog(),
	})
	if got := fsDefault.MaxFileSize(); got != 1<<40 {
		t.Errorf("MaxFileSize() of a new FS with the default chunk size, 1 MiB, = %d, want 2^40 = %d "+
			"(ADR 0007 §1's table)", got, uint64(1<<40))
	}

	st := bpLocal(t)
	bpSeedFile(t, st, "seed.bin", 100)
	adopted := mfsMount(t, "New with ChunkSize 8192 on a bucket created with 4096-byte chunks", Config{
		Store:     st,
		ChunkSize: 8192,
		Log:       quietLog(),
	})
	if got := adopted.MaxFileSize(); got != 1<<32 {
		t.Errorf("MaxFileSize() of a mount configured with 8192-byte chunks, of a bucket created with "+
			"4096-byte chunks, = %d, want 2^32 = %d: the limit follows the chunk size New adopts "+
			"from the bucket, whatever the configuration asked for (ADR 0007 §1, §7)",
			got, uint64(1<<32))
	}
}

// TestMaxFileSizeSetAttrAboveTheLimit pins ADR 0007 §3, "SetAttr. A sa.Size
// above MaxFileSize fails with ErrFBig, whatever the file's current size, and
// none of sa is applied", §4's truncate bullet and its list of what a refused
// call leaves alone, and §1, "The limit is inclusive: a file may be exactly
// MaxFileSize bytes long" (route 1 of #55).
//
// warm.bin has 100 unsynced bytes, so it has an FS.open entry; cold.bin was
// seeded through another mount and is never written on this FS, so it has
// none. Each is refused and touches nothing for a size of 2^32 + 1, for the
// same size with mode 0600 (the mode stays as it was), and then for 2^63 and
// 2^64 − 1. After a Sync, a fresh mount finds both files as they were. Then
// cold.bin is set to exactly 2^32, and its last byte reads as a zero with eof.
func TestMaxFileSizeSetAttrAboveTheLimit(t *testing.T) {
	st := bpLocal(t)
	coldData := bpSeedFile(t, st, "cold.bin", 100)
	fs := mfsNew(t, st, quietLog())
	cold, coldAttr := bpLookup(t, fs, "cold.bin")
	warm, warmAttr := bpCreate(t, fs, "warm.bin", 0o644)
	warmData := bpUnique(100)
	bpMustWrite(t, fs, "the Write of 100 bytes at 0 of warm.bin, which gives it an FS.open entry "+
		"(ADR 0005 §4)", warm, 0, warmData)
	bpMustOpen(t, fs, warmAttr.FileID, "setup: warm.bin after its Write")
	if of := bpOpen(t, fs, coldAttr.FileID, "setup: cold.bin before any call"); of != nil {
		t.Fatalf("setup: cold.bin has an FS.open entry on a fresh mount before anything wrote or " +
			"truncated it; this test needs a file without one (ADR 0005 §4)")
	}

	type file struct {
		name string
		h    vfs.Handle
	}
	files := []file{{"warm.bin", warm}, {"cold.bin", cold}}
	sizeOf := func(n uint64) *uint64 { return &n }
	refuse := func(f file, sa vfs.SetAttr, desc string) {
		t.Helper()
		what := fmt.Sprintf("SetAttr of %s with %s", f.name, desc)
		before := mfsCapture(t, fs, f.h, "before "+what)
		err := bpDo(t, what, func() error {
			_, err := fs.SetAttr(context.Background(), testCaller, f.h, sa)
			return err
		})
		mfsWantFBig(t, what, err)
		mfsWantUnchanged(t, fs, f.h, before, what)
	}

	for _, f := range files {
		refuse(f, vfs.SetAttr{Size: sizeOf(mfsMax + 1)}, "size MaxFileSize + 1 = 2^32 + 1")
	}
	mode := uint32(0o600)
	for _, f := range files {
		if a := bpGetAttr(t, fs, f.h, "setup: GetAttr of "+f.name); a.Mode == mode {
			t.Fatalf("setup: %s's mode is already 0600, so a SetAttr that applied it could not be "+
				"told from one that did not", f.name)
		}
		refuse(f, vfs.SetAttr{Size: sizeOf(mfsMax + 1), Mode: &mode},
			"size 2^32 + 1 and mode 0600, of which none is applied")
	}
	for _, f := range files {
		refuse(f, vfs.SetAttr{Size: sizeOf(1 << 63)}, "size 2^63")
		refuse(f, vfs.SetAttr{Size: sizeOf(math.MaxUint64)}, "size 2^64 − 1")
	}

	bpMustSync(t, fs, "Sync after the refused SetAttrs")
	bpWantRemount(t, st, "warm.bin", warmData, "warm.bin after refused SetAttrs of its size and a Sync")
	bpWantRemount(t, st, "cold.bin", coldData, "cold.bin after refused SetAttrs of its size and a Sync")

	bpTruncate(t, fs, cold, mfsMax, "SetAttr of cold.bin to size MaxFileSize, 2^32, exactly (ADR "+
		"0007 §1: the limit is inclusive)")
	if a := bpGetAttr(t, fs, cold, "GetAttr of cold.bin after its size was set to 2^32"); a.Size != mfsMax {
		t.Errorf("cold.bin's size after a SetAttr to 2^32 = %d, want %d", a.Size, mfsMax)
	}
	r := rcStartRead(fs, cold, mfsMax-1, 1).wait(t, "the Read of 1 byte at 2^32 − 1 of cold.bin")
	ptWantRead(t, "the Read of 1 byte at 2^32 − 1 of cold.bin, grown to 2^32 by SetAttr", r,
		[]byte{0}, true, "a file grown with SetAttr reads as zeros past its old end")
}

// TestMaxFileSizeSetAttrTakesNoLock pins ADR 0007 §4, "A refused call takes no
// lock, neither FS.mu nor any openFile.mu", and §7, "A refused call returns
// while another goroutine holds FS.mu for writing and the file's openFile.mu",
// reached as §7 says: the test takes the file's openFile.mu, if it has an
// FS.open entry, then FS.mu for writing, and makes the call in another
// goroutine under a bound. Sizes 2^32 + 1 and then 2^64 − 1 are refused, first
// for cold.bin, which has no FS.open entry, with FS.mu held, and then for
// warm.bin, which has one, with its openFile.mu and FS.mu held. A call that
// hangs fails the test with the locks still held (§7).
func TestMaxFileSizeSetAttrTakesNoLock(t *testing.T) {
	st := bpLocal(t)
	bpSeedFile(t, st, "cold.bin", 100)
	fs := mfsNew(t, st, quietLog())
	cold, coldAttr := bpLookup(t, fs, "cold.bin")
	warm, warmAttr := bpCreate(t, fs, "warm.bin", 0)
	bpMustWrite(t, fs, "the Write of 100 bytes at 0 of warm.bin, which gives it an FS.open entry "+
		"(ADR 0005 §4)", warm, 0, bpUnique(100))
	if of := bpOpen(t, fs, coldAttr.FileID, "setup: cold.bin before any call"); of != nil {
		t.Fatalf("setup: cold.bin has an FS.open entry on a fresh mount before anything wrote or " +
			"truncated it; this test needs a file without one (ADR 0005 §4)")
	}
	of := bpMustOpen(t, fs, warmAttr.FileID, "setup: warm.bin after its Write")

	coldBefore := mfsCapture(t, fs, cold, "before the SetAttrs of cold.bin")
	mfsRefusedUnderLocks(t, fs, nil, "FS.mu for writing",
		mfsCall{"SetAttr of cold.bin, which has no FS.open entry, to size 2^32 + 1", mfsSetSize(fs, cold, mfsMax+1)},
		mfsCall{"SetAttr of cold.bin, which has no FS.open entry, to size 2^64 − 1", mfsSetSize(fs, cold, math.MaxUint64)},
	)
	mfsWantUnchanged(t, fs, cold, coldBefore, "the refused SetAttrs of cold.bin under FS.mu")

	warmBefore := mfsCapture(t, fs, warm, "before the SetAttrs of warm.bin")
	mfsRefusedUnderLocks(t, fs, of, "warm.bin's openFile.mu and then FS.mu for writing",
		mfsCall{"SetAttr of warm.bin, which has an FS.open entry, to size 2^32 + 1", mfsSetSize(fs, warm, mfsMax+1)},
		mfsCall{"SetAttr of warm.bin, which has an FS.open entry, to size 2^64 − 1", mfsSetSize(fs, warm, math.MaxUint64)},
	)
	mfsWantUnchanged(t, fs, warm, warmBefore, "the refused SetAttrs of warm.bin under its openFile.mu and FS.mu")
}

// TestMaxFileSizeWriteAtOrPastTheLimit pins ADR 0007 §3, "Write. A write of at
// least one byte that starts at or past MaxFileSize fails with ErrFBig and
// changes nothing", "An empty write is not refused for its offset", and §4's
// order: the empty write returns (0, FILE_SYNC, nil) at any offset, and a
// refused stable write syncs nothing. This is route 2 of #55, and #66's wrap:
// before ADR 0007 a write of 20 bytes at 2^64 − 10 put its first 10 bytes near
// 2^64, wrapped, and put the rest over bytes 0 to 9 of the file (#66; §4:
// "nothing computes off + len(data)").
//
// f.bin holds 100 synced bytes, and second.bin is then created and not
// synced, so the namespace is dirty. Each of these writes to f.bin is refused
// and touches nothing: UNSTABLE, 1 byte at 2^32, then 1 byte at 2^64 − 1, 20
// bytes at 2^64 − 10 (#66's case) and 20 bytes at 2^64 − 20, whose end is
// exactly 2^64; then DATA_SYNC and FILE_SYNC, 1 byte at 2^32. A fresh mount
// must not find second.bin, which a stable write's sync would have committed.
func TestMaxFileSizeWriteAtOrPastTheLimit(t *testing.T) {
	st := bpLocal(t)
	fs := mfsNew(t, st, quietLog())
	f, _ := bpCreate(t, fs, "f.bin", 0)
	data := bpUnique(100)
	bpMustWrite(t, fs, "the Write of 100 bytes at 0 of f.bin", f, 0, data)
	bpMustSync(t, fs, "Sync of f.bin's 100 bytes")
	bpCreate(t, fs, "second.bin", 0)

	refuse := func(off uint64, n int, how vfs.Stability, at string) {
		t.Helper()
		what := fmt.Sprintf("the %s Write of %d bytes at %s to f.bin", bpHow(how), n, at)
		before := mfsCapture(t, fs, f, "before "+what)
		mfsWantRefusedWrite(t, what, bpWrite(t, fs, what, f, off, bpUnique(n), how))
		mfsWantUnchanged(t, fs, f, before, what)
	}
	refuse(mfsMax, 1, vfs.Unstable, "MaxFileSize, 2^32")
	refuse(math.MaxUint64, 1, vfs.Unstable, "2^64 − 1")
	refuse(1<<64-10, 20, vfs.Unstable, "2^64 − 10, which runs past 2^64 (#66)")
	refuse(1<<64-20, 20, vfs.Unstable, "2^64 − 20, whose end is exactly 2^64 (#66)")
	refuse(mfsMax, 1, vfs.DataSync, "MaxFileSize, 2^32")
	refuse(mfsMax, 1, vfs.FileSync, "MaxFileSize, 2^32")

	fresh := mfsNew(t, st, quietLog())
	mfsWantNoName(t, fresh, "second.bin", "on a fresh mount, after refused DATA_SYNC and FILE_SYNC "+
		"Writes to f.bin while second.bin was created and not synced (ADR 0007 §4, §7: a refused "+
		"stable write syncs nothing)")
	bpWantRemount(t, st, "f.bin", data, "f.bin after the refused Writes")

	r := bpWrite(t, fs, "an empty UNSTABLE Write at 2^64 − 1 to f.bin", f, math.MaxUint64, []byte{}, vfs.Unstable)
	if r.err != nil || r.n != 0 || r.how != vfs.FileSync {
		t.Errorf("an empty UNSTABLE Write at 2^64 − 1 to f.bin = %v, want (0, FILE_SYNC, nil): an "+
			"empty write is not refused for its offset (ADR 0007 §3, Assumption 8), and returns "+
			"(0, FILE_SYNC, nil) at any offset (§4)", r)
	}
}

// TestMaxFileSizeRefusedWriteTakesNoLockAndDoesNotWait pins ADR 0007 §4, "A
// refused call takes no lock, neither FS.mu nor any openFile.mu; ... neither
// waits on the budget nor charges it", with ADR 0003 §4's list of the paths
// that never wait, which now holds "a write of at least one byte at an offset
// at or past vfs.FS.MaxFileSize". Both are reached as ADR 0007 §7 says: under
// held locks, and while a drain is held in its chunk Put with the budget full
// (bpBlockDrain).
func TestMaxFileSizeRefusedWriteTakesNoLockAndDoesNotWait(t *testing.T) {
	t.Run("under held locks", func(t *testing.T) {
		fs := mfsNew(t, bpLocal(t), quietLog())
		w, wAttr := bpCreate(t, fs, "w.bin", 0)
		bpMustWrite(t, fs, "the Write of 100 bytes at 0 of w.bin, which gives it an FS.open entry "+
			"(ADR 0005 §4)", w, 0, bpUnique(100))
		of := bpMustOpen(t, fs, wAttr.FileID, "setup: w.bin after its Write")
		before := mfsCapture(t, fs, w, "before the refused Writes")
		mfsRefusedUnderLocks(t, fs, nil, "FS.mu for writing",
			mfsCall{"the UNSTABLE Write of 1 byte at 2^32 to w.bin", mfsWriteRefusal(fs, w, mfsMax, bpUnique(1), vfs.Unstable)},
		)
		mfsRefusedUnderLocks(t, fs, of, "w.bin's openFile.mu and then FS.mu for writing",
			mfsCall{"the UNSTABLE Write of 1 byte at 2^32 to w.bin", mfsWriteRefusal(fs, w, mfsMax, bpUnique(1), vfs.Unstable)},
			mfsCall{"the FILE_SYNC Write of 1 byte at 2^32 to w.bin", mfsWriteRefusal(fs, w, mfsMax, bpUnique(1), vfs.FileSync)},
			mfsCall{"the UNSTABLE Write of 20 bytes at 2^64 − 10 to w.bin (#66)", mfsWriteRefusal(fs, w, 1<<64-10, bpUnique(20), vfs.Unstable)},
		)
		mfsWantUnchanged(t, fs, w, before, "the refused Writes under held locks")
	})

	t.Run("while a drain is held", func(t *testing.T) {
		var (
			other   vfs.Handle
			otherID uint64
		)
		b := bpBlockDrain(t, quietLog(), func(fs *FS) {
			h, attr := bpCreate(t, fs, "other.bin", 0)
			other, otherID = h, attr.FileID
		})
		before := b.fs.DirtyBytes()
		what := "the UNSTABLE Write of 1 byte at 2^32 to other.bin, made while the budget is full and " +
			"writer A's drain is held in its chunk Put (a hang here means the refused Write waits on " +
			"the budget: ADR 0007 §4, §7; ADR 0003 §4, the paths that never wait)"
		r := bpStartWrite(context.Background(), b.fs, testCaller, other, mfsMax, bpUnique(1), vfs.Unstable).wait(t, what)
		b.stillBlocked(t, "after the refused Write to other.bin returned")
		mfsWantRefusedWrite(t, what, r)
		if n := btWaiters(t, b.fs.budget); n != 0 {
			t.Errorf("budget.waiters() = %d after the refused Write returned, want 0: a refused call "+
				"never parks in the budget (ADR 0007 §4, §7)", n)
		}
		bpWantDirty(t, b.fs, "after the refused Write to other.bin (ADR 0007 §4: a refused call "+
			"neither waits on the budget nor charges it)", before)
		if of := bpOpen(t, b.fs, otherID, "after the refused Write to other.bin"); of != nil {
			t.Errorf("the refused Write added an FS.open entry for other.bin. %s", mfsRule)
		}
		b.release(t)
	})
}

// TestMaxFileSizeShortWriteAcrossTheLimit pins ADR 0007 §3, "One that starts
// below MaxFileSize and would run past it writes only the bytes below
// MaxFileSize and returns how many that was: a short write", §4's clamp to
// data[:MaxFileSize − off], and §7: "A write that crosses the limit returns the
// count of its bytes below it, and makes the file exactly MaxFileSize long if
// it was shorter; it is charged as any write of those bytes is (ADR 0003 §2)".
//
// UNSTABLE: 20 bytes at 2^32 − 10 of an empty file return 10. The file is then
// 2^32 long, reads back those 10 bytes with eof, and is charged one chunk, for
// its one new index (ADR 0003 §2's table). A write of 1 byte at 2^32 − 1, which
// ends exactly at the limit, is taken whole, and 1 byte at 2^32 is refused.
// FILE_SYNC: the same crossing write on another empty file returns
// (10, FILE_SYNC, nil), and a fresh mount finds the file 2^32 long, ending in
// those 10 bytes.
func TestMaxFileSizeShortWriteAcrossTheLimit(t *testing.T) {
	t.Run("UNSTABLE", func(t *testing.T) {
		fs := mfsNew(t, bpLocal(t), quietLog())
		h, _ := bpCreate(t, fs, "a.bin", 0)
		data := bpUnique(20)
		before := fs.DirtyBytes()
		what := "the UNSTABLE Write of 20 bytes at 2^32 − 10 to the empty a.bin"
		r := bpWrite(t, fs, what, h, mfsMax-10, data, vfs.Unstable)
		if r.err != nil || r.n != 10 || r.how != vfs.Unstable {
			t.Fatalf("%s = %v, want (10, UNSTABLE, nil): it writes only the bytes below MaxFileSize "+
				"and returns how many that was (ADR 0007 §3, §4)", what, r)
		}
		if a := bpGetAttr(t, fs, h, "GetAttr of a.bin after the short Write"); a.Size != mfsMax {
			t.Errorf("a.bin's size after the short Write = %d, want MaxFileSize, %d: a crossing write "+
				"makes the file exactly MaxFileSize long if it was shorter (ADR 0007 §7)", a.Size, mfsMax)
		}
		rd := rcStartRead(fs, h, mfsMax-10, 20).wait(t, "the Read of 20 bytes at 2^32 − 10 of a.bin")
		ptWantRead(t, "the Read of 20 bytes at 2^32 − 10 of a.bin after the short Write", rd, data[:10],
			true, "the first 10 bytes of the Write, which are the file's last 10")
		if got := fs.DirtyBytes() - before; got != bpCS {
			t.Errorf("the short Write changed DirtyBytes() by %d, want %d: it is charged as any write "+
				"of those bytes is, and a Write to an index absent from of.dirty is +cs exactly (ADR "+
				"0007 §7; ADR 0003 §2)", got, bpCS)
		}
		bpCheckAccounting(t, fs, "after the short Write")

		last := bpUnique(1)
		what = "the UNSTABLE Write of 1 byte at 2^32 − 1 to a.bin, which ends exactly at the limit"
		if r := bpWrite(t, fs, what, h, mfsMax-1, last, vfs.Unstable); r.err != nil || r.n != 1 {
			t.Errorf("%s = %v, want (1, UNSTABLE, nil) (ADR 0007 §1: the limit is inclusive)", what, r)
		}
		what = "the UNSTABLE Write of 1 byte at 2^32 to a.bin"
		mfsWantRefusedWrite(t, what, bpWrite(t, fs, what, h, mfsMax, bpUnique(1), vfs.Unstable))
		rd = rcStartRead(fs, h, mfsMax-10, 20).wait(t, "the Read of 20 bytes at 2^32 − 10 of a.bin, again")
		ptWantRead(t, "the Read of 20 bytes at 2^32 − 10 of a.bin after all three Writes", rd,
			rcCat(data[:9], last), true, "the short Write's first 9 bytes, then the byte written at "+
				"2^32 − 1; the refused Write changed nothing")
		if a := bpGetAttr(t, fs, h, "GetAttr of a.bin after all three Writes"); a.Size != mfsMax {
			t.Errorf("a.bin's size after all three Writes = %d, want %d", a.Size, mfsMax)
		}
	})

	t.Run("FILE_SYNC", func(t *testing.T) {
		st := bpLocal(t)
		fs := mfsNew(t, st, quietLog())
		h, _ := bpCreate(t, fs, "b.bin", 0)
		data := bpUnique(20)
		what := "the FILE_SYNC Write of 20 bytes at 2^32 − 10 to the empty b.bin"
		r := bpWrite(t, fs, what, h, mfsMax-10, data, vfs.FileSync)
		if r.err != nil || r.n != 10 || r.how != vfs.FileSync {
			t.Fatalf("%s = %v, want (10, FILE_SYNC, nil): it writes only the bytes below MaxFileSize "+
				"and returns how many that was (ADR 0007 §3, §4), and a FILE_SYNC write is answered "+
				"FILE_SYNC", what, r)
		}
		fs2, h2, attr := wsRemountLookup(t, st, "b.bin")
		if attr.Size != mfsMax {
			t.Errorf("b.bin's size on a fresh mount after the short FILE_SYNC Write = %d, want "+
				"MaxFileSize, %d (ADR 0007 §7)", attr.Size, mfsMax)
		}
		rd := rcStartRead(fs2, h2, mfsMax-10, 20).wait(t, "the Read of 20 bytes at 2^32 − 10 of b.bin on a fresh mount")
		ptWantRead(t, "the Read of 20 bytes at 2^32 − 10 of b.bin on a fresh mount", rd, data[:10],
			true, "the first 10 bytes of the short FILE_SYNC Write, which are the file's last 10")
	})
}

// TestMaxFileSizeCreate pins ADR 0007 §3, "Create. If sa.Size is set and above
// MaxFileSize, Create fails with ErrFBig before it creates or changes
// anything, whether or not name exists", §4's Create bullet ("for a regular
// file only ... before it takes FS.mu"; "Mkdir and Symlink ignore sa.Size"),
// §4's order ("ErrFBig comes after ErrROFS and a diverged mount's ErrStale,
// and after a bad name's ErrInval ... before ... ErrExist"), and §7: "Mkdir and
// Symlink accept a sa.Size of any value". Whether Create applies a size at or
// below the limit is not pinned, and is not asserted.
//
// The read-only subtest reaches a read-only mount as §7's "A read-only mount"
// says, Config{ReadOnly: true} passed to New on a bucket that already holds a
// filesystem, and requires Create, SetAttr and Write, each over the limit, to
// answer vfs.ErrROFS. The diverged subtest does the same for vfs.ErrStale.
func TestMaxFileSizeCreate(t *testing.T) {
	ctx := context.Background()
	fs := mfsNew(t, bpLocal(t), quietLog())
	root := fs.Root()
	e, _ := bpCreate(t, fs, "e.bin", 0)
	bpMustWrite(t, fs, "the Write of 100 bytes at 0 of e.bin", e, 0, bpUnique(100))
	create := func(name string, size uint64, excl bool) func() error {
		return func() error {
			_, _, err := fs.Create(ctx, testCaller, root, name, vfs.SetAttr{Size: &size}, excl)
			return err
		}
	}
	rootBefore := bpGetAttr(t, fs, root, "GetAttr of the root before the refused Creates")
	wantNoTrace := func(name, what string) {
		t.Helper()
		mfsWantNoName(t, fs, name, "after "+what+" (ADR 0007 §4: a refused call creates nothing)")
		a := bpGetAttr(t, fs, root, "GetAttr of the root after "+what)
		if a.Size != rootBefore.Size || a.NLink != rootBefore.NLink ||
			!a.MTime.Equal(rootBefore.MTime) || !a.CTime.Equal(rootBefore.CTime) {
			t.Errorf("%s changed the root directory: before, size %d, nlink %d, mtime %v, ctime %v; "+
				"after, %d, %d, %v, %v (ADR 0007 §3: Create fails before it creates or changes "+
				"anything)", what, rootBefore.Size, rootBefore.NLink, rootBefore.MTime,
				rootBefore.CTime, a.Size, a.NLink, a.MTime, a.CTime)
		}
	}

	what := "the exclusive Create of x.bin with sa.Size 2^32 + 1"
	mfsWantFBig(t, what, bpDo(t, what, create("x.bin", mfsMax+1, true)))
	wantNoTrace("x.bin", what)

	what = "the non-exclusive Create of the new name y.bin with sa.Size 2^32 + 1"
	mfsWantFBig(t, what, bpDo(t, what, create("y.bin", mfsMax+1, false)))
	wantNoTrace("y.bin", what)

	what = "the non-exclusive Create of the existing e.bin with sa.Size 2^64 − 1"
	before := mfsCapture(t, fs, e, "before "+what)
	mfsWantFBig(t, what, bpDo(t, what, create("e.bin", math.MaxUint64, false)))
	mfsWantUnchanged(t, fs, e, before, what)

	what = "the exclusive Create of the existing e.bin with sa.Size 2^32 + 1 (ErrFBig comes before ErrExist)"
	before = mfsCapture(t, fs, e, "before "+what)
	mfsWantFBig(t, what, bpDo(t, what, create("e.bin", mfsMax+1, true)))
	mfsWantUnchanged(t, fs, e, before, what)

	what = "the exclusive Create of z.bin with sa.Size 2^32 + 1"
	mfsRefusedUnderLocks(t, fs, nil, "FS.mu for writing", mfsCall{what, create("z.bin", mfsMax+1, true)})
	wantNoTrace("z.bin", what+", made while the test held FS.mu")

	what = "the exclusive Create of m.bin with sa.Size 2^32, exactly the limit"
	if err := bpDo(t, what, create("m.bin", mfsMax, true)); err != nil {
		t.Errorf("%s = %v, want success: only a size above MaxFileSize is refused (ADR 0007 §1, §3)",
			what, err)
	}

	huge := uint64(math.MaxUint64)
	err := bpDo(t, "Mkdir of d with sa.Size 2^64 − 1", func() error {
		_, _, err := fs.Mkdir(ctx, testCaller, root, "d", vfs.SetAttr{Size: &huge})
		return err
	})
	if err != nil {
		t.Errorf("Mkdir of d with sa.Size 2^64 − 1 = %v, want success: Mkdir accepts a sa.Size of "+
			"any value (ADR 0007 §4, §7)", err)
	}
	err = bpDo(t, "Symlink of s with sa.Size 2^64 − 1", func() error {
		_, _, err := fs.Symlink(ctx, testCaller, root, "s", "e.bin", vfs.SetAttr{Size: &huge})
		return err
	})
	if err != nil {
		t.Errorf("Symlink of s with sa.Size 2^64 − 1 = %v, want success: Symlink accepts a sa.Size "+
			"of any value (ADR 0007 §4, §7)", err)
	}

	what = "the exclusive Create of the bad name \"a/b\" with sa.Size 2^32 + 1"
	if err := bpDo(t, what, create("a/b", mfsMax+1, true)); !errors.Is(err, vfs.ErrInval) {
		t.Errorf("%s = %v, want vfs.ErrInval: ErrFBig comes after a bad name's ErrInval (ADR 0007 §4)",
			what, err)
	}

	t.Run("a read-only mount answers ErrROFS first", func(t *testing.T) {
		st := bpLocal(t)
		data := bpSeedFile(t, st, "seeded.bin", 100)
		ro := mfsMount(t, "New with ReadOnly: true on a bucket that holds a filesystem", Config{
			Store:          st,
			ChunkSize:      bpCS,
			ReadOnly:       true,
			CommitInterval: time.Hour,
			Log:            quietLog(),
		})
		h, _ := bpLookup(t, ro, "seeded.bin")
		rctx := context.Background()

		what := "the exclusive Create of x.bin with sa.Size 2^32 + 1 on the read-only mount"
		err := bpDo(t, what, func() error {
			size := mfsMax + 1
			_, _, err := ro.Create(rctx, testCaller, ro.Root(), "x.bin", vfs.SetAttr{Size: &size}, true)
			return err
		})
		if !errors.Is(err, vfs.ErrROFS) {
			t.Fatalf("%s = %v, want vfs.ErrROFS: ErrFBig comes after ErrROFS, and a mount made with "+
				"Config{ReadOnly: true} answers Create with vfs.ErrROFS (ADR 0007 §4, §7)", what, err)
		}
		mfsWantNoName(t, ro, "x.bin", "after "+what)

		what = "SetAttr of the seeded seeded.bin to size 2^32 + 1 on the read-only mount"
		before := mfsCapture(t, ro, h, "before "+what)
		if err := bpDo(t, what, mfsSetSize(ro, h, mfsMax+1)); !errors.Is(err, vfs.ErrROFS) {
			t.Errorf("%s = %v, want vfs.ErrROFS (ADR 0007 §4, §7)", what, err)
		}
		mfsWantUnchanged(t, ro, h, before, what)

		what = "the UNSTABLE Write of 1 byte at 2^32 to the seeded seeded.bin on the read-only mount"
		before = mfsCapture(t, ro, h, "before "+what)
		if r := bpWrite(t, ro, what, h, mfsMax, bpUnique(1), vfs.Unstable); !errors.Is(r.err, vfs.ErrROFS) || r.n != 0 {
			t.Errorf("%s = %v, want (0, _, vfs.ErrROFS) (ADR 0007 §4, §7)", what, r)
		}
		mfsWantUnchanged(t, ro, h, before, what)
		bpWantRemount(t, st, "seeded.bin", data, "seeded.bin after the refused calls on the read-only mount")
	})

	t.Run("a diverged mount answers ErrStale first", func(t *testing.T) {
		st := bpLocal(t)
		fsA := mfsNew(t, st, quietLog())
		fsB := mfsNew(t, st, quietLog())
		hB, _ := bpCreate(t, fsB, "b.bin", 0)
		bpCreate(t, fsA, "a.bin", 0)
		bpMustSync(t, fsA, "fsA's Sync, which moves the root pointer out from under fsB")
		if err := bpSync(t, fsB, "fsB's Sync, after fsA moved the root pointer"); err == nil {
			t.Fatalf("setup: fsB's Sync succeeded after fsA moved the root pointer; this test " +
				"needs fsB diverged (ADR 0003 §5)")
		}
		bctx := context.Background()
		what := "the exclusive Create of x.bin with sa.Size 2^32 + 1 on the diverged fsB"
		err := bpDo(t, what, func() error {
			size := mfsMax + 1
			_, _, err := fsB.Create(bctx, testCaller, fsB.Root(), "x.bin", vfs.SetAttr{Size: &size}, true)
			return err
		})
		if !errors.Is(err, vfs.ErrStale) {
			t.Fatalf("%s = %v, want vfs.ErrStale: ErrFBig comes after a diverged mount's ErrStale "+
				"(ADR 0007 §4, §7)", what, err)
		}
		what = "SetAttr of b.bin to size 2^32 + 1 on the diverged fsB"
		if err := bpDo(t, what, mfsSetSize(fsB, hB, mfsMax+1)); !errors.Is(err, vfs.ErrStale) {
			t.Errorf("%s = %v, want vfs.ErrStale (ADR 0007 §4, §7)", what, err)
		}
		what = "the UNSTABLE Write of 1 byte at 2^32 to b.bin on the diverged fsB"
		if r := bpWrite(t, fsB, what, hB, mfsMax, bpUnique(1), vfs.Unstable); !errors.Is(r.err, vfs.ErrStale) {
			t.Errorf("%s = %v, want vfs.ErrStale (ADR 0007 §4, §7)", what, r)
		}
	})
}

// TestMaxFileSizeCheckedBeforeTheHandle pins ADR 0007 §4's order: "It comes
// before a stale handle's ErrStale, ErrBadHandle, ErrIsDir, ErrAcces,
// ErrNotDir, ErrExist and any wait", and Assumption 7: "a request that is both
// too large and for a stale handle is answered NFS3ERR_FBIG". Each call below
// would fail for its handle, its caller or its parent, and must fail with
// vfs.ErrFBig instead. rcOther is neither f.bin's owner nor in its group.
func TestMaxFileSizeCheckedBeforeTheHandle(t *testing.T) {
	ctx := context.Background()
	fs := mfsNew(t, bpLocal(t), quietLog())
	root := fs.Root()
	f, _ := bpCreate(t, fs, "f.bin", 0o644)
	gone, _ := bpCreate(t, fs, "gone.bin", 0)
	bpRemove(t, fs, "gone.bin", "Remove of gone.bin, which makes its handle stale")

	// check makes one call, which must fail with vfs.ErrFBig. The first call
	// fails the test at once if it does not (ADR 0007 §7: a wrong answer to the
	// first over-limit request ends the test); later ones report and go on.
	checked := 0
	check := func(what string, fn func() error) {
		t.Helper()
		checked++
		err := bpDo(t, what, fn)
		if errors.Is(err, vfs.ErrFBig) {
			return
		}
		const why = "ADR 0007 §4's checks read only the call's arguments and come before the " +
			"handle is resolved and checked (§4; Assumption 7)"
		if checked == 1 {
			t.Fatalf("%s = %v, want vfs.ErrFBig: %s", what, err, why)
		}
		t.Errorf("%s = %v, want vfs.ErrFBig: %s", what, err, why)
	}
	write := func(c vfs.Caller, h vfs.Handle) func() error {
		return func() error {
			_, _, err := fs.Write(ctx, c, h, mfsMax, bpUnique(1), vfs.Unstable)
			return err
		}
	}
	setSize := func(c vfs.Caller, h vfs.Handle) func() error {
		return func() error {
			size := mfsMax + 1
			_, err := fs.SetAttr(ctx, c, h, vfs.SetAttr{Size: &size})
			return err
		}
	}
	check("the Write of 1 byte at 2^32 to gone.bin's stale handle", write(testCaller, gone))
	check("SetAttr of gone.bin's stale handle to size 2^32 + 1", setSize(testCaller, gone))
	check("the Write of 1 byte at 2^32 to the root directory", write(testCaller, root))
	check("the Write of 1 byte at 2^32 to f.bin by a caller it does not let write", write(rcOther, f))
	check("SetAttr of f.bin to size 2^32 + 1 by a caller who does not own it", setSize(rcOther, f))
	check("the exclusive Create of n.bin, in f.bin as though it were a directory, with sa.Size 2^32 + 1",
		func() error {
			size := mfsMax + 1
			_, _, err := fs.Create(ctx, testCaller, f, "n.bin", vfs.SetAttr{Size: &size}, true)
			return err
		})
}

// TestMaxFileSizeFileLeftLarger pins ADR 0007 §6, "A file an earlier build left
// larger": such a file "is read whole ... at any offset below its size"; "a
// write at or past the limit is refused, even one inside the file; one that
// crosses the limit is short; one below it is written as before. None makes
// the file larger"; it "can be shrunk to the limit ... while a size above the
// limit is refused, even one that would shrink it (Assumption 6)"; and it "can
// be renamed and removed". It is reached as §7 says: seed the file through one
// mount, mount the bucket afresh, and lower that mount's maxFileSize below the
// file's size before its first use; here to 8192, below old.bin's 12288.
func TestMaxFileSizeFileLeftLarger(t *testing.T) {
	ctx := context.Background()
	st := bpLocal(t)
	old := bpSeedFile(t, st, "old.bin", 3*bpCS)
	bpSeedFile(t, st, "gone.bin", bpCS)
	fs := mfsNew(t, st, quietLog())
	fs.maxFileSize = 2 * bpCS
	const lim = 2 * bpCS

	if got := fs.MaxFileSize(); got != lim {
		t.Fatalf("setup: MaxFileSize() = %d after FS.maxFileSize was set to %d before first use; ADR "+
			"0007 §7: MaxFileSize returns it", got, lim)
	}
	h, attr := bpLookup(t, fs, "old.bin")
	if attr.Size != 3*bpCS {
		t.Errorf("old.bin's size = %d, want %d", attr.Size, 3*bpCS)
	}
	bpWantFile(t, fs, h, old, "old.bin, 12288 bytes under a limit of 8192, read whole (ADR 0007 §6)")

	err := bpDo(t, "Rename of old.bin to old2.bin", func() error {
		return fs.Rename(ctx, testCaller, fs.Root(), "old.bin", fs.Root(), "old2.bin")
	})
	if err != nil {
		t.Fatalf("Rename of old.bin, larger than the limit, to old2.bin = %v, want success (ADR 0007 §6)", err)
	}

	refuse := func(off uint64, at string) {
		t.Helper()
		what := "the UNSTABLE Write of 1 byte at " + at + " to old2.bin"
		before := mfsCapture(t, fs, h, "before "+what)
		mfsWantRefusedWrite(t, what, bpWrite(t, fs, what, h, off, bpUnique(1), vfs.Unstable))
		mfsWantUnchanged(t, fs, h, before, what)
	}
	refuse(lim, "8192, the limit")
	refuse(3*bpCS-1, "12287, inside the file but past the limit (ADR 0007 §6)")

	w1 := bpUnique(10)
	what := "the UNSTABLE Write of 10 bytes at 8187 to old2.bin, which crosses the limit"
	if r := bpWrite(t, fs, what, h, lim-5, w1, vfs.Unstable); r.err != nil || r.n != 5 || r.how != vfs.Unstable {
		t.Errorf("%s = %v, want (5, UNSTABLE, nil): one that crosses the limit is short (ADR 0007 §6)", what, r)
	}
	w2 := bpUnique(10)
	bpMustWrite(t, fs, "the UNSTABLE Write of 10 bytes at 100 to old2.bin, below the limit", h, 100, w2)
	want := rcCat(old)
	copy(want[100:], w2)
	copy(want[lim-5:lim], w1[:5])
	if a := bpGetAttr(t, fs, h, "GetAttr of old2.bin after the Writes"); a.Size != 3*bpCS {
		t.Errorf("old2.bin's size after the Writes = %d, want %d: none of them makes the file larger "+
			"(ADR 0007 §6)", a.Size, 3*bpCS)
	}
	bpWantFile(t, fs, h, want, "old2.bin after the Writes below the limit")

	shrinkRefused := func(size uint64, desc string) {
		t.Helper()
		what := fmt.Sprintf("SetAttr of old2.bin to size %d, %s", size, desc)
		before := mfsCapture(t, fs, h, "before "+what)
		mfsWantFBig(t, what, bpDo(t, what, mfsSetSize(fs, h, size)))
		mfsWantUnchanged(t, fs, h, before, what)
	}
	shrinkRefused(3*bpCS, "its own size, above the limit")
	shrinkRefused(lim+1, "a shrink to a size still above the limit (ADR 0007 §6, Assumption 6)")

	bpTruncate(t, fs, h, lim, "SetAttr of old2.bin to 8192, the limit (ADR 0007 §6)")
	if a := bpGetAttr(t, fs, h, "GetAttr of old2.bin after it was shrunk to the limit"); a.Size != lim {
		t.Errorf("old2.bin's size after a SetAttr to 8192 = %d, want %d", a.Size, lim)
	}
	want = want[:lim]
	bpWantFile(t, fs, h, want, "old2.bin shrunk to the limit")

	bpRemove(t, fs, "gone.bin", "Remove of gone.bin (ADR 0007 §6)")
	bpMustSync(t, fs, "Sync after the Rename, the Writes, the shrink and the Remove")
	bpWantRemount(t, st, "old2.bin", want, "old2.bin, on a mount with the default limit")
	fresh := mfsNew(t, st, quietLog())
	mfsWantNoName(t, fresh, "gone.bin", "on a fresh mount after its Remove and a Sync")
	mfsWantNoName(t, fresh, "old.bin", "on a fresh mount after its Rename and a Sync")
}

// TestMaxFileSizeMountRecord pins ADR 0007 §6's record: "New logs one record
// when the filesystem it has loaded holds at least one regular file whose size
// is above the MaxFileSize it computed, and nothing otherwise", at Warn, with
// the message "files larger than the maximum file size" and the top-level
// attributes count (slog.KindInt64), largest and max_file_size (both
// slog.KindUint64); and the rest of §6 for such a file. It is reached as §7
// says: a mount whose maxFileSize is math.MaxUint64 grows a file to
// 2^20 × cs + 1 with SetAttr and Syncs, and the bucket is mounted afresh.
func TestMaxFileSizeMountRecord(t *testing.T) {
	st := bpLocal(t)
	cA := newBTCapture(false)
	a := mfsNew(t, st, cA.logger())
	if recs := cA.withMessage(mfsMsgLarger); len(recs) != 0 {
		t.Errorf("New of an empty bucket logged %d %q records, want none: nothing is logged "+
			"otherwise (ADR 0007 §6)", len(recs), mfsMsgLarger)
	}
	a.maxFileSize = math.MaxUint64
	wide, _ := bpCreate(t, a, "wide.bin", 0)
	bpTruncate(t, a, wide, mfsMax+1, "SetAttr of wide.bin to 2^32 + 1 on a mount standing for a "+
		"build with no limit (ADR 0007 §7)")
	small, _ := bpCreate(t, a, "small.bin", 0)
	bpMustWrite(t, a, "the Write of 10 bytes at 0 of small.bin", small, 0, bpUnique(10))
	bpMustSync(t, a, "Sync of wide.bin and small.bin")

	cB := newBTCapture(false)
	b := mfsNew(t, st, cB.logger())
	recs := cB.withMessage(mfsMsgLarger)
	if len(recs) != 1 {
		t.Errorf("New of a bucket holding one regular file of 2^32 + 1 bytes, above its limit of "+
			"2^32, logged %d %q records, want exactly 1 (ADR 0007 §6)", len(recs), mfsMsgLarger)
	}
	if len(recs) > 0 {
		rec := recs[0]
		if rec.Level != slog.LevelWarn {
			t.Errorf("the %q record is logged at %v, want %v (ADR 0007 §6)", mfsMsgLarger, rec.Level,
				slog.LevelWarn)
		}
		if v, ok := mfsWantKind(t, rec, "count", slog.KindInt64); ok && v.Int64() != 1 {
			t.Errorf("the %q record's count = %d, want 1: one regular file is above the limit, and "+
				"directories and symlinks are not counted (ADR 0007 §6)", mfsMsgLarger, v.Int64())
		}
		if v, ok := mfsWantKind(t, rec, "largest", slog.KindUint64); ok && v.Uint64() != mfsMax+1 {
			t.Errorf("the %q record's largest = %d, want %d (ADR 0007 §6)", mfsMsgLarger, v.Uint64(),
				mfsMax+1)
		}
		if v, ok := mfsWantKind(t, rec, "max_file_size", slog.KindUint64); ok && v.Uint64() != mfsMax {
			t.Errorf("the %q record's max_file_size = %d, want %d (ADR 0007 §6)", mfsMsgLarger,
				v.Uint64(), mfsMax)
		}
	}

	h, attr := bpLookup(t, b, "wide.bin")
	if attr.Size != mfsMax+1 {
		t.Fatalf("wide.bin's size on a fresh mount = %d, want %d: New does not refuse the bucket, "+
			"and such a file is mounted (ADR 0007 §6)", attr.Size, mfsMax+1)
	}
	r := rcStartRead(b, h, mfsMax, 1).wait(t, "the Read of 1 byte at 2^32 of wide.bin")
	ptWantRead(t, "the Read of 1 byte at 2^32 of wide.bin, whose size is 2^32 + 1", r, []byte{0},
		true, "such a file is read whole, at any offset below its size (ADR 0007 §6), and a file "+
			"grown with SetAttr reads as zeros")
	what := "the UNSTABLE Write of 1 byte at 2^32 to wide.bin"
	mfsWantRefusedWrite(t, what, bpWrite(t, b, what, h, mfsMax, bpUnique(1), vfs.Unstable))
	bpTruncate(t, b, h, mfsMax, "SetAttr of wide.bin to 2^32, the limit (ADR 0007 §6)")
	if a := bpGetAttr(t, b, h, "GetAttr of wide.bin after it was shrunk to the limit"); a.Size != mfsMax {
		t.Errorf("wide.bin's size after a SetAttr to 2^32 = %d, want %d", a.Size, mfsMax)
	}
}
