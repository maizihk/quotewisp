package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"sentence-api/internal/database"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
)

type versionLoader interface {
	Version(context.Context) (uint64, error)
}
type refreshManager interface {
	Current() *snapshot.Snapshot
	IsReloading() bool
	TryStartRefresh(context.Context, bool, func(bool, error)) bool
}

type refreshController struct {
	life        context.Context
	loader      versionLoader
	manager     refreshManager
	metrics     *observability.Metrics
	logger      *slog.Logger
	loadTimeout time.Duration
	stopping    *atomic.Bool
	bg          *sync.WaitGroup
	startMu     sync.Mutex
}

func (c *refreshController) start(force bool, cb func(bool, error)) bool {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.stopping.Load() || c.life.Err() != nil {
		return false
	}
	c.bg.Add(1)
	started := time.Now()
	ready := make(chan struct{})
	ok := c.manager.TryStartRefresh(c.life, force, func(changed bool, err error) {
		<-ready
		defer c.bg.Done()
		c.metrics.SetRefreshInProgress(false)
		result := "success"
		if err != nil {
			result = "failure"
			if c.life.Err() != nil {
				result = "canceled"
			}
		}
		c.metrics.ObserveRefresh(result, time.Since(started))
		s := c.manager.Current()
		if changed {
			setSnapshotMetrics(c.metrics, s)
		}
		c.logRefresh(result, changed, err, s, time.Since(started))
		if cb != nil {
			cb(changed, err)
		}
	})
	if !ok {
		close(ready)
		c.bg.Done()
		return false
	}
	c.metrics.SetRefreshInProgress(true)
	close(ready)
	return true
}

func (c *refreshController) pollOnce() {
	if c.stopping.Load() || c.life.Err() != nil || c.manager.IsReloading() {
		return
	}
	ctx, cancel := context.WithTimeout(c.life, c.loadTimeout)
	version, err := c.loader.Version(ctx)
	cancel()
	if err != nil {
		if c.life.Err() == nil {
			c.logger.Warn("snapshot_version_check_failed", "category", operationCategory(err))
		}
		return
	}
	current := c.manager.Current()
	if current != nil && version == current.Version {
		return
	}
	if current != nil && version < current.Version {
		c.logger.Warn("snapshot_version_decreased", "current_version", strconv.FormatUint(current.Version, 10), "database_version", strconv.FormatUint(version, 10))
	}
	c.start(true, nil)
}

func (c *refreshController) runPoll(interval time.Duration) {
	c.bg.Add(1)
	go func() {
		defer c.bg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-c.life.Done():
				return
			case <-ticker.C:
				c.pollOnce()
			}
		}
	}()
}

func (c *refreshController) logRefresh(result string, changed bool, err error, s *snapshot.Snapshot, d time.Duration) {
	attrs := []any{"result", result, "changed", changed, "duration_ms", float64(d.Microseconds()) / 1000}
	if s != nil {
		attrs = append(attrs, "version", strconv.FormatUint(s.Version, 10), "sentences", s.SentenceCount, "categories", len(s.Categories), "text_bytes", s.TextBytes, "estimated_memory_bytes", estimateSnapshotBytes(s))
	}
	if err != nil {
		attrs = append(attrs, "error_category", operationCategory(err))
	}
	level := slog.LevelInfo
	if result == "failure" {
		level = slog.LevelError
	}
	c.logger.Log(context.Background(), level, "snapshot_refresh", attrs...)
}

func estimateSnapshotBytes(s *snapshot.Snapshot) uint64 {
	if s == nil {
		return 0
	}
	return s.TextBytes + s.SentenceCount*uint64(unsafe.Sizeof(snapshot.Sentence{})) + uint64(len(s.Categories))*uint64(unsafe.Sizeof(snapshot.Category{}))
}
func operationCategory(err error) string {
	var validation *database.ValidationError
	if errors.As(err, &validation) {
		return "validation"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "database"
}

type observedLoader struct {
	inner   database.Loader
	metrics *observability.Metrics
	life    context.Context
}

func (l observedLoader) Version(ctx context.Context) (uint64, error) {
	v, e := l.inner.Version(ctx)
	recordDatabaseOperation(l.metrics, l.life, e)
	return v, e
}
func (l observedLoader) Load(ctx context.Context) (*snapshot.Snapshot, error) {
	s, e := l.inner.Load(ctx)
	recordDatabaseOperation(l.metrics, l.life, e)
	return s, e
}

func recordDatabaseOperation(m *observability.Metrics, life context.Context, err error) {
	if life.Err() != nil {
		return
	}
	ok := err == nil
	var validation *database.ValidationError
	if errors.As(err, &validation) {
		ok = true
	}
	m.ObserveDatabase(ok, time.Now().UTC())
}

type shutdownResult struct{ http, db error }
type httpShutdowner interface {
	Shutdown(context.Context) error
	Close() error
}

func shutdownAll(ctx context.Context, srv httpShutdowner, bg *sync.WaitGroup, closeDB func() error) shutdownResult {
	var closeOnce sync.Once
	var dbErr error
	closeDatabase := func() error { closeOnce.Do(func() { dbErr = closeDB() }); return dbErr }
	done := make(chan shutdownResult, 1)
	go func() {
		httpErr := srv.Shutdown(ctx)
		bg.Wait()
		done <- shutdownResult{http: httpErr, db: closeDatabase()}
	}()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		_ = srv.Close()
		// A driver Close implementation may block. The shutdown deadline must still
		// bound process teardown, so make the best-effort close without waiting.
		go func() { _ = closeDatabase() }()
		return shutdownResult{http: ctx.Err()}
	}
}

// refreshAndWait shares the regular refresh controller's single-flight and
// lifecycle accounting, while letting an import report its actual outcome.
func (c *refreshController) refreshAndWait(ctx context.Context) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done := make(chan error, 1)
		if c.start(true, func(_ bool, err error) { done <- err }) {
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.life.Done():
			return c.life.Err()
		case <-tick.C:
		}
	}
}
