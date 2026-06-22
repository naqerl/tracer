package ratelimit

import (
	"net/http"
	"sync"
	"time"
)

// TokenBucket implements a simple token bucket rate limiter per key.
type TokenBucket struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int
	burst    int
	interval time.Duration
}

type bucket struct {
	tokens    int
	lastCheck time.Time
}

// NewTokenBucket creates a new token bucket rate limiter.
// rate: number of requests allowed per interval per key.
// burst: maximum burst size.
// interval: the time window for rate calculation.
func NewTokenBucket(rate int, burst int, interval time.Duration) *TokenBucket {
	return &TokenBucket{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		burst:    burst,
		interval: interval,
	}
}

func (tb *TokenBucket) Allow(key string) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	b, exists := tb.buckets[key]
	if !exists {
		tb.buckets[key] = &bucket{
			tokens:    tb.burst - 1,
			lastCheck: time.Now(),
		}
		return tb.burst > 0
	}

	// Refill tokens based on elapsed time
	now := time.Now()
	elapsed := now.Sub(b.lastCheck)
	tokensToAdd := int(elapsed / tb.interval * time.Duration(tb.rate))
	if tokensToAdd > 0 {
		b.tokens += tokensToAdd
		if b.tokens > tb.burst {
			b.tokens = tb.burst
		}
		b.lastCheck = now
	}

	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
}

// Middleware returns an HTTP middleware that rate limits by a given key function.
func (tb *TokenBucket) Middleware(keyFn func(r *http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if !tb.Allow(key) {
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LimitOf returns a key function that rate limits by a combination of IP and request path.
func LimitOf(limitBy string) func(r *http.Request) string {
	return func(r *http.Request) string {
		ip := r.RemoteAddr
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			ip = forwarded
		}
		return ip + ":" + limitBy
	}
}
