package security

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// PaymentRateLimiter implements token bucket rate limiting for payment endpoints.
type PaymentRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int           // tokens per window
	window   time.Duration // window duration
	maxBurst int           // maximum burst size
}

type bucket struct {
	tokens    int
	lastReset time.Time
}

func NewPaymentRateLimiter(rate int, window time.Duration) *PaymentRateLimiter {
	return &PaymentRateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		window:   window,
		maxBurst: rate * 2, // allow 2x burst
	}
}

// Allow checks if a request from the given key (IP, API key, etc.) is allowed.
func (r *PaymentRateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, exists := r.buckets[key]
	now := time.Now()

	if !exists || now.Sub(b.lastReset) > r.window {
		r.buckets[key] = &bucket{
			tokens:    r.rate - 1, // consume one token
			lastReset: now,
		}
		return true
	}

	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
}

// GetRemaining returns the remaining tokens for a key.
func (r *PaymentRateLimiter) GetRemaining(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	b, exists := r.buckets[key]
	if !exists {
		return r.rate
	}
	return b.tokens
}

// Middleware returns an HTTP middleware that enforces rate limiting.
func (r *PaymentRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		key := req.RemoteAddr
		if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
			key = forwarded
		}

		if !r.Allow(key) {
			w.Header().Set("Retry-After", "60")
			w.Header().Set("X-RateLimit-Limit", "0")
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		remaining := r.GetRemaining(key)
		w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", r.rate))
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))

		next.ServeHTTP(w, req)
	})
}

// Cleanup removes expired buckets periodically.
func (r *PaymentRateLimiter) Cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		now := time.Now()
		for key, b := range r.buckets {
			if now.Sub(b.lastReset) > r.window*2 {
				delete(r.buckets, key)
			}
		}
		r.mu.Unlock()
	}
}
