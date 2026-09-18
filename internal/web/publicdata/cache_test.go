package publicdata_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sentence-api/internal/web/publicdata"
	"sentence-api/internal/web/store"
)

type fakeSource struct {
	mu       sync.Mutex
	version  uint64
	builds   int
	block    chan struct{}
	buildErr error
}

func (f *fakeSource) BuildPublicData(ctx context.Context) (*store.PublicData, error) {
	f.mu.Lock()
	f.builds++
	n := f.builds
	block := f.block
	err := f.buildErr
	version := f.version
	f.mu.Unlock()

	if block != nil && n >= 2 {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return &store.PublicData{
		Version:    version,
		ExportJSON: []byte(`{"categories":[],"sentences":[]}`),
		BuiltAt:    time.Now().UTC(),
	}, nil
}

func (f *fakeSource) DatasetVersion(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.version, nil
}

func (f *fakeSource) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds
}

func TestLoadInitialFailure(t *testing.T) {
	src := &fakeSource{version: 1, buildErr: errors.New("boom")}
	cache := publicdata.New(src, nil, nil, time.Second)
	if err := cache.LoadInitial(context.Background()); err == nil {
		t.Fatal("expected initial load failure")
	}
	if cache.Current() != nil {
		t.Fatal("expected nil current before successful load")
	}
}

func TestRefreshCoalescing(t *testing.T) {
	block := make(chan struct{})
	src := &fakeSource{version: 1, block: block}
	cache := publicdata.New(src, nil, nil, 5*time.Second)
	if err := cache.LoadInitial(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.buildCount() != 1 {
		t.Fatalf("expected 1 initial build, got %d", src.buildCount())
	}

	src.mu.Lock()
	src.version = 2
	src.mu.Unlock()

	cache.Refresh()
	waitForBuilds(t, src, 2)

	for i := 0; i < 10; i++ {
		cache.Refresh()
	}

	close(block)
	waitForBuilds(t, src, 3)

	time.Sleep(100 * time.Millisecond)
	if got := src.buildCount(); got != 3 {
		t.Fatalf("expected 3 total builds (initial + 2 coalesced refresh builds), got %d", got)
	}
}

func TestPollTriggersOnVersionChangeOnly(t *testing.T) {
	src := &fakeSource{version: 1}
	cache := publicdata.New(src, nil, nil, time.Second)
	if err := cache.LoadInitial(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	cache.RunPoll(ctx, 20*time.Millisecond, &wg)

	time.Sleep(80 * time.Millisecond)
	if got := src.buildCount(); got != 1 {
		t.Fatalf("poll with unchanged version should not build, got %d builds", got)
	}

	src.mu.Lock()
	src.version = 2
	src.mu.Unlock()

	waitForBuilds(t, src, 2)
	cancel()
	wg.Wait()
}

func TestRefreshCoalescingRace(t *testing.T) {
	block := make(chan struct{})
	src := &fakeSource{version: 1, block: block}
	cache := publicdata.New(src, nil, nil, 5*time.Second)
	if err := cache.LoadInitial(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.mu.Lock()
	src.version = 2
	src.mu.Unlock()

	var started sync.WaitGroup
	started.Add(1)
	go func() {
		cache.Refresh()
		started.Done()
	}()
	started.Wait()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.Refresh()
		}()
	}
	wg.Wait()

	close(block)
	waitForBuilds(t, src, 3)
}

func TestRefreshKeepsPreviousOnFailure(t *testing.T) {
	src := &fakeSource{version: 1}
	cache := publicdata.New(src, nil, nil, time.Second)
	if err := cache.LoadInitial(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := cache.Current()

	src.mu.Lock()
	src.buildErr = errors.New("db down")
	src.version = 2
	src.mu.Unlock()

	cache.Refresh()
	waitForBuilds(t, src, 2)
	time.Sleep(50 * time.Millisecond)

	after := cache.Current()
	if after != before || after.Version != 1 {
		t.Fatal("failed refresh should keep previous snapshot")
	}
}

func TestConcurrentCurrentRace(t *testing.T) {
	src := &fakeSource{version: 1}
	cache := publicdata.New(src, nil, nil, time.Second)
	if err := cache.LoadInitial(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cur := cache.Current()
				if cur == nil || cur.Version == 0 {
					t.Error("invalid current snapshot")
				}
			}
		}()
	}
	src.mu.Lock()
	src.version = 2
	src.mu.Unlock()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.Refresh()
		}()
	}
	wg.Wait()
}

func waitForBuilds(t *testing.T, src *fakeSource, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for src.buildCount() < want {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d builds, got %d", want, src.buildCount())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
