package preview

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCacheDedupesConcurrentLoads(t *testing.T) {
	c := NewCache(8)
	var loads atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := c.Get("k", func() ([]byte, error) {
				loads.Add(1)
				return []byte("payload"), nil
			})
			if err != nil || string(data) != "payload" {
				t.Errorf("Get: %q, %v", data, err)
			}
		}()
	}
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Errorf("loader ran %d times, want 1", n)
	}
}

func TestCacheEvictsOldest(t *testing.T) {
	c := NewCache(2)
	var loads atomic.Int32
	get := func(k string) {
		c.Get(k, func() ([]byte, error) { loads.Add(1); return []byte(k), nil })
	}
	get("a")
	get("b")
	get("a") // touch a: now b is oldest
	get("c") // evicts b
	get("a") // still cached
	if n := loads.Load(); n != 3 {
		t.Fatalf("loads=%d, want 3 (a, b, c)", n)
	}
	get("b") // was evicted: reloads
	if n := loads.Load(); n != 4 {
		t.Errorf("loads=%d, want 4 after re-fetching evicted key", n)
	}
}

func TestCacheDoesNotCacheErrors(t *testing.T) {
	c := NewCache(4)
	calls := 0
	load := func() ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("card hiccup")
		}
		return []byte("ok"), nil
	}
	if _, err := c.Get("k", load); err == nil {
		t.Fatal("first Get should surface the load error")
	}
	data, err := c.Get("k", load)
	if err != nil || string(data) != "ok" {
		t.Fatalf("retry after error: %q, %v", data, err)
	}
	if calls != 2 {
		t.Errorf("loader ran %d times, want 2 (errors are not cached)", calls)
	}
}
