package blobfs

// Clean-room tests for ADR 0004, "The chunk cache owns its bytes and charges
// what it holds" (docs/adr/0004-chunk-cache-owns-its-bytes.md), written from
// that ADR, docs/DESIGN.md §3 ("Keys, integrity and the cache") and ADR 0003
// §2 and §4, without reading the implementation. They cover §1 (put stores a
// copy with cap == len, which the caller may then change, and a cached chunk's
// bytes cannot change); §2 (the charge equals Σ len and Σ cap over the entries
// and is at most maxBytes; an entry longer than maxBytes is not stored; a put
// of a present hash keeps the entry, charges nothing and refreshes its
// recency; least-recently-used eviction with a hit counting as a use; and
// Config.CacheBytes as maxBytes); §5 (when the filesystem caches a chunk, and
// that the key is the chunk key after chunkPrefix); and §7 (the test surface,
// and "How a test reaches #46").
//
// The cache on its own is driven through newChunkCache, put, get and stats.
// The filesystem tests read FS.cache, which §7 lets a test read without a
// lock, and take cacheBytes from the cache's stats, which §7 names as "the
// number FS.Stats reports as cacheBytes". Every call that could hang goes
// through the bp* helpers or btBounded. Nothing here sleeps, and nothing
// writes to a slice that get returned (§3, §7).
//
// The #46 tests (TestChunkCacheFailedFlush*) follow §7's recipe: content new
// to the bucket; a store whose chunk Put fails after the flush's first chunk
// upload has succeeded (bpStore with passFirst 1); get to find which chunk that
// upload cached, because the upload order is not pinned; and a second file
// that comes to name the same hash after the failed flush uploaded it, because
// the file whose flush failed reads its own dirty buffers.
//
// Not covered, on purpose:
//   - Pending-trim resolution reading a changed cache entry (Context: the third
//     path that copies cached bytes into a buffer the next flush stores). It
//     needs a truncate made while the chunk is not cached, then that chunk
//     cached through an upload. The FS skips uploading a hash that is already
//     in the bucket (§5), so the upload would have to follow a HEAD that fails
//     to find an object that is there, a fault bpStore does not have.
//   - FS.Stats itself. Nothing this file is written from gives its signature
//     or where cacheBytes sits among its results, so cacheBytes is read
//     through chunkCache.stats, which §7 says is what FS.Stats reports.
//   - §5's "nothing else is cached" for skipped uploads, failed fetches and
//     fetches that fail verification (holes are covered); §3's rule for get's
//     callers, except as the #46 tests exercise it; §4 (hits are not
//     verified), which is a negative; the cache's mutex being a leaf lock; and
//     §6, which is a cost.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"strata/internal/store"
	"strata/internal/vfs"
)

// ccF and ccG name the two files of the #46 tests.
const (
	ccF = "f.bin"
	ccG = "g.bin"
)

// ccSmallCache is the CacheBytes of TestChunkCacheBoundedByCacheBytes. It is
// an untyped constant so that it assigns to Config.CacheBytes whatever integer
// type that field has: ADR 0004 §2 names the field but not its type.
const ccSmallCache = 2 * bpCS

// ccHash returns the cache key of a chunk whose bytes are b: the lowercase hex
// SHA-256 of b (ADR 0004 §5).
func ccHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ccShort abbreviates a hash for a failure message.
func ccShort(h string) string {
	if len(h) > 12 {
		return h[:12] + "..."
	}
	return h
}

// ccGot is what one get returned.
type ccGot struct {
	b  []byte
	ok bool
}

// ccGet runs c.get under the hang bound. The caller must not modify what it
// returns (ADR 0004 §3, §7).
func ccGet(t *testing.T, c *chunkCache, hash, what string) ([]byte, bool) {
	t.Helper()
	r, ok := btBounded(btHangBound, func() ccGot {
		b, found := c.get(hash)
		return ccGot{b, found}
	})
	if !ok {
		t.Fatalf("%s: chunkCache.get has not returned after %v, so the cache's mutex is "+
			"held and never released", what, btHangBound)
	}
	return r.b, r.ok
}

// ccPut runs c.put under the hang bound.
func ccPut(t *testing.T, c *chunkCache, hash string, data []byte, what string) {
	t.Helper()
	_, ok := btBounded(btHangBound, func() struct{} {
		c.put(hash, data)
		return struct{}{}
	})
	if !ok {
		t.Fatalf("%s: chunkCache.put has not returned after %v, so the cache's mutex is "+
			"held and never released", what, btHangBound)
	}
}

// ccStats is what one stats call returned.
type ccStats struct {
	hits, misses, bytes int64
}

// ccStatsOf runs c.stats under the hang bound.
func ccStatsOf(t *testing.T, c *chunkCache, what string) ccStats {
	t.Helper()
	s, ok := btBounded(btHangBound, func() ccStats {
		h, m, b := c.stats()
		return ccStats{h, m, b}
	})
	if !ok {
		t.Fatalf("%s: chunkCache.stats has not returned after %v, so the cache's mutex is "+
			"held and never released", what, btHangBound)
	}
	return s
}

// ccBuf returns a buffer of length len(content) and capacity capacity holding
// content.
func ccBuf(content []byte, capacity int) []byte {
	b := make([]byte, len(content), capacity)
	copy(b, content)
	return b
}

// ccWantEntry requires c to hold exactly want under hash, in a slice whose
// capacity equals its length (ADR 0004 §1, §2). why says why the entry must be
// present.
func ccWantEntry(t *testing.T, c *chunkCache, hash string, want []byte, what, why string) {
	t.Helper()
	got, ok := ccGet(t, c, hash, what)
	if !ok {
		t.Errorf("%s: get(%s) found nothing, want an entry of %d bytes (%s)",
			what, ccShort(hash), len(want), why)
		return
	}
	if len(got) != len(want) || cap(got) != len(want) {
		t.Errorf("%s: get(%s) returned len %d, cap %d, want len == cap == %d (ADR 0004 §1: "+
			"put stores a copy allocated with make([]byte, len(data)), whose capacity equals "+
			"its length; §2: cap == len)", what, ccShort(hash), len(got), cap(got), len(want))
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s: get(%s) returned bytes other than those put: %s (ADR 0004 §1: put "+
			"stores a copy, complete when put returns, after which the caller may change, "+
			"reslice, append to or reuse data)", what, ccShort(hash), wsDiff(got, want, 64))
	}
}

// ccWantCharge requires the cache's charge to be want.
func ccWantCharge(t *testing.T, c *chunkCache, want int64, what, why string) {
	t.Helper()
	if got := ccStatsOf(t, c, what).bytes; got != want {
		t.Errorf("%s: stats() bytes = %d, want %d (%s)", what, got, want, why)
	}
}

// ccCachedWhy is why ccCheckBucket requires every chunk in the bucket to be
// cached.
const ccCachedWhy = "ADR 0004 §5: putChunk caches a chunk as soon as its upload succeeds, " +
	"and loadChunk caches a chunk it fetches; the key is the chunk's object key after " +
	"chunkPrefix; nothing here is large enough to evict"

// ccCheckBucket requires every chunk object in st to be in c under the hash
// its key names after chunkPrefix, with len == cap == the object's length and
// the object's bytes (ADR 0004 §1, §2, §5). It returns Σ len over the objects,
// and how many there are.
func ccCheckBucket(t *testing.T, st *store.Local, c *chunkCache, what string) (int64, int) {
	t.Helper()
	ctx := context.Background()
	objs, err := st.List(ctx, chunkPrefix, "", 0)
	if err != nil {
		t.Fatalf("%s: listing %q: %v", what, chunkPrefix, err)
	}
	var sum int64
	for _, o := range objs {
		body, err := st.Get(ctx, o.Key)
		if err != nil {
			t.Fatalf("%s: reading %s: %v", what, o.Key, err)
		}
		hash := strings.TrimPrefix(o.Key, chunkPrefix)
		step := fmt.Sprintf("%s: chunk %s (%d bytes)", what, ccShort(hash), len(body))
		ccWantEntry(t, c, hash, body, step, ccCachedWhy)
		sum += int64(len(body))
	}
	return sum, len(objs)
}

// ccNewSmallCache mounts an FS on st as bpNew does with the default
// MaxDirtyBytes, and with CacheBytes set to ccSmallCache. It is a helper of
// its own because bpNew leaves CacheBytes at its default.
func ccNewSmallCache(t *testing.T, st store.Store) *FS {
	t.Helper()
	fs, err := New(context.Background(), Config{
		Store:             st,
		ChunkSize:         bpCS,
		OwnerUID:          501,
		OwnerGID:          20,
		SnapshotRetention: -1,
		CacheBytes:        ccSmallCache,
		Log:               quietLog(),
	})
	if err != nil {
		t.Fatalf("New with CacheBytes %d: %v", ccSmallCache, err)
	}
	return fs
}

// TestChunkCachePutStoresACopy (T1) pins ADR 0004 §1: put "stores a copy of
// data ... It never keeps data itself. The copy is complete when put returns,
// and the caller may then change, reslice, append to or reuse data."
func TestChunkCachePutStoresACopy(t *testing.T) {
	c := newChunkCache(1 << 20)
	want := wsPattern(1000, 7)
	h := ccHash(want)
	buf := ccBuf(want, 4096)
	ccPut(t, c, h, buf, "put of 1000 bytes in a buffer of capacity 4096")

	for i := range buf {
		buf[i] ^= 0xff
	}
	resliced := append(buf[:10], bytes.Repeat([]byte{0xee}, 500)...)
	grown := append(buf, bytes.Repeat([]byte{0xdd}, 3000)...)
	if &resliced[0] != &buf[0] || &grown[0] != &buf[0] {
		t.Fatalf("precondition: the appends did not write into the put buffer's array, so " +
			"the test proves nothing")
	}

	ccWantEntry(t, c, h, want, "after the caller overwrote its buffer, appended to a "+
		"reslice of it and appended into its spare capacity", "ADR 0004 §1: put stores "+
		"what it is given")
}

// TestChunkCacheExactLength (T2) pins ADR 0004 §1, "make sets the capacity
// equal to the length", and §2, "Each entry is charged len(data) ... Because
// cap == len (§1), that is what the entry holds". A slice whose capacity
// exceeds maxBytes but whose length fits is stored, because only "An entry
// longer than maxBytes is not stored".
func TestChunkCacheExactLength(t *testing.T) {
	c := newChunkCache(1 << 20)
	want := wsPattern(100, 1)
	h := ccHash(want)
	ccPut(t, c, h, ccBuf(want, 4096), "put of a slice of len 100 and cap 4096")
	ccWantCharge(t, c, 100, "after putting len 100, cap 4096", "ADR 0004 §2: the charge "+
		"is len, and equals Σ cap because cap == len; #47")
	ccWantEntry(t, c, h, want, "after putting len 100, cap 4096", "ADR 0004 §1: put "+
		"stores a copy")

	const maxBytes = 1000
	c2 := newChunkCache(maxBytes)
	want2 := wsPattern(500, 2)
	h2 := ccHash(want2)
	ccPut(t, c2, h2, ccBuf(want2, 2000), "put of len 500, cap 2000, into a cache of 1000")
	ccWantCharge(t, c2, 500, "after putting len 500, cap 2000, into a cache of 1000",
		"ADR 0004 §2: only an entry longer than maxBytes is not stored, and it is charged "+
			"its len")
	ccWantEntry(t, c2, h2, want2, "after putting len 500, cap 2000, into a cache of 1000",
		"ADR 0004 §2: only an entry longer than maxBytes is not stored; a cap beyond "+
			"maxBytes does not matter, because the copy has cap == len")
}

// TestChunkCacheChargeAndEviction (T3) pins ADR 0004 §2: "Whenever the cache's
// mutex is not held, the charge equals Σ len and Σ cap over the entries, and is
// at most maxBytes", and "Eviction removes the least recently used entry
// first, where an insertion, a put of a present hash and a hit each count as a
// use". Entries A, B and C are put in that order, each 300 bytes with spare
// capacity, and a hit on A makes B the least recently used. D then needs one
// eviction, which must take B; without the hit it would have taken A.
func TestChunkCacheChargeAndEviction(t *testing.T) {
	const maxBytes = 1000
	c := newChunkCache(maxBytes)
	type ent struct {
		name string
		want []byte
		cap  int
		hash string
	}
	mk := func(name string, seed byte, capacity int) ent {
		w := wsPattern(300, seed)
		return ent{name, w, capacity, ccHash(w)}
	}
	a, b, cc, d := mk("A", 1, 4096), mk("B", 2, 1024), mk("C", 3, 600), mk("D", 4, 800)
	for _, e := range []ent{a, b, cc} {
		ccPut(t, c, e.hash, ccBuf(e.want, e.cap), fmt.Sprintf("put of %s (len 300, cap %d)",
			e.name, e.cap))
	}
	ccWantCharge(t, c, 900, "after putting A, B and C", "ADR 0004 §2: each entry is "+
		"charged len(data), which is what it holds")

	before := ccStatsOf(t, c, "before the hit on A")
	if _, ok := ccGet(t, c, a.hash, "the hit on A"); !ok {
		t.Fatalf("get(A) found nothing after A, B and C (900 bytes) were put into a cache of " +
			"1000 (ADR 0004 §2)")
	}
	if after := ccStatsOf(t, c, "after the hit on A"); after.hits != before.hits+1 {
		t.Errorf("a hit changed stats() hits from %d to %d, want %d (ADR 0004 §7: get counts "+
			"a hit or a miss)", before.hits, after.hits, before.hits+1)
	}

	ccPut(t, c, d.hash, ccBuf(d.want, d.cap), "put of D (len 300, cap 800), which needs one "+
		"eviction")
	charge := ccStatsOf(t, c, "after putting D").bytes
	if charge > maxBytes {
		t.Errorf("after putting D, stats() bytes = %d, want at most maxBytes, %d (ADR 0004 §2)",
			charge, maxBytes)
	}

	present := map[string]bool{}
	var sumLen, sumCap int64
	for _, e := range []ent{a, b, cc, d} {
		got, ok := ccGet(t, c, e.hash, "probing "+e.name)
		if !ok {
			continue
		}
		present[e.name] = true
		sumLen += int64(len(got))
		sumCap += int64(cap(got))
		if !bytes.Equal(got, e.want) {
			t.Errorf("get(%s) returned bytes other than those put: %s (ADR 0004 §1)", e.name,
				wsDiff(got, e.want, 64))
		}
	}
	if present["B"] {
		t.Errorf("B is still cached after D was put, want it evicted: after A's hit, B was " +
			"the least recently used entry (ADR 0004 §2: eviction removes the least recently " +
			"used entry first, and a hit counts as a use)")
	}
	if !present["A"] {
		t.Errorf("A was evicted when D was put, want it kept: its hit made it the most " +
			"recently used of A, B and C, and removing B alone brings the charge within " +
			"maxBytes (ADR 0004 §2: least recently used first, and a hit counts as a use)")
	}
	if !present["D"] {
		t.Errorf("D is not cached, want it stored: 300 bytes is not longer than maxBytes, " +
			"1000 (ADR 0004 §2)")
	}
	if charge != sumLen || charge != sumCap {
		t.Errorf("after the eviction, stats() bytes = %d, Σ len = %d and Σ cap = %d over the "+
			"entries present (%v), want all three equal (ADR 0004 §2; #47)",
			charge, sumLen, sumCap, present)
	}
	if sumCap > maxBytes {
		t.Errorf("after the eviction, Σ cap over the entries present = %d, want at most "+
			"maxBytes, %d (ADR 0004 §2)", sumCap, maxBytes)
	}
}

// TestChunkCacheOversizedEntryNotStored (T4) pins ADR 0004 §2: "An entry
// longer than maxBytes is not stored, and put changes nothing." An entry of
// exactly maxBytes is not longer than it, and is stored.
func TestChunkCacheOversizedEntryNotStored(t *testing.T) {
	const maxBytes = 1000
	c := newChunkCache(maxBytes)
	e1, e2 := wsPattern(400, 1), wsPattern(400, 2)
	ccPut(t, c, ccHash(e1), e1, "put of E1 (400 bytes)")
	ccPut(t, c, ccHash(e2), e2, "put of E2 (400 bytes)")
	before := ccStatsOf(t, c, "before the oversized put")

	big := wsPattern(maxBytes+1, 3)
	ccPut(t, c, ccHash(big), big, "put of 1001 bytes into a cache of 1000")
	if after := ccStatsOf(t, c, "after the oversized put"); after != before {
		t.Errorf("a put of an entry longer than maxBytes changed stats() from %+v to %+v, "+
			"want no change (ADR 0004 §2: put changes nothing)", before, after)
	}
	if _, ok := ccGet(t, c, ccHash(big), "probing the oversized entry"); ok {
		t.Errorf("get found the 1001-byte entry, want it not stored in a cache of 1000 " +
			"(ADR 0004 §2)")
	}
	why := "ADR 0004 §2: a put of an entry longer than maxBytes changes nothing, so it " +
		"evicts nothing"
	ccWantEntry(t, c, ccHash(e1), e1, "E1 after the oversized put", why)
	ccWantEntry(t, c, ccHash(e2), e2, "E2 after the oversized put", why)

	c2 := newChunkCache(maxBytes)
	exact := wsPattern(maxBytes, 4)
	ccPut(t, c2, ccHash(exact), exact, "put of exactly 1000 bytes into a cache of 1000")
	ccWantCharge(t, c2, maxBytes, "after putting exactly maxBytes", "ADR 0004 §2: only an "+
		"entry longer than maxBytes is not stored")
	ccWantEntry(t, c2, ccHash(exact), exact, "the entry of exactly maxBytes", "ADR 0004 §2: "+
		"only an entry longer than maxBytes is not stored")
}

// TestChunkCachePutOfPresentHash (T5) pins ADR 0004 §2: "A put of a hash
// already present keeps the existing entry, charges nothing, and refreshes the
// entry's recency", and §1: "A put that then finds its hash already present
// discards its copy." P and Q are put, P is put again with other bytes under
// the same hash, and R then needs one eviction, which must take Q.
func TestChunkCachePutOfPresentHash(t *testing.T) {
	const maxBytes = 1000
	c := newChunkCache(maxBytes)
	p, q, r := wsPattern(400, 1), wsPattern(400, 2), wsPattern(400, 3)
	hp, hq, hr := ccHash(p), ccHash(q), ccHash(r)
	ccPut(t, c, hp, p, "put of P (400 bytes)")
	ccPut(t, c, hq, q, "put of Q (400 bytes)")
	ccWantCharge(t, c, 800, "after putting P and Q", "ADR 0004 §2")

	other := ccBuf(wsPattern(300, 9), 900)
	ccPut(t, c, hp, other, "a second put under P's hash, of 300 other bytes in a buffer of "+
		"capacity 900")
	ccWantCharge(t, c, 800, "after the second put under P's hash", "ADR 0004 §2: a put of "+
		"a hash already present charges nothing")

	ccPut(t, c, hr, r, "put of R (400 bytes), which needs one eviction")
	charge := ccStatsOf(t, c, "after putting R").bytes
	if _, ok := ccGet(t, c, hq, "probing Q"); ok {
		t.Errorf("Q is still cached after R was put, want it evicted: the second put under " +
			"P's hash refreshed P's recency, so Q was the least recently used entry (ADR 0004 " +
			"§2)")
	}
	ccWantEntry(t, c, hp, p, "P after the second put under its hash and the put of R",
		"ADR 0004 §2: a put of a hash already present keeps the existing entry and "+
			"refreshes its recency")
	ccWantEntry(t, c, hr, r, "R", "ADR 0004 §2: R is not longer than maxBytes")
	if charge != 800 {
		t.Errorf("after putting R, stats() bytes = %d, want 800: P and R (ADR 0004 §2)", charge)
	}
}

// TestChunkCacheOwnershipUnderRace (T6) is a should, not a must: under -race it
// pins ADR 0004 §1, "The copy is complete when put returns, and the caller may
// then change ... or reuse data", and "every entry is immutable while it is
// cached". Writers put entries from buffers they then overwrite, capacity and
// all, while readers get entries and compare them with what was put. A cache
// that kept the caller's buffer would be a data race here, and would return
// the overwritten bytes.
func TestChunkCacheOwnershipUnderRace(t *testing.T) {
	const (
		writers   = 4
		readers   = 4
		perWriter = 64
		entryLen  = 200
	)
	// 1 MiB holds every entry the writers put, so nothing is evicted.
	c := newChunkCache(1 << 20)
	type item struct {
		hash string
		want []byte
	}
	items := make([][]item, writers)
	for w := range items {
		items[w] = make([]item, perWriter)
		for k := range items[w] {
			b := bpUnique(entryLen)
			items[w][k] = item{ccHash(b), b}
		}
	}

	var (
		mu       sync.Mutex
		failures []string
		dropped  int
	)
	fail := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(failures) < 10 {
			failures = append(failures, fmt.Sprintf(format, args...))
		} else {
			dropped++
		}
	}
	wrong := func(got []byte, it item) bool {
		return !bytes.Equal(got, it.want) || cap(got) != len(it.want)
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fail("writer %d panicked: %v", w, r)
				}
			}()
			for k, it := range items[w] {
				buf := ccBuf(it.want, 2*entryLen)
				c.put(it.hash, buf)
				buf = buf[:cap(buf)]
				for j := range buf {
					buf[j] ^= 0xff
				}
				got, ok := c.get(it.hash)
				switch {
				case !ok:
					fail("writer %d, entry %d: get right after put found nothing, want the "+
						"entry: the cache holds everything this test puts (ADR 0004 §2)", w, k)
				case wrong(got, it):
					fail("writer %d, entry %d: after the writer overwrote its buffer, get "+
						"returned len %d, cap %d, bytes equal %v; want len == cap == %d and "+
						"the bytes put (ADR 0004 §1)", w, k, len(got), cap(got),
						bytes.Equal(got, it.want), len(it.want))
				}
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					fail("reader %d panicked: %v", r, p)
				}
			}()
			for pass := 0; pass < 8; pass++ {
				for w := 0; w < writers; w++ {
					src := (w + r) % writers
					for k, it := range items[src] {
						if got, ok := c.get(it.hash); ok && wrong(got, it) {
							fail("reader %d: get of writer %d's entry %d returned len %d, cap "+
								"%d, bytes equal %v; want len == cap == %d and the bytes put "+
								"(ADR 0004 §1: every entry is immutable while it is cached)",
								r, src, k, len(got), cap(got), bytes.Equal(got, it.want),
								len(it.want))
						}
					}
				}
			}
		}(r)
	}
	if _, ok := btBounded(btHangBound, func() struct{} {
		wg.Wait()
		return struct{}{}
	}); !ok {
		t.Fatalf("the writers and readers have not finished after %v, so the cache's mutex "+
			"is held and never released", btHangBound)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, f := range failures {
		t.Error(f)
	}
	if dropped > 0 {
		t.Errorf("and %d more failures like those", dropped)
	}
}

// TestChunkCacheFlushedShortChunks (T7) pins ADR 0004 §1 and §2 for chunks the
// filesystem caches as it uploads them (§5: "putChunk caches a chunk as soon
// as its upload succeeds"). Each file is shorter than a chunk, or ends in a
// partial chunk, so the write path's buffer has capacity bpCS (ADR 0003 §2)
// while the chunk is shorter (#47). Each short chunk is the whole of its own
// file, because a chunk shorter than bpCS is only certain to be stored short
// at the end of a file: how a chunk followed by a hole is stored is not
// pinned.
func TestChunkCacheFlushedShortChunks(t *testing.T) {
	st := bpLocal(t)
	fs := bpNew(t, st, 0, quietLog())
	sizes := []int{1, 100, 1000, 3000, bpCS - 1, bpCS + 500}
	for k, n := range sizes {
		name := fmt.Sprintf("short-%d.bin", k)
		h, _ := bpCreate(t, fs, name, 0)
		bpMustWrite(t, fs, fmt.Sprintf("%d bytes at 0 of %s", n, name), h, 0, bpUnique(n))
	}
	bpMustSync(t, fs, "Sync uploading every chunk for the first time")

	sum, n := ccCheckBucket(t, st, fs.cache, "after the Sync")
	if n < len(sizes) {
		t.Fatalf("the bucket holds %d chunk objects, want at least %d: one or more per file, "+
			"all of new content", n, len(sizes))
	}
	ccWantCharge(t, fs.cache, sum, "after the Sync", "cacheBytes must be Σ len over the "+
		"chunks cached, which is every chunk in the bucket (ADR 0004 §2: the cacheBytes that "+
		"FS.Stats reports is this charge; §5)")
}

// TestChunkCacheBoundedByCacheBytes (T8) pins ADR 0004 §2: "Config.CacheBytes
// is maxBytes", and the charge "equals Σ len and Σ cap over the entries, and
// is at most maxBytes". With CacheBytes at two chunks, a hundred 100-byte
// chunks are uploaded. Each is its own file, for the reason
// TestChunkCacheFlushedShortChunks gives. A cache that charged len while
// holding the write path's buffer of capacity bpCS would hold about forty
// times CacheBytes (#47).
func TestChunkCacheBoundedByCacheBytes(t *testing.T) {
	st := bpLocal(t)
	fs := ccNewSmallCache(t, st)
	const files = 100
	for k := 0; k < files; k++ {
		name := fmt.Sprintf("small-%03d.bin", k)
		h, _ := bpCreate(t, fs, name, 0)
		bpMustWrite(t, fs, "100 bytes at 0 of "+name, h, 0, bpUnique(100))
	}
	bpMustSync(t, fs, "Sync uploading a hundred 100-byte chunks")

	charge := ccStatsOf(t, fs.cache, "after the Sync").bytes
	if charge > ccSmallCache {
		t.Errorf("cacheBytes = %d, want at most CacheBytes, %d (ADR 0004 §2)", charge,
			ccSmallCache)
	}

	ctx := context.Background()
	objs, err := st.List(ctx, chunkPrefix, "", 0)
	if err != nil {
		t.Fatalf("listing %q: %v", chunkPrefix, err)
	}
	if len(objs) < files {
		t.Fatalf("the bucket holds %d chunk objects, want at least %d, one per file", len(objs),
			files)
	}
	var (
		present        int
		sumLen, sumCap int64
	)
	for _, o := range objs {
		body, err := st.Get(ctx, o.Key)
		if err != nil {
			t.Fatalf("reading %s: %v", o.Key, err)
		}
		hash := strings.TrimPrefix(o.Key, chunkPrefix)
		got, ok := ccGet(t, fs.cache, hash, "probing chunk "+ccShort(hash))
		if !ok {
			continue
		}
		present++
		sumLen += int64(len(got))
		sumCap += int64(cap(got))
		if !bytes.Equal(got, body) {
			t.Errorf("get(%s) returned bytes other than the chunk's object: %s (ADR 0004 §1)",
				ccShort(hash), wsDiff(got, body, 64))
		}
	}
	if present == 0 {
		t.Errorf("no chunk is cached, want at least the last uploaded: each is 100 bytes, " +
			"within CacheBytes (ADR 0004 §5: putChunk caches a chunk as soon as its upload " +
			"succeeds)")
	}
	if sumCap != charge || sumLen != charge {
		t.Errorf("over the %d chunks cached, Σ cap = %d and Σ len = %d, want both equal to "+
			"cacheBytes, %d (ADR 0004 §2; #47: the charge must be what the cache holds)",
			present, sumCap, sumLen, charge)
	}
	if sumCap > ccSmallCache {
		t.Errorf("Σ cap over the chunks cached = %d, want at most CacheBytes, %d (ADR 0004 "+
			"§2; #47)", sumCap, ccSmallCache)
	}
}

// TestChunkCacheFetchedChunks (T9) pins ADR 0004 §1 and §2 for chunks fetched
// from the bucket (§5: "loadChunk caches a chunk fetched from the bucket once
// it passes verification, or at once when verification is skipped"). The
// store promises nothing about the capacity of what it returns (Context), so
// only a copy is certain to have cap == len.
func TestChunkCacheFetchedChunks(t *testing.T) {
	st := bpLocal(t)
	const name = "fetched.bin"
	data := bpSeedFile(t, st, name, 2*bpCS+100)
	fs := bpNew(t, st, 0, quietLog())
	h, _ := bpLookup(t, fs, name)
	bpWantFile(t, fs, h, data, "the seeded file read through a fresh mount")

	sum, n := ccCheckBucket(t, st, fs.cache, "after reading every chunk through a fresh mount")
	if n < 3 {
		t.Fatalf("the bucket holds %d chunk objects, want at least 3 for %d bytes in chunks "+
			"of %d", n, len(data), bpCS)
	}
	ccWantCharge(t, fs.cache, sum, "after reading every chunk through a fresh mount",
		"cacheBytes must be Σ len over the chunks fetched (ADR 0004 §2, §5)")
}

// TestChunkCacheFreshMountAndHoles (T10) pins ADR 0004 §5: "New leaves the
// cache empty: mounting reads the root pointer and the snapshot from the store
// directly", and "Nothing else is cached: ... not a hole".
func TestChunkCacheFreshMountAndHoles(t *testing.T) {
	wantEmpty := func(t *testing.T, fs *FS, what string) {
		t.Helper()
		if s := ccStatsOf(t, fs.cache, what); s != (ccStats{}) {
			t.Errorf("%s: stats() = %+v, want zero hits, misses and bytes (ADR 0004 §5: New "+
				"leaves the cache empty)", what, s)
		}
	}
	t.Run("a fresh mount of an empty bucket", func(t *testing.T) {
		wantEmpty(t, bpNew(t, bpLocal(t), 0, quietLog()), "a fresh mount of an empty bucket")
	})
	t.Run("a fresh mount of a bucket holding committed data", func(t *testing.T) {
		st := bpLocal(t)
		bpSeedFile(t, st, "committed.bin", 3*bpCS)
		wantEmpty(t, bpNew(t, st, 0, quietLog()), "a fresh mount of a bucket holding "+
			"committed data")
	})
	t.Run("reading a file that is only holes", func(t *testing.T) {
		st := bpLocal(t)
		fs := bpNew(t, st, 0, quietLog())
		const name = "holes.bin"
		const size = 3*bpCS + 10
		zeros := make([]byte, size)
		why := "ADR 0004 §5: a hole is not cached"
		h, _ := bpCreate(t, fs, name, 0)
		bpTruncate(t, fs, h, size, fmt.Sprintf("SetAttr(size %d) of an empty file", size))
		bpWantFile(t, fs, h, zeros, "a file grown by SetAttr")
		ccWantCharge(t, fs.cache, 0, "after reading a file that is only holes", why)

		bpMustSync(t, fs, "Sync of the file that is only holes")
		bpWantFile(t, fs, h, zeros, "a file grown by SetAttr, after a Sync")
		ccWantCharge(t, fs.cache, 0, "after reading it again after a Sync", why)

		fs2 := bpNew(t, st, 0, quietLog())
		h2, _ := bpLookup(t, fs2, name)
		bpWantFile(t, fs2, h2, zeros, "a file grown by SetAttr, on a fresh mount")
		ccWantCharge(t, fs2.cache, 0, "after reading it on a fresh mount", why)
	})
}

// cc46 is the state that ADR 0004 §7, "How a test reaches #46", sets up: f.bin
// holds C0||C1, both of content new to the bucket; its flush uploaded one of
// the two chunks and failed on the other; and that one, chunk i, is the only
// one of the two in the cache. Which chunk it is is found with get, because
// the order in which flushOpen uploads a file's chunks is not pinned (§7).
type cc46 struct {
	st *store.Local
	fs *FS
	f  vfs.Handle
	c  [2][]byte
	h  [2]string
	i  int
}

// cc46Setup reaches the cc46 state on a fresh bucket, with the default
// CacheBytes. The store's chunk Put is armed so that the first call passes and
// the rest fail, then disarmed once the Sync has failed.
func cc46Setup(t *testing.T) *cc46 {
	t.Helper()
	st := bpLocal(t)
	bps := &bpStore{Store: st}
	s := &cc46{st: st, fs: bpNew(t, bps, 0, quietLog())}
	s.f, _ = bpCreate(t, s.fs, ccF, 0)
	s.c[0], s.c[1] = bpUnique(bpCS), bpUnique(bpCS)
	s.h[0], s.h[1] = ccHash(s.c[0]), ccHash(s.c[1])
	bpMustWrite(t, s.fs, "C0||C1 at 0 of "+ccF, s.f, 0, s.whole())

	injected := errors.New("chunk cache test: injected failure of every chunk Put after the first")
	put := &bpHook{passFirst: 1, err: injected}
	bps.setPut(put)
	err := bpSync(t, s.fs, "Sync of "+ccF+" whose second chunk Put fails")
	calls := bps.callsOf(put)
	bps.setPut(nil)
	if err == nil {
		t.Fatalf("setup: Sync with every chunk Put after the first failing = nil, want an "+
			"error (the flush made %d chunk Puts)", calls)
	}

	var cached []int
	for k := 0; k < 2; k++ {
		got, ok := ccGet(t, s.fs.cache, s.h[k], fmt.Sprintf("setup: probing chunk %d", k))
		if !ok {
			continue
		}
		if !bytes.Equal(got, s.c[k]) {
			t.Fatalf("setup: straight after the failed Sync, get(h%d) returns bytes other than "+
				"C%d: %s (ADR 0004 §5: putChunk caches the chunk it uploaded)", k, k,
				wsDiff(got, s.c[k], 256))
		}
		cached = append(cached, k)
	}
	if len(cached) != 1 {
		t.Fatalf("setup: after a flush whose first chunk upload succeeded and whose other one "+
			"failed, the cache holds chunks %v of f.bin's two, want exactly one (ADR 0004 §5: "+
			"putChunk caches a chunk as soon as its upload succeeds, whatever then happens to "+
			"the rest of the flush, and a failed upload caches nothing); the flush made %d "+
			"chunk Puts. Every later check would prove nothing", cached, calls)
	}
	s.i = cached[0]
	return s
}

// whole returns a new slice holding C0||C1.
func (s *cc46) whole() []byte {
	out := make([]byte, 0, 2*bpCS)
	out = append(out, s.c[0]...)
	return append(out, s.c[1]...)
}

// patchF writes 32 bytes at 1000 and 32 bytes at bpCS+1000 of f.bin, which
// changes the middle of every dirty index the failed flush left, whichever
// chunk it uploaded (ADR 0004 §7). It returns what f.bin must now read as.
func (s *cc46) patchF(t *testing.T) []byte {
	t.Helper()
	want := s.whole()
	for _, off := range []int{1000, bpCS + 1000} {
		p := bpUnique(32)
		bpMustWrite(t, s.fs, fmt.Sprintf("32 bytes at %d of %s", off, ccF), s.f, uint64(off), p)
		copy(want[off:], p)
	}
	return want
}

// shareG creates g.bin, writes C0||C1 into it and syncs, which must succeed.
// g.bin then names chunk i's hash after the failed flush uploaded it, so this
// flush neither uploads nor caches that chunk again (ADR 0004 §7).
func (s *cc46) shareG(t *testing.T) (vfs.Handle, vfs.Attr) {
	t.Helper()
	g, attr := bpCreate(t, s.fs, ccG, 0)
	bpMustWrite(t, s.fs, "C0||C1 at 0 of "+ccG, g, 0, s.whole())
	bpMustSync(t, s.fs, "Sync of "+ccG+" and of "+ccF+", with the store healthy again")
	return g, attr
}

// wantCached requires the cache still to hold C_i under h_i, and those bytes
// still to hash to h_i (ADR 0004 §1).
func (s *cc46) wantCached(t *testing.T, after string) {
	t.Helper()
	got, ok := ccGet(t, s.fs.cache, s.h[s.i], after)
	if !ok {
		t.Fatalf("%s: get(h%d) found nothing, so the test cannot check it: nothing here "+
			"comes near the default CacheBytes (ADR 0004 §2)", after, s.i)
	}
	if !bytes.Equal(got, s.c[s.i]) || ccHash(got) != s.h[s.i] {
		t.Errorf("%s: get(h%d) returns bytes that hash to %s, want C%d, which hashes to %s: %s "+
			"(ADR 0004 §1: the cache stores its own copy, so every entry is immutable while "+
			"it is cached; #46)", after, s.i, ccShort(ccHash(got)), s.i, ccShort(s.h[s.i]),
			wsDiff(got, s.c[s.i], 256))
	}
}

// TestChunkCacheFailedFlushWriteCannotChangeCachedChunk (T11) pins ADR 0004
// §1: "It follows that every entry is immutable while it is cached." After a
// flush fails partway, the uploaded chunk is both dirty and cached (Context),
// and a Write into that index must not reach the cached bytes (#46).
func TestChunkCacheFailedFlushWriteCannotChangeCachedChunk(t *testing.T) {
	s := cc46Setup(t)
	s.patchF(t)
	s.wantCached(t, "after writing 32 bytes into the middle of each dirty index of "+ccF)
}

// TestChunkCacheFailedFlushTruncateThenWritePastEnd (T12) pins ADR 0004 §1 for
// Context's second route: "a truncate into that index, which reslices the
// dirty buffer in place, followed by a write past the new end, which appends
// into the spare capacity of the same array".
func TestChunkCacheFailedFlushTruncateThenWritePastEnd(t *testing.T) {
	s := cc46Setup(t)
	base := uint64(s.i * bpCS)
	bpTruncate(t, s.fs, s.f, base+500, fmt.Sprintf("SetAttr(size %d) of %s, into dirty index %d",
		base+500, ccF, s.i))
	bpMustWrite(t, s.fs, fmt.Sprintf("100 bytes at %d of %s, past its new end", base+1500, ccF),
		s.f, base+1500, bpUnique(100))
	s.wantCached(t, fmt.Sprintf("after truncating %s into index %d and writing past the new end",
		ccF, s.i))
}

// TestChunkCacheFailedFlushSharedChunkReadsCorrectly (T13) pins ADR 0004 §1 and
// §3, and the first Consequence: "A file that shares a chunk with another
// reads the right bytes." g.bin names chunk i's hash and reads it through a
// hit; f.bin reads its own content.
func TestChunkCacheFailedFlushSharedChunkReadsCorrectly(t *testing.T) {
	s := cc46Setup(t)
	fWant := s.patchF(t)
	g, _ := s.shareG(t)
	bpWantFile(t, s.fs, g, s.whole(), ccG+", which names the hash of the chunk that "+ccF+
		"'s failed flush uploaded, read on the same FS (ADR 0004 §1, §3; #46)")
	bpWantFile(t, s.fs, s.f, fWant, ccF+", C0||C1 with its two patches (ADR 0004 §1)")
}

// TestChunkCacheFailedFlushWriteFromCachedChunk (T14) pins ADR 0004 §1 and §3,
// and the first Consequence: "a write or truncate that starts from a cached
// chunk stores the right bytes." A partial write into g.bin's index i loads
// that chunk through a hit, and what the flush stores is checked on a fresh
// mount, because a wrong chunk would be stored under the hash of what it is.
func TestChunkCacheFailedFlushWriteFromCachedChunk(t *testing.T) {
	s := cc46Setup(t)
	s.patchF(t)
	g, _ := s.shareG(t)
	want := s.whole()
	off := s.i*bpCS + 3000
	p := bpUnique(16)
	bpMustWrite(t, s.fs, fmt.Sprintf("16 bytes at %d of %s, into its index %d", off, ccG, s.i),
		g, uint64(off), p)
	copy(want[off:], p)
	bpMustSync(t, s.fs, "Sync of the write into "+ccG)
	bpWantRemount(t, s.st, ccG, want, ccG+" after a write that started from the cached "+
		"chunk (ADR 0004 §1, §3; #46)")
}

// TestChunkCacheFailedFlushTruncateStagesCachedChunk (T15) pins ADR 0004 §1
// and §3 for truncate "staging a shortened chunk from the cache", which copies
// data[:tail], and ADR 0003 §2, "Staging from the cache": the chunk must be in
// this FS's cache, which the failed flush's successful upload put there
// (ADR 0004 §5).
func TestChunkCacheFailedFlushTruncateStagesCachedChunk(t *testing.T) {
	s := cc46Setup(t)
	s.patchF(t)
	g, gAttr := s.shareG(t)
	size := uint64(s.i*bpCS + 2000)
	bpTruncate(t, s.fs, g, size, fmt.Sprintf("SetAttr(size %d) of %s, into its index %d", size,
		ccG, s.i))
	of := bpMustOpen(t, s.fs, gAttr.FileID, "after truncating "+ccG)
	snap := bpSnapshot(t, of, "after truncating "+ccG)
	if _, staged := snap.caps[uint64(s.i)]; !staged || snap.pending != 0 {
		t.Fatalf("after truncating %s to %d, of.dirty holds indices %v and %d pending trims, "+
			"want index %d staged and nothing pending, so the test has not reached the staging "+
			"path (ADR 0003 §2, \"Staging from the cache\"; ADR 0004 §5: the failed flush's "+
			"successful upload cached chunk %d)", ccG, size, snap.indices(), snap.pending, s.i, s.i)
	}
	bpMustSync(t, s.fs, "Sync of the truncated "+ccG)
	want := s.whole()[:size]
	bpWantFile(t, s.fs, g, want, ccG+" after a truncate that staged the cached chunk (ADR "+
		"0004 §1, §3)")
	bpWantRemount(t, s.st, ccG, want, ccG+" after a truncate that staged the cached chunk "+
		"(ADR 0004 §1, §3; #46)")
}
