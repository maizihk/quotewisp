package publicdata

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"sentence-api/internal/web/store"
)

// Source loads public-facing dataset snapshots.
type Source interface {
	BuildPublicData(context.Context) (*store.PublicData, error)
	DatasetVersion(context.Context) (uint64, error)
}

// Metrics records export build outcomes. Nil is a no-op.
type Metrics interface {
	ExportBuilt(version uint64, bytes int)
	ExportFailed()
}

type noopMetrics struct{}

func (noopMetrics) ExportBuilt(uint64, int) {}
func (noopMetrics) ExportFailed()           {}

// Cache holds an atomically switched public data snapshot.
type Cache struct {
	src          Source
	logger       *slog.Logger
	metrics      Metrics
	buildTimeout time.Duration

	current atomic.Pointer[store.PublicData]

	mu       sync.Mutex
	building bool
	dirty    bool
	life     context.Context
	wg       *sync.WaitGroup
}

// New returns a cache that must be initialized with LoadInitial before serving.
func New(src Source, logger *slog.Logger, metrics Metrics, buildTimeout time.Duration) *Cache {
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &Cache{
		src:          src,
		logger:       logger,
		metrics:      metrics,
		buildTimeout: buildTimeout,
		life:         context.Background(),
	}
}

// Bind attaches refresh work to a service lifetime. Call before Refresh or RunPoll.
func (c *Cache) Bind(ctx context.Context, wg *sync.WaitGroup) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	c.life = ctx
	c.wg = wg
	c.mu.Unlock()
}

// LoadInitial builds the first snapshot and must succeed before serving.
func (c *Cache) LoadInitial(ctx context.Context) error {
	data, err := c.buildOnce(ctx)
	if err != nil {
		return err
	}
	c.current.Store(data)
	c.logSuccess(data)
	c.metrics.ExportBuilt(data.Version, len(data.ExportJSON))
	return nil
}

// Current returns the active snapshot. Never nil after LoadInitial.
func (c *Cache) Current() *store.PublicData {
	return c.current.Load()
}

// Refresh starts a non-blocking rebuild. Concurrent calls coalesce to at most one follow-up build.
func (c *Cache) Refresh() {
	c.mu.Lock()
	if c.building {
		c.dirty = true
		c.mu.Unlock()
		return
	}
	if c.life != nil && c.life.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.building = true
	wg := c.wg
	c.mu.Unlock()
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		c.runBuildLoop()
	}()
}

// RunPoll checks dataset version on interval and refreshes when it changes.
func (c *Cache) RunPoll(ctx context.Context, interval time.Duration, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cur := c.Current()
				if cur == nil {
					continue
				}
				vctx, cancel := context.WithTimeout(ctx, c.buildTimeout)
				version, err := c.src.DatasetVersion(vctx)
				cancel()
				if err != nil {
					continue
				}
				if version != cur.Version {
					c.Refresh()
				}
			}
		}
	}()
}

func (c *Cache) runBuildLoop() {
	for {
		c.mu.Lock()
		parent := c.life
		c.mu.Unlock()
		if parent == nil {
			parent = context.Background()
		}
		if parent.Err() != nil {
			c.mu.Lock()
			c.building = false
			c.dirty = false
			c.mu.Unlock()
			return
		}

		ctx, cancel := context.WithTimeout(parent, c.buildTimeout)
		data, err := c.buildOnce(ctx)
		cancel()

		if err != nil {
			// Database helpers may replace context cancellation with a generic read
			// error. The service lifetime is therefore the authoritative signal for
			// an expected shutdown cancellation. Build deadlines and other failures
			// still count and log normally while the service remains alive.
			if parent.Err() == nil {
				c.metrics.ExportFailed()
				c.logFailure(err)
			}
		} else {
			c.current.Store(data)
			c.metrics.ExportBuilt(data.Version, len(data.ExportJSON))
			c.logSuccess(data)
		}

		c.mu.Lock()
		stopping := c.life != nil && c.life.Err() != nil
		if stopping || !c.dirty {
			c.building = false
			c.dirty = false
			c.mu.Unlock()
			return
		}
		c.dirty = false
		c.mu.Unlock()
	}
}

func (c *Cache) buildOnce(ctx context.Context) (*store.PublicData, error) {
	if c.buildTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.buildTimeout)
		defer cancel()
	}
	return c.src.BuildPublicData(ctx)
}

func (c *Cache) logSuccess(data *store.PublicData) {
	c.logger.Info("public_data_refresh",
		"result", "success",
		"version", strconv.FormatUint(data.Version, 10),
		"sentences", sentenceCount(data),
		"bytes", len(data.ExportJSON),
	)
}

func (c *Cache) logFailure(err error) {
	c.logger.Error("public_data_refresh",
		"result", "failure",
		"error_category", errorCategory(err),
	)
}

func errorCategory(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "database"
}

func sentenceCount(data *store.PublicData) int {
	if data == nil {
		return 0
	}
	var total int
	for _, cat := range data.Categories {
		total += int(cat.Count)
	}
	if total > 0 {
		return total
	}
	var doc struct {
		Sentences []json.RawMessage `json:"sentences"`
	}
	if json.Unmarshal(data.ExportJSON, &doc) == nil {
		return len(doc.Sentences)
	}
	return 0
}
