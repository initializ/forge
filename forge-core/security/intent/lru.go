package intent

import (
	"container/list"
	"sync"
)

// lruCache is a tiny fixed-size LRU keyed by string, valued by
// []float32 (embedding vectors). The alignment engine uses it to
// dedupe action-side embeddings across repeated tool calls with the
// same description+args payload.
//
// Not exported — no other package in Forge needs a byte-vector LRU
// today. If a second caller appears, promote to a proper util.
type lruCache struct {
	mu    sync.Mutex
	cap   int
	list  *list.List
	items map[string]*list.Element
}

type lruItem struct {
	key   string
	value []float32
}

func newLRUCache(capacity int) *lruCache {
	if capacity <= 0 {
		return nil // caller treats nil as "no cache"
	}
	return &lruCache{
		cap:   capacity,
		list:  list.New(),
		items: make(map[string]*list.Element, capacity),
	}
}

// Get returns the cached vector for key, marking it as most-recently
// used.
func (c *lruCache) Get(key string) ([]float32, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.list.MoveToFront(el)
	return el.Value.(*lruItem).value, true
}

// Put inserts key→value, evicting the least-recently-used entry
// when at capacity.
func (c *lruCache) Put(key string, value []float32) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*lruItem).value = value
		c.list.MoveToFront(el)
		return
	}
	if c.list.Len() >= c.cap {
		tail := c.list.Back()
		if tail != nil {
			c.list.Remove(tail)
			delete(c.items, tail.Value.(*lruItem).key)
		}
	}
	el := c.list.PushFront(&lruItem{key: key, value: value})
	c.items[key] = el
}

// Len returns the current entry count.
func (c *lruCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.list.Len()
}
