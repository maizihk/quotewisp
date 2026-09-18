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
	}
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
	c.building = true
	c.mu.Unlock()
	go c.runBuildLoop()
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
		c.dirty = false
		c.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), c.buildTimeout)
		data, err := c.buildOnce(ctx)
		cancel()

		if err != nil {
			c.metrics.ExportFailed()
			c.logFailure(err)
		} else {
			c.current.Store(data)
			c.metrics.ExportBuilt(data.Version, len(data.ExportJSON))
			c.logSuccess(data)
		}

		c.mu.Lock()
		if c.dirty {
			c.mu.Unlock()
			continue
		}
		c.building = false
		c.mu.Unlock()
		return
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
