package blobfs

// The chunk cache: a byte-bounded LRU of chunk contents keyed by hash, which
// owns the bytes it holds and charges exactly what it holds.
//
// Spec: docs/adr/0004-chunk-cache-owns-its-bytes.md §1–§3, §7; docs/DESIGN.md §3

import (
	"container/list"
	"sync"
)

// chunkCache is a byte-bounded LRU over immutable chunk contents.
//
// Chunks are content-addressed, so a cached entry can never go stale: the key
// is the hash of the value. That makes caching safe without any invalidation
// protocol, which is one of the quieter benefits of content addressing.
//
// The premise holds only while an entry's bytes cannot change, because a hit
// is never hashed again. An entry is immutable only if nobody else holds its
// bytes, so put stores a copy of its own rather than the caller's slice: a
// flush that fails partway leaves the buffers it uploaded dirty, and the next
// write changes them in place (ADR 0004 §1, #46). The copy also has cap == len,
// so the charge, len, is what the cache actually holds (§2, #47).
//
// mu is a leaf lock: nothing is acquired and nothing is logged while it is
// held (ADR 0004 §3).
type chunkCache struct {
	mu       sync.Mutex
	maxBytes int64
	curBytes int64
	ll       *list.List
	items    map[string]*list.Element

	hits, misses int64
}

type cacheEntry struct {
	hash string
	data []byte
}

func newChunkCache(maxBytes int64) *chunkCache {
	if maxBytes <= 0 {
		maxBytes = 256 << 20
	}
	return &chunkCache{
		maxBytes: maxBytes,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
	}
}

// get returns the cache's own slice for hash, shared with every caller and
// every later hit (ADR 0004 §3). A caller must not write through it, and must
// not append to it or to any reslice of it: appending to a reslice shorter
// than its capacity writes into the cache's array. A caller that needs to
// change the bytes copies them first.
func (c *chunkCache) get(hash string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[hash]
	if !ok {
		c.misses++
		return nil, false
	}
	c.hits++
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).data, true
}

// put stores a copy of data under hash, so the caller may change, reslice,
// append to or reuse data once put returns (ADR 0004 §1). The copy is made
// with make and copy, which gives cap == len, where bytes.Clone and append
// promise nothing about capacity. It is made after the size guard, so an
// oversized chunk costs nothing, and before taking mu, because truncate reads
// the cache while holding FS.mu and a memcpy under mu would make the whole
// namespace wait on it. A put that finds its hash already present discards
// the copy.
func (c *chunkCache) put(hash string, data []byte) {
	if int64(len(data)) > c.maxBytes {
		return // a single chunk larger than the whole budget is not worth evicting everything for
	}
	own := make([]byte, len(data))
	copy(own, data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[hash]; ok {
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&cacheEntry{hash: hash, data: own})
	c.items[hash] = el
	c.curBytes += int64(len(own))
	for c.curBytes > c.maxBytes {
		back := c.ll.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*cacheEntry)
		c.ll.Remove(back)
		delete(c.items, ent.hash)
		c.curBytes -= int64(len(ent.data))
	}
}

// stats returns hit/miss counters for the status line.
func (c *chunkCache) stats() (hits, misses, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses, c.curBytes
}
