package cache

import (
	"strings"
	"sync"
	"time"
)

type Options struct {
	StaleTime  time.Duration
	GCTime     time.Duration
	GCInterval time.Duration
}

func (o Options) withDefaults() Options {
	if o.StaleTime == 0 {
		o.StaleTime = 30 * time.Second
	}
	if o.GCTime == 0 {
		o.GCTime = 5 * time.Minute
	}
	if o.GCInterval == 0 {
		o.GCInterval = 1 * time.Minute
	}
	return o
}

type entry struct {
	data         any
	err          error
	fetchedAt    time.Time
	lastAccessed time.Time
	fetching     bool
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]*entry
	opts    Options
}

func New(opts Options) *Cache {
	return &Cache{
		entries: make(map[string]*entry),
		opts:    opts.withDefaults(),
	}
}

// GCInterval returns the configured GC interval for use with tick commands.
func (c *Cache) GCInterval() time.Duration {
	return c.opts.GCInterval
}

// Query checks the cache for key and returns cached data if available.
// It returns the data, whether it was found, whether it's stale, and a
// refetch function (nil if data is fresh or a fetch is already in-flight).
func Query[T any](c *Cache, key string, fetchFn func() (T, error)) (data T, found bool, stale bool, refetchFn func() (T, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	e, ok := c.entries[key]

	if !ok {
		// Cache miss — create a placeholder entry marked as fetching.
		c.entries[key] = &entry{fetching: true, lastAccessed: now}
		refetchFn = makeRefetchFn(c, key, fetchFn)
		return data, false, false, refetchFn
	}

	e.lastAccessed = now

	if e.fetching && e.fetchedAt.IsZero() {
		// Initial fetch still in-flight, no data yet.
		return data, false, false, nil
	}

	// We have data (possibly with an error).
	if e.err != nil {
		data = zeroVal[T]()
	} else {
		data = e.data.(T)
	}

	fresh := now.Sub(e.fetchedAt) < c.opts.StaleTime
	if fresh {
		return data, true, false, nil
	}

	// Stale — offer a refetch if one isn't already running.
	if !e.fetching {
		e.fetching = true
		refetchFn = makeRefetchFn(c, key, fetchFn)
	}
	return data, true, true, refetchFn
}

func makeRefetchFn[T any](c *Cache, key string, fetchFn func() (T, error)) func() (T, error) {
	return func() (T, error) {
		result, err := fetchFn()

		c.mu.Lock()
		defer c.mu.Unlock()

		now := time.Now()
		e, ok := c.entries[key]
		if !ok {
			e = &entry{}
			c.entries[key] = e
		}
		if err != nil {
			e.err = err
			e.data = nil
		} else {
			e.data = result
			e.err = nil
		}
		e.fetchedAt = now
		e.lastAccessed = now
		e.fetching = false

		return result, err
	}
}

// Set manually populates a cache entry.
func Set[T any](c *Cache, key string, data T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.entries[key] = &entry{
		data:         data,
		fetchedAt:    now,
		lastAccessed: now,
	}
}

// Invalidate removes a specific key from the cache.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// InvalidatePrefix removes all keys that start with the given prefix.
func (c *Cache) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
}

// GC removes entries that haven't been accessed within the GC time window.
func (c *Cache) GC() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, e := range c.entries {
		if now.Sub(e.lastAccessed) > c.opts.GCTime {
			delete(c.entries, key)
		}
	}
}

func zeroVal[T any]() T {
	var zero T
	return zero
}
