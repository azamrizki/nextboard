package auth

import (
	"sync"
	"time"
)

// RateLimiter allows at most Max failures per key within Window.
type RateLimiter struct {
	Max    int
	Window time.Duration

	mu    sync.Mutex
	fails map[string][]time.Time
}

func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	return &RateLimiter{Max: max, Window: window, fails: map[string][]time.Time{}}
}

func (l *RateLimiter) prune(key string, now time.Time) []time.Time {
	list := l.fails[key]
	i := 0
	for i < len(list) && now.Sub(list[i]) > l.Window {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = list
	}
	return list
}

// Blocked reports whether key has exhausted its failure budget.
func (l *RateLimiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, time.Now())) >= l.Max
}

// Fail records a failed attempt for key.
func (l *RateLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.fails[key] = append(l.prune(key, now), now)
}
