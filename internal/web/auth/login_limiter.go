package auth

import (
	"sync"
	"time"
)

type loginEntry struct {
	failures []time.Time
	lastFail time.Time
}

// LoginLimiter tracks failed login attempts per IP and username.
type LoginLimiter struct {
	mu      sync.Mutex
	perIP   int
	perUser int
	window  time.Duration
	maxKeys int
	ips     map[string]*loginEntry
	users   map[string]*loginEntry
}

// NewLoginLimiter returns a sliding-window failure counter for login abuse.
func NewLoginLimiter(perIP, perUser int, window time.Duration, maxKeys int) *LoginLimiter {
	return &LoginLimiter{
		perIP:   perIP,
		perUser: perUser,
		window:  window,
		maxKeys: maxKeys,
		ips:     make(map[string]*loginEntry),
		users:   make(map[string]*loginEntry),
	}
}

// Allow reports whether ip and username are under their failure limits without recording.
func (l *LoginLimiter) Allow(ip, username string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count(l.ips[ip], now) < l.perIP && l.count(l.users[username], now) < l.perUser
}

// Failure records a failed attempt for both ip and username.
func (l *LoginLimiter) Failure(ip, username string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.record(l.ips, ip, now)
	l.record(l.users, username, now)
}

// Success clears failure history for username only.
func (l *LoginLimiter) Success(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.users, username)
}

func (l *LoginLimiter) count(e *loginEntry, now time.Time) int {
	if e == nil {
		return 0
	}
	cutoff := now.Add(-l.window)
	n := 0
	for _, t := range e.failures {
		if !t.Before(cutoff) {
			n++
		}
	}
	return n
}

func (l *LoginLimiter) record(m map[string]*loginEntry, key string, now time.Time) {
	e := m[key]
	if e == nil {
		if len(m) >= l.maxKeys {
			l.evictStalest(m)
		}
		e = &loginEntry{failures: make([]time.Time, 0, l.perIP+l.perUser)}
		m[key] = e
	}
	cutoff := now.Add(-l.window)
	pruned := e.failures[:0]
	for _, t := range e.failures {
		if !t.Before(cutoff) {
			pruned = append(pruned, t)
		}
	}
	e.failures = append(pruned, now)
	e.lastFail = now
}

func (l *LoginLimiter) evictStalest(m map[string]*loginEntry) {
	var stalestKey string
	var stalestTime time.Time
	first := true
	for key, e := range m {
		if first || e.lastFail.Before(stalestTime) {
			stalestKey = key
			stalestTime = e.lastFail
			first = false
		}
	}
	if stalestKey != "" {
		delete(m, stalestKey)
	}
}
