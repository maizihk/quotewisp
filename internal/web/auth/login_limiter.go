package auth

import (
	"sync"
	"time"
)

const defaultMaxPasswordChecks = 2

type loginEntry struct {
	failures []time.Time
	inflight int
	lastFail time.Time
}

// LoginLimiter tracks failed login attempts per IP and username.
type LoginLimiter struct {
	mu        sync.Mutex
	perIP     int
	perUser   int
	window    time.Duration
	maxKeys   int
	ips       map[string]*loginEntry
	users     map[string]*loginEntry
	verifySem chan struct{}
}

// NewLoginLimiter returns a sliding-window failure counter for login abuse.
func NewLoginLimiter(perIP, perUser int, window time.Duration, maxKeys int) *LoginLimiter {
	return &LoginLimiter{
		perIP:     perIP,
		perUser:   perUser,
		window:    window,
		maxKeys:   maxKeys,
		ips:       make(map[string]*loginEntry),
		users:     make(map[string]*loginEntry),
		verifySem: make(chan struct{}, defaultMaxPasswordChecks),
	}
}

// Reserve occupies one failure-budget slot before expensive password work.
func (l *LoginLimiter) Reserve(ip, username string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.occupied(l.ips[ip], now) >= l.perIP || l.occupied(l.users[username], now) >= l.perUser {
		return false
	}
	l.addInflight(l.ips, ip, 1)
	l.addInflight(l.users, username, 1)
	return true
}

// Release frees a reservation that did not become a recorded failure.
func (l *LoginLimiter) Release(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addInflight(l.ips, ip, -1)
	l.addInflight(l.users, username, -1)
}

// Failure records a failed attempt and consumes the matching reservation.
func (l *LoginLimiter) Failure(ip, username string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addInflight(l.ips, ip, -1)
	l.addInflight(l.users, username, -1)
	l.record(l.ips, ip, now)
	l.record(l.users, username, now)
}

// Success clears failure history for username and frees both reservations.
func (l *LoginLimiter) Success(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addInflight(l.ips, ip, -1)
	l.addInflight(l.users, username, -1)
	delete(l.users, username)
}

// TryAcquireVerify takes one process-wide password-check slot without waiting.
func (l *LoginLimiter) TryAcquireVerify() bool {
	select {
	case l.verifySem <- struct{}{}:
		return true
	default:
		return false
	}
}

// ReleaseVerify returns a password-check slot.
func (l *LoginLimiter) ReleaseVerify() {
	select {
	case <-l.verifySem:
	default:
	}
}

func (l *LoginLimiter) occupied(e *loginEntry, now time.Time) int {
	if e == nil {
		return 0
	}
	return l.count(e, now) + e.inflight
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

func (l *LoginLimiter) addInflight(m map[string]*loginEntry, key string, delta int) {
	e := m[key]
	if e == nil {
		if delta <= 0 {
			return
		}
		if len(m) >= l.maxKeys {
			l.evictStalest(m)
		}
		e = &loginEntry{failures: make([]time.Time, 0, l.perIP+l.perUser)}
		m[key] = e
	}
	e.inflight += delta
	if e.inflight < 0 {
		e.inflight = 0
	}
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
		if e.inflight > 0 {
			continue
		}
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
