package sunrpc

// The duplicate request cache: a short-term memory of the replies sent for
// non-idempotent calls, so that a retransmission is answered with the bytes
// the original produced instead of being executed a second time.
//
// Spec: docs/adr/0002-duplicate-request-cache.md §2 (the key), §5 (the cached
// artefact), §6 (the three states), §7 (bounds, purge order, eviction) and §8
// (this API).

import (
	"sync"
	"time"
)

// The production bounds. Both are dictated by client retransmission behaviour
// rather than by the host, which is why neither is a flag (ADR 0002 §7).
const (
	replyCacheMaxEntries = 4096
	replyCacheMaxAge     = 120 * time.Second
)

// replyKey identifies one call. conn is a server-assigned serial rather than
// the peer address because an address and port recur once a connection is
// gone, and an entry must only ever be returned to the connection that
// created it.
type replyKey struct {
	conn             uint64 // serial of the connection the call arrived on
	xid              uint32
	prog, vers, proc uint32
}

type cacheState int

const (
	cacheMiss     cacheState = iota // nothing to replay: execute the call
	cacheInFlight                   // an identical call is still executing
	cacheHit                        // reply holds the bytes sent for the original call
)

// doneRef is a done entry's place in the eviction queue.
type doneRef struct {
	key replyKey
	at  time.Time // when finish stored the reply; carries a monotonic reading
}

type replyCache struct {
	maxEntries int
	maxAge     time.Duration

	mu sync.Mutex
	// entries maps a key to its stored reply, or to nil while the call is in
	// flight. A stored reply is never empty, because finish refuses to store
	// one, so nil is unambiguous.
	entries map[replyKey][]byte
	// done holds every done entry and nothing else, oldest finish first. Only
	// the head is ever removed — abandon never touches a done entry and
	// finish never restamps one — so a slice used as a queue suffices, and
	// the expired entries are always a prefix of it.
	done []doneRef
}

func newReplyCache(maxEntries int, maxAge time.Duration) *replyCache {
	return &replyCache{
		maxEntries: maxEntries,
		maxAge:     maxAge,
		entries:    make(map[replyKey][]byte),
	}
}

// begin reports what the cache knows about k, and reserves k for execution
// when it knows nothing about it.
//
// On cacheMiss the caller must execute the call and afterwards call exactly
// one of finish or abandon for k. begin normally installs an in-flight marker
// for k, so that a duplicate arriving before the call completes is reported as
// cacheInFlight; when the cache has no room for one it reports cacheMiss
// having installed nothing (§7). The caller cannot tell those two apart and
// does not need to: finish and abandon are no-ops for a key with no entry, so
// the obligation — and the defer that discharges it — is the same either way.
//
// reply is non-nil only for cacheHit. It aliases the stored bytes and must not
// be modified.
func (c *replyCache) begin(k replyKey) (reply []byte, state cacheState) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Purge before classifying, or an expired entry would be replayed.
	now := time.Now()
	for len(c.done) > 0 && now.Sub(c.done[0].at) > c.maxAge {
		c.popDone()
	}

	// A lookup refreshes nothing: this is a FIFO, not an LRU, so that 120 s
	// stays a ceiling on an entry's life rather than a window a
	// retransmitting client can keep sliding.
	if stored, ok := c.entries[k]; ok {
		if stored != nil {
			return stored, cacheHit
		}
		return nil, cacheInFlight
	}

	// In-flight markers are never evicted: dropping one would let the next
	// duplicate install a second marker and re-execute the call. With no done
	// entry left to evict, the call runs uncached rather than breaking the
	// bound.
	for len(c.entries) >= c.maxEntries && len(c.done) > 0 {
		c.popDone()
	}
	if len(c.entries) < c.maxEntries {
		c.entries[k] = nil
	}
	return nil, cacheMiss
}

// finish completes an in-flight entry for k with reply, which must be the
// complete encoded reply record and must not be modified afterwards. It does
// nothing if k has no entry or if k's entry is already done.
//
// An empty reply — len(reply) == 0, nil or not — is never stored: finish(k,
// reply) for such a reply is exactly abandon(k). An in-flight entry for k is
// removed, so a later begin(k) reports cacheMiss and not cacheInFlight, and a
// done entry is left alone, because abandon leaves one alone.
func (c *replyCache) finish(k replyKey, reply []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(reply) == 0 {
		c.abandonLocked(k)
		return
	}
	stored, ok := c.entries[k]
	if !ok || stored != nil {
		return
	}
	// The clock starts here, not at begin: a slow call is the one most likely
	// to be retransmitted, and must not get a shortened entry for being slow.
	c.entries[k] = reply
	c.done = append(c.done, doneRef{key: k, at: time.Now()})
}

// abandon removes an in-flight entry for k. It does nothing if k has no entry
// or if k's entry is already done.
func (c *replyCache) abandon(k replyKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.abandonLocked(k)
}

func (c *replyCache) abandonLocked(k replyKey) {
	if stored, ok := c.entries[k]; ok && stored == nil {
		delete(c.entries, k)
	}
}

// len reports the number of entries held, in flight and done alike, including
// any that have expired but have not yet been purged (§7). It is for tests and
// diagnostics; no dispatch path calls it.
func (c *replyCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// popDone removes the head of the done queue and its entry. c.mu must be held.
func (c *replyCache) popDone() {
	delete(c.entries, c.done[0].key)
	c.done[0] = doneRef{}
	c.done = c.done[1:]
}
