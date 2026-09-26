package blobfs

// Clean-room tests for ADR 0006, "A pending trim is part of the file, and is
// never buffered" (docs/adr/0006-a-pending-trim-is-part-of-the-file.md), which
// are the tests for #40 and #56. They are written from that ADR, from ADR 0003
// §2 (sites 3 and 4, the table "What each operation does to DirtyBytes()" and
// how a test reaches each truncate row), §4 (the test surface), §6 and §8, from
// ADR 0004 §5 and §7, from ADR 0005 §1, §2, §6 and §7, and from
// internal/vfs/vfs.go, without reading the implementation. They cover:
//
//   - §1, what a pending trim means, on each of Context's five routes: a
//     Write into the trimmed chunk (#40), a second truncate at the same index
//     and a truncate below the entry (#56 §2), a Read after growth, and a
//     truncate over a trimmed chunk that a Read has cached;
//   - §2's I4 and §8's "After any truncate a file has at most one entry, none
//     for an index whose chunk is a hole, and none for an index in of.dirty";
//   - §3, truncate: (a) dropping entries, (b) the smallest length winning,
//     (c) no entry for a hole, and (d) no store call, no charge, no wait and
//     no buffer staged in of.dirty;
//   - §4, a Write into an index with an entry keeps only the first to bytes of
//     the chunk, removes the entry, and fetches nothing when it covers the
//     index whole;
//   - §5, a flush: at most one chunk Get and one chunk Put for its entry,
//     nothing charged while it runs or when it fails, the trim kept when a
//     fetch or an upload fails, and no chunk stored for a hole;
//   - §6, a Read honours the trim, including one held in its fetch across the
//     flush that applies it (ADR 0005 §5, §6);
//   - §7, what a truncate and a flush cost, seen through DirtyBytes() and
//     through the store calls a flush makes;
//   - §1 as a whole, through a differential test against a byte-slice model.
//
// openFile.pendingTrim is read only through len(), holding openFile.mu
// (bpSnapshot), never through its keys or values. §8 pins it as a map, and a
// test that reads only its length compiles against a slice too (Assumption 6).
// Which index an entry is at is therefore never asserted directly; the tests
// assert the count, and what the file reads as.
//
// A cold chunk is reached as §8 says: the file is seeded through another mount
// (bpSeedFile) and the bucket is mounted afresh, whose cache is empty (ADR
// 0004 §5). A cached chunk is reached through a Read on the same FS. A hole is
// reached by growing the file with a SetAttr of the size. Every content
// expectation is checked three times: on the FS before the Sync that follows
// it, after that Sync, and on a fresh mount.
//
// Every FS call runs under btHangBound, through the bp* helpers or bpGo, and
// goroutines never touch t. Nothing sleeps: a flush or a Read is held in a
// store call by bpStore's gates, and is known to be there because the fake
// closes a channel on entry or counts the call.
//
// Nothing here relies on the order of a flush's fetches and uploads (§5), or
// on whether a Read fetches a chunk whose part of its range lies wholly at or
// past its trim's length (§6); §8 leaves both unpinned. A test that injects a
// failure into the fetch of a trimmed chunk reads nothing of that chunk
// before the flush, so that the flush has to fetch it from the store rather
// than find it in the cache (§7: "none if this FS's cache holds it").
//
// Not covered, on purpose:
//   - The commit window (#64), which ADR 0006 leaves open.
//   - §7's memory figures, which are arithmetic, not behaviour.
//   - Site 4's check with the inode gone: backpressure_test.go's bpSite4Gone
//     covers it.
//   - I1 observed directly: it needs the map's keys. It is checked through
//     its consequences instead: an index that a Write makes dirty has no
//     entry left (len), and an index a truncate queues a trim for is not dirty.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"strata/internal/store"
	"strata/internal/vfs"
)

// ptZeros returns n zero bytes.
func ptZeros(n int) []byte { return make([]byte, n) }

// ptAt names an offset or a size by its chunk index and its offset within the
// chunk, for failure messages.
func ptAt(n uint64) string {
	return fmt.Sprintf("%d (%d×cs+%d)", n, n/bpCS, n%bpCS)
}

// ptCold is one file on a mount of its own bucket. The mount's store is a
// bpStore wrapping st, and its MaxDirtyBytes is -1, so nothing drains while a
// test runs (accounting runs regardless, ADR 0003 §8).
type ptCold struct {
	st   *store.Local
	bps  *bpStore
	fs   *FS
	h    vfs.Handle
	id   uint64
	name string
	data []byte
}

// ptSeedCold seeds a file of n unique bytes through another mount and mounts
// the bucket afresh, so every chunk of the file is cold: stored, and not in
// this FS's cache (ADR 0006 §8; ADR 0004 §5).
func ptSeedCold(t *testing.T, name string, n int) *ptCold {
	t.Helper()
	st := bpLocal(t)
	data := bpSeedFile(t, st, name, n)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, -1, quietLog())
	h, attr := bpLookup(t, fs, name)
	return &ptCold{st: st, bps: bps, fs: fs, h: h, id: attr.FileID, name: name, data: data}
}

// ptEmpty creates an empty file on a fresh mount of an empty bucket.
func ptEmpty(t *testing.T, name string) *ptCold {
	t.Helper()
	st := bpLocal(t)
	bps := &bpStore{Store: st}
	fs := bpNew(t, bps, -1, quietLog())
	h, attr := bpCreate(t, fs, name, 0)
	return &ptCold{st: st, bps: bps, fs: fs, h: h, id: attr.FileID, name: name}
}

// setSize sets the file's size with SetAttr and requires DirtyBytes() not to
// rise (ADR 0006 §8: "A truncate makes no store call and never raises
// DirtyBytes()").
func (c *ptCold) setSize(t *testing.T, size uint64) {
	t.Helper()
	before := c.fs.DirtyBytes()
	bpTruncate(t, c.fs, c.h, size, fmt.Sprintf("SetAttr(size %s) of %s", ptAt(size), c.name))
	if after := c.fs.DirtyBytes(); after > before {
		t.Errorf("SetAttr(size %s) of %s raised DirtyBytes() from %d to %d; ADR 0006 §3(d): "+
			"truncate never charges the budget, and §8: a truncate never raises DirtyBytes()",
			ptAt(size), c.name, before, after)
	}
}

// snap reads the file's openFile state holding its mu.
func (c *ptCold) snap(t *testing.T, what string) bpOFSnap {
	t.Helper()
	return bpSnapshot(t, bpMustOpen(t, c.fs, c.id, what), what)
}

// wantPending requires len(of.pendingTrim) to be want, and returns the
// snapshot it read.
func (c *ptCold) wantPending(t *testing.T, want int, what, why string) bpOFSnap {
	t.Helper()
	s := c.snap(t, what)
	if s.pending != want {
		t.Errorf("%s: len(of.pendingTrim) = %d, want %d (%s)", what, s.pending, want, why)
	}
	return s
}

// ptWantNotDirty requires index idx to be absent from of.dirty in s.
func ptWantNotDirty(t *testing.T, s bpOFSnap, idx uint64, what, why string) {
	t.Helper()
	if _, ok := s.caps[idx]; ok {
		t.Errorf("%s: of.dirty holds index %d (its indices are %v), want it absent (%s)",
			what, idx, s.indices(), why)
	}
}

// ptWantNoneDirty requires of.dirty to be empty in s, for a file that nothing
// has written on this mount.
func ptWantNoneDirty(t *testing.T, s bpOFSnap, what string) {
	t.Helper()
	if len(s.caps) != 0 {
		t.Errorf("%s: of.dirty holds indices %v, want none: nothing has written the file on this "+
			"mount, and truncate never stages a buffer in of.dirty (ADR 0006 §3(d))", what, s.indices())
	}
}

// wantEverywhere checks that the file reads as want three times: on this FS
// before a Sync, after a Sync, and on a fresh mount of the bucket.
func (c *ptCold) wantEverywhere(t *testing.T, want []byte, what string) {
	t.Helper()
	bpWantFile(t, c.fs, c.h, want, what+", before the Sync")
	bpMustSync(t, c.fs, "Sync of "+c.name+": "+what)
	bpWantFile(t, c.fs, c.h, want, what+", after the Sync")
	bpWantRemount(t, c.st, c.name, want, what)
}

// step sets the size, then requires exactly pending entries, nothing dirty
// and DirtyBytes() == 0 (ADR 0006 §2 I4, §3, §7).
func (c *ptCold) step(t *testing.T, size uint64, pending int, why string) {
	t.Helper()
	c.setSize(t, size)
	what := "after SetAttr(size " + ptAt(size) + ") of " + c.name
	s := c.wantPending(t, pending, what, why)
	ptWantNoneDirty(t, s, what)
	bpWantDirty(t, c.fs, what+" (ADR 0006 §3(d), §7: a truncate charges nothing, and nothing "+
		"has been written)", 0)
}

// ptWantRead requires r, one Read, to have returned want and eof.
func ptWantRead(t *testing.T, what string, r rcReply, want []byte, eof bool, why string) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%s = %v, want success", what, r.err)
	}
	if !bytes.Equal(r.data, want) {
		t.Errorf("%s: %s (%s)", what, wsDiff(r.data, want, 250), why)
	}
	if r.eof != eof {
		t.Errorf("%s returned eof = %v, want %v (ADR 0005 §1: eof is decided by the size at the view)",
			what, r.eof, eof)
	}
}

// ptWantNoChunks requires st to hold no chunk object.
func ptWantNoChunks(t *testing.T, st *store.Local, what string) {
	t.Helper()
	objs, err := st.List(context.Background(), chunkPrefix, "", 0)
	if err != nil {
		t.Fatalf("%s: listing %q: %v", what, chunkPrefix, err)
	}
	if len(objs) != 0 {
		t.Errorf("%s: the bucket holds %d chunk objects, want none: the file is only holes, a hole "+
			"takes no entry (ADR 0006 §3(c)), and so the flush stores no chunk of zeros where a hole "+
			"was (Consequences)", what, len(objs))
	}
}

// Why a truncate into a stored chunk that is not dirty makes one entry.
const ptWhyEntry = "ADR 0006 §3(b): a truncate that ends inside a stored chunk whose index is not " +
	"dirty makes an entry there; §2 I4: a file has at most one entry"

// ptWriteIntoTrim truncates c's cold two-chunk file to cs+1000, which queues a
// trim at index 1, and then writes 10 new bytes at off, inside index 1. It
// requires the write to have made index 1 dirty and removed the entry, and
// returns the bytes written.
func ptWriteIntoTrim(t *testing.T, c *ptCold, off uint64) []byte {
	t.Helper()
	c.setSize(t, bpCS+1000)
	s := c.wantPending(t, 1, "after truncating a cold two-chunk file to cs+1000", ptWhyEntry)
	ptWantNoneDirty(t, s, "after truncating a cold two-chunk file to cs+1000")
	w := bpUnique(10)
	bpMustWrite(t, c.fs, fmt.Sprintf("a Write of 10 bytes at %s, into index 1, which has a pending "+
		"trim", ptAt(off)), c.h, off, w)
	what := "after the Write into index 1"
	s = c.wantPending(t, 0, what, "ADR 0006 §4: bufferWrite \"removes the entry whenever it stores "+
		"of.dirty[idx], in the same hold of openFile.mu\"; §8: a Write into an index that has an "+
		"entry removes the entry; §2 I1: an index is never in both of.dirty and of.pendingTrim")
	if _, ok := s.caps[1]; !ok {
		t.Errorf("%s: of.dirty holds indices %v, want index 1 among them: the Write stored it", what,
			s.indices())
	}
	return w
}

// TestPendingTrimWriteIntoTrimmedChunk pins ADR 0006 §4 on Context's route 1
// (#40): "When bufferWrite fills an index that is absent from of.dirty and has
// an entry, it fetches the chunk the list names ... It keeps only the first to
// bytes of what it fetched ... so the bytes past to read as zeros unless the
// write puts something there ... It removes the entry whenever it stores
// of.dirty[idx]", and §1: bytes a truncate removed never reach a reply, a
// dirty buffer or the bucket. A cold two-chunk file is truncated to cs+1000,
// which queues a trim at index 1 (§3(b)), and written at index 1. The code
// before ADR 0006 loaded the whole chunk there, removed bytes included, and
// the flush then skipped the trim because the index was dirty, so growing the
// file back over [cs+1000, 2cs) showed the old data.
//
// The file is then grown to 3cs, by a SetAttr or by a Write near the end; and
// in "write past the trim point" the Write lands past cs+1000, so the gap
// between the trim and the Write must read as zeros too.
func TestPendingTrimWriteIntoTrimmedChunk(t *testing.T) {
	t.Run("grow by SetAttr", func(t *testing.T) {
		c := ptSeedCold(t, "write-into.bin", 2*bpCS)
		w := ptWriteIntoTrim(t, c, bpCS+100)
		c.setSize(t, 3*bpCS)
		want := rcCat(c.data[:bpCS+100], w, c.data[bpCS+110:bpCS+1000], ptZeros(2*bpCS-1000))
		c.wantEverywhere(t, want, "a cold two-chunk file truncated to cs+1000, written with 10 bytes "+
			"at cs+100 and grown to 3cs by SetAttr: [cs+1000, 3cs) must be zeros (ADR 0006 §1, §4; #40)")
	})

	t.Run("grow by a write", func(t *testing.T) {
		c := ptSeedCold(t, "write-into.bin", 2*bpCS)
		w := ptWriteIntoTrim(t, c, bpCS+100)
		w2 := bpUnique(10)
		bpMustWrite(t, c.fs, "a Write of 10 bytes at 3cs−10, growing the file to 3cs", c.h, 3*bpCS-10, w2)
		want := rcCat(c.data[:bpCS+100], w, c.data[bpCS+110:bpCS+1000], ptZeros(2*bpCS-1010), w2)
		c.wantEverywhere(t, want, "a cold two-chunk file truncated to cs+1000, written with 10 bytes "+
			"at cs+100 and grown to 3cs by a Write at 3cs−10: [cs+1000, 3cs−10) must be zeros (ADR "+
			"0006 §1, §4; #40)")
	})

	t.Run("write past the trim point", func(t *testing.T) {
		c := ptSeedCold(t, "write-into.bin", 2*bpCS)
		w := ptWriteIntoTrim(t, c, bpCS+2000)
		c.setSize(t, 3*bpCS)
		want := rcCat(c.data[:bpCS+1000], ptZeros(1000), w, ptZeros(2*bpCS-2010))
		c.wantEverywhere(t, want, "a cold two-chunk file truncated to cs+1000, written with 10 bytes "+
			"at cs+2000 and grown to 3cs: [cs+1000, cs+2000) must be zeros, because the Write keeps "+
			"only the first to bytes of the chunk it fetched (ADR 0006 §4; #40)")
	})
}

// TestPendingTrimSecondTruncateSameIndex pins ADR 0006 §3(b) on Context's
// route 2 (#56 §2): "If tail < v, it sets the entry at lastIdx to (tail, ref);
// otherwise it leaves the map alone. The smallest length wins, so a truncate
// that grows the file within the chunk keeps the trim an earlier one made, and
// the bytes between the two lengths read as zeros." A cold two-chunk file is
// truncated to cs+2048 and then to cs+1024. The code before ADR 0006 queued an
// entry for each, applied the first and skipped the second, so the stored
// chunk kept [cs+1024, cs+2048).
func TestPendingTrimSecondTruncateSameIndex(t *testing.T) {
	setup := func(t *testing.T, name string) *ptCold {
		t.Helper()
		c := ptSeedCold(t, name, 2*bpCS)
		c.setSize(t, bpCS+2048)
		c.setSize(t, bpCS+1024)
		s := c.wantPending(t, 1, "after truncating a cold two-chunk file to cs+2048 and then to "+
			"cs+1024", "ADR 0006 §3(b): the second truncate lowers the entry at index 1 rather than "+
			"adding one; §2 I4 and §8: after any truncate a file has at most one entry")
		ptWantNoneDirty(t, s, "after the two truncates")
		return c
	}
	kept := func(c *ptCold) []byte { return c.data[:bpCS+1024] }
	whyEmpty := "ADR 0006 §5: when a flush succeeds it resets of.pendingTrim"

	t.Run("grow after a commit", func(t *testing.T) {
		c := setup(t, "second-truncate.bin")
		bpMustSync(t, c.fs, "Sync applying the trim")
		c.wantPending(t, 0, "after the Sync", whyEmpty)
		c.setSize(t, 2*bpCS)
		c.wantEverywhere(t, rcCat(kept(c), ptZeros(bpCS-1024)), "truncated to cs+2048, then "+
			"cs+1024, synced and grown to 2cs: [cs+1024, 2cs) must be zeros (ADR 0006 §3(b), §5; #56)")
	})

	t.Run("write between the two ends after a commit", func(t *testing.T) {
		c := setup(t, "second-truncate.bin")
		bpMustSync(t, c.fs, "Sync applying the trim")
		c.wantPending(t, 0, "after the Sync", whyEmpty)
		w := bpUnique(10)
		bpMustWrite(t, c.fs, "a Write of 10 bytes at cs+1500", c.h, bpCS+1500, w)
		c.wantEverywhere(t, rcCat(kept(c), ptZeros(476), w), "truncated to cs+2048, then cs+1024, "+
			"synced and written with 10 bytes at cs+1500: [cs+1024, cs+1500) must be zeros (ADR "+
			"0006 §3(b), §5; #56)")
	})

	t.Run("grow before a commit", func(t *testing.T) {
		c := setup(t, "second-truncate.bin")
		c.setSize(t, 2*bpCS)
		c.wantEverywhere(t, rcCat(kept(c), ptZeros(bpCS-1024)), "truncated to cs+2048, then "+
			"cs+1024, and grown to 2cs with no Sync between: [cs+1024, 2cs) must be zeros (ADR 0006 "+
			"§3(b), §6; #56)")
	})

	t.Run("grow within the chunk", func(t *testing.T) {
		c := setup(t, "second-truncate.bin")
		c.setSize(t, bpCS+3000)
		s := c.wantPending(t, 1, "after growing the file to cs+3000, within chunk 1", "ADR 0006 "+
			"§3(b): 3000 is not below the entry's 1024, so the map is left alone: the smallest "+
			"length wins")
		ptWantNoneDirty(t, s, "after growing the file to cs+3000")
		c.wantEverywhere(t, rcCat(kept(c), ptZeros(3000-1024)), "truncated to cs+2048, then cs+1024, "+
			"then grown within the chunk to cs+3000: [cs+1024, cs+3000) must be zeros (ADR 0006 "+
			"§3(b), Assumption 1; #56)")
	})
}

// TestPendingTrimTruncateBelowTrimmedIndex pins ADR 0006 §3(a) on Context's
// route 3 (#56 §2): "It drops every entry with idx > lastIdx, and the entry at
// lastIdx when tail == 0." A cold three-chunk file is truncated to 2cs+1000,
// which queues a trim at index 2, and then below it. The code before ADR 0006
// left the entry in place, and the flush's namespace update put the shortened
// chunk back at index 2, so growing the file showed its first 1000 bytes. The
// file is synced after the second truncate and then grown to 3cs.
func TestPendingTrimTruncateBelowTrimmedIndex(t *testing.T) {
	cases := []struct {
		name    string
		size    uint64
		pending int
		why     string
	}{
		{"to zero", 0, 0, "ADR 0006 §3(a): a truncate to 0 drops every entry"},
		{"into a lower chunk", bpCS + 500, 1, "ADR 0006 §3(a): the entry at index 2 is dropped; " +
			"(b): one is made at index 1; §2 I4: at most one entry"},
		{"to a chunk boundary", 2 * bpCS, 0, "ADR 0006 §3(a): a truncate to 2cs has lastIdx 2 and " +
			"tail 0, so it drops the entry at index 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ptSeedCold(t, "below.bin", 3*bpCS)
			c.setSize(t, 2*bpCS+1000)
			c.wantPending(t, 1, "after truncating a cold three-chunk file to 2cs+1000", ptWhyEntry)
			c.setSize(t, tc.size)
			what := "after truncating it again, to " + ptAt(tc.size)
			s := c.wantPending(t, tc.pending, what, tc.why)
			ptWantNoneDirty(t, s, what)
			bpMustSync(t, c.fs, "Sync after the second truncate")
			c.setSize(t, 3*bpCS)
			want := rcCat(c.data[:tc.size], ptZeros(3*bpCS-int(tc.size)))
			c.wantEverywhere(t, want, fmt.Sprintf("truncated to 2cs+1000, then to %s, synced and grown "+
				"to 3cs: everything from %s on must be zeros (ADR 0006 §3(a); #56)", ptAt(tc.size),
				ptAt(tc.size)))
		})
	}
}

// TestPendingTrimReadAfterGrowth pins ADR 0006 §6 on Context's route 4: "In
// step 3 it copies from each fetched chunk only the bytes below that length;
// the rest of the range at that index reads as zeros", and ADR 0005 §1: "A
// pending trim is part of the view". A cold two-chunk file is truncated to
// cs+1000 and grown back to 2cs, with no Sync, and ONE Read of [cs, 2cs) must
// return the chunk's first 1000 bytes and then zeros. The Read before ADR
// 0006 fetched the whole chunk and returned the removed bytes.
func TestPendingTrimReadAfterGrowth(t *testing.T) {
	c := ptSeedCold(t, "read-after-growth.bin", 2*bpCS)
	c.setSize(t, bpCS+1000)
	c.setSize(t, 2*bpCS)
	s := c.wantPending(t, 1, "after truncating to cs+1000 and growing back to 2cs", "ADR 0006 §3(a): "+
		"growing to 2cs has lastIdx 2, so the entry at index 1 stays")
	ptWantNoneDirty(t, s, "after truncating to cs+1000 and growing back to 2cs")

	what := "one Read of [cs, 2cs) after truncating to cs+1000 and growing back to 2cs, before any Sync"
	r := rcStartRead(c.fs, c.h, bpCS, bpCS).wait(t, what)
	ptWantRead(t, what, r, rcCat(c.data[bpCS:bpCS+1000], ptZeros(bpCS-1000)), true,
		"ADR 0006 §6: bytes of a chunk at or past its pending trim's length read as zeros; ADR 0005 "+
			"§1, §2")

	c.wantEverywhere(t, rcCat(c.data[:bpCS+1000], ptZeros(bpCS-1000)), "truncated to cs+1000 and "+
		"grown back to 2cs (ADR 0006 §1, §6)")
}

// TestPendingTrimTruncateOverCachedTrimmedChunk pins ADR 0006 §3(b) and (d)
// on Context's route 5, reached as §8 says: "truncate into a cold chunk, Read
// it, then truncate to a larger size within the same chunk". The Read caches
// the chunk (ADR 0004 §5). The code before ADR 0006 then found it in the cache
// and staged its first 800 bytes in of.dirty, bringing back what the first
// truncate removed. Truncate "never reads the chunk cache, never stages a
// buffer in of.dirty", and "The smallest length wins".
func TestPendingTrimTruncateOverCachedTrimmedChunk(t *testing.T) {
	c := ptSeedCold(t, "route5.bin", 2*bpCS)
	c.setSize(t, bpCS+500)
	c.wantPending(t, 1, "after truncating a cold two-chunk file to cs+500", ptWhyEntry)
	bpWantFile(t, c.fs, c.h, c.data[:bpCS+500], "the file truncated to cs+500, read whole, which "+
		"fetches chunk 1 (ADR 0005 §2 step 3)")
	if _, ok := ccGet(t, c.fs.cache, ccHash(c.data[bpCS:]), "probing chunk 1"); !ok {
		t.Fatalf("setup: after a Read of [cs, cs+500) on this FS, the cache does not hold chunk 1, " +
			"so route 5 is not reached: the Read had to fetch it, the range lying below the trim, " +
			"and loadChunk caches a chunk it fetches (ADR 0004 §5; ADR 0006 §6)")
	}

	c.setSize(t, bpCS+800)
	what := "after growing the file to cs+800, within the cached chunk 1"
	s := c.wantPending(t, 1, what, "ADR 0006 §3(b): 800 is not below the entry's 500, so the map "+
		"is left alone")
	ptWantNotDirty(t, s, 1, what, "ADR 0006 §3(d): truncate never reads the chunk cache and never "+
		"stages a buffer in of.dirty")
	c.wantEverywhere(t, rcCat(c.data[:bpCS+500], ptZeros(300)), "truncated to cs+500, read, and "+
		"grown to cs+800 within the cached chunk: [cs+500, cs+800) must be zeros (ADR 0006 §3(b), "+
		"(d); Context, route 5)")
}

// TestPendingTrimWholeChunkOverwrite pins ADR 0006 §4: the Write "removes the
// entry whenever it stores of.dirty[idx] ... including for a write that covers
// the index whole and so fetches nothing", and §8: "A Write that covers an
// index whole fetches nothing for it (ADR 0005 §7)". A hook on chunk 1's key
// counts its Gets and passes them through.
func TestPendingTrimWholeChunkOverwrite(t *testing.T) {
	c := ptSeedCold(t, "whole.bin", 2*bpCS)
	get := &bpHook{key: bpChunkKey(t, c.st, c.data[bpCS:])}
	c.bps.setGet(get)
	c.setSize(t, bpCS+1000)
	c.wantPending(t, 1, "after truncating a cold two-chunk file to cs+1000", ptWhyEntry)

	w := bpUnique(bpCS)
	bpMustWrite(t, c.fs, "a Write of cs bytes at cs, covering index 1 whole", c.h, bpCS, w)
	if n := c.bps.callsOf(get); n != 0 {
		t.Errorf("the truncate and a Write covering index 1 whole made %d store Gets of chunk 1's "+
			"key, want 0: a truncate makes no store call, and a Write that covers an index whole "+
			"fetches nothing for it (ADR 0006 §4, §8; ADR 0005 §7)", n)
	}
	what := "after the Write covering index 1 whole"
	s := c.wantPending(t, 0, what, "ADR 0006 §4: the Write removes the entry whenever it stores "+
		"of.dirty[idx], including for a write that covers the index whole; §2 I1")
	if _, ok := s.caps[1]; !ok {
		t.Errorf("%s: of.dirty holds indices %v, want index 1 among them", what, s.indices())
	}
	c.bps.setGet(nil)
	c.wantEverywhere(t, rcCat(c.data[:bpCS], w), "truncated to cs+1000 and then written whole at "+
		"index 1 (ADR 0006 §4)")
}

// TestPendingTrimFailedWriteFetchKeepsTrim pins ADR 0006 §4: "A fetch that
// fails stores nothing for the index and leaves the entry", with ADR 0003 §2's
// table, "Write that stores nothing because it failed first: ... a failed
// first fetch | 0", and ADR 0006 §1 and §6: the trim the entry records is
// still honoured afterwards. A cold two-chunk file is truncated to cs+1000,
// which queues a trim at index 1 (§3(b)), and a Write of 10 bytes at cs+100,
// which has to fetch chunk 1 because it fills only part of the index (§4;
// ADR 0005 §7), has that fetch fail. The entry must still be there, and index
// 1 must not be dirty. With the store healthy again, the file is grown to 2cs,
// and [cs+1000, 2cs) must read as zeros in one Read, after a Sync and on a
// fresh mount.
//
// A regression that removed the entry before the fetch, or on the fetch's
// error path, fails the len(of.pendingTrim) check; the chunk list still names
// the whole chunk then, so the Read after growth returns the removed bytes and
// the flush keeps the long chunk, which the content checks catch. One that
// stored a buffer for the index despite the failure fails the of.dirty and
// DirtyBytes() checks.
func TestPendingTrimFailedWriteFetchKeepsTrim(t *testing.T) {
	c := ptSeedCold(t, "failed-write-fetch.bin", 2*bpCS)
	key1 := bpChunkKey(t, c.st, c.data[bpCS:])
	c.setSize(t, bpCS+1000)
	c.wantPending(t, 1, "after truncating a cold two-chunk file to cs+1000", ptWhyEntry)

	injected := errors.New("pending trim test: injected failure of the Write's fetch of chunk 1")
	get := &bpHook{key: key1, err: injected}
	c.bps.setGet(get)
	what := "a Write of 10 bytes at cs+100, into index 1, whose fetch of the trimmed chunk fails"
	r := bpWrite(t, c.fs, what, c.h, bpCS+100, bpUnique(10), vfs.Unstable)
	calls := c.bps.callsOf(get)
	c.bps.setGet(nil)
	if calls == 0 {
		t.Fatalf("setup: %s made no store Get of chunk 1's key, so the failure was never "+
			"injected: a Write that fills part of an index absent from of.dirty fetches the chunk "+
			"the list names (ADR 0006 §4; ADR 0003 §4), and a fresh mount's cache does not hold it "+
			"(ADR 0004 §5)", what)
	}
	if r.n != 0 || !errors.Is(r.err, injected) {
		t.Errorf("%s = %v, want (0, UNSTABLE, the injected error): a failed first fetch stores "+
			"nothing (ADR 0006 §4; ADR 0003 §2's table)", what, r)
	}
	after := "after the Write whose fetch of the trimmed chunk failed"
	s := c.wantPending(t, 1, after, "ADR 0006 §4: a fetch that fails stores nothing for the "+
		"index and leaves the entry")
	ptWantNotDirty(t, s, 1, after, "ADR 0006 §4: a fetch that fails stores nothing for the index")
	bpWantDirty(t, c.fs, after+" (ADR 0003 §2's table: a failed first fetch charges nothing)", 0)
	bpCheckAccounting(t, c.fs, after)

	c.setSize(t, 2*bpCS)
	rwhat := "one Read of [cs, 2cs) after the failed Write and the growth to 2cs"
	rr := rcStartRead(c.fs, c.h, bpCS, bpCS).wait(t, rwhat)
	ptWantRead(t, rwhat, rr, rcCat(c.data[bpCS:bpCS+1000], ptZeros(bpCS-1000)), true,
		"ADR 0006 §4: a failed fetch leaves the entry; §6: a Read copies only the bytes below "+
			"the trim's length")
	c.wantEverywhere(t, rcCat(c.data[:bpCS+1000], ptZeros(bpCS-1000)), "truncated to cs+1000, "+
		"a Write into index 1 failed in its fetch, then grown to 2cs (ADR 0006 §4)")
}

// TestPendingTrimTruncateIntoDirtyIndexMakesNoEntry pins ADR 0006 §3(b): a
// truncate makes an entry only "If tail != 0, lastIdx is not in of.dirty, and
// the chunk list ... reaches lastIdx with a stored chunk ref there"; §2 I1,
// "An index is never in both of.dirty and of.pendingTrim"; and §8, "After any
// truncate a file has ... none for an index in of.dirty". This is the one case
// where the dirty guard matters: index 1 of a cold two-chunk file has a stored
// chunk in the chunk list AND a dirty buffer, made by a Write, when the file
// is truncated to cs+1000. The truncate shortens the dirty tail in place
// (§3(d)) and must make no entry. The flush then applies no trim (§5), so it
// uploads the dirty chunk and nothing else: one chunk Put, since that content
// is new to the bucket (ADR 0005 §7), and no store Get of chunk 1's key.
//
// "after a partial write" makes index 1 dirty the way the reviewer's case
// does: the Write fetches chunk 1, which caches it (ADR 0004 §5), so a
// wrongly made entry would be applied from the cache with no Get, and only
// the extra chunk Put of the shortened copy shows it. "after a whole-chunk
// write" fetches nothing (§4; ADR 0005 §7), so chunk 1 is not cached, and a
// wrongly made entry costs a Get of its key as well. In both, a wrongly made
// entry fails the len(of.pendingTrim) check first. Content, which such an
// entry would not change, is checked before the Sync, after it and on a fresh
// mount. Nothing depends on the order of the flush's uploads.
func TestPendingTrimTruncateIntoDirtyIndexMakesNoEntry(t *testing.T) {
	cases := []struct {
		name string
		off  uint64
		n    int
	}{
		{"after a partial write", bpCS + 100, 10},
		{"after a whole-chunk write", bpCS, bpCS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ptSeedCold(t, "dirty-tail.bin", 2*bpCS)
			key1 := bpChunkKey(t, c.st, c.data[bpCS:])
			w := bpUnique(tc.n)
			bpMustWrite(t, c.fs, fmt.Sprintf("a Write of %d bytes at %s, into stored chunk 1", tc.n,
				ptAt(tc.off)), c.h, tc.off, w)
			want := append([]byte(nil), c.data...)
			copy(want[tc.off:], w)
			want = want[:bpCS+1000]

			c.setSize(t, bpCS+1000)
			what := "after truncating to cs+1000, into dirty index 1, whose chunk is also stored"
			s := c.wantPending(t, 0, what, "ADR 0006 §3(b): a truncate makes an entry only when "+
				"lastIdx is not in of.dirty; §2 I1 and §8: none for an index in of.dirty")
			if _, ok := s.caps[1]; !ok {
				t.Errorf("%s: of.dirty holds indices %v, want index 1 among them: truncate shortens a "+
					"dirty tail in place (ADR 0006 §3(d))", what, s.indices())
			}
			bpWantDirty(t, c.fs, what+" (the Write's +cs; ADR 0006 §3(d): a truncate adds nothing)", bpCS)

			bpWantFile(t, c.fs, c.h, want, "the written and truncated file, before the Sync")
			get := &bpHook{key: key1}
			put := &bpHook{}
			c.bps.setGet(get)
			c.bps.setPut(put)
			bpMustSync(t, c.fs, "Sync of the truncated dirty index 1")
			gets, puts := c.bps.callsOf(get), c.bps.callsOf(put)
			c.bps.setGet(nil)
			c.bps.setPut(nil)
			if gets != 0 {
				t.Errorf("the Sync made %d store Gets of chunk 1's key, want 0: the file has no "+
					"pending trim, so the flush applies none (ADR 0006 §3(b), §5), and its dirty index "+
					"1 needs no fetch", gets)
			}
			if puts != 1 {
				t.Errorf("the Sync made %d chunk Puts, want 1: the flush uploads the one dirty index, "+
					"whose content is new to the bucket (ADR 0005 §7), and applies no trim, which "+
					"would upload a shortened copy of the stored chunk 1 as well (ADR 0006 §3(b), §5)",
					puts)
			}
			bpWantDirty(t, c.fs, "after the Sync", 0)
			bpWantFile(t, c.fs, c.h, want, "the written and truncated file, after the Sync")
			bpWantRemount(t, c.st, c.name, want, "the written and truncated file")
		})
	}
}

// TestPendingTrimFailedFlushKeepsTrim pins ADR 0006 §5: "A fetch or an upload
// that fails repoints nothing, and leaves of.dirty and of.pendingTrim as they
// were, so the file's next flush applies the trims again", "The buffer never
// enters of.dirty and is never charged", and §6: a Read after that failure
// still honours the trim. The flush's Get of the trimmed chunk fails; the
// file is then grown to 2cs, read, and synced with the store healthy again.
func TestPendingTrimFailedFlushKeepsTrim(t *testing.T) {
	c := ptSeedCold(t, "failed-fetch.bin", 2*bpCS)
	key1 := bpChunkKey(t, c.st, c.data[bpCS:])
	c.setSize(t, bpCS+1000)

	injected := errors.New("pending trim test: injected failure of the Get of the trimmed chunk")
	get := &bpHook{key: key1, err: injected}
	c.bps.setGet(get)
	err := bpSync(t, c.fs, "Sync whose fetch of the trimmed chunk 1 fails")
	calls := c.bps.callsOf(get)
	c.bps.setGet(nil)
	if err == nil {
		t.Fatalf("Sync with every Get of chunk 1's key failing = nil, want an error: the file's "+
			"flush has a pending trim of that chunk to apply, and this FS has not read it (ADR 0006 "+
			"§5); it made %d Gets of that key", calls)
	}
	what := "after the Sync whose fetch of the trimmed chunk failed"
	s := c.wantPending(t, 1, what, "ADR 0006 §5: a fetch that fails leaves of.pendingTrim as it was")
	ptWantNoneDirty(t, s, what)
	bpWantDirty(t, c.fs, what+" (ADR 0006 §5: a trim is never charged; ADR 0003 §2's table)", 0)
	bpCheckAccounting(t, c.fs, what)

	c.setSize(t, 2*bpCS)
	bpWantDirty(t, c.fs, "after growing the file to 2cs", 0)
	rwhat := "one Read of [cs, 2cs) after the failed flush and the growth to 2cs"
	r := rcStartRead(c.fs, c.h, bpCS, bpCS).wait(t, rwhat)
	ptWantRead(t, rwhat, r, rcCat(c.data[bpCS:bpCS+1000], ptZeros(bpCS-1000)), true,
		"ADR 0006 §6; ADR 0005 §6: after a flush that fails, step 2 takes the references with "+
			"their trims")
	c.wantEverywhere(t, rcCat(c.data[:bpCS+1000], ptZeros(bpCS-1000)), "truncated to cs+1000, "+
		"its flush failed, then grown to 2cs and synced (ADR 0006 §5)")
}

// TestPendingTrimReadHeldAcrossFlush pins ADR 0006 §6 with ADR 0005 §5 and §6:
// a Read takes the reference of the trimmed index with the trim's length in
// step 2, and "copies from each fetched chunk only the bytes below that
// length" in step 3, holding no lock; and a flush that applies the trim while
// the Read is held in its fetch "holds openFile.mu ... until it has repointed
// n.Chunks ... and emptied of.pendingTrim" without changing what the Read's
// reference names. A cold two-chunk file is truncated to cs+1000 and grown to
// 2cs. A Read of [cs, 2cs) is held in its Get of chunk 1; a Sync is started
// and waited for until its own Get of chunk 1 is held too, because the Read's
// fetch has not returned and so has cached nothing (ADR 0004 §5). Both are
// then released.
func TestPendingTrimReadHeldAcrossFlush(t *testing.T) {
	c := ptSeedCold(t, "held-across-flush.bin", 2*bpCS)
	key1 := bpChunkKey(t, c.st, c.data[bpCS:])
	c.setSize(t, bpCS+1000)
	c.setSize(t, 2*bpCS)

	gate := newBTGate(t)
	get := &bpHook{key: key1, entered: make(chan struct{}), gate: gate.ch}
	c.bps.setGet(get)
	rd := rcStartRead(c.fs, c.h, bpCS, bpCS)
	bpWaitEntered(t, "the Read of [cs, 2cs) entering its Get of chunk 1 (ADR 0005 §2 step 3, §7)",
		get.entered, rd)
	sc := bpGo(func() error { return c.fs.Sync(context.Background()) })
	btEventually(t, "the Sync's flush entering its own Get of chunk 1 while the Read's is held (ADR "+
		"0006 §5: it fetches the entry's chunk with loadChunk, which this FS's cache does not hold)",
		func() bool { return c.bps.callsOf(get) >= 2 || sc.finished() },
		func() string {
			return fmt.Sprintf("%d Gets of chunk 1's key so far; the Sync has returned: %v",
				c.bps.callsOf(get), sc.finished())
		})
	if sc.finished() {
		t.Fatalf("the Sync returned (%v, panic %v) while the Read's Get of chunk 1 was held, "+
			"without fetching chunk 1 itself; its flush has a pending trim of that chunk to apply, "+
			"and at most one Get of its key, none if the cache holds it (ADR 0006 §5, §7)",
			sc.val, sc.pval)
	}
	gate.open()

	r := rd.wait(t, "the Read of [cs, 2cs), after its Get of chunk 1 was released")
	if err := sc.wait(t, "the Sync, after its Get of chunk 1 was released"); err != nil {
		t.Errorf("the Sync = %v, want success against a healthy writable store", err)
	}
	c.bps.setGet(nil)
	ptWantRead(t, "the Read of [cs, 2cs), held in its fetch across the flush that applied the trim",
		r, rcCat(c.data[bpCS:bpCS+1000], ptZeros(bpCS-1000)), true, "ADR 0006 §6: step 3 copies "+
			"only the bytes below the trim's length taken in step 2; ADR 0005 §5, §6")
	c.wantPending(t, 0, "after the Sync", "ADR 0006 §5: when a flush succeeds it resets "+
		"of.pendingTrim")
	c.wantEverywhere(t, rcCat(c.data[:bpCS+1000], ptZeros(bpCS-1000)), "truncated to cs+1000 and "+
		"grown to 2cs, read across the flush that applied the trim (ADR 0006 §5, §6)")
}

// TestPendingTrimQueueBound pins ADR 0006 §2 I4, "A file has at most one
// entry", §3, and §8: "After any truncate a file has at most one entry, none
// for an index whose chunk is a hole, and none for an index in of.dirty", and
// "A truncate makes no store call and never raises DirtyBytes()". After every
// SetAttr the exact number of entries is asserted, and DirtyBytes() == 0. The
// code before ADR 0006 appended an entry for every truncate into a chunk it
// could not stage, holes included, and kept them until the next flush (#56 §1).
func TestPendingTrimQueueBound(t *testing.T) {
	const whyLower = "ADR 0006 §3(a): the entry above the new end is dropped; (b): one is made at " +
		"the new tail index; §2 I4: at most one entry"
	const whySame = "ADR 0006 §3(b): a truncate at the same index lowers the entry or leaves it " +
		"alone, and never adds one; §2 I4"
	const whyHole = "ADR 0006 §3(c): a hole takes no entry"

	t.Run("descending across stored chunks", func(t *testing.T) {
		c := ptSeedCold(t, "descending.bin", 8*bpCS)
		c.step(t, 7*bpCS+1000, 1, ptWhyEntry)
		for k := uint64(6); k >= 1; k-- {
			c.step(t, k*bpCS+1000, 1, whyLower)
		}
		c.wantEverywhere(t, c.data[:bpCS+1000], "a cold eight-chunk file truncated to k×cs+1000 "+
			"for k = 7 down to 1 (ADR 0006 §3(a))")
	})

	t.Run("same index, down and up", func(t *testing.T) {
		c := ptSeedCold(t, "down-and-up.bin", 2*bpCS)
		c.step(t, bpCS+3000, 1, ptWhyEntry)
		for _, r := range []uint64{2000, 2500, 1000, 3500} {
			c.step(t, bpCS+r, 1, whySame)
		}
		c.wantEverywhere(t, rcCat(c.data[:bpCS+1000], ptZeros(2500)), "a cold two-chunk file "+
			"truncated to cs+3000, cs+2000, cs+2500, cs+1000 and cs+3500: [cs+1000, cs+3500) must be "+
			"zeros (ADR 0006 §3(b))")
	})

	t.Run("ascending through holes", func(t *testing.T) {
		c := ptEmpty(t, "holes.bin")
		for k := uint64(0); k < 8; k++ {
			c.step(t, (k+1)*bpCS, 0, "ADR 0006 §3: growing the file appends holes, and a hole "+
				"takes no entry")
			c.step(t, k*bpCS+2048, 0, whyHole)
		}
		c.wantEverywhere(t, ptZeros(7*bpCS+2048), "an empty file grown to (k+1)×cs and truncated to "+
			"k×cs+2048 for k = 0 to 7 (ADR 0006 §3(c))")
		ptWantNoChunks(t, c.st, "after the Sync of a file that is only holes")
	})

	t.Run("to a chunk boundary", func(t *testing.T) {
		c := ptSeedCold(t, "boundary.bin", 3*bpCS)
		c.step(t, 2*bpCS+1000, 1, ptWhyEntry)
		c.step(t, 2*bpCS, 0, "ADR 0006 §3(a): a truncate to 2cs has tail 0 at lastIdx 2, so it "+
			"drops the entry there")
		c.step(t, bpCS, 0, "ADR 0006 §3(a), (b): a truncate to a chunk boundary makes no entry")
		c.step(t, 3*bpCS, 0, "ADR 0006 §3: growing the file makes no entry")
		c.wantEverywhere(t, rcCat(c.data[:bpCS], ptZeros(2*bpCS)), "a cold three-chunk file "+
			"truncated to 2cs+1000, 2cs and cs, then grown to 3cs (ADR 0006 §3(a))")
	})

	t.Run("to zero", func(t *testing.T) {
		c := ptSeedCold(t, "zero.bin", 3*bpCS)
		c.step(t, 2*bpCS+1000, 1, ptWhyEntry)
		c.step(t, 0, 0, "ADR 0006 §3(a): a truncate to 0 drops every entry")
		c.step(t, 3*bpCS, 0, "ADR 0006 §3: growing the file makes no entry")
		c.wantEverywhere(t, ptZeros(3*bpCS), "a cold three-chunk file truncated to 2cs+1000 and to "+
			"0, then grown to 3cs (ADR 0006 §3(a))")
	})

	t.Run("grow over a trim, then into a hole", func(t *testing.T) {
		c := ptSeedCold(t, "over-then-hole.bin", 2*bpCS)
		c.step(t, bpCS+1000, 1, ptWhyEntry)
		c.step(t, 3*bpCS, 1, "ADR 0006 §3(a): growing to 3cs drops no entry at or below index 3")
		c.step(t, 2*bpCS+500, 1, "ADR 0006 §3(a): the entry at index 1 is below the new end and "+
			"stays; (c): index 2 is a hole and takes no entry")
		c.wantEverywhere(t, rcCat(c.data[:bpCS+1000], ptZeros(bpCS-500)), "a cold two-chunk file "+
			"truncated to cs+1000, grown to 3cs and truncated to 2cs+500 (ADR 0006 §3)")
	})
}

// ptCounting arms a counting hook for chunk Gets and one for chunk Puts on c's
// store, and returns them.
func ptCounting(c *ptCold) (get, put *bpHook) {
	get, put = &bpHook{}, &bpHook{}
	c.bps.setGet(get)
	c.bps.setPut(put)
	return get, put
}

// TestPendingTrimFlushWorkBounded pins ADR 0006 §7, "for each file with an
// entry, a flush makes at most one Get of the trimmed chunk, none if this FS's
// cache holds it, and at most one Head and one Put of the shortened copy",
// with §8's "A flush applies each entry with at most one store Get of its
// chunk's key ... and at most one Put", resting on I4. The code before ADR
// 0006 materialised every queued trim at the next flush (#56 §1): here, 63 of
// them. For a file that is only holes, a flush stores nothing (§3(c)).
func TestPendingTrimFlushWorkBounded(t *testing.T) {
	t.Run("stored chunks", func(t *testing.T) {
		c := ptSeedCold(t, "stored.bin", 64*bpCS)
		for k := uint64(63); k >= 1; k-- {
			c.setSize(t, k*bpCS+1000)
		}
		c.wantPending(t, 1, "after 63 truncates to k×cs+1000 for k = 63 down to 1", whyAtMostOne)
		get, put := ptCounting(c)
		bpMustSync(t, c.fs, "Sync after the 63 truncates")
		gets, puts := c.bps.callsOf(get), c.bps.callsOf(put)
		c.bps.setGet(nil)
		c.bps.setPut(nil)
		if gets > 1 {
			t.Errorf("the Sync after 63 truncates of a cold file made %d chunk Gets, want at most 1 "+
				"(ADR 0006 §7, §8; #56)", gets)
		}
		if puts > 1 {
			t.Errorf("the Sync after 63 truncates of a cold file made %d chunk Puts, want at most 1 "+
				"(ADR 0006 §7, §8; #56)", puts)
		}
		c.wantEverywhere(t, c.data[:bpCS+1000], "a cold 64-chunk file truncated to k×cs+1000 for k "+
			"= 63 down to 1")
	})

	t.Run("holes", func(t *testing.T) {
		c := ptEmpty(t, "holes.bin")
		c.setSize(t, 64*bpCS)
		for k := uint64(63); k >= 1; k-- {
			c.setSize(t, k*bpCS+1000)
		}
		c.wantPending(t, 0, "after growing an empty file to 64cs and truncating it to k×cs+1000 for "+
			"k = 63 down to 1", "ADR 0006 §3(c): a hole takes no entry")
		_, put := ptCounting(c)
		bpMustSync(t, c.fs, "Sync of a file that is only holes")
		puts := c.bps.callsOf(put)
		c.bps.setGet(nil)
		c.bps.setPut(nil)
		if puts != 0 {
			t.Errorf("the Sync of a file that is only holes made %d chunk Puts, want 0: a hole takes "+
				"no entry, so the flush stores no chunk of zeros (ADR 0006 §3(c), Consequences)", puts)
		}
		c.wantEverywhere(t, ptZeros(bpCS+1000), "an empty file grown to 64cs and truncated to "+
			"k×cs+1000 for k = 63 down to 1")
	})
}

// whyAtMostOne is why a file has one entry after a run of truncates, each into
// a stored chunk below the last.
const whyAtMostOne = "ADR 0006 §2 I4 and §8: after any truncate a file has at most one entry; " +
	"§3(a) drops the entry above each new end"

// TestPendingTrimFlushChargesNothing pins ADR 0006 §5, "The buffer never
// enters of.dirty and is never charged, and nothing of it is kept once it is
// uploaded", §7, "ADR 0003 §2's sites 3 and 4 only release, and site 1 alone
// adds", and ADR 0003 §2's table: "flushOpen failing during upload, its inode
// still there | unchanged; trims stay pending". The code before ADR 0006
// materialised the trim into of.dirty and charged it (site 4).
func TestPendingTrimFlushChargesNothing(t *testing.T) {
	t.Run("held in the trim's upload", func(t *testing.T) {
		c := ptSeedCold(t, "held-upload.bin", 2*bpCS)
		c.setSize(t, bpCS+1000)
		gate := newBTGate(t)
		put := &bpHook{entered: make(chan struct{}), gate: gate.ch}
		c.bps.setPut(put)
		sc := bpGo(func() error { return c.fs.Sync(context.Background()) })
		bpWaitEntered(t, "the Sync's flush entering the Put of the shortened chunk 1, content new "+
			"to the bucket", put.entered, sc)
		bpWantDirty(t, c.fs, "while the flush is held in the upload of the trimmed chunk (ADR 0006 "+
			"§5: the buffer never enters of.dirty and is never charged)", 0)
		gate.open()
		if err := sc.wait(t, "the Sync, after its chunk Put was released"); err != nil {
			t.Errorf("the Sync = %v, want success against a healthy writable store", err)
		}
		c.bps.setPut(nil)
		bpWantDirty(t, c.fs, "after the Sync", 0)
		c.wantPending(t, 0, "after the Sync", "ADR 0006 §5: when a flush succeeds it resets "+
			"of.pendingTrim")
		c.wantEverywhere(t, c.data[:bpCS+1000], "truncated to cs+1000 and synced")
	})

	t.Run("upload fails, inode still there", func(t *testing.T) {
		c := ptSeedCold(t, "failed-upload.bin", 2*bpCS)
		c.setSize(t, bpCS+1000)
		injected := errors.New("pending trim test: injected failure of every chunk Put")
		put := &bpHook{err: injected}
		c.bps.setPut(put)
		err := bpSync(t, c.fs, "Sync whose upload of the shortened chunk fails")
		calls := c.bps.callsOf(put)
		c.bps.setPut(nil)
		if err == nil {
			t.Fatalf("Sync with every chunk Put failing = nil, want an error: the shortened chunk "+
				"is content new to the bucket (it made %d chunk Puts)", calls)
		}
		what := "after the Sync whose upload failed"
		bpWantDirty(t, c.fs, what+" (ADR 0006 §5; ADR 0003 §2's table: unchanged)", 0)
		s := c.wantPending(t, 1, what, "ADR 0006 §5: an upload that fails leaves of.pendingTrim as "+
			"it was")
		ptWantNoneDirty(t, s, what)
		bpCheckAccounting(t, c.fs, what)
		c.wantEverywhere(t, c.data[:bpCS+1000], "truncated to cs+1000, its flush's upload failed, "+
			"then synced with the store healthy")
	})
}

// ptRandBytes returns n bytes from r.
func ptRandBytes(r *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := 0; i < n; i += 8 {
		v := r.Uint64()
		for j := 0; j < 8 && i+j < n; j++ {
			b[i+j] = byte(v >> (8 * j))
		}
	}
	return b
}

// ptModelRead is what a Read of count bytes at off returns from a file whose
// bytes are model (ADR 0005 §1, §2): the range clamped to the size, and eof
// when it reaches the end; no bytes, with eof, at or past the end.
func ptModelRead(model []byte, off, count int) ([]byte, bool) {
	if off >= len(model) {
		return nil, true
	}
	end := off + min(count, len(model)-off)
	return model[off:end], end == len(model)
}

// ptLast formats the last n of ops, oldest first.
func ptLast(ops []string, n int) string {
	if len(ops) > n {
		ops = ops[len(ops)-n:]
	}
	return "  " + strings.Join(ops, "\n  ")
}

// TestPendingTrimDifferential pins ADR 0006 §1 as a whole: "Read (§6), Write
// (§4) and a flush (§5) honour the trim, and truncate (§3) only ever lowers
// it. So bytes that a truncate removed never reach a reply, a dirty buffer or
// the bucket", with POSIX's and RFC 1813's reading of a size change that the
// ADR quotes (Sources). For each of eight seeds, 300 random operations run
// against a byte-slice model of the file, which a size change cuts or extends
// with zeros, starting from a cold five-chunk file:
//
//   - 30% Write, at an offset in [0, 6cs), of 1 to 1.5cs bytes;
//   - 30% SetAttr of the size: half k×cs+r in the middle of a chunk, a
//     quarter k×cs, a quarter anywhere in [0, 6cs];
//   - 25% one Read, compared with the model, clamp and eof included;
//   - 10% Sync;
//   - 5% remount: a Sync, then a fresh mount of the bucket, whose cache is
//     empty, and a new Lookup.
//
// After each operation: GetAttr's size is the model's; ADR 0003 §2's
// accounting holds (bpCheckAccounting); the file has at most one pending trim
// (§2 I4, §8); and a SetAttr has not raised DirtyBytes() (§8). At the end the
// file is compared on this FS, after a Sync and on a fresh mount. A failure
// names the seed, the step and the last 20 operations.
func TestPendingTrimDifferential(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) { ptDifferential(t, seed) })
	}
}

// ptDifferential is one seed of TestPendingTrimDifferential.
func ptDifferential(t *testing.T, seed uint64) {
	t.Helper()
	const (
		name  = "model.bin"
		steps = 300
		span  = 6 * bpCS
	)
	r := rand.New(rand.NewPCG(seed, seed^0x5eed))
	st := bpLocal(t)
	model := append([]byte(nil), bpSeedFile(t, st, name, 5*bpCS)...)
	fs := bpNew(t, st, -1, quietLog())
	h, attr := bpLookup(t, fs, name)
	id := attr.FileID

	var ops []string
	stop := func(step int, format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d, step %d: %s\nthe last %d operations, oldest first:\n%s", seed, step,
			fmt.Sprintf(format, args...), min(len(ops), 20), ptLast(ops, 20))
	}

	for step := 0; step < steps; step++ {
		var op string
		p := r.IntN(100)
		switch {
		case p < 30:
			off := r.IntN(span)
			n := 1 + r.IntN(bpCS+bpCS/2)
			b := ptRandBytes(r, n)
			op = fmt.Sprintf("Write of %d bytes at %s", n, ptAt(uint64(off)))
			ops = append(ops, op)
			w := bpWrite(t, fs, op, h, uint64(off), b, vfs.Unstable)
			if w.err != nil || int(w.n) != n {
				stop(step, "%s = %v, want (%d, UNSTABLE, nil)", op, w, n)
			}
			if end := off + n; end > len(model) {
				model = append(model, make([]byte, end-len(model))...)
			}
			copy(model[off:], b)
		case p < 60:
			var size int
			q := r.IntN(4)
			switch {
			case q < 2:
				size = r.IntN(6)*bpCS + 1 + r.IntN(bpCS-1)
			case q == 2:
				size = r.IntN(7) * bpCS
			default:
				size = r.IntN(span + 1)
			}
			op = fmt.Sprintf("SetAttr(size %s)", ptAt(uint64(size)))
			ops = append(ops, op)
			before := fs.DirtyBytes()
			bpTruncate(t, fs, h, uint64(size), op)
			if after := fs.DirtyBytes(); after > before {
				stop(step, "%s raised DirtyBytes() from %d to %d; ADR 0006 §3(d), §7 and §8: a "+
					"truncate never raises DirtyBytes()", op, before, after)
			}
			if size < len(model) {
				model = model[:size]
			} else {
				model = append(model, make([]byte, size-len(model))...)
			}
		case p < 85:
			off := r.IntN(span + bpCS)
			count := 1 + r.IntN(2*bpCS)
			op = fmt.Sprintf("Read of %d bytes at %s", count, ptAt(uint64(off)))
			ops = append(ops, op)
			got := rcStartRead(fs, h, uint64(off), uint32(count)).wait(t, op)
			want, wantEOF := ptModelRead(model, off, count)
			if got.err != nil {
				stop(step, "%s = %v, want success", op, got.err)
			}
			if !bytes.Equal(got.data, want) {
				stop(step, "%s of a %d-byte file: %s (ADR 0006 §1: bytes a truncate removed never "+
					"reach a reply, and a grown area reads as zeros; ADR 0005 §1)", op, len(model),
					wsDiff(got.data, want, 256))
			}
			if got.eof != wantEOF {
				stop(step, "%s of a %d-byte file returned eof = %v, want %v (ADR 0005 §1, §2)", op,
					len(model), got.eof, wantEOF)
			}
		case p < 95:
			op = "Sync"
			ops = append(ops, op)
			if err := bpSync(t, fs, op); err != nil {
				stop(step, "Sync = %v, want success against a healthy writable store", err)
			}
		default:
			op = "remount: a Sync, then a fresh mount of the bucket"
			ops = append(ops, op)
			if err := bpSync(t, fs, "Sync before a remount"); err != nil {
				stop(step, "the Sync before a remount = %v, want success", err)
			}
			fs = bpNew(t, st, -1, quietLog())
			h, attr = bpLookup(t, fs, name)
			id = attr.FileID
		}

		if a := bpGetAttr(t, fs, h, "GetAttr after "+op); a.Size != uint64(len(model)) {
			stop(step, "after %s, GetAttr reports size %d, want %d", op, a.Size, len(model))
		}
		bpCheckAccounting(t, fs, fmt.Sprintf("seed %d, step %d, after %s", seed, step, op))
		if of := bpOpen(t, fs, id, "FS.open after "+op); of != nil {
			if s := bpSnapshot(t, of, "after "+op); s.pending > 1 {
				stop(step, "after %s, len(of.pendingTrim) = %d, want at most 1 (ADR 0006 §2 I4, §8)",
					op, s.pending)
			}
		}
		if t.Failed() {
			stop(step, "a check after %s failed; see above", op)
		}
	}

	what := fmt.Sprintf("seed %d: the file after %d operations", seed, steps)
	bpWantFile(t, fs, h, model, what+", before the last Sync")
	if err := bpSync(t, fs, "the last Sync"); err != nil {
		stop(steps, "the last Sync = %v, want success", err)
	}
	bpWantFile(t, fs, h, model, what+", after the last Sync")
	bpWantRemount(t, st, name, model, what)
	if t.Failed() {
		stop(steps, "the final comparison failed; see above")
	}
}
