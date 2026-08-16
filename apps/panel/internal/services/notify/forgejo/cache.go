package forgejo

import (
	"sync"
	"time"
)

// Cache remembers recently reported message hashes so the notifier does not
// have to wait for Forgejo's search index to catch up after creating an
// issue. Forgejo stays the long-term source of truth; the cache only bridges
// the short index-lag window and doubles as an in-flight guard for concurrent
// identical alerts.
type Cache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]time.Time
}

// NewCache creates an empty cache whose entries expire after ttl. A zero or
// negative ttl disables caching.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl: ttl,
		m:   make(map[string]time.Time),
	}
}

// Remember marks the key as reported until now+ttl.
func (c *Cache) Remember(baseURL, owner, repo, hash string) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(baseURL, owner, repo, hash)] = time.Now().Add(c.ttl)
	c.sweepLocked()
}

// Check reports whether the key was remembered and has not expired yet.
func (c *Cache) Check(baseURL, owner, repo, hash string) bool {
	if c.ttl <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.m[c.key(baseURL, owner, repo, hash)]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(c.m, c.key(baseURL, owner, repo, hash))
		return false
	}
	return true
}

// Forget removes a remembered key, used when the issue creation failed so the
// next alert retries instead of being swallowed by a stale entry.
func (c *Cache) Forget(baseURL, owner, repo, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, c.key(baseURL, owner, repo, hash))
}

// key namespaces the cache by instance, repo, and message hash so a shared
// cache can serve multiple forgejo notifiers without collisions.
func (c *Cache) key(baseURL, owner, repo, hash string) string {
	return baseURL + "|" + owner + "/" + repo + "|" + hash
}

// sweepLocked drops expired entries once the cache grows past a threshold so
// long-running panels do not accumulate dead keys. Callers hold mu.
func (c *Cache) sweepLocked() {
	if len(c.m) < 256 {
		return
	}
	now := time.Now()
	for k, expiry := range c.m {
		if now.After(expiry) {
			delete(c.m, k)
		}
	}
}
