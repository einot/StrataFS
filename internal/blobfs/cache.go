package blobfs

import (
	"container/list"
	"sync"
)

// chunkCache is a byte-bounded LRU over immutable chunk contents.
//
// Chunks are content-addressed, so a cached entry can never go stale: the key
// is the hash of the value. That makes caching safe without any invalidation
// protocol, which is one of the quieter benefits of content addressing.
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

func (c *chunkCache) put(hash string, data []byte) {
	if int64(len(data)) > c.maxBytes {
		return // a single chunk larger than the whole budget is not worth evicting everything for
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[hash]; ok {
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&cacheEntry{hash: hash, data: data})
	c.items[hash] = el
	c.curBytes += int64(len(data))
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
