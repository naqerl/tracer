package forgejo

import (
	"sync"
	"testing"
	"time"
)

func TestCacheRememberAndCheck(t *testing.T) {
	c := NewCache(time.Minute)
	if c.Check("https://x", "o", "r", "hash1") {
		t.Fatal("expected miss before remember")
	}

	c.Remember("https://x", "o", "r", "hash1")
	if !c.Check("https://x", "o", "r", "hash1") {
		t.Fatal("expected hit after remember")
	}
}

func TestCacheExpires(t *testing.T) {
	c := NewCache(20 * time.Millisecond)
	c.Remember("https://x", "o", "r", "hash1")
	time.Sleep(40 * time.Millisecond)
	if c.Check("https://x", "o", "r", "hash1") {
		t.Fatal("expected miss after ttl expiry")
	}
}

func TestCacheKeyIsolation(t *testing.T) {
	c := NewCache(time.Minute)
	c.Remember("https://a", "o1", "r1", "hash1")

	// Same hash, different instance/repo → must not collide.
	for _, tc := range []struct {
		base, owner, repo string
	}{
		{"https://a", "o1", "r2"},
		{"https://a", "o2", "r1"},
		{"https://b", "o1", "r1"},
	} {
		if c.Check(tc.base, tc.owner, tc.repo, "hash1") {
			t.Errorf("cache key collided for %s %s/%s", tc.base, tc.owner, tc.repo)
		}
	}
	if !c.Check("https://a", "o1", "r1", "hash1") {
		t.Fatal("expected the remembered key to still hit")
	}
}

func TestCacheForget(t *testing.T) {
	c := NewCache(time.Minute)
	c.Remember("https://x", "o", "r", "hash1")
	c.Forget("https://x", "o", "r", "hash1")
	if c.Check("https://x", "o", "r", "hash1") {
		t.Fatal("expected miss after forget")
	}
}

func TestCacheZeroTTLDisabled(t *testing.T) {
	c := NewCache(0)
	c.Remember("https://x", "o", "r", "hash1")
	if c.Check("https://x", "o", "r", "hash1") {
		t.Fatal("zero ttl must disable caching")
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache(time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			hash := "h" + string(rune('a'+n%3))
			c.Remember("https://x", "o", "r", hash)
			_ = c.Check("https://x", "o", "r", hash)
			c.Forget("https://x", "o", "r", hash)
		}(i)
	}
	wg.Wait()
}

func TestCacheSweepBounded(t *testing.T) {
	c := NewCache(10 * time.Millisecond)
	for i := 0; i < 300; i++ {
		c.Remember("https://x", "o", "r", "h"+string(rune(i)))
	}
	time.Sleep(20 * time.Millisecond)
	// A remember after growth triggers a sweep of expired entries.
	c.Remember("https://x", "o", "r", "fresh")
	if len(c.m) >= 300 {
		t.Fatalf("expected expired entries to be swept, cache has %d", len(c.m))
	}
	if !c.Check("https://x", "o", "r", "fresh") {
		t.Fatal("expected the fresh entry to survive the sweep")
	}
}
