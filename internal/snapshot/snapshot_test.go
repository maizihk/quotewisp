package snapshot

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func built(t *testing.T) *Snapshot {
	t.Helper()
	b := NewBuilder(7, time.Time{})
	for _, c := range []CategoryRow{{"b", "Bee", 0}, {"a", "Aye", 0}, {"empty", "Empty", 2}} {
		if e := b.AddCategory(c); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []SentenceRow{{2, "00000000-0000-0000-0000-000000000002", "xx", "a", "", "", 2}, {1, "00000000-0000-0000-0000-000000000001", "x", "a", "", "", 1}, {3, "00000000-0000-0000-0000-000000000003", "z", "b", "", "", 1}} {
		if e := b.AddSentence(s); e != nil {
			t.Fatal(e)
		}
	}
	v, e := b.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestBuildAndSelectBoundaries(t *testing.T) {
	s := built(t)
	if s.Categories[0].Code != "a" || s.ByCategory["a"][0].ID != 1 {
		t.Fatal("indexes not sorted")
	}
	want := []uint64{1, 2, 3}
	for off, id := range want {
		x, e := s.Select([]string{"a", "a", "b"}, 0, 2, func(n uint64) uint64 {
			if n != 3 {
				t.Fatalf("total=%d", n)
			}
			return uint64(off)
		})
		if e != nil || x.ID != id {
			t.Fatalf("offset %d got %#v %v", off, x, e)
		}
	}
	if _, e := s.Select([]string{"empty"}, 0, 100, nil); !errors.Is(e, ErrNotFound) {
		t.Fatalf("got %v", e)
	}
	if _, e := s.Select([]string{"missing"}, 0, 100, nil); !errors.Is(e, ErrUnknownCategory) {
		t.Fatalf("got %v", e)
	}
}
func TestBuilderValidation(t *testing.T) {
	b := NewBuilder(1, time.Time{})
	if _, e := NewBuilder(0, time.Time{}).Build(context.Background()); e == nil {
		t.Fatal("accepted zero version")
	}
	if e := b.AddCategory(CategoryRow{"a", "A", 0}); e != nil {
		t.Fatal(e)
	}
	if e := b.AddSentence(SentenceRow{1, "00000000-0000-0000-0000-00000000000A", "x", "a", "", "", 1}); e == nil {
		t.Fatal("accepted uppercase uuid")
	}
	if e := b.AddSentence(SentenceRow{1, "00000000-0000-0000-0000-000000000001", "x", "a", "", "", 2}); e == nil {
		t.Fatal("accepted bad length")
	}
}

type fakeLoader struct {
	mu      sync.Mutex
	version uint64
	loads   int
	wait    chan struct{}
	err     error
}

func (f *fakeLoader) Version(context.Context) (uint64, error) { return f.version, nil }
func (f *fakeLoader) Load(ctx context.Context) (*Snapshot, error) {
	f.mu.Lock()
	f.loads++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.wait != nil {
		select {
		case <-f.wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s := builtForVersion(f.version)
	return s, nil
}
func builtForVersion(v uint64) *Snapshot {
	b := NewBuilder(v, time.Time{})
	_ = b.AddCategory(CategoryRow{"a", "A", 0})
	_ = b.AddSentence(SentenceRow{1, "00000000-0000-0000-0000-000000000001", "x", "a", "", "", 1})
	s, _ := b.Build(context.Background())
	return s
}
func TestManagerAtomicRefresh(t *testing.T) {
	f := &fakeLoader{version: 1}
	m := NewManager(f, time.Second)
	if e := m.LoadInitial(context.Background()); e != nil {
		t.Fatal(e)
	}
	if changed, e := m.Refresh(context.Background(), false); e != nil || changed {
		t.Fatalf("unchanged: %v %v", changed, e)
	}
	f.version = 2
	block := make(chan struct{})
	f.wait = block
	done := make(chan error, 1)
	if !m.TryStartRefresh(context.Background(), true, func(_ bool, e error) { done <- e }) {
		t.Fatal("start rejected")
	}
	if m.TryStartRefresh(context.Background(), true, nil) {
		t.Fatal("concurrent start accepted")
	}
	close(block)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if m.Current().Version != 2 {
		t.Fatal("snapshot not published")
	}
}

func TestManagerFailureCancellationAndLowerVersion(t *testing.T) {
	f := &fakeLoader{version: 5}
	m := NewManager(f, 20*time.Millisecond)
	if e := m.LoadInitial(context.Background()); e != nil {
		t.Fatal(e)
	}
	old := m.Current()
	f.version = 4
	f.err = errors.New("broken")
	if _, e := m.Refresh(context.Background(), false); e == nil {
		t.Fatal("lower version did not trigger load")
	}
	if m.Current() != old {
		t.Fatal("failure replaced snapshot")
	}
	f.err = nil
	f.wait = make(chan struct{})
	if _, e := m.Refresh(context.Background(), true); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("timeout=%v", e)
	}
	if m.Current() != old {
		t.Fatal("timeout replaced snapshot")
	}
	f.wait = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := m.Refresh(ctx, true); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel=%v", e)
	}
	if m.Current() != old {
		t.Fatal("cancel replaced snapshot")
	}
}

// Empty datasets are valid loaded snapshots; failed loads are a separate state.
type emptyLoader struct {
	value *Snapshot
	err   error
}

func (l *emptyLoader) Version(context.Context) (uint64, error) { return l.value.Version, nil }
func (l *emptyLoader) Load(context.Context) (*Snapshot, error) { return l.value, l.err }

func TestEmptyDatasetReadinessAndRefresh(t *testing.T) {
	ctx := context.Background()
	empty, err := NewBuilder(8, time.Time{}).Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if empty.SentenceCount != 0 || len(empty.Categories) != 0 || empty.LoadedAt.IsZero() {
		t.Fatalf("invalid empty snapshot: %+v", empty)
	}
	if _, err := empty.Select(nil, 0, 1000, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty random: %v", err)
	}
	loader := &emptyLoader{value: empty}
	manager := NewManager(loader, time.Second)
	if manager.Ready() {
		t.Fatal("unloaded manager ready")
	}
	if err := manager.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	if !manager.Ready() || manager.Current() != empty {
		t.Fatal("empty snapshot not ready")
	}
	loader.value = builtForVersion(9)
	if changed, err := manager.Refresh(ctx, false); !changed || err != nil {
		t.Fatalf("populate: %v %v", changed, err)
	}
	empty.Version = 10
	loader.value = empty
	if changed, err := manager.Refresh(ctx, false); !changed || err != nil {
		t.Fatalf("delete all: %v %v", changed, err)
	}
	loader.err = errors.New("database unavailable")
	if _, err := manager.Refresh(ctx, true); err == nil {
		t.Fatal("failed reload accepted")
	}
	if !manager.Ready() || manager.Current() != empty {
		t.Fatal("failed reload discarded empty snapshot")
	}
	loader.err = nil
	loader.value = &Snapshot{Version: 0}
	if _, err := manager.Refresh(ctx, true); err == nil {
		t.Fatal("zero version accepted")
	}
	if manager.Current() != empty {
		t.Fatal("invalid reload discarded snapshot")
	}
}
