package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter enforces a sliding-window request limit per key and periodically
// evicts expired buckets to prevent unbounded memory growth.
type rateLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	nextCleanup time.Time
	now         func() time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		attempts: make(map[string][]time.Time),
		now:      time.Now,
	}
}

func (l *rateLimiter) allow(key string, limit int) bool {
	allowed, _ := l.allowWindow(key, limit, time.Minute)
	return allowed
}

func (l *rateLimiter) allowWindow(key string, limit int, window time.Duration) (bool, int) {
	if limit <= 0 || window <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	nowFn := l.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	cutoff := now.Add(-window)
	if l.attempts == nil {
		l.attempts = make(map[string][]time.Time)
	}
	if !now.Before(l.nextCleanup) {
		for k, times := range l.attempts {
			if len(times) == 0 || !times[len(times)-1].After(cutoff) {
				delete(l.attempts, k)
			}
		}
		l.nextCleanup = now.Add(window)
	}

	times := l.attempts[key]
	for len(times) > 0 && !times[0].After(cutoff) {
		times = times[1:]
	}
	if len(times) == 0 {
		delete(l.attempts, key)
	} else {
		l.attempts[key] = times
	}

	if len(times) >= limit {
		retryAfter := int(math.Ceil(times[0].Add(window).Sub(now).Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
		return false, retryAfter
	}

	l.attempts[key] = append(times, now)
	return true, 0
}

// allowUserAndIPRate enforces reusable per-user and per-IP sliding-window limits
// for profile updates and future upload endpoints, writing 429 + Retry-After when exceeded.
func (a *API) allowUserAndIPRate(w http.ResponseWriter, r *http.Request, limiter *rateLimiter, scope, userID string, userLimit, ipLimit int, window time.Duration) bool {
	if limiter == nil {
		return true
	}
	if allowed, retryAfter := limiter.allowWindow(scope+":user:"+userID, userLimit, window); !allowed {
		rateLimitedWithRetry(w, retryAfter)
		return false
	}
	if allowed, retryAfter := limiter.allowWindow(scope+":ip:"+a.clientIP(r), ipLimit, window); !allowed {
		rateLimitedWithRetry(w, retryAfter)
		return false
	}
	return true
}

func rateLimitedWithRetry(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, retryAfter)))
	fail(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again later.")
}
