package sunrpc

// Tests for the duplicate request cache, written against
// docs/adr/0002-duplicate-request-cache.md. Section references below are to
// that ADR:
//
//	§2 the key is (connection serial, xid, prog, vers, proc)
//	§5 the cached artefact is the complete encoded reply record
//	§6 three states, and duplicates that arrive mid-flight
//	§7 bounds: 4096 entries, 120 seconds, no flags
//	§8 the pinned shape of the cache type

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"
)

// testBaseKey is a key with every field non-zero, so a test that varies one
// field varies it away from a value that could not be confused with a zero
// value. §2 pins the five fields.
func testBaseKey() replyKey {
	return replyKey{conn: 7, xid: 42, prog: 100003, vers: 3, proc: 12}
}

func testStateName(s cacheState) string {
	switch s {
	case cacheMiss:
		return "cacheMiss"
	case cacheInFlight:
		return "cacheInFlight"
	case cacheHit:
		return "cacheHit"
	}
	return fmt.Sprintf("cacheState(%d)", int(s))
}

// testReplyFor derives deterministic reply bytes from a key, so that a replay
// can be checked against what was stored without the test having to remember
// anything. §5 requires byte-identical replay.
func testReplyFor(k replyKey) []byte {
	return fmt.Appendf(nil, "reply for conn=%d xid=%d prog=%d vers=%d proc=%d",
		k.conn, k.xid, k.prog, k.vers, k.proc)
}

// TestReplyCacheBeginOnEmptyCacheMisses covers §6 row 1: a lookup with no
// entry reports cacheMiss, and the caller then owns an in-flight entry.
func TestReplyCacheBeginOnEmptyCacheMisses(t *testing.T) {
	c := newReplyCache(16, time.Minute)
	k := testBaseKey()

	reply, state := c.begin(k)
	if state != cacheMiss {
		t.Errorf("begin on an empty cache = %s, want cacheMiss: ADR 0002 §6 row 1 says an absent entry installs an in-flight marker and reports a miss", testStateName(state))
	}
	if reply != nil {
		t.Errorf("begin on an empty cache returned %d reply bytes, want none: ADR 0002 §8 only defines reply bytes for cacheHit", len(reply))
	}
}

// TestReplyCacheSecondBeginBeforeFinishIsInFlight covers §6 row 2: while the
// first caller is still executing, an identical call is recognised as a
// mid-flight duplicate.
func TestReplyCacheSecondBeginBeforeFinishIsInFlight(t *testing.T) {
	c := newReplyCache(16, time.Minute)
	k := testBaseKey()

	if _, state := c.begin(k); state != cacheMiss {
		t.Fatalf("first begin = %s, want cacheMiss", testStateName(state))
	}

	reply, state := c.begin(k)
	if state != cacheInFlight {
		t.Errorf("second begin before finish = %s, want cacheInFlight: ADR 0002 §6 row 2 requires an identical call that is still executing to be reported as in flight so the caller can drop the duplicate", testStateName(state))
	}
	if reply != nil {
		t.Errorf("begin for an in-flight entry returned %d reply bytes, want none: there is no reply to replay until finish has stored one (ADR 0002 §6)", len(reply))
	}
}

// TestReplyCacheFinishedEntryReplaysBytesExactly covers §5 and §6 row 3: the
// cached artefact is the complete encoded reply record and is replayed
// byte-for-byte.
func TestReplyCacheFinishedEntryReplaysBytesExactly(t *testing.T) {
	c := newReplyCache(16, time.Minute)
	k := testBaseKey()
	want := testReplyFor(k)

	if _, state := c.begin(k); state != cacheMiss {
		t.Fatalf("first begin = %s, want cacheMiss", testStateName(state))
	}
	c.finish(k, want)

	got, state := c.begin(k)
	if state != cacheHit {
		t.Fatalf("begin after finish = %s, want cacheHit: ADR 0002 §6 row 3 says a done entry is written back without executing", testStateName(state))
	}
	if !bytes.Equal(got, want) {
		t.Errorf("replayed reply = %q, want %q: ADR 0002 §5 caches the whole RPC reply record and requires the replay to be byte-identical, not merely equivalent", got, want)
	}
}

// TestReplyCacheAbandonReleasesEntry covers §8: abandon is the other way a
// caller that observed cacheMiss resolves the entry it owns, and it must leave
// no trace behind.
func TestReplyCacheAbandonReleasesEntry(t *testing.T) {
	c := newReplyCache(16, time.Minute)
	k := testBaseKey()

	if _, state := c.begin(k); state != cacheMiss {
		t.Fatalf("first begin = %s, want cacheMiss", testStateName(state))
	}
	c.abandon(k)

	reply, state := c.begin(k)
	if state != cacheMiss {
		t.Errorf("begin after abandon = %s, want cacheMiss: ADR 0002 §8 makes abandon the alternative to finish for an entry the caller owns, so an abandoned key must be absent again", testStateName(state))
	}
	if reply != nil {
		t.Errorf("begin after abandon returned %d reply bytes, want none: abandon stores no reply to replay (ADR 0002 §8)", len(reply))
	}
}

// TestReplyCacheKeyFieldsAreLoadBearing covers §2: all five fields take part
// in identity. §2 is entirely an argument that two calls differing in any of
// them must not be confusable, because serving one caller another's reply is
// silent, one-sided corruption.
func TestReplyCacheKeyFieldsAreLoadBearing(t *testing.T) {
	base := testBaseKey()

	tests := []struct {
		field string
		other replyKey
		why   string
	}{
		{
			field: "conn",
			other: replyKey{conn: base.conn + 1, xid: base.xid, prog: base.prog, vers: base.vers, proc: base.proc},
			why:   "ADR 0002 §2: an entry can only ever be returned to the connection that created it",
		},
		{
			field: "xid",
			other: replyKey{conn: base.conn, xid: base.xid + 1, prog: base.prog, vers: base.vers, proc: base.proc},
			why:   "ADR 0002 §2: the xid is tested for equality and is what distinguishes a retransmission from a new call",
		},
		{
			field: "prog",
			other: replyKey{conn: base.conn, xid: base.xid, prog: base.prog + 1, vers: base.vers, proc: base.proc},
			why:   "ADR 0002 §2: prog is in the key because a client uses one xid space across both programs on a connection",
		},
		{
			field: "vers",
			other: replyKey{conn: base.conn, xid: base.xid, prog: base.prog, vers: base.vers + 1, proc: base.proc},
			why:   "ADR 0002 §2: vers is in the key because a client uses one xid space across versions on a connection",
		},
		{
			field: "proc",
			other: replyKey{conn: base.conn, xid: base.xid, prog: base.prog, vers: base.vers, proc: base.proc + 1},
			why:   "ADR 0002 §2: two different procedures must never share an entry, since the replayed reply would be for an operation the client never issued",
		},
	}

	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			c := newReplyCache(16, time.Minute)
			if _, state := c.begin(base); state != cacheMiss {
				t.Fatalf("begin(base) = %s, want cacheMiss", testStateName(state))
			}
			c.finish(base, testReplyFor(base))

			reply, state := c.begin(tc.other)
			if state != cacheMiss {
				t.Errorf("a key differing only in %s = %s, want cacheMiss; %s", tc.field, testStateName(state), tc.why)
			}
			if reply != nil {
				t.Errorf("a key differing only in %s was replayed %q; %s", tc.field, reply, tc.why)
			}
		})
	}
}

// TestReplyCacheEntryBoundIsAbsolute covers §7: the entry bound holds at all
// times and eviction is FIFO, ordered by finish.
func TestReplyCacheEntryBoundIsAbsolute(t *testing.T) {
	const maxEntries = 4
	const inserted = 12

	c := newReplyCache(maxEntries, time.Minute)

	keys := make([]replyKey, inserted)
	for i := range keys {
		keys[i] = replyKey{conn: 1, xid: uint32(i + 1), prog: 100003, vers: 3, proc: 12}
	}

	for i, k := range keys {
		if _, state := c.begin(k); state != cacheMiss {
			t.Fatalf("begin(key %d) = %s, want cacheMiss: each key is distinct", i, testStateName(state))
		}
		if n := c.len(); n > maxEntries {
			t.Fatalf("len() = %d after begin of key %d, want at most %d: ADR 0002 §7 makes the entry bound absolute, so the cache never grows past it", n, i, maxEntries)
		}
		c.finish(k, testReplyFor(k))
		if n := c.len(); n > maxEntries {
			t.Fatalf("len() = %d after finish of key %d, want at most %d: ADR 0002 §7 makes the entry bound absolute, so the cache never grows past it", n, i, maxEntries)
		}
	}

	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d after inserting %d keys, want at most %d (ADR 0002 §7)", n, inserted, maxEntries)
	}

	// Check the survivor first: a hit stores nothing and so cannot perturb what
	// follows.
	newest := keys[inserted-1]
	got, state := c.begin(newest)
	if state != cacheHit {
		t.Errorf("the most recently inserted key = %s, want cacheHit: ADR 0002 §7 evicts FIFO, ordered by finish, so the last key to finish is the last to go", testStateName(state))
	} else if want := testReplyFor(newest); !bytes.Equal(got, want) {
		t.Errorf("replayed reply for the newest key = %q, want %q (ADR 0002 §5)", got, want)
	}

	oldest := keys[0]
	if _, state := c.begin(oldest); state != cacheMiss {
		t.Errorf("the earliest inserted key = %s, want cacheMiss: ADR 0002 §7 evicts FIFO, ordered by finish, so with %d entries of room the first of %d keys to finish must have been evicted", testStateName(state), maxEntries, inserted)
	}
	c.abandon(oldest)
}

// TestReplyCacheEntriesExpire covers §7's age bound: retention is short-term
// memory, and an entry past maxAge is purged lazily on lookup.
func TestReplyCacheEntriesExpire(t *testing.T) {
	const maxAge = 150 * time.Millisecond

	c := newReplyCache(16, maxAge)
	k := testBaseKey()

	if _, state := c.begin(k); state != cacheMiss {
		t.Fatalf("first begin = %s, want cacheMiss", testStateName(state))
	}
	c.finish(k, testReplyFor(k))

	if _, state := c.begin(k); state != cacheHit {
		t.Fatalf("begin immediately after finish = %s, want cacheHit: the entry is well inside the %v age bound", testStateName(state), maxAge)
	}

	time.Sleep(4 * maxAge)

	reply, state := c.begin(k)
	if state != cacheMiss {
		t.Errorf("begin %v after finish with a %v age bound = %s, want cacheMiss: ADR 0002 §7 bounds retention by age and purges expired entries lazily on lookup", 4*maxAge, maxAge, testStateName(state))
	}
	if reply != nil {
		t.Errorf("an expired entry replayed %q; ADR 0002 §7 requires it to be gone", reply)
	}
}

// TestReplyCacheFullOfInFlightEntriesRunsUncached covers §7's fourth bullet:
// in-flight entries are never evicted or expired, so a cache with no evictable entry
// left degrades to running the new call uncached rather than breaking its
// bound or dropping a marker someone still owns.
func TestReplyCacheFullOfInFlightEntriesRunsUncached(t *testing.T) {
	const maxEntries = 4

	c := newReplyCache(maxEntries, time.Minute)

	inFlight := make([]replyKey, maxEntries)
	for i := range inFlight {
		inFlight[i] = replyKey{conn: 1, xid: uint32(i + 1), prog: 100003, vers: 3, proc: 12}
		if _, state := c.begin(inFlight[i]); state != cacheMiss {
			t.Fatalf("begin(key %d) = %s, want cacheMiss: each key is distinct", i, testStateName(state))
		}
		if n := c.len(); n > maxEntries {
			t.Fatalf("len() = %d after begin of key %d, want at most %d (ADR 0002 §7)", n, i, maxEntries)
		}
	}

	extra := replyKey{conn: 1, xid: uint32(maxEntries + 1), prog: 100003, vers: 3, proc: 12}
	reply, state := c.begin(extra)
	if state != cacheMiss {
		t.Errorf("begin on a cache whose every entry is in flight = %s, want cacheMiss: ADR 0002 §7 says begin still reports a miss in that case so the caller runs uncached without needing to know", testStateName(state))
	}
	if reply != nil {
		t.Errorf("begin on a full cache replayed %q, want no bytes (ADR 0002 §7)", reply)
	}
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d after a begin the cache had no room for, want at most %d: ADR 0002 §7 keeps the bound absolute rather than growing past it", n, maxEntries)
	}

	for i, k := range inFlight {
		if _, state := c.begin(k); state != cacheInFlight {
			t.Errorf("in-flight key %d = %s, want cacheInFlight: ADR 0002 §7 says in-flight entries are never evicted, so a new call must not displace a marker its owner still has to resolve", i, testStateName(state))
		}
	}

	// The extra call was never installed, so finishing it must store nothing.
	c.finish(extra, testReplyFor(extra))
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d after finishing a key that was never installed, want at most %d: ADR 0002 §7 says finish completes only an entry that exists and is in flight", n, maxEntries)
	}
	for i, k := range inFlight {
		if _, state := c.begin(k); state != cacheInFlight {
			t.Errorf("in-flight key %d = %s after finishing an uninstalled key, want cacheInFlight: ADR 0002 §7 says finish does nothing for a key that was never installed", i, testStateName(state))
		}
	}
}

// TestReplyCacheFinishWithoutBeginDoesNothing covers §7: "finish completes
// only an entry that exists and is in flight, and does nothing for a key that
// was never installed."
func TestReplyCacheFinishWithoutBeginDoesNothing(t *testing.T) {
	c := newReplyCache(16, time.Minute)
	k := testBaseKey()

	c.finish(k, testReplyFor(k))

	if n := c.len(); n != 0 {
		t.Errorf("len() = %d after finish on a key that was never begun, want 0: ADR 0002 §7 says finish completes only an entry that exists and is in flight", n)
	}

	reply, state := c.begin(k)
	if state != cacheMiss {
		t.Errorf("begin after a finish with no matching begin = %s, want cacheMiss: ADR 0002 §7 says finish does nothing for a key that was never installed", testStateName(state))
	}
	if reply != nil {
		t.Errorf("begin after a finish with no matching begin replayed %q, want no bytes (ADR 0002 §7)", reply)
	}
}

// TestReplyCacheConcurrentBeginFinishAbandon covers §6's invariant under
// concurrency: between a begin that reports cacheMiss and its matching finish
// or abandon, every other begin for that key must report cacheInFlight or
// cacheHit, never a second miss. The suite runs under -race, so this also
// pins that the cache is safe for concurrent use by the per-connection
// handler goroutines that call it.
func TestReplyCacheConcurrentBeginFinishAbandon(t *testing.T) {
	const (
		workers    = 16
		iterations = 300
		keySpace   = 4
		maxEntries = 8 // > the most entries that can be in flight at once
	)

	c := newReplyCache(maxEntries, time.Minute)

	keys := make([]replyKey, keySpace)
	for i := range keys {
		keys[i] = replyKey{conn: uint64(i + 1), xid: uint32(100 + i), prog: 100003, vers: 3, proc: 12}
	}

	var mu sync.Mutex
	owned := make(map[replyKey]bool)
	var problems []string

	report := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(problems) < 8 {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				k := keys[(w+i)%keySpace]
				want := testReplyFor(k)

				reply, state := c.begin(k)
				switch state {
				case cacheMiss:
					mu.Lock()
					if owned[k] {
						mu.Unlock()
						report("two callers observed cacheMiss for key %+v without an intervening finish or abandon: ADR 0002 §6 makes the lookup that misses install the in-flight marker, so exactly one caller may own a key at a time", k)
					} else {
						owned[k] = true
						mu.Unlock()
					}

					// Clear ownership while the entry is still installed: after
					// finish the entry is done and after abandon it is absent,
					// and in either case a later miss is a new, legitimate
					// ownership.
					mu.Lock()
					delete(owned, k)
					mu.Unlock()

					if i%2 == 0 {
						c.finish(k, testReplyFor(k))
					} else {
						c.abandon(k)
					}
				case cacheInFlight:
					if reply != nil {
						report("cacheInFlight for key %+v carried %d reply bytes, want none: there is no reply to replay until finish stores one (ADR 0002 §6)", k, len(reply))
					}
				case cacheHit:
					if !bytes.Equal(reply, want) {
						report("cacheHit for key %+v replayed %q, want %q: ADR 0002 §5 requires byte-identical replay of the stored record", k, reply, want)
					}
				default:
					report("begin for key %+v returned %s, which is not one of the three states ADR 0002 §8 defines", k, testStateName(state))
				}
			}
		}(w)
	}
	wg.Wait()

	for _, p := range problems {
		t.Error(p)
	}
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d after concurrent use, want at most %d: ADR 0002 §7 keeps the entry bound absolute", n, maxEntries)
	}
}

// TestReplyCacheEvictionOrderFollowsFinishNotBegin covers §7's eviction
// bullet as amended: the queue is in finish order as the cache observed it,
// not in begin order. The amendment resolves "FIFO by insertion time" to the
// finish that stores the reply and names the reading it rules out — ordered
// by begin — so the discriminating case is two calls that complete in the
// reverse of the order they started.
func TestReplyCacheEvictionOrderFollowsFinishNotBegin(t *testing.T) {
	const maxEntries = 2

	c := newReplyCache(maxEntries, time.Minute)

	keyA := replyKey{conn: 1, xid: 1, prog: 100003, vers: 3, proc: 12}
	keyB := replyKey{conn: 1, xid: 2, prog: 100003, vers: 3, proc: 12}
	keyC := replyKey{conn: 1, xid: 3, prog: 100003, vers: 3, proc: 12}

	if _, state := c.begin(keyA); state != cacheMiss {
		t.Fatalf("begin(A) = %s, want cacheMiss", testStateName(state))
	}
	if _, state := c.begin(keyB); state != cacheMiss {
		t.Fatalf("begin(B) = %s, want cacheMiss", testStateName(state))
	}
	if n := c.len(); n > maxEntries {
		t.Fatalf("len() = %d with A and B in flight, want at most %d (ADR 0002 §7)", n, maxEntries)
	}

	// Completion order is the reverse of begin order: B finishes first, so B
	// is the head of the done queue under §7 and A is behind it.
	c.finish(keyB, testReplyFor(keyB))
	c.finish(keyA, testReplyFor(keyA))
	if n := c.len(); n > maxEntries {
		t.Fatalf("len() = %d with A and B done, want at most %d (ADR 0002 §7)", n, maxEntries)
	}

	// The cache is at capacity with two done entries, so this begin must evict
	// the head of the done queue before installing its own marker.
	if _, state := c.begin(keyC); state != cacheMiss {
		t.Fatalf("begin(C) = %s, want cacheMiss: C is a key the cache has never seen", testStateName(state))
	}
	if n := c.len(); n > maxEntries {
		t.Fatalf("len() = %d after begin(C) evicted and installed, want at most %d: ADR 0002 §7 makes the entry bound absolute", n, maxEntries)
	}

	// Check the survivor first: a hit stores nothing and so cannot perturb what
	// follows, whereas the miss below installs a marker and may itself evict.
	got, state := c.begin(keyA)
	if state != cacheHit {
		t.Errorf("A = %s after C evicted one entry, want cacheHit: A finished last of the two, so ADR 0002 §7's queue — ordered by finish — puts B at the head and evicts B. Evicting A here would mean the implementation ordered its queue by begin, the reading §7 explicitly rules out", testStateName(state))
	} else if want := testReplyFor(keyA); !bytes.Equal(got, want) {
		t.Errorf("replayed reply for A = %q, want %q (ADR 0002 §5)", got, want)
	}
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d after a cacheHit, want at most %d: a lookup that hits stores nothing (ADR 0002 §7)", n, maxEntries)
	}

	if _, state := c.begin(keyB); state != cacheMiss {
		t.Errorf("B = %s after C evicted one entry, want cacheMiss: B finished first, so ADR 0002 §7's queue — ordered by finish, not by begin — puts B at the head and it is the entry C displaces", testStateName(state))
	}
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d at the end of the test, want at most %d (ADR 0002 §7)", n, maxEntries)
	}

	// Both keys were left in flight by the two lookups that missed; §6 requires
	// exactly one of finish or abandon for each.
	c.abandon(keyB)
	c.abandon(keyC)
}

// TestReplyCacheHitDoesNotMoveEntryInQueue covers §7's "a lookup refreshes
// nothing. This is a FIFO, not an LRU": a cacheHit does not move an entry in
// the eviction queue, so the entry that has been done longest is still the
// one the next insertion displaces.
func TestReplyCacheHitDoesNotMoveEntryInQueue(t *testing.T) {
	const maxEntries = 2

	c := newReplyCache(maxEntries, time.Minute)

	keyA := replyKey{conn: 1, xid: 1, prog: 100003, vers: 3, proc: 12}
	keyB := replyKey{conn: 1, xid: 2, prog: 100003, vers: 3, proc: 12}
	keyC := replyKey{conn: 1, xid: 3, prog: 100003, vers: 3, proc: 12}

	if _, state := c.begin(keyA); state != cacheMiss {
		t.Fatalf("begin(A) = %s, want cacheMiss", testStateName(state))
	}
	c.finish(keyA, testReplyFor(keyA))
	if _, state := c.begin(keyB); state != cacheMiss {
		t.Fatalf("begin(B) = %s, want cacheMiss", testStateName(state))
	}
	c.finish(keyB, testReplyFor(keyB))
	if n := c.len(); n > maxEntries {
		t.Fatalf("len() = %d with A and B done, want at most %d (ADR 0002 §7)", n, maxEntries)
	}

	// The lookup that must not refresh anything. A finished first, so it is at
	// the head of the queue; an LRU would move it to the back here.
	got, state := c.begin(keyA)
	if state != cacheHit {
		t.Fatalf("begin(A) = %s, want cacheHit: A is done and well inside the age bound", testStateName(state))
	}
	if want := testReplyFor(keyA); !bytes.Equal(got, want) {
		t.Fatalf("replayed reply for A = %q, want %q (ADR 0002 §5)", got, want)
	}

	// At capacity with two done entries, so this begin evicts the head.
	if _, state := c.begin(keyC); state != cacheMiss {
		t.Fatalf("begin(C) = %s, want cacheMiss: C is a key the cache has never seen", testStateName(state))
	}
	if n := c.len(); n > maxEntries {
		t.Fatalf("len() = %d after begin(C) evicted and installed, want at most %d (ADR 0002 §7)", n, maxEntries)
	}

	// Check the survivor first: a hit stores nothing and so cannot perturb what
	// follows.
	got, state = c.begin(keyB)
	if state != cacheHit {
		t.Errorf("B = %s after C evicted one entry, want cacheHit: ADR 0002 §7 says a lookup refreshes nothing, so the hit on A did not move A behind B in the queue. An LRU would have evicted B here, which is exactly what §7 rules out with \"this is a FIFO, not an LRU\"", testStateName(state))
	} else if want := testReplyFor(keyB); !bytes.Equal(got, want) {
		t.Errorf("replayed reply for B = %q, want %q (ADR 0002 §5)", got, want)
	}
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d after a cacheHit, want at most %d: a lookup that hits stores nothing (ADR 0002 §7)", n, maxEntries)
	}

	if _, state := c.begin(keyA); state != cacheMiss {
		t.Errorf("A = %s after C evicted one entry, want cacheMiss: ADR 0002 §7 says a cacheHit does not move an entry in the queue, so A — which finished first and was only looked up since — is still the head and is the entry C displaces", testStateName(state))
	}
	if n := c.len(); n > maxEntries {
		t.Errorf("len() = %d at the end of the test, want at most %d (ADR 0002 §7)", n, maxEntries)
	}

	// Both keys were left in flight by the two lookups that missed; §6 requires
	// exactly one of finish or abandon for each.
	c.abandon(keyA)
	c.abandon(keyC)
}

// TestReplyCacheHitDoesNotExtendAge covers the age half of §7's "a lookup
// refreshes nothing": a cacheHit does not restamp an entry, so the entry
// still expires maxAge after the finish that stored it. §7's reason is that
// refreshing on hit would let a client keep one entry alive indefinitely by
// retransmitting it, which is what makes Assumption 6 tolerable.
//
// The margins here are chosen, not guessed. The test has to observe the cache
// in the window between maxAge after finish (when a FIFO entry is gone) and
// maxAge after the hit (when an LRU entry would also be gone), and both edges
// of that window are wall-clock deadlines — exactly the shape that goes flaky
// on a loaded machine under -race. So no assertion in this test depends on a
// sleep or a call completing *before* a deadline:
//
//   - Every sleep only has to overshoot, which is the direction time.Sleep
//     guarantees. Each one sleeps until an absolute instant derived from the
//     timestamps actually taken around finish, so a late wakeup never shortens
//     the next phase.
//   - Each timing precondition is then re-checked against measured timestamps
//     rather than assumed from the sleep. The lookup that must hit demands a
//     hit only if the measured upper bound on the entry's age is still inside
//     maxAge; if the machine stalled past it, the test skips instead of
//     failing on a scheduling artefact.
//   - The final assertion — expired, so cacheMiss — is safe unconditionally,
//     because a reply replayed more than maxAge after its finish is wrong
//     whether the implementation refreshed on hit or simply never expires. It
//     is measured from the timestamp taken *after* finish, which underestimates
//     the entry's true age, so the precondition cannot be claimed falsely.
//   - Only a *pass* can be inconclusive: if the stall was long enough that an
//     LRU implementation would have expired the entry too, the test proves
//     nothing and says so by skipping rather than reporting a green it has not
//     earned.
//
// maxAge is 500ms with the hit at ~250ms and the final lookup ~350ms after
// that: a quarter of a second of slack on each edge, which is generous for a
// cache lookup, and the guards above turn any larger stall into a skip.
func TestReplyCacheHitDoesNotExtendAge(t *testing.T) {
	const maxAge = 500 * time.Millisecond

	c := newReplyCache(16, maxAge)
	k := testBaseKey()
	want := testReplyFor(k)

	if _, state := c.begin(k); state != cacheMiss {
		t.Fatalf("first begin = %s, want cacheMiss", testStateName(state))
	}

	// finish stamps the entry somewhere between these two readings, so
	// beforeFinish bounds its age from above and afterFinish from below.
	beforeFinish := time.Now()
	c.finish(k, want)
	afterFinish := time.Now()

	time.Sleep(time.Until(afterFinish.Add(maxAge / 2)))

	beforeHit := time.Now()
	got, state := c.begin(k)
	afterHit := time.Now()

	if state != cacheHit {
		if age := afterHit.Sub(beforeFinish); age >= maxAge {
			t.Skipf("the mid-point lookup landed %v after finish with a %v bound, so the entry may legitimately have expired before it: this machine is too loaded to observe whether a hit refreshes an entry's age (ADR 0002 §7)", age, maxAge)
		}
		t.Fatalf("begin %v after finish = %s, want cacheHit: the entry is inside the %v age bound", afterHit.Sub(beforeFinish), testStateName(state), maxAge)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replayed reply = %q, want %q (ADR 0002 §5)", got, want)
	}

	time.Sleep(time.Until(afterFinish.Add(maxAge + maxAge/5)))

	beforeFinal := time.Now()
	reply, state := c.begin(k)
	afterFinal := time.Now()

	// Measured from afterFinish, so this understates the entry's true age: if
	// this much has passed, at least this much has passed since the stamp.
	age := beforeFinal.Sub(afterFinish)
	if age <= maxAge {
		t.Fatalf("the final lookup ran only %v after finish with a %v bound, so the test could not observe expiry at all: this is a bug in the test's timing, not in the cache", age, maxAge)
	}

	if state != cacheMiss {
		t.Errorf("begin %v after finish with a %v age bound = %s, want cacheMiss: ADR 0002 §7 says a lookup refreshes nothing, so the cacheHit taken midway through the bound did not restamp the entry and it expires maxAge after the finish that stored it, not maxAge after the last time it was read", age, maxAge, testStateName(state))
	}
	if reply != nil {
		t.Errorf("an entry %v past its %v bound replayed %q; ADR 0002 §7 requires it to be gone, and a hit does not extend its life", age, maxAge, reply)
	}

	// A pass only means something if an LRU would still have been holding the
	// entry at the moment of the final lookup.
	if state == cacheMiss {
		if since := afterFinal.Sub(beforeHit); since >= maxAge {
			t.Skipf("the final lookup ran %v after the mid-point hit, which is past the %v bound, so an implementation that restamped on hit would have expired the entry too: this run proves nothing either way (ADR 0002 §7)", since, maxAge)
		}
	}

	// The lookup above missed, so it owns an in-flight entry; §6 requires
	// exactly one of finish or abandon for it.
	c.abandon(k)
}

// TestReplyCacheProductionBounds covers §7's two numbers and §8's naming of
// them: the ADR names replyCacheMaxEntries and replyCacheMaxAge so that a test
// written from it can fail if either is quietly changed, "otherwise the ADR
// pins two values that nothing can check". This is that tripwire and nothing
// more — §8 makes what NewServer does with the constants a review item rather
// than something a test asserts.
func TestReplyCacheProductionBounds(t *testing.T) {
	if replyCacheMaxEntries != 4096 {
		t.Errorf("replyCacheMaxEntries = %d, want 4096: ADR 0002 §7 fixes the entry bound at 4096 and §8 names the constant so a change to it cannot pass unnoticed. Changing the number requires amending the ADR, not the test", replyCacheMaxEntries)
	}
	if replyCacheMaxAge != 120*time.Second {
		t.Errorf("replyCacheMaxAge = %v, want %v: ADR 0002 §7 fixes retention at 120 s to outlast a client's retransmission window, and §8 names the constant so a change to it cannot pass unnoticed. Changing the number requires amending the ADR, not the test", replyCacheMaxAge, 120*time.Second)
	}
}
