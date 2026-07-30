package preview

import "sync"

// Cache is a small LRU keyed by file path, with in-flight deduplication:
// concurrent Gets for the same key run the loader once and share the result.
// Failed loads are not cached, so a flaky SD read can be retried.
type Cache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*entry
	order    []string // LRU order, oldest first
}

type entry struct {
	done chan struct{}
	data []byte
	err  error
}

func NewCache(capacity int) *Cache {
	return &Cache{capacity: capacity, entries: map[string]*entry{}}
}

// Get returns the cached bytes for key, calling load to fill on a miss.
func (c *Cache) Get(key string, load func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.touchLocked(key)
		c.mu.Unlock()
		<-e.done
		return e.data, e.err
	}
	e := &entry{done: make(chan struct{})}
	c.entries[key] = e
	c.order = append(c.order, key)
	c.evictLocked()
	c.mu.Unlock()

	e.data, e.err = load()
	close(e.done)
	if e.err != nil {
		c.mu.Lock()
		if c.entries[key] == e {
			c.removeLocked(key)
		}
		c.mu.Unlock()
	}
	return e.data, e.err
}

func (c *Cache) touchLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(append(c.order[:i], c.order[i+1:]...), key)
			return
		}
	}
}

func (c *Cache) removeLocked(key string) {
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// evictLocked drops the oldest completed entries over capacity. In-flight
// loads are never evicted; the cache may briefly overshoot instead.
func (c *Cache) evictLocked() {
	for len(c.order) > c.capacity {
		victim := ""
		for _, k := range c.order {
			select {
			case <-c.entries[k].done:
				victim = k
			default:
			}
			if victim != "" {
				break
			}
		}
		if victim == "" {
			return
		}
		c.removeLocked(victim)
	}
}
