package blobfs

// Clean-room tests for ADR 0005, "A read takes one view of a file, under the
// file's lock" (docs/adr/0005-read-takes-one-view-under-the-file-lock.md),
// written from that ADR, docs/DESIGN.md §7, ADR 0003 §2, §3 and §4 (its test
// surface) and "What this does not decide", ADR 0004 §5 and §7, ADR 0002
// Assumption 7, and internal/vfs/vfs.go, without reading the implementation. They are the tests for #41 and #49, and cover:
//
//   - §1, what a Read returns: freshness, (a), for a Write acknowledged before
//     the Read was called, whatever flush of the file runs meanwhile; and
//     atomicity, (b), that one reply never holds bytes from both before and
//     after one Write;
//   - §2, the three steps: step 2 copies the dirty bytes under the file's
//     lock, step 3 fetches holding no lock, and Read never adds an entry to
//     FS.open;
//   - §2 step 2's second check, whose answer is the Read's: a Read waiting for
//     the file's lock across a Remove of the file gets vfs.ErrStale (§6, "A
//     removal"; Assumption 5), and across a SetAttr of the mode alone that
//     takes its read permission away gets vfs.ErrAcces. The window between
//     steps 1 and 2 is reached as §7 reaches #41's, with a flush held in its
//     chunk Put and rcGrace;
//   - §2's end of the range, which cannot wrap (Assumption 9), for a file whose
//     size one buffered Write makes 2^64 − 1;
//   - §4's path for a file with no FS.open entry, and §3's invariant 3, which
//     makes that path's view complete;
//   - §5, that a slow fetch holds up no Write, flush or namespace operation;
//   - §6, a Read meeting a flush of its file through Sync, Commit and a budget
//     drain (ADR 0003 §4);
//   - §7, each case reached as it says: #49 deterministically, #41 with a
//     grace period, and a stress test with and without flushes, under -race.
//
// Every check is about ONE Read call. A window is never read in pieces, which
// would let each piece see a different instant; bpReadAll, which reads 1 KiB
// per call, is used only to read back a file nothing is changing.
//
// Every call that could hang runs in its own goroutine and is waited for under
// btHangBound, and the failure names the call. A Read is held in its chunk
// fetch, and a flush in its chunk Put, through bpStore's gates, and each is
// known to be there because the fake closes a channel on entry. Goroutines
// never touch t. The one fixed wait is rcGrace, in
// TestReadSeesWriteAcknowledgedBeforeAFlush and
// TestReadAnswersFromTheCheckUnderTheFileLock.
//
// Two setups rest on how a call other than Read behaves:
//   - The permission case of TestReadAnswersFromTheCheckUnderTheFileLock needs
//     a SetAttr of the mode alone to complete while a flush holds the file's
//     openFile.mu. ADR 0005 pins that: §6, "An attribute change" ("It takes
//     FS.mu for writing and never an openFile.mu"), §7's list of behaviours a
//     test may rely on, and Assumption 15. If that changes, the SetAttr hangs
//     and the failure says so.
//   - TestReadRangeEndDoesNotWrap needs a Write at 2^64 − 2 to be accepted,
//     which Assumption 9 and ADR 0003's "What this does not decide" (#55)
//     imply. A decision on #55 may change its setup.
//
// Not covered, on purpose:
//   - Truncate interleavings: a Read meeting a size change (§1's atomicity
//     against a size change, §6's truncate bullet, eof decided by the size at
//     the view). Nothing here truncates or sets a size. What a Read returns
//     for a pending trim, which §1 and §2 now make part of the view (ADR 0006
//     §1, §6), is covered by pending_trim_test.go, including a Read held in its
//     fetch across the flush that applies the trim (§5, §6); a Read running
//     concurrently with the truncate itself is not.
//   - Symlinks: §1 defines what a Read of a regular file returns, and nothing
//     else.
//   - A Read whose file goes by a Rename over it, the other way ADR 0003 §2
//     site 5 takes an entry out of FS.open: §6's removal bullet speaks of
//     dropOpen whichever call makes it, and one removal pins step 2's answer.
//   - What a Sync returns when the file it is uploading is removed meanwhile:
//     ADR 0003 §2 site 4 says what a failed flush of a removed file does, and
//     nothing says what a successful one returns, so
//     TestReadAnswersFromTheCheckUnderTheFileLock only logs it.
//   - A Read meeting a flush that fails (§6), which repoints nothing and
//     leaves the dirty buffers in place. The Read that ADR 0005 replaces
//     handled it correctly too, so it is not part of #41.
//   - A Read waiting for FS.mu behind a commit that holds it (§6, #43).
//   - §2's lock rules and §4's hold times (FS.mu read-locked at most once,
//     only the references the range touches copied): nothing observable
//     through the interface distinguishes them.
//   - Anything across files: §1 promises nothing there.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// rcGrace is how long TestReadSeesWriteAcknowledgedBeforeAFlush waits, after
// starting a Read, before it releases the flush that the Read must meet. It is
// the only fixed wait in this file, and its only purpose is to let the Read
// reach the file's lock before the flush lets go of it (ADR 0005 §7, #41:
// "waits a fixed grace period for it to reach its first lock"). A correct Read
// passes however long or short it is; only the Read that ADR 0005 replaces
// needs it to be long enough to be caught.
// TestReadAnswersFromTheCheckUnderTheFileLock waits it for the same reason,
// before it changes the file under the Read.
const rcGrace = 100 * time.Millisecond

// rcNoLock ends the description of a call made while a Read is held in its
// chunk fetch, so that a hang names the rule it breaks.
const rcNoLock = " (a hang here means the Read holds a lock while it fetches: ADR 0005 §2 step 3, §4 and §5)"

// rcReply is what one Read returned.
type rcReply struct {
	data []byte
	eof  bool
	err  error
}

// rcStartRead starts one fs.Read as testCaller in its own goroutine.
func rcStartRead(fs *FS, h vfs.Handle, off uint64, count uint32) *bpCall[rcReply] {
	return bpGo(func() rcReply {
		data, eof, err := fs.Read(context.Background(), testCaller, h, off, count)
		return rcReply{data, eof, err}
	})
}

// rcCat returns the concatenation of parts, in a new slice.
func rcCat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// rcWhich says which of pre and post b equals.
func rcWhich(b, pre, post []byte) string {
	switch {
	case bytes.Equal(b, pre):
		return "match the range from before the Write"
	case bytes.Equal(b, post):
		return "match the range from after the Write"
	}
	return "match neither"
}

// rcWantOneView requires r, one Read of a range that ends before the end of
// the file, to have succeeded with len(pre) bytes and no eof, and to be the
// range either as it was before one Write, pre, or as it was after it, post.
// split is where the reply crosses a chunk boundary; a mismatch says which
// side of the Write each part of the reply is from.
func rcWantOneView(t *testing.T, what string, r rcReply, pre, post []byte, split int) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if len(r.data) != len(pre) {
		t.Fatalf("%s returned %d bytes, want %d: the range lies inside the file, and a reply "+
			"is exactly as long as the clamped range (ADR 0005 §2)", what, len(r.data), len(pre))
	}
	if r.eof {
		t.Errorf("%s returned eof, want false: the range ends before the end of the file "+
			"(ADR 0005 §1: eof is decided by the size at the view)", what)
	}
	if bytes.Equal(r.data, pre) || bytes.Equal(r.data, post) {
		return
	}
	t.Errorf("%s: reply bytes [0, %d), from chunk index 0, %s, and reply bytes [%d, %d), from "+
		"chunk index 1, %s. ADR 0005 §1 (atomicity): one reply never holds bytes from both "+
		"before and after one Write, so the whole reply must be the range from before the "+
		"Write or the range from after it (#49)",
		what, split, rcWhich(r.data[:split], pre[:split], post[:split]),
		split, len(pre), rcWhich(r.data[split:], pre[split:], post[split:]))
}

// rcHeld is ADR 0005 §7's set-up for #49: a file of two chunks seeded through
// another mount, a fresh mount of the same bucket, and on it a Read of
// [cs−100, cs+100) held in its store Get of chunk 0. If d1 is set, chunk index
// 1 was written whole with d1 before the Read started, so the Read's view
// holds index 1 as a dirty buffer and index 0 as a stored chunk.
type rcHeld struct {
	st   *store.Local
	fs   *FS
	h    vfs.Handle
	data []byte
	d1   []byte
	gate *btGate
	read *bpCall[rcReply]
}

// rcHoldRead builds rcHeld for a file called name. It returns once the Read
// has entered its Get of chunk 0's key: a fresh mount caches nothing (ADR 0004
// §5), so that chunk is fetched with the store's Get, in step 3 (ADR 0005 §7).
func rcHoldRead(t *testing.T, name string, buffered bool) *rcHeld {
	t.Helper()
	st := bpLocal(t)
	data := bpSeedFile(t, st, name, 2*bpCS)
	key0 := bpChunkKey(t, st, data[:bpCS])
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, 0, quietLog())
	h, _ := bpLookup(t, fs, name)
	var d1 []byte
	if buffered {
		d1 = bpUnique(bpCS)
		bpMustWrite(t, fs, "a Write of chunk index 1 whole, [4096, 8192), which fetches "+
			"nothing (ADR 0005 §7)", h, bpCS, d1)
	}
	gate := newBTGate(t)
	get := &bpHook{key: key0, entered: make(chan struct{}), gate: gate.ch}
	bps.setGet(get)
	rd := rcStartRead(fs, h, bpCS-100, 200)
	bpWaitEntered(t, "the Read of [3996, 4196) entering its Get of chunk 0", get.entered, rd)
	return &rcHeld{st: st, fs: fs, h: h, data: data, d1: d1, gate: gate, read: rd}
}

// rcMustCreate creates name in the root directory, while a Read is held in its
// fetch, under the hang bound.
func rcMustCreate(t *testing.T, fs *FS, name string) {
	t.Helper()
	err := bpDo(t, "a Create of "+name+rcNoLock, func() error {
		_, _, err := fs.Create(context.Background(), testCaller, fs.Root(), name, vfs.SetAttr{}, true)
		return err
	})
	if err != nil {
		t.Fatalf("Create of %s = %v, want success", name, err)
	}
}

// TestReadIsNotTornByAConcurrentWrite pins ADR 0005 §1(b), atomicity: "One
// reply never holds bytes from both before and after one Write", reached as
// §7's "#49, deterministically" says; §2, whose step 2 copies the bytes the
// range needs from a dirty buffer holding openFile.mu and whose step 3
// fetches holding no lock; and §5, through the Write that must complete while
// the Read is held (§7: "That the write completes while the Read is held is
// §5's property").
//
// Index 1 of the file is a dirty buffer and index 0 a stored chunk. A Read of
// [cs−100, cs+100) is held in its fetch of chunk 0 while a Write of
// [0, cs+100) lands, which covers index 0 whole and the part of index 1 the
// Read needs, and fetches nothing (§7). The Read that ADR 0005 replaces copied
// index 1 out of the shared buffer after its fetch, holding no lock, so it
// returned index 0 from before the Write and index 1 from after it, and -race
// reports the copy (#49). Afterwards a new Read must see the Write (§1(a)),
// and a fresh mount must see it after a Sync.
func TestReadIsNotTornByAConcurrentWrite(t *testing.T) {
	const name = "torn.bin"
	x := rcHoldRead(t, name, true)

	w := bpUnique(bpCS + 100)
	what := "the Write of [0, 4196) made while the Read is held in its fetch of chunk 0" + rcNoLock
	wr := bpStartWrite(context.Background(), x.fs, testCaller, x.h, 0, w, vfs.Unstable).wait(t, what)
	if wr.err != nil || int(wr.n) != len(w) || wr.how != vfs.Unstable {
		t.Fatalf("the Write of [0, 4196) = %v, want (%d, UNSTABLE, nil)", wr, len(w))
	}
	x.gate.open()
	got := x.read.wait(t, "the Read of [3996, 4196), after its fetch of chunk 0 was released")
	pre := rcCat(x.data[bpCS-100:bpCS], x.d1[:100])
	post := w[bpCS-100 : bpCS+100]
	rcWantOneView(t, "the Read of [3996, 4196), held in its fetch of chunk 0 across a Write of "+
		"[0, 4196)", got, pre, post, 100)

	again := rcStartRead(x.fs, x.h, bpCS-100, 200).wait(t, "a new Read of [3996, 4196), after the Write returned")
	if again.err != nil {
		t.Fatalf("a new Read of [3996, 4196), after the Write returned, = %v, want success", again.err)
	}
	if !bytes.Equal(again.data, post) {
		t.Errorf("a Read of [3996, 4196) called after the Write of [0, 4196) returned: %s. ADR "+
			"0005 §1 (freshness): every byte a Write that returned before the Read was called "+
			"put in the file reads as that Write left it", wsDiff(again.data, post, 100))
	}
	bpMustSync(t, x.fs, "Sync after the Reads")
	bpWantRemount(t, x.st, name, rcCat(w, x.d1[100:]), "the Write of [0, 4196) over index 0 "+
		"and the start of the rewritten index 1")
}

// The flushes TestReadSeesWriteAcknowledgedBeforeAFlush holds.
const (
	rcFlushSync   = "Sync"
	rcFlushCommit = "Commit"
	rcFlushDrain  = "budget drain"
)

// TestReadSeesWriteAcknowledgedBeforeAFlush pins ADR 0005 §1(a), freshness:
// "Every byte that a Write which returned before Read was called put in the
// file reads as that Write left it", for a Read that meets a flush of its file
// (§6: "step 2 comes entirely before a flush and copies the dirty bytes ... or
// entirely after it and takes the new references"), reached as §7's "#41"
// says. Every flush is flushOpen (§6), reached here through Sync, through
// Commit(h, 0, 0), and through a budget drain: with MaxDirtyBytes one chunk,
// a file g.bin created first, and a 10-byte Write to it that finds the charge
// at the limit and so becomes the drainer (ADR 0003 §4). The other flushes run
// with the default limit.
//
// Each runs over an overwrite, 100 new bytes at 0 of a stored chunk, where the
// Read of [0, 200) must return them followed by the stored chunk's [100, 200);
// and over a growth, 100 bytes at 0 of an empty file, where the Read of
// [0, 100) must return them, with eof. The flush is held in its chunk Put,
// which holds openFile.mu and has repointed nothing yet (§7), the Read is
// started, rcGrace passes, and the flush is released. The Read that ADR 0005
// replaces took the chunk list in its first hold and the dirty map in its
// second, so a flush between the two left it reading the stored chunk's old
// bytes, or zeros, in place of the acknowledged write (#41). It is caught if
// it took its first hold within rcGrace; a correct Read passes whatever the
// scheduling.
func TestReadSeesWriteAcknowledgedBeforeAFlush(t *testing.T) {
	for _, flush := range []string{rcFlushSync, rcFlushCommit, rcFlushDrain} {
		t.Run(flush, func(t *testing.T) {
			t.Run("overwrite", func(t *testing.T) { rcFlushWindow(t, flush, false) })
			t.Run("growth", func(t *testing.T) { rcFlushWindow(t, flush, true) })
		})
	}
}

// rcFlushWindow is one case of TestReadSeesWriteAcknowledgedBeforeAFlush.
func rcFlushWindow(t *testing.T, flush string, growth bool) {
	t.Helper()
	ctx := context.Background()
	st := bpLocal(t)
	bps := &bpStore{Store: st}
	var limit int64
	if flush == rcFlushDrain {
		limit = bpCS
	}
	fs := bpNew(t, bps, limit, quietLog())
	var g vfs.Handle
	if flush == rcFlushDrain {
		g, _ = bpCreate(t, fs, "g.bin", 0)
	}
	const name = "f.bin"
	f, _ := bpCreate(t, fs, name, 0)

	var (
		wantFile []byte
		stale    []byte
		staleIs  string
		count    uint32
	)
	if growth {
		nw := bpUnique(100)
		bpMustWrite(t, fs, "the Write of 100 bytes at 0 of the empty f.bin", f, 0, nw)
		wantFile, stale, count = nw, make([]byte, 100), 100
		staleIs = "zeros, as though the file had no chunk"
	} else {
		old := bpUnique(bpCS)
		bpMustWrite(t, fs, "a Write of chunk 0 of f.bin whole", f, 0, old)
		bpMustSync(t, fs, "Sync storing chunk 0 of f.bin")
		nw := bpUnique(100)
		bpMustWrite(t, fs, "the Write of 100 bytes at 0 of f.bin, over its stored chunk", f, 0, nw)
		wantFile, stale, count = rcCat(nw, old[100:]), old[:200], 200
		staleIs = "the stored chunk's bytes from before that Write"
	}
	want := wantFile[:count]
	if flush == rcFlushDrain && fs.DirtyBytes() < bpCS {
		t.Fatalf("setup: DirtyBytes() = %d after 100 bytes into an index absent from of.dirty, "+
			"want at least the limit, %d, so that the next Write drains (ADR 0003 §2)",
			fs.DirtyBytes(), bpCS)
	}

	gate := newBTGate(t)
	put := &bpHook{entered: make(chan struct{}), gate: gate.ch}
	bps.setPut(put)
	var fl *bpCall[error]
	switch flush {
	case rcFlushSync:
		fl = bpGo(func() error { return fs.Sync(ctx) })
	case rcFlushCommit:
		fl = bpGo(func() error { return fs.Commit(ctx, f, 0, 0) })
	default:
		gData := bpUnique(10)
		fl = bpGo(func() error {
			n, how, err := fs.Write(ctx, testCaller, g, 0, gData, vfs.Unstable)
			r := bpWR{n, how, err}
			if err != nil || n != 10 || how != vfs.Unstable {
				return fmt.Errorf("the Write of 10 bytes at 0 of g.bin, the drainer, = %v, want "+
					"(10, UNSTABLE, nil): its drain succeeded, so it is admitted (ADR 0003 §4)", r)
			}
			return nil
		})
	}
	bpWaitEntered(t, "the "+flush+" of f.bin entering its chunk Put", put.entered, fl)

	rd := rcStartRead(fs, f, 0, count)
	time.Sleep(rcGrace)
	if rd.finished() {
		t.Logf("the Read returned within the %v grace period, while the %s was still held in "+
			"its chunk Put, so this run did not exercise #41's window; the reply is still checked",
			rcGrace, flush)
	}
	gate.open()
	if err := fl.wait(t, "the "+flush+", after its chunk Put was released"); err != nil {
		t.Fatalf("the %s = %v, want success against a healthy writable store", flush, err)
	}
	r := rd.wait(t, "the Read, after the "+flush+" was released")
	if r.err != nil {
		t.Fatalf("the Read of [0, %d) = %v, want success", count, r.err)
	}
	if r.eof != growth {
		t.Errorf("the Read of [0, %d) of a %d-byte file returned eof = %v, want %v (ADR 0005 §1: "+
			"eof is decided by the size at the view)", count, len(wantFile), r.eof, growth)
	}
	switch {
	case bytes.Equal(r.data, want):
	case bytes.Equal(r.data, stale):
		t.Errorf("the Read of [0, %d), called after a Write of [0, 100) had been acknowledged and "+
			"while a %s of the file was held in its chunk Put, returned %s: a flush made the Read "+
			"miss an acknowledged write. ADR 0005 §1(a): every byte that a Write which returned "+
			"before the Read was called put in the file reads as that Write left it; §6: step 2 "+
			"comes entirely before a flush or entirely after it (#41)", count, flush, staleIs)
	default:
		t.Errorf("the Read of [0, %d), called after a Write of [0, 100) had been acknowledged and "+
			"while a %s of the file was held in its chunk Put: %s (ADR 0005 §1(a))",
			count, flush, wsDiff(r.data, want, 50))
	}
	bpMustSync(t, fs, "Sync after the Read")
	bpWantFile(t, fs, f, wantFile, "f.bin after the "+flush+" and a Sync")
	bpWantRemount(t, st, name, wantFile, "f.bin after the "+flush+" and a Sync")
}

// The file TestReadConcurrentWithWrites reads and writes is rcFileSize bytes,
// two chunks, cut into rcRanges ranges of rcRangeSize bytes. Range k at
// version v holds rcRangeSize/8 copies of the big-endian word
// uint64(k)<<rcVersionBits | v. Ranges rcPairLo and rcPairLo+1 straddle the
// chunk boundary and are only ever written together, by one Write. rcReaders
// goroutines read. The writer stops after rcStressTime or rcMaxWrites Writes,
// whichever comes first, but never before one full cycle of its units. At most
// rcMaxReports failures are reported in full. rcDrainLimit is the budget of
// the drain shape: the file's two chunk indices.
const (
	rcFileSize   = 2 * bpCS
	rcRangeSize  = 512
	rcRanges     = rcFileSize / rcRangeSize
	rcPairLo     = 7
	rcReaders    = 4
	rcMaxWrites  = 3000
	rcStressTime = 500 * time.Millisecond
	rcMaxReports = 10
	rcDrainLimit = rcFileSize
)

// rcVersionBits is how many low bits of a range's word hold its version; the
// bits above them hold the range's index.
const rcVersionBits = 40

// rcVersionMask selects the version from a range's word.
const rcVersionMask = 1<<rcVersionBits - 1

// rcRangeBytes returns range k's contents at version v.
func rcRangeBytes(k int, v uint64) []byte {
	word := uint64(k)<<rcVersionBits | v
	b := make([]byte, rcRangeSize)
	for i := 0; i < rcRangeSize; i += 8 {
		binary.BigEndian.PutUint64(b[i:], word)
	}
	return b
}

// rcUnits returns the writer's units in order: each range on its own, except
// the pair, which is one unit.
func rcUnits() [][]int {
	var units [][]int
	for k := 0; k < rcRanges; k++ {
		if k == rcPairLo {
			units = append(units, []int{k, k + 1})
			k++
			continue
		}
		units = append(units, []int{k})
	}
	return units
}

// rcWindow is the byte range [off, end) of one Read.
type rcWindow struct{ off, end int }

// rcWindows returns the readers' windows: the whole file; [3072, 5120), which
// is ranges 6 to 9 and crosses the chunk boundary with the pair; and each
// range on its own.
func rcWindows() []rcWindow {
	ws := []rcWindow{{0, rcFileSize}, {3072, 5120}}
	for k := range rcRanges {
		off := k * rcRangeSize
		end := off + rcRangeSize
		ws = append(ws, rcWindow{off, end})
	}
	return ws
}

// rcCheckReply checks one Read of win against ADR 0005 §1. floor[k] is what
// done[k] held before the Read was called, and seen[k] what started[k] held
// after it returned, for each range k in win. It returns what is wrong.
func rcCheckReply(win rcWindow, r rcReply, floor, seen *[rcRanges]uint64) []string {
	what := fmt.Sprintf("a Read of [%d, %d)", win.off, win.end)
	if r.err != nil {
		return []string{fmt.Sprintf("%s = %v, want success", what, r.err)}
	}
	if len(r.data) != win.end-win.off {
		return []string{fmt.Sprintf("%s returned %d bytes, want %d: the file is %d bytes "+
			"throughout, and a reply is exactly as long as the clamped range (ADR 0005 §2)",
			what, len(r.data), win.end-win.off, rcFileSize)}
	}
	var probs []string
	if wantEOF := win.end == rcFileSize; r.eof != wantEOF {
		probs = append(probs, fmt.Sprintf("%s returned eof = %v, want %v: the file is %d bytes "+
			"throughout, and eof is decided by the size at the view (ADR 0005 §1)",
			what, r.eof, wantEOF, rcFileSize))
	}
	var vers [rcRanges]uint64
	var have [rcRanges]bool
	for k := win.off / rcRangeSize; k < win.end/rcRangeSize; k++ {
		lo := k*rcRangeSize - win.off
		b := r.data[lo : lo+rcRangeSize]
		word := binary.BigEndian.Uint64(b)
		torn := -1
		for i := 8; i < rcRangeSize; i += 8 {
			if binary.BigEndian.Uint64(b[i:]) != word {
				torn = i / 8
				break
			}
		}
		if torn >= 0 {
			probs = append(probs, fmt.Sprintf("%s: range %d is torn, its word %d being %#x and "+
				"its word 0 %#x. Every Write writes a range whole, so a range holding two "+
				"different words holds bytes from before and after one Write. ADR 0005 §1 "+
				"(atomicity): one reply never holds bytes from both before and after one Write "+
				"(#49)", what, k, torn, binary.BigEndian.Uint64(b[torn*8:]), word))
			continue
		}
		if gk := int(word >> rcVersionBits); gk != k {
			probs = append(probs, fmt.Sprintf("%s: range %d holds the word %#x, which only range "+
				"%d is ever written with: bytes at the wrong offset", what, k, word, gk))
			continue
		}
		v := word & rcVersionMask
		if v < floor[k] {
			probs = append(probs, fmt.Sprintf("%s: range %d is at version %d, but the Write of "+
				"version %d had returned before the Read was called, and only later Writes, of "+
				"higher versions, change the range. ADR 0005 §1 (freshness): every byte that a "+
				"Write which returned before Read was called put in the file reads as that Write "+
				"left it (#41)", what, k, v, floor[k]))
		}
		if v > seen[k] {
			probs = append(probs, fmt.Sprintf("%s: range %d is at version %d, but no Write of "+
				"it newer than version %d had been started by the time the Read returned. ADR "+
				"0005 §1: a Read returns the bytes the file holds at one instant between the call "+
				"and its return", what, k, v, seen[k]))
		}
		vers[k], have[k] = v, true
	}
	if lo, hi := rcPairLo, rcPairLo+1; have[lo] && have[hi] && vers[lo] != vers[hi] {
		probs = append(probs, fmt.Sprintf("%s: ranges %d and %d are at versions %d and %d. Only "+
			"one Write, of both at once, ever changes either, so a reply holding different "+
			"versions of them holds bytes from before and after one Write. ADR 0005 §1 "+
			"(atomicity): one reply never holds bytes from both before and after one Write (#49)",
			what, lo, hi, vers[lo], vers[hi]))
	}
	return probs
}

// TestReadConcurrentWithWrites is the stress test that #41's and #49's
// done-when ask for (ADR 0005 §7, "Stress"): readers against a writer, with
// and without flushes, checking §1's freshness, (a), and atomicity, (b), for
// each Read call. It runs in three shapes: buffered writes only, with the
// default limit and nothing flushing; with the default limit and one goroutine
// looping Sync and one looping Commit(h, 0, 0); and with MaxDirtyBytes two
// chunks and no loops, so that the writer's own Writes drain the budget (a
// drain is a flush, §6).
//
// The file is rcFileSize bytes of rcRanges ranges, prefilled at version 1 in
// one Write and synced. One writer cycles through the ranges, writing each
// whole in one Write at the next version of a single counter, except ranges 7
// and 8, which straddle the chunk boundary and are written together by one
// 1024-byte Write. Before each Write it stores started[k] = v for the ranges
// it writes, and after it returns done[k] = v. Four readers loop over the
// windows of rcWindows, each Read made once and checked by rcCheckReply. Once
// every goroutine has stopped, a Sync runs, and every range must be at exactly
// its done version on this mount and on a fresh one.
//
// No check can fail a Read that returns the file at one instant between its
// call and its return (§1):
//   - Each range is only ever written whole, with 64 copies of one word naming
//     the range and the Write's version. So at every instant each range holds
//     64 equal words with its own index, and ranges 7 and 8, only ever written
//     together, hold the same version. A reply that is one instant of the file
//     passes the torn, offset and pair checks; failing one means the reply held
//     bytes from both before and after one Write.
//   - floor[k] is loaded from done[k] before the Read is called, and done[k] = v
//     is stored only after the Write of version v has returned. That Write
//     therefore returned before the Read was called, and §1(a) says the range
//     reads as it left it unless another Write changed it by the view. With one
//     writer and one counter, every later Write of the range has a higher
//     version. So v >= floor[k].
//   - started[k] is loaded after the Read returns, and started[k] = v is stored
//     before the Write of version v is called. The view is an instant before
//     the Read returns, and no Write's bytes are in the file before the Write
//     is called. sync/atomic operations are sequentially consistent in the Go
//     memory model, so the version the view holds had been stored before the
//     load. So v <= started[k].
//   - The prefill makes the file rcFileSize bytes and no Write extends it, so
//     the length and eof are fixed.
//
// Detection is probabilistic. The Read that ADR 0005 replaces copied dirty
// bytes holding no lock, which -race reports in every shape if a Write lands in
// the copy during the run (the race detector finds only races that happen,
// §7), and in the two shapes that flush it could read a chunk list from
// before a flush with a dirty map from after it, which fails the floor check.
// The goroutines never touch t: failures are collected under a mutex, the
// first rcMaxReports in full and the rest counted.
func TestReadConcurrentWithWrites(t *testing.T) {
	shapes := []struct {
		name  string
		limit int64
		loops bool
	}{
		{"buffered writes only", 0, false},
		{"with Sync and Commit loops", 0, true},
		{"with budget drains", rcDrainLimit, false},
	}
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) { rcStress(t, s.limit, s.loops) })
	}
}

// rcStress is one shape of TestReadConcurrentWithWrites.
func rcStress(t *testing.T, limit int64, loops bool) {
	t.Helper()
	ctx := context.Background()
	st := bpLocal(t)
	c := newBTCapture(false)
	fs := bpNew(t, st, limit, c.logger())
	const name = "stress.bin"
	h, _ := bpCreate(t, fs, name, 0)

	var started, done [rcRanges]atomic.Uint64
	prefill := make([]byte, 0, rcFileSize)
	for k := range rcRanges {
		prefill = append(prefill, rcRangeBytes(k, 1)...)
		started[k].Store(1)
		done[k].Store(1)
	}
	bpMustWrite(t, fs, "the prefill, every range at version 1, in one Write", h, 0, prefill)
	bpMustSync(t, fs, "Sync of the prefill")

	var failMu sync.Mutex
	var failures []string
	failCount := 0
	fail := func(format string, args ...any) {
		failMu.Lock()
		defer failMu.Unlock()
		failCount++
		if len(failures) < rcMaxReports {
			failures = append(failures, fmt.Sprintf(format, args...))
		}
	}

	var writes, reads, syncs, commits, progress atomic.Int64
	begin := make(chan struct{})
	stop := make(chan struct{})
	var stopOnce sync.Once
	halt := func() { stopOnce.Do(func() { close(stop) }) }
	defer halt()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		<-begin
		units := rcUnits()
		t0 := time.Now()
		v := uint64(1)
		for i := 0; ; i++ {
			if i >= len(units) && (i >= rcMaxWrites || time.Since(t0) >= rcStressTime) {
				return
			}
			u := units[i%len(units)]
			v++
			data := make([]byte, 0, len(u)*rcRangeSize)
			for _, k := range u {
				started[k].Store(v)
				data = append(data, rcRangeBytes(k, v)...)
			}
			off := u[0] * rcRangeSize
			n, _, err := fs.Write(ctx, testCaller, h, uint64(off), data, vfs.Unstable)
			if err != nil || int(n) != len(data) {
				fail("the Write of version %d to [%d, %d) = (%d, %v), want (%d, nil): every "+
					"Write here must succeed in full", v, off, off+len(data), n, err, len(data))
				return
			}
			for _, k := range u {
				done[k].Store(v)
			}
			writes.Add(1)
		}
	}()

	wins := rcWindows()
	var readers sync.WaitGroup
	for r := range rcReaders {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-begin
			for i := r; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				win := wins[i%len(wins)]
				first, last := win.off/rcRangeSize, win.end/rcRangeSize
				var floor, seen [rcRanges]uint64
				for k := first; k < last; k++ {
					floor[k] = done[k].Load()
				}
				data, eof, err := fs.Read(ctx, testCaller, h, uint64(win.off), uint32(win.end-win.off))
				for k := first; k < last; k++ {
					seen[k] = started[k].Load()
				}
				reads.Add(1)
				progress.Add(1)
				for _, p := range rcCheckReply(win, rcReply{data, eof, err}, &floor, &seen) {
					fail("reader %d: %s", r, p)
				}
			}
		}()
	}

	var loopers sync.WaitGroup
	loop := func(what string, count *atomic.Int64, fn func() error) {
		loopers.Add(1)
		go func() {
			defer loopers.Done()
			<-begin
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := fn(); err != nil {
					fail("%s concurrent with Reads and Writes = %v; a flush against a healthy "+
						"writable store must succeed", what, err)
					return
				}
				count.Add(1)
				progress.Add(1)
			}
		}()
	}
	if loops {
		loop("Sync", &syncs, func() error { return fs.Sync(ctx) })
		loop("Commit(h, 0, 0)", &commits, func() error { return fs.Commit(ctx, h, 0, 0) })
	}

	t0 := time.Now()
	close(begin)
	bpWaitProgress(t, "the writer's Writes", writerDone, &writes)
	halt()
	rest := make(chan struct{})
	go func() {
		readers.Wait()
		loopers.Wait()
		close(rest)
	}()
	bpWaitProgress(t, "the readers' Reads and the flush loops, after being told to stop", rest, &progress)
	elapsed := time.Since(t0)

	failMu.Lock()
	errs := append([]string(nil), failures...)
	total := failCount
	failMu.Unlock()
	for _, e := range errs {
		t.Error(e)
	}
	if total > len(errs) {
		t.Errorf("and %d more failures like those", total-len(errs))
	}
	t.Logf("%d Writes, %d Reads by %d readers, %d Syncs, %d Commits and %d budget drains in %v",
		writes.Load(), reads.Load(), rcReaders, syncs.Load(), commits.Load(),
		len(c.withMessage(btMsgInfo)), elapsed.Round(time.Millisecond))

	bpMustSync(t, fs, "Sync after every goroutine stopped")
	want := make([]byte, 0, rcFileSize)
	for k := range rcRanges {
		want = append(want, rcRangeBytes(k, done[k].Load())...)
	}
	bpWantFile(t, fs, h, want, "every range at the version of its last acknowledged Write")
	bpWantRemount(t, st, name, want, "every range at the version of its last acknowledged Write")
}

// TestReadHoldsNoLockWhileFetching pins ADR 0005 §2 step 3, "Holding no lock.
// Fetch each recorded reference with loadChunk", §4, "No lock: every store
// call and every cache call Read makes", and §5, "a slow bucket holds up no
// Write, flush, truncate or namespace operation" (truncate aside, as the file
// header says). A Read of [cs−100, cs+100) is held in its store Get of chunk 0
// (§7: "fetches their chunk with the store's Get ... in step 3, holding no
// filesystem lock"), and each call below must complete while it is held.
//
// "no open-file entry" is §4's path for a file not written since the mount,
// which takes no openFile.mu at all, and §3's invariant 3, "No entry, no
// buffered state": step 1's view is complete. A Write to the same file, a
// Sync and a Create must complete, and the reply must be the range from
// before the Write or from after it, never a mixture (§1(b)).
//
// "with buffered writes" holds a Read whose view took index 1 from a dirty
// buffer. A Sync that flushes that buffer, a Write elsewhere in index 1 and a
// Create must complete, and the reply must be the view: nothing in the range
// changed, and §5: "A flush after the view repoints an index at a chunk
// holding ... the bytes the view already copied".
func TestReadHoldsNoLockWhileFetching(t *testing.T) {
	t.Run("no open-file entry", func(t *testing.T) {
		x := rcHoldRead(t, "unopened.bin", false)
		w := bpUnique(bpCS + 100)
		bpMustWrite(t, x.fs, "a Write of [0, 4196) to the same file"+rcNoLock, x.h, 0, w)
		bpMustSync(t, x.fs, "a Sync"+rcNoLock)
		rcMustCreate(t, x.fs, "made-while-held.bin")
		x.gate.open()
		got := x.read.wait(t, "the Read of [3996, 4196), after its fetch of chunk 0 was released")
		pre := x.data[bpCS-100 : bpCS+100]
		post := w[bpCS-100 : bpCS+100]
		rcWantOneView(t, "the Read of [3996, 4196) of a file with no FS.open entry, held in its "+
			"fetch of chunk 0 across a Write of [0, 4196)", got, pre, post, 100)
	})

	t.Run("with buffered writes", func(t *testing.T) {
		x := rcHoldRead(t, "buffered.bin", true)
		bpMustSync(t, x.fs, "a Sync flushing the file's dirty index 1"+rcNoLock)
		bpMustWrite(t, x.fs, "a Write of 100 bytes at 6000 to the same file"+rcNoLock, x.h, 6000, bpUnique(100))
		rcMustCreate(t, x.fs, "made-while-held.bin")
		x.gate.open()
		got := x.read.wait(t, "the Read of [3996, 4196), after its fetch of chunk 0 was released")
		if got.err != nil {
			t.Fatalf("the Read of [3996, 4196) = %v, want success", got.err)
		}
		if got.eof {
			t.Errorf("the Read of [3996, 4196) of an 8192-byte file returned eof, want false")
		}
		want := rcCat(x.data[bpCS-100:bpCS], x.d1[:100])
		if !bytes.Equal(got.data, want) {
			t.Errorf("the Read of [3996, 4196), whose view held index 0 as stored and index 1 "+
				"as a dirty buffer, and which was held in its fetch across a Sync of that buffer "+
				"and a Write outside the range: %s. ADR 0005 §1 and §5: the fetch returns what "+
				"the file held at the view", wsDiff(got.data, want, 100))
		}
	})
}

// TestReadCreatesNoOpenFile pins ADR 0005 §2, "Read never calls getOpen, so it
// never adds an entry to FS.open", and §7, "Read never adds an entry to
// FS.open". FS.open is read holding FS.mu for reading, as ADR 0003 §4's test
// surface says. A file on a fresh mount has no entry until its first write or
// truncate (§4), so an entry after nothing but Reads is one a Read added.
func TestReadCreatesNoOpenFile(t *testing.T) {
	st := bpLocal(t)
	const name = "read-only.bin"
	data := bpSeedFile(t, st, name, bpCS+100)
	fs := bpNew(t, st, 0, quietLog())
	h, attr := bpLookup(t, fs, name)
	if of := bpOpen(t, fs, attr.FileID, "after Lookup, before any Read"); of != nil {
		t.Fatalf("setup: FS.open already has an entry for inode %d after a Lookup on a fresh "+
			"mount, before any Read; ADR 0005 §4: a file gets its entry from its first write or "+
			"truncate on this mount", attr.FileID)
	}
	got := bpReadAll(t, fs, h, "reading the whole file on a fresh mount")
	if d := wsDiff(got, data, 512); d != "" {
		t.Errorf("the file read on a fresh mount: %s", d)
	}
	if of := bpOpen(t, fs, attr.FileID, "after the Reads"); of != nil {
		t.Errorf("FS.open has an entry for inode %d after nothing but Reads of it on a fresh "+
			"mount; ADR 0005 §2 and §7: Read never adds an entry to FS.open", attr.FileID)
	}
}

// rcOther is a caller who is neither the owner of the files these tests
// create, testCaller, nor in their group, as in TestPermissionsEnforced.
var rcOther = vfs.Caller{UID: 999, GID: 999}

// rcStartReadAs starts one fs.Read as c in its own goroutine.
func rcStartReadAs(fs *FS, c vfs.Caller, h vfs.Handle, off uint64, count uint32) *bpCall[rcReply] {
	return bpGo(func() rcReply {
		data, eof, err := fs.Read(context.Background(), c, h, off, count)
		return rcReply{data, eof, err}
	})
}

// The changes TestReadAnswersFromTheCheckUnderTheFileLock makes to a file
// while a Read of it waits for the file's lock.
const (
	rcChangeRemove = "Remove"
	rcChangeMode   = "SetAttr of the mode alone"
)

// rcRemoveNoLock ends the description of the Remove that
// TestReadAnswersFromTheCheckUnderTheFileLock makes while a Sync of the file
// holds its lock, so that a hang names the rule it breaks.
const rcRemoveNoLock = " (a hang here means the Remove waits for the file's openFile.mu, which " +
	"the Sync holds in its chunk Put: ADR 0005 §6, \"A removal\", says dropOpen runs under FS.mu " +
	"alone, ADR 0003 §3 that site 5 takes no openFile.mu, and ADR 0005 §6 that a flush's " +
	"uploads need no FS.mu)"

// rcModeNoLock does the same for the SetAttr of the mode alone.
const rcModeNoLock = " (a hang here means a SetAttr of the mode alone waits for the file's " +
	"openFile.mu, which the Sync holds in its chunk Put: ADR 0005 §6, \"An attribute change\", " +
	"§7 and Assumption 15 say a SetAttr that sets no size takes FS.mu for writing and never an " +
	"openFile.mu, so a flush of the file held in a chunk upload does not hold it up)"

// rcStaleRule and rcAccesRule are what the two cases of
// TestReadAnswersFromTheCheckUnderTheFileLock pin, for their failure messages.
const rcStaleRule = "ADR 0005 §2 step 2: \"The answer comes from this hold: a handle that has " +
	"gone since step 1 returns its error, vfs.ErrStale\"; §6, \"A removal\": a Read \"whose " +
	"step 2 comes after the removal returns vfs.ErrStale\"; Assumption 5: \"A handle removed " +
	"between the steps now gets vfs.ErrStale where the old Read returned data from its first hold\""

const rcAccesRule = "ADR 0005 §2 step 2: \"Resolve the handle and apply both checks again. The " +
	"answer comes from this hold: ... a permission change made since step 1 applies\""

// TestReadAnswersFromTheCheckUnderTheFileLock pins ADR 0005 §2 step 2:
// "Resolve the handle and apply both checks again. The answer comes from this
// hold: a handle that has gone since step 1 returns its error, vfs.ErrStale,
// and a permission change made since step 1 applies"; Assumption 5, "The
// checks are repeated in step 2, and the answer comes from there"; and §6, "A
// removal": "dropOpen (ADR 0003 §2, site 5) runs under FS.mu alone and leaves
// of.dirty in place. ... one whose step 2 comes after the removal returns
// vfs.ErrStale".
//
// It reaches the point between steps 1 and 2 the way §7 reaches #41's window.
// f.bin is created with mode 0644 and written, which gives it an FS.open entry
// (§4), so a Read of it takes that entry's openFile.mu in step 2. A Sync of it
// is held in its chunk Put, which holds that lock and "has repointed nothing
// yet" (§7). A Read of [0, 100) is started, rcGrace passes for it to get
// through step 1 and wait for the lock (§6: "A Read of a file waits for a
// flush of that file that is in progress"; Assumption 8), and then, while the
// Sync is still held, the file is changed:
//
//   - "removal": a Remove of f.bin, which must complete while the Sync holds
//     the file's lock: dropOpen "runs under FS.mu alone" (§6), site 5 "runs
//     under FS.mu and takes no openFile.mu" (ADR 0003 §3), and "a flush's
//     uploads ... need no FS.mu" (§6). The Read, by the owner, must return
//     vfs.ErrStale.
//   - "permission": a SetAttr of the mode alone, from 0644 to 0600, by the
//     owner. The Read, by rcOther, whom 0644 lets read and 0600 does not
//     (TestPermissionsEnforced), must return vfs.ErrAcces. The SetAttr must
//     complete while the Sync holds the file's lock: §6, "An attribute
//     change", says a SetAttr that sets no size "takes FS.mu for writing and
//     never an openFile.mu", and that it "can fall between the two while a
//     Read waits for the file's lock; step 2's checks then see it"; §7 lists
//     it among the behaviours a test may rely on; and Assumption 15 pins it.
//
// Then the Sync is released. A correct Read gives the same answer however it
// is scheduled: if its step 1 ran before the change, its step 2 runs after it
// and answers from there; if its step 1 ran after the change, step 1 fails the
// same way. A Read that answered from step 1's checks returns the 100 bytes,
// and is caught if it got through step 1 within rcGrace. A setup Read first
// shows that the same caller can read the file, so that the case cannot pass
// for want of it. What the Sync returns after the removal is not pinned
// (see the file header).
func TestReadAnswersFromTheCheckUnderTheFileLock(t *testing.T) {
	t.Run("removal", func(t *testing.T) { rcChangeWindow(t, rcChangeRemove) })
	t.Run("permission", func(t *testing.T) { rcChangeWindow(t, rcChangeMode) })
}

// rcChangeWindow is one case of TestReadAnswersFromTheCheckUnderTheFileLock.
func rcChangeWindow(t *testing.T, change string) {
	t.Helper()
	ctx := context.Background()
	bps := &bpStore{Store: bpLocal(t)}
	fs := bpNew(t, bps, 0, quietLog())
	const name = "f.bin"
	f, _ := bpCreate(t, fs, name, 0o644)
	data := bpUnique(100)
	bpMustWrite(t, fs, "the Write of 100 bytes at 0 of f.bin, which gives it an FS.open entry "+
		"(ADR 0005 §4)", f, 0, data)

	reader, readerIs, want, wantIs, rule := testCaller, "its owner", vfs.ErrStale, "vfs.ErrStale", rcStaleRule
	if change == rcChangeMode {
		reader, readerIs = rcOther, "a caller who is neither its owner nor in its group"
		want, wantIs, rule = vfs.ErrAcces, "vfs.ErrAcces", rcAccesRule
	}

	pre := rcStartReadAs(fs, reader, f, 0, 100).wait(t, "setup: a Read of [0, 100) of f.bin by "+readerIs)
	if pre.err != nil || !bytes.Equal(pre.data, data) {
		t.Fatalf("setup: a Read of [0, 100) of f.bin, mode 0644, by %s = (%d bytes, %v), want the "+
			"100 bytes just written; without that, a Read that answered from step 1's checks could "+
			"not be told from one that answered from step 2's", readerIs, len(pre.data), pre.err)
	}

	gate := newBTGate(t)
	put := &bpHook{entered: make(chan struct{}), gate: gate.ch}
	bps.setPut(put)
	fl := bpGo(func() error { return fs.Sync(ctx) })
	bpWaitEntered(t, "the Sync of f.bin entering its chunk Put", put.entered, fl)

	rd := rcStartReadAs(fs, reader, f, 0, 100)
	time.Sleep(rcGrace)
	if change == rcChangeRemove {
		err := bpDo(t, "the Remove of f.bin, made while a Read of it waits for its lock"+rcRemoveNoLock,
			func() error { return fs.Remove(ctx, testCaller, fs.Root(), name) })
		if err != nil {
			t.Fatalf("the Remove of f.bin by its owner = %v, want success", err)
		}
	} else {
		type setAttrResult struct {
			attr vfs.Attr
			err  error
		}
		mode := uint32(0o600)
		sa := bpGo(func() setAttrResult {
			attr, err := fs.SetAttr(ctx, testCaller, f, vfs.SetAttr{Mode: &mode})
			return setAttrResult{attr, err}
		}).wait(t, "the SetAttr of f.bin's mode alone, to 0600, made while a Read of it waits for "+
			"its lock"+rcModeNoLock)
		if sa.err != nil {
			t.Fatalf("the SetAttr of f.bin's mode to 0600 by its owner = %v, want success", sa.err)
		}
		if sa.attr.Mode != 0o600 {
			t.Fatalf("setup: the SetAttr of f.bin's mode to 0600 returned mode %#o, want 0600",
				sa.attr.Mode)
		}
	}
	if rd.finished() {
		t.Logf("the Read returned while the Sync still held the file's lock: its step 1 most "+
			"likely ran after the %s, so this run did not exercise the window between steps 1 "+
			"and 2; the reply is still checked", change)
	}

	gate.open()
	ferr := fl.wait(t, "the Sync, after its chunk Put was released")
	switch {
	case ferr == nil:
	case change == rcChangeMode:
		t.Errorf("the Sync of f.bin = %v, want success against a healthy writable store", ferr)
	default:
		t.Logf("the Sync of the removed f.bin = %v; no document says what a flush of a file "+
			"removed during its upload returns, so this test does not check it", ferr)
	}

	r := rd.wait(t, "the Read of [0, 100) of f.bin, after the Sync was released")
	switch {
	case r.err == nil:
		t.Errorf("the Read of [0, 100) of f.bin by %s, which waited for the file's lock while a %s "+
			"was made, returned %d bytes and no error, want %s: it answered from checks made "+
			"before the %s. %s", readerIs, change, len(r.data), wantIs, change, rule)
	case !errors.Is(r.err, want):
		t.Errorf("the Read of [0, 100) of f.bin by %s, which waited for the file's lock while a %s "+
			"was made, = %v, want %s. %s", readerIs, change, r.err, wantIs, rule)
	}
}

// The offsets TestReadRangeEndDoesNotWrap uses. The file's one written byte is
// at rcHugeByte, which makes its size rcHugeByte+1 = 2^64 − 1, and the Read
// asks for rcHugeCount bytes at rcHugeOff. Both offsets lie in the file's last
// chunk index, 2^52 − 1. Both are far past the MaxFileSize of ADR 0007 §1, so
// the test's FS stands for a build before ADR 0007, which had no limit (ADR
// 0007 §6, §7).
const (
	rcHugeByte  uint64 = 1<<64 - 2
	rcHugeOff   uint64 = 1<<64 - 10
	rcHugeCount uint32 = 100
)

// TestReadRangeEndDoesNotWrap pins ADR 0005 §2's end of the range, "The end of
// the range is off + min(count, size − off), computed once off < size is
// known, so that it cannot wrap", and Assumption 9, that off + count can wrap
// for an offset near 2^64; with §2's "the reply is exactly as long as the
// clamped range" and §1's "eof decided by that size".
//
// One UNSTABLE Write of 1 byte at 2^64 − 2 to an empty file makes its size
// 2^64 − 1. A Read of 100 bytes at 2^64 − 10 must then return the 9 bytes
// [2^64 − 10, 2^64 − 1): 8 zeros, which nothing wrote (§2: "bytes past the end
// of a chunk or of a dirty buffer read as zeros"; ADR 0003 §4: an index past
// the end of the chunk list starts from zeros), and the written byte, with
// eof. An end computed as off + count wraps round to 90, below off.
//
// Since ADR 0007 no Write puts a byte at or past MaxFileSize, 2^32 at this
// test's 4 KiB chunks (§1, §3, §4), so a file this large is one that a build
// before ADR 0007, which had no limit, left in the bucket; and such a file is
// still read whole, at any offset below its size (§6). The test stands for
// such a build as ADR 0007 §7 says: right after New, before first use, it sets
// the FS's maxFileSize to math.MaxUint64, and the Write at 2^64 − 2 is then
// accepted. The file is still never flushed, because a flush would append a
// hole to its chunk list for every chunk index up to 2^52 − 1 (ADR 0007,
// Context, route 2): the Write is UNSTABLE, the budget is the default, which
// one chunk does not reach, and nothing here Syncs, Commits or remounts.
func TestReadRangeEndDoesNotWrap(t *testing.T) {
	fs := bpNew(t, bpLocal(t), 0, quietLog())
	fs.maxFileSize = 1<<64 - 1 // math.MaxUint64
	h, _ := bpCreate(t, fs, "huge.bin", 0)
	b := []byte{0xa5}
	w := bpWrite(t, fs, "the UNSTABLE Write of 1 byte at 2^64 − 2 of the empty huge.bin", h, rcHugeByte, b,
		vfs.Unstable)
	if w.err != nil || w.n != 1 {
		t.Fatalf("setup: the UNSTABLE Write of 1 byte at 2^64 − 2 of the empty huge.bin = %v, want "+
			"(1, UNSTABLE, nil). This test stands for a build before ADR 0007, which had no limit: "+
			"fs.maxFileSize is math.MaxUint64, so the Write is below MaxFileSize and ends within it "+
			"(ADR 0007 §3, §4, §6, §7)", w)
	}
	if a := bpGetAttr(t, fs, h, "GetAttr of huge.bin after the Write"); a.Size != rcHugeByte+1 {
		t.Fatalf("setup: huge.bin's size after 1 byte written at 2^64 − 2 is %d, want 2^64 − 1 = %d",
			a.Size, rcHugeByte+1)
	}

	r := rcStartRead(fs, h, rcHugeOff, rcHugeCount).wait(t, "the Read of 100 bytes at 2^64 − 10 of huge.bin")
	if r.err != nil {
		t.Fatalf("the Read of 100 bytes at 2^64 − 10 of the (2^64 − 1)-byte huge.bin = %v, want success",
			r.err)
	}
	want := append(make([]byte, 8), b...)
	if !bytes.Equal(r.data, want) {
		t.Errorf("the Read of 100 bytes at 2^64 − 10 of the (2^64 − 1)-byte huge.bin returned %d "+
			"bytes, %x, want the 9 bytes to the end of the file, %x: 8 zeros and the byte written "+
			"at 2^64 − 2. ADR 0005 §2: the reply is exactly as long as the clamped range, whose end "+
			"is off + min(count, size − off), computed so that it cannot wrap (Assumption 9); "+
			"off + count wraps round to 90", len(r.data), r.data, want)
	}
	if !r.eof {
		t.Errorf("the Read of 100 bytes at 2^64 − 10 of the (2^64 − 1)-byte huge.bin returned eof = " +
			"false, want true: its range reaches the end of the file (ADR 0005 §1: eof is decided by " +
			"the size at the view; §2, the end of the range)")
	}
}
