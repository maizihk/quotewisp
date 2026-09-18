package ratelimit

import (
	"sync"
	"time"
)

const (
	hourWindow = time.Hour
	dayWindow  = 24 * time.Hour
)

type keyEntry struct {
	events    []time.Time
	lastEvent time.Time
}

// Limiter is an in-memory sliding-window rate limiter keyed by arbitrary strings.
type Limiter struct {
	mu      sync.Mutex
	perHour int
	perDay  int
	maxKeys int
	keys    map[string]*keyEntry
}

// New returns a limiter with per-hour and per-day caps. maxKeys bounds memory;
// when exceeded the stalest key (oldest last event) is evicted.
func New(perHour, perDay, maxKeys int) *Limiter {
	return &Limiter{
		perHour: perHour,
		perDay:  perDay,
		maxKeys: maxKeys,
		keys:    make(map[string]*keyEntry),
	}
}

// Allow reports whether key is under both limits. On true the event is counted;
// on false the event is rejected and not recorded.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.keys[key]
	if e == nil {
		if len(l.keys) >= l.maxKeys {
			l.evictStalest()
		}
		e = &keyEntry{events: make([]time.Time, 0, l.perDay)}
		l.keys[key] = e
	}

	e.events = pruneOlderThan(e.events, now.Add(-dayWindow))
	hourCutoff := now.Add(-hourWindow)

	hourCount, dayCount := 0, len(e.events)
	for _, t := range e.events {
		if !t.Before(hourCutoff) {
			hourCount++
		}
	}

	if hourCount >= l.perHour || dayCount >= l.perDay {
		return false
	}

	e.events = append(e.events, now)
	e.lastEvent = now
	return true
}

// Sweep removes entries whose newest event is older than 24 hours.
func (l *Limiter) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-dayWindow)
	for key, e := range l.keys {
		if e.lastEvent.Before(cutoff) {
			delete(l.keys, key)
		}
	}
}

func (l *Limiter) evictStalest() {
	var stalestKey string
	var stalestTime time.Time
	first := true
	for key, e := range l.keys {
		if first || e.lastEvent.Before(stalestTime) {
			stalestKey = key
			stalestTime = e.lastEvent
			first = false
		}
	}
	if stalestKey != "" {
		delete(l.keys, stalestKey)
	}
}

func pruneOlderThan(events []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for _, t := range events {
		if !t.Before(cutoff) {
			events[i] = t
			i++
		}
	}
	return events[:i]
}
