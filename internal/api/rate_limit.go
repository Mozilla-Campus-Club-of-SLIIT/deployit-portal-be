package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rateEntry struct {
	windowStart time.Time
	count       int
}

type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateEntry
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		entries: make(map[string]*rateEntry),
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.cleanupExpired(30 * time.Minute)
	}
}

func (rl *RateLimiter) cleanupExpired(ttl time.Duration) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, v := range rl.entries {
		if now.Sub(v.windowStart) > ttl {
			delete(rl.entries, k)
		}
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[0])
			if ip != "" {
				return ip
			}
		}
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

// LimitByIP applies a fixed-window per-IP rate limit and returns HTTP 429 when exceeded.
func (rl *RateLimiter) LimitByIP(next http.HandlerFunc, scope string, limit int, window time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if limit <= 0 || window <= 0 {
			next(w, r)
			return
		}

		now := time.Now()
		key := scope + "|" + clientIP(r)

		rl.mu.Lock()
		entry, ok := rl.entries[key]
		if !ok || now.Sub(entry.windowStart) >= window {
			rl.entries[key] = &rateEntry{windowStart: now, count: 1}
			rl.mu.Unlock()
			next(w, r)
			return
		}

		entry.count++
		if entry.count > limit {
			retryAfter := int(window.Seconds() - now.Sub(entry.windowStart).Seconds())
			if retryAfter < 1 {
				retryAfter = 1
			}
			rl.mu.Unlock()
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			http.Error(w, "Too many requests. Please try again later.", http.StatusTooManyRequests)
			return
		}
		rl.mu.Unlock()

		next(w, r)
	}
}
