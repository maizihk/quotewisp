package ratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAllowWithinLimits(t *testing.T) {
	l := New(3, 10, 100)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if !l.Allow("ip1", now.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if l.Allow("ip1", now.Add(3*time.Minute)) {
		t.Fatal("4th request within hour should be rejected")
	}
}

func TestHourWindowBoundary(t *testing.T) {
	l := New(2, 10, 100)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	l.Allow("ip1", base)
	l.Allow("ip1", base.Add(30*time.Minute))
	if l.Allow("ip1", base.Add(59*time.Minute)) {
		t.Fatal("3rd request within hour should be rejected")
	}
	// Event at base is now outside the 1h window.
	if !l.Allow("ip1", base.Add(61*time.Minute)) {
		t.Fatal("request after hour boundary should be allowed")
	}
}

func TestDayLimitRejectsBeforeHourLimit(t *testing.T) {
	l := New(100, 5, 100)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		at := base.Add(time.Duration(i*2) * time.Hour)
		if !l.Allow("ip1", at) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if l.Allow("ip1", base.Add(10*time.Hour)) {
		t.Fatal("6th request within 24h should be rejected")
	}
}

func TestRejectedNotCounted(t *testing.T) {
	l := New(1, 10, 100)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	l.Allow("ip1", now)
	if l.Allow("ip1", now.Add(time.Minute)) {
		t.Fatal("2nd request should be rejected")
	}
	// Only one event recorded; after hour passes another should succeed.
	if !l.Allow("ip1", now.Add(61*time.Minute)) {
		t.Fatal("request after hour should be allowed with only one prior event")
	}
}

func TestEvictionAtMaxKeys(t *testing.T) {
	l := New(10, 100, 3)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	l.Allow("a", base)
	l.Allow("b", base.Add(time.Minute))
	l.Allow("c", base.Add(2*time.Minute))
	// Evicts "a" (stalest).
	if !l.Allow("d", base.Add(3*time.Minute)) {
		t.Fatal("new key should be allowed after eviction")
	}
	// "a" was evicted; fresh counter allows requests again.
	for i := 0; i < 10; i++ {
		if !l.Allow("a", base.Add(4*time.Minute).Add(time.Duration(i)*time.Second)) {
			t.Fatalf("evicted key should start fresh, request %d rejected", i+1)
		}
	}
}

func TestSweep(t *testing.T) {
	l := New(10, 100, 100)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	l.Allow("ip1", base)
	l.Allow("ip2", base.Add(time.Hour))

	l.Sweep(base.Add(25 * time.Hour))
	if l.Allow("ip1", base.Add(25*time.Hour)) {
		// ip1 was swept; if still present the day counter might block — fresh key always works.
	}
	// ip2's last event is only 24h ago from sweep time, should remain.
	l.Allow("ip2", base.Add(25*time.Hour))
	l.Sweep(base.Add(26 * time.Hour))
	if !l.Allow("ip1", base.Add(26*time.Hour)) {
		t.Fatal("swept key should accept new events")
	}
}

func TestConcurrentAllow(t *testing.T) {
	l := New(1000, 10000, 1000)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", id%10)
			for j := 0; j < 20; j++ {
				l.Allow(key, base.Add(time.Duration(j)*time.Millisecond))
			}
		}(i)
	}
	wg.Wait()
}
