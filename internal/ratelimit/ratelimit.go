// Package ratelimit implements a small in-memory sliding-window limiter.
//
// GitHost is a single-process application, so an in-process limiter is
// the honest design: it protects login and token endpoints against
// online guessing without external services. Restarting the process
// resets counters — acceptable for this threat (documented in the threat
// model: SEC-003).
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows at most max events per key inside the window.
type Limiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
	max    int
	window time.Duration
}

// New builds a limiter.
func New(max int, window time.Duration) *Limiter {
	return &Limiter{events: make(map[string][]time.Time), max: max, window: window}
}

// Allow records an event for key and reports whether it fits the budget.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-l.window)
	list := l.events[key]

	// Drop old entries.
	kept := list[:0]
	for _, t := range list {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.events[key] = kept
		return false
	}
	l.events[key] = append(kept, now)
	return true
}

// Reset forgets a key's events (call after a successful authentication,
// so a valid user is never locked out by earlier failures).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.events, key)
}

// Prune drops stale keys entirely (periodic housekeeping).
func (l *Limiter) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-l.window)
	for key, list := range l.events {
		if len(list) == 0 || !list[len(list)-1].After(cutoff) {
			delete(l.events, key)
		}
	}
}
