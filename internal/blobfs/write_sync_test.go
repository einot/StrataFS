package blobfs

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"strata/internal/store"
	"strata/internal/vfs"
)

// Tests for the stability contract of Write, and for Write racing a flush.
//
// The stability rule, RFC 1813 §3.3.7 (WRITE), as recalled by the dispatching
// session, not verified against the RFC text:
//
//	If stable was FILE_SYNC, then committed must also be FILE_SYNC: anything
//	else constitutes a protocol violation. If stable was DATA_SYNC, then
//	committed may be FILE_SYNC or DATA_SYNC: anything else constitutes a
//	protocol violation.
//
// FILE_SYNC also means the data and the file's metadata are on stable storage
// by the time the reply is sent, with no COMMIT to follow. docs/DESIGN.md §9
// and docs/adr/0003-write-backpressure.md describe the UNSTABLE/COMMIT half of
// the same contract. ADR 0003's Context also lists a FILE_SYNC write among the
// things that drain buffered data.
//
// Every call that a hang could block runs under wsWithin, so a hang fails its
// test instead of blocking the suite.

// wsHangBound is how long a call gets before it counts as hung. The vfs
// contract lets Write delay a reply as backpressure, so a caller must not put a
// deadline on Write. This is a test watchdog, not a caller deadline. Nothing
// here buffers more than a few KiB against a 256 MiB default budget, so no
// correct implementation has a reason to delay. A correct implementation
// returns in milliseconds, so a 10 s bound fails only on a real hang.
const wsHangBound = 10 * time.Second

// wsWithin runs fn in its own goroutine and fails the test if it has not
// returned within wsHangBound. fn must not touch t: after a timeout its
// goroutine is abandoned, still blocked, and it may outlive the test.
func wsWithin(t *testing.T, what string, fn func(ctx context.Context) error) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(wsHangBound):
		t.Fatalf("%s has not returned after %v. With nothing near the dirty "+
			"budget, a correct implementation returns in milliseconds, so this "+
			"call is hung", what, wsHangBound)
		return nil
	}
}

// wsStability names a Stability the way RFC 1813 spells stable_how.
func wsStability(s vfs.Stability) string {
	switch s {
	case vfs.Unstable:
		return "UNSTABLE"
	case vfs.DataSync:
		return "DATA_SYNC"
	case vfs.FileSync:
		return "FILE_SYNC"
	}
	return fmt.Sprintf("Stability(%d)", uint32(s))
}

// wsPattern returns n bytes of seed-dependent, position-dependent content,
// so a byte that lands at the wrong offset does not read back as correct.
func wsPattern(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%251)
	}
	return b
}

// wsSync runs Sync under the hang bound and fails the test if it errors.
func wsSync(t *testing.T, fs *FS, what string) {
	t.Helper()
	if err := wsWithin(t, what, fs.Sync); err != nil {
		t.Fatalf("%s = %v; a Sync against a healthy writable store must succeed", what, err)
	}
}

// wsFileSyncWrite issues a FILE_SYNC Write under the hang bound. It checks
// that the write returns, that it accepts every byte, and that it answers
// committed=FILE_SYNC.
func wsFileSyncWrite(t *testing.T, fs *FS, h vfs.Handle, off uint64, data []byte) {
	t.Helper()
	var (
		n   uint32
		got vfs.Stability
	)
	what := fmt.Sprintf("Write(FILE_SYNC) of %d bytes at offset %d", len(data), off)
	err := wsWithin(t, what, func(ctx context.Context) error {
		var err error
		n, got, err = fs.Write(ctx, testCaller, h, off, data, vfs.FileSync)
		return err
	})
	if err != nil {
		t.Fatalf("%s = %v; a FILE_SYNC write to a regular file on a healthy "+
			"writable store must succeed", what, err)
	}
	if int(n) != len(data) {
		t.Fatalf("%s wrote %d bytes; a write that succeeds against a healthy "+
			"store must accept all %d", what, n, len(data))
	}
	if got != vfs.FileSync {
		t.Errorf("%s answered committed=%s; RFC 1813 §3.3.7 requires committed "+
			"to be FILE_SYNC when stable was FILE_SYNC, and anything else is a "+
			"protocol violation (rule as recalled by the dispatching session, "+
			"not verified against the RFC text)", what, wsStability(got))
	}
}

// wsReadAll reads h to EOF like readAll does, but under the hang bound. The
// tests that use it read a file whose flushes may be wedged behind a hung
// write.
func wsReadAll(t *testing.T, fs *FS, h vfs.Handle) []byte {
	t.Helper()
	var out []byte
	err := wsWithin(t, "reading the file back", func(ctx context.Context) error {
		var off uint64
		for {
			chunk, eof, err := fs.Read(ctx, testCaller, h, off, 1024)
			if err != nil {
				return fmt.Errorf("read at %d: %w", off, err)
			}
			out = append(out, chunk...)
			off += uint64(len(chunk))
			if eof || len(chunk) == 0 {
				return nil
			}
		}
	})
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	return out
}

// wsRemountLookup mounts a second FS on st, as TestWriteReadRemount does, and
// looks up name in its root. The new FS shares nothing with the first one
// except the bucket, so what it sees is what reached stable storage.
func wsRemountLookup(t *testing.T, st *store.Local, name string) (*FS, vfs.Handle, vfs.Attr) {
	t.Helper()
	ctx := context.Background()
	fs2, err := New(ctx, Config{Store: st, ChunkSize: 4096, Log: quietLog()})
	if err != nil {
		t.Fatalf("remount: %v", err)
	}
	h2, attr, err := fs2.Lookup(ctx, testCaller, fs2.Root(), name)
	if err != nil {
		t.Fatalf("lookup of %s on a second mount: %v", name, err)
	}
	return fs2, h2, attr
}

// wsDiff compares got with want in ranges of rangeSize bytes. It returns ""
// when they are equal. Otherwise it names the first few damaged ranges, with
// the first wrong byte in each, and counts the rest.
func wsDiff(got, want []byte, rangeSize int) string {
	var b strings.Builder
	if len(got) != len(want) {
		fmt.Fprintf(&b, "read %d bytes, want %d; ", len(got), len(want))
	}
	const maxReported = 8
	damaged, reported, total := 0, 0, 0
	for lo := 0; lo < len(want); lo += rangeSize {
		total++
		hi := min(lo+rangeSize, len(want))
		for i := lo; i < hi; i++ {
			if i < len(got) && got[i] == want[i] {
				continue
			}
			damaged++
			if reported < maxReported {
				reported++
				if i >= len(got) {
					fmt.Fprintf(&b, "range %d [%d,%d): byte %d missing (file too short), want 0x%02x; ",
						lo/rangeSize, lo, hi, i, want[i])
				} else {
					fmt.Fprintf(&b, "range %d [%d,%d): byte %d is 0x%02x, want 0x%02x; ",
						lo/rangeSize, lo, hi, i, got[i], want[i])
				}
			}
			break
		}
	}
	if b.Len() == 0 {
		return ""
	}
	if damaged > reported {
		fmt.Fprintf(&b, "and %d more damaged ranges; ", damaged-reported)
	}
	fmt.Fprintf(&b, "%d of %d ranges damaged", damaged, total)
	return b.String()
}

// TestWriteFileSyncReturns checks that a FILE_SYNC write with data returns,
// accepts every byte, and answers committed=FILE_SYNC (RFC 1813 §3.3.7, rule
// quoted at the top of this file, as recalled by the dispatching session and
// not verified against the RFC text). Targets Bug 1: such a write never
// returns. Covers both a fresh file and an overwrite of committed data.
func TestWriteFileSyncReturns(t *testing.T) {
	cases := []struct {
		name    string
		prefill bool
		off     uint64
		size    int
	}{
		{"new file", false, 0, 6000},
		{"overwrite of committed data", true, 3000, 3000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, _, _ := newTestFS(t)
			h := mustCreate(t, fs, fs.Root(), "filesync.bin")
			if tc.prefill {
				mustWrite(t, fs, h, 0, bytes.Repeat([]byte{'.'}, 10000))
				wsSync(t, fs, "Sync of the prefill")
			}
			wsFileSyncWrite(t, fs, h, tc.off, wsPattern(tc.size, 'F'))
		})
	}
}

// TestWriteFileSyncDurableWithoutCommit checks what FILE_SYNC promises: once
// the write has returned, its data and the file's metadata are on stable
// storage, with no Sync or Commit to follow. A second mount on the same bucket
// must read the data back. The create, and any prefill, are committed before
// the FILE_SYNC write. That way the test depends only on what FILE_SYNC
// promises about the write, not on whether CREATE is durable. Targets Bug 1
// (the write never returns) and any fix that returns FILE_SYNC without
// committing.
func TestWriteFileSyncDurableWithoutCommit(t *testing.T) {
	cases := []struct {
		name    string
		prefill bool
		off     uint64
		size    int
	}{
		{"new file", false, 0, 10000},
		{"overwrite of committed data", true, 3000, 3000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, st, _ := newTestFS(t)
			const name = "durable.bin"
			h := mustCreate(t, fs, fs.Root(), name)
			var want []byte
			if tc.prefill {
				prefill := bytes.Repeat([]byte{'.'}, 10000)
				mustWrite(t, fs, h, 0, prefill)
				want = append([]byte(nil), prefill...)
			}
			wsSync(t, fs, "Sync of the create and prefill")

			data := wsPattern(tc.size, 'D')
			wsFileSyncWrite(t, fs, h, tc.off, data)
			if end := int(tc.off) + len(data); end > len(want) {
				want = append(want, make([]byte, end-len(want))...)
			}
			copy(want[tc.off:], data)

			// No Sync, no Commit: the FILE_SYNC reply alone must have made
			// this durable.
			fs2, h2, attr := wsRemountLookup(t, st, name)
			if attr.Size != uint64(len(want)) {
				t.Errorf("size on a second mount = %d, want %d; a FILE_SYNC write "+
					"must commit the file's metadata before replying, with no "+
					"COMMIT to follow", attr.Size, len(want))
			}
			if d := wsDiff(readAll(t, fs2, h2, len(want)), want, 512); d != "" {
				t.Errorf("a second mount does not see the FILE_SYNC data; a "+
					"FILE_SYNC write must be on stable storage when it replies, "+
					"with no COMMIT to follow: %s", d)
			}
		})
	}
}

// TestWriteFileSyncDoesNotWedgeFile checks that the file stays usable after a
// FILE_SYNC write. The following must each return within wsHangBound, in
// order: an UNSTABLE Write to the same file, a Commit on it, and a Sync. The
// data must then read back. Targets Bug 1's second symptom: every later flush
// of the file hangs behind the stuck FILE_SYNC write.
func TestWriteFileSyncDoesNotWedgeFile(t *testing.T) {
	fs, _, _ := newTestFS(t)
	h := mustCreate(t, fs, fs.Root(), "wedge.bin")

	first := wsPattern(6000, 'A')
	wsFileSyncWrite(t, fs, h, 0, first)

	second := wsPattern(500, 'B')
	var n uint32
	err := wsWithin(t, "an UNSTABLE Write after a FILE_SYNC write", func(ctx context.Context) error {
		var err error
		n, _, err = fs.Write(ctx, testCaller, h, 3000, second, vfs.Unstable)
		return err
	})
	if err != nil {
		t.Fatalf("UNSTABLE Write after a FILE_SYNC write = %v; it must succeed", err)
	}
	if int(n) != len(second) {
		t.Fatalf("UNSTABLE Write after a FILE_SYNC write wrote %d bytes; a write "+
			"that succeeds must accept all %d", n, len(second))
	}

	err = wsWithin(t, "Commit after a FILE_SYNC write", func(ctx context.Context) error {
		return fs.Commit(ctx, h, 0, 0)
	})
	if err != nil {
		t.Fatalf("Commit after a FILE_SYNC write = %v; a Commit against a healthy "+
			"writable store must succeed", err)
	}
	wsSync(t, fs, "Sync after a FILE_SYNC write")

	want := append([]byte(nil), first...)
	copy(want[3000:], second)
	if d := wsDiff(wsReadAll(t, fs, h), want, 512); d != "" {
		t.Errorf("after FILE_SYNC write, UNSTABLE write, Commit and Sync, the "+
			"file must hold the FILE_SYNC data with the UNSTABLE write "+
			"overlaid: %s", d)
	}
}

// TestWriteDataSyncAnsweredStably checks that a DATA_SYNC write answers
// committed=DATA_SYNC or committed=FILE_SYNC, never UNSTABLE. That is RFC 1813
// §3.3.7, as recalled by the dispatching session and not verified against the
// RFC text: "If stable was DATA_SYNC, then committed may be FILE_SYNC or
// DATA_SYNC: anything else constitutes a protocol violation." Targets
// suspected Bug 3: DATA_SYNC answered as UNSTABLE. The call is bounded as
// well, in case DATA_SYNC shares FILE_SYNC's path and therefore Bug 1.
func TestWriteDataSyncAnsweredStably(t *testing.T) {
	fs, _, _ := newTestFS(t)
	h := mustCreate(t, fs, fs.Root(), "datasync.bin")

	data := wsPattern(5000, 'S')
	var (
		n   uint32
		got vfs.Stability
	)
	err := wsWithin(t, "Write(DATA_SYNC)", func(ctx context.Context) error {
		var err error
		n, got, err = fs.Write(ctx, testCaller, h, 0, data, vfs.DataSync)
		return err
	})
	if err != nil {
		t.Fatalf("Write(DATA_SYNC) = %v; a DATA_SYNC write to a regular file on "+
			"a healthy writable store must succeed", err)
	}
	if int(n) != len(data) {
		t.Fatalf("Write(DATA_SYNC) wrote %d bytes; a write that succeeds against "+
			"a healthy store must accept all %d", n, len(data))
	}
	if got != vfs.DataSync && got != vfs.FileSync {
		t.Errorf("Write(DATA_SYNC) answered committed=%s; RFC 1813 §3.3.7 allows "+
			"only DATA_SYNC or FILE_SYNC when stable was DATA_SYNC, and anything "+
			"else is a protocol violation (rule as recalled by the dispatching "+
			"session, not verified against the RFC text)", wsStability(got))
	}
}

// Parameters for TestWriteConcurrentFlushLosesNothing.
//
// The file spans wsRaceChunks chunks of 4096 bytes, the chunk size newTestFS
// configures. The writer fills it in wsRaceRange-byte pieces, 1024 writes per
// iteration, and never lets a piece straddle a chunk. Each write is one chance
// for a concurrent flush to land between Write reading the file's chunk list
// and Write modifying the chunk. Small pieces give many chances per iteration
// for the same fixed cost of mounting, remounting and reading back.
//
// Each shape runs up to wsRaceMaxIters iterations, about 40,000 windows. It
// also stops once wsRaceBudget of wall-clock time has passed. At least one
// iteration always runs. Under -race an iteration should take tens of
// milliseconds. The cap keeps each shape near 1.5 s, and the whole test near
// 3 s, even on a slow CI machine. A faster machine runs more iterations in the
// same time.
//
// Each range k is filled with wsRangeByte(k), which is never 0x00 (what a
// rebuild from a missing chunk yields) and never wsRaceBase (the overwrite
// shape's original contents). A failure message's actual byte therefore shows
// which way the range was lost.
const (
	wsRaceChunkSize = 4096
	wsRaceChunks    = 4
	wsRaceRange     = 16
	wsRaceFileSize  = wsRaceChunks * wsRaceChunkSize
	wsRaceRanges    = wsRaceFileSize / wsRaceRange
	wsRaceMaxIters  = 40
	wsRaceBudget    = 1500 * time.Millisecond
	wsRaceBase      = 0xFF
)

// wsRangeByte is range k's fill byte, cycling through 0x01..0xFE.
func wsRangeByte(k int) byte { return byte(1 + k%254) }

// TestWriteConcurrentFlushLosesNothing checks that a flush running
// concurrently with Write never makes a write discard bytes an earlier write
// put in the same chunk. Targets Bug 2: if a concurrent Sync or Commit uploads
// a chunk after Write has read the file's chunk list but before Write modifies
// the chunk, Write rebuilds the chunk from the out-of-date list. The earlier
// write's bytes are lost, both on read-back and after a remount.
//
// Two shapes:
//   - growth: an empty file filled by appending. The out-of-date view is "no
//     such chunk", so the rebuild starts from zeros.
//   - overwrite: a file written and committed with wsRaceBase, then
//     overwritten piece by piece. The out-of-date view is the older chunk
//     contents.
//
// One goroutine writes the pieces in ascending order with UNSTABLE writes.
// Two more loop Sync and Commit(h, 0, 0) until the writer finishes. Commit
// adds a second flush path, so more flushes land inside a write's window.
// Once the writer and both loops have stopped, a final Sync runs. Every byte
// is then checked on the same mount and again on a fresh mount of the bucket.
//
// A correct implementation cannot fail this, however the goroutines
// interleave. The pieces are disjoint and each is written exactly once, so
// every acknowledged byte has exactly one right value. Flushes only change
// where a byte is stored, never what it is. The only assertion is that the
// bytes written are the bytes read back.
//
// Detection is probabilistic. The bug needs a flush inside a narrow window, so
// buggy code can pass a run. A failure is always a real loss of acknowledged
// data.
func TestWriteConcurrentFlushLosesNothing(t *testing.T) {
	shapes := []struct {
		name      string
		overwrite bool
	}{
		{"growth", false},
		{"overwrite", true},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			start := time.Now()
			iters := 0
			for iters < wsRaceMaxIters {
				if iters > 0 && time.Since(start) >= wsRaceBudget {
					break
				}
				wsRaceIteration(t, shape.name, iters, shape.overwrite)
				iters++
			}
			t.Logf("%d iterations of %d writes in %v", iters, wsRaceRanges,
				time.Since(start).Round(time.Millisecond))
		})
	}
}

// wsRaceIteration runs one iteration of TestWriteConcurrentFlushLosesNothing
// on a fresh filesystem and bucket.
func wsRaceIteration(t *testing.T, shape string, iter int, overwrite bool) {
	t.Helper()
	ctx := context.Background()
	fs, st, _ := newTestFS(t)
	const name = "race.bin"
	h := mustCreate(t, fs, fs.Root(), name)
	if overwrite {
		mustWrite(t, fs, h, 0, bytes.Repeat([]byte{wsRaceBase}, wsRaceFileSize))
		wsSync(t, fs, "Sync of the overwrite shape's original contents")
	}

	want := make([]byte, wsRaceFileSize)
	for k := range wsRaceRanges {
		for i := k * wsRaceRange; i < (k+1)*wsRaceRange; i++ {
			want[i] = wsRangeByte(k)
		}
	}

	// The goroutines report failures here rather than through t: after a
	// hang they are abandoned and may outlive the test.
	var (
		mu       sync.Mutex
		failures []string
	)
	fail := func(format string, args ...any) {
		mu.Lock()
		failures = append(failures, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	stop := make(chan struct{})
	var stopOnce sync.Once
	halt := func() { stopOnce.Do(func() { close(stop) }) }
	defer halt()

	var flushers sync.WaitGroup
	running := make(chan struct{}, 2)
	flush := func(what string, fn func(context.Context) error) {
		defer flushers.Done()
		running <- struct{}{}
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := fn(ctx); err != nil {
				fail("%s concurrent with writes = %v; a flush against a healthy "+
					"writable store must succeed", what, err)
				return
			}
		}
	}
	flushers.Add(2)
	go flush("Sync", fs.Sync)
	go flush("Commit", func(ctx context.Context) error { return fs.Commit(ctx, h, 0, 0) })
	<-running
	<-running

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for k := range wsRaceRanges {
			lo := k * wsRaceRange
			data := bytes.Repeat([]byte{wsRangeByte(k)}, wsRaceRange)
			n, _, err := fs.Write(ctx, testCaller, h, uint64(lo), data, vfs.Unstable)
			if err != nil {
				fail("UNSTABLE Write of range %d [%d,%d) = %v; it must succeed",
					k, lo, lo+wsRaceRange, err)
				return
			}
			if int(n) != len(data) {
				fail("UNSTABLE Write of range %d [%d,%d) wrote %d bytes; a write "+
					"that succeeds must accept all %d", k, lo, lo+wsRaceRange, n, len(data))
				return
			}
		}
	}()

	wsWithin(t, "the writer's UNSTABLE writes", func(context.Context) error {
		<-writerDone
		return nil
	})
	halt()
	wsWithin(t, "the concurrent Sync and Commit loops, after being told to stop",
		func(context.Context) error {
			flushers.Wait()
			return nil
		})

	mu.Lock()
	errs := append([]string(nil), failures...)
	mu.Unlock()
	if len(errs) > 0 {
		t.Fatalf("%s iteration %d: %s", shape, iter, strings.Join(errs, "; "))
	}

	wsSync(t, fs, "final Sync after the writes and flush loops finished")

	if d := wsDiff(readAll(t, fs, h, wsRaceFileSize), want, wsRaceRange); d != "" {
		t.Fatalf("%s iteration %d, same mount: every byte an acknowledged "+
			"Write put in the file must read back as written, whatever "+
			"flushes ran concurrently (0x00 = rebuilt from a missing chunk, "+
			"0x%02x = rebuilt from the older contents): %s",
			shape, iter, wsRaceBase, d)
	}

	fs2, h2, _ := wsRemountLookup(t, st, name)
	if d := wsDiff(readAll(t, fs2, h2, wsRaceFileSize), want, wsRaceRange); d != "" {
		t.Fatalf("%s iteration %d, second mount after the final Sync: every "+
			"byte an acknowledged Write put in the file must be durable once "+
			"Sync returns, whatever flushes ran concurrently (0x00 = rebuilt "+
			"from a missing chunk, 0x%02x = rebuilt from the older contents): %s",
			shape, iter, wsRaceBase, d)
	}
}
