package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sentence-api/internal/config"
	"sentence-api/internal/database"
	"sentence-api/internal/httpapi"
	"sentence-api/internal/importer"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
)

var version = "dev"
var gitCommit = "unknown"
var buildTime = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		logger, loggerErr := observability.NewJSONLogger(os.Stderr, "info")
		if loggerErr != nil {
			panic(loggerErr)
		}
		logger.Error("command_failed", "category", safeCategory(err))
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "import":
			return runImport(args[1:])
		case "migrate":
			return runMigrate(args[1:])
		default:
			return errors.New("unknown command")
		}
	}
	return runService()
}

func pool(c config.Config) database.PoolConfig {
	return database.PoolConfig{MaxOpenConns: c.MySQLMaxOpenConns, MaxIdleConns: c.MySQLMaxIdleConns, ConnMaxLifetime: c.MySQLConnMaxLifetime}
}

func runImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "JSON import file")
	dry := fs.Bool("dry-run", false, "validate without writes")
	if err := fs.Parse(args); err != nil {
		return errors.New("invalid import arguments")
	}
	if *file == "" || fs.NArg() != 0 {
		return errors.New("import requires --file")
	}
	c, err := config.Load(config.ModeImport)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.ImportTimeout)
	defer cancel()
	f, err := os.Open(*file)
	if err != nil {
		return errors.New("open import file")
	}
	data, err := importer.Parse(ctx, f)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return errors.New("close import file")
	}
	db, err := database.Open(ctx, c.MYSQLDSN, pool(c))
	if err != nil {
		return err
	}
	defer db.Close()
	if err = database.CheckSchema(ctx, db); err != nil {
		return err
	}
	sum, err := importer.Run(ctx, db, data, *dry)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(sum); err != nil {
		return errors.New("write import summary")
	}
	return nil
}

func runMigrate(args []string) error {
	if len(args) == 0 {
		return errors.New("migrate requires up or down")
	}
	c, err := config.Load(config.ModeMigrate)
	if err != nil {
		return err
	}
	switch args[0] {
	case "up":
		if len(args) != 1 {
			return errors.New("migrate up accepts no arguments")
		}
		return database.MigrateUp(c.MYSQLDSN)
	case "down":
		fs := flag.NewFlagSet("migrate down", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		steps := fs.Int("steps", 0, "positive migration count")
		if err = fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *steps <= 0 {
			return errors.New("migrate down requires positive --steps")
		}
		return database.MigrateDown(c.MYSQLDSN, *steps)
	default:
		return errors.New("migrate requires up or down")
	}
}

func runService() error {
	c, err := config.Load(config.ModeService)
	if err != nil {
		return err
	}
	logger, err := observability.NewJSONLogger(os.Stderr, c.LogLevel.String())
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	life, stopLife := context.WithCancel(context.Background())
	defer stopLife()
	startCtx, cancel := context.WithTimeout(life, c.SnapshotLoadTimeout)
	db, err := database.Open(startCtx, c.MYSQLDSN, pool(c))
	if err == nil {
		err = database.CheckSchema(startCtx, db)
	}
	if err != nil {
		cancel()
		if db != nil {
			db.Close()
		}
		return err
	}
	metrics := observability.NewMetrics()
	loader := observedLoader{inner: database.Loader{DB: db}, metrics: metrics, life: life}
	manager := snapshot.NewManager(loader, c.SnapshotLoadTimeout)
	initialStart := time.Now()
	metrics.SetRefreshInProgress(true)
	err = manager.LoadInitial(startCtx)
	metrics.SetRefreshInProgress(false)
	cancel()
	if err != nil {
		metrics.ObserveRefresh("failure", time.Since(initialStart))
		logger.Error("snapshot_initial_load", "result", "failure", "error_category", operationCategory(err), "duration_ms", float64(time.Since(initialStart).Microseconds())/1000)
		db.Close()
		return errors.New("initial snapshot load failed")
	}
	metrics.ObserveRefresh("success", time.Since(initialStart))
	setSnapshotMetrics(metrics, manager.Current())
	initial := manager.Current()
	logger.Info("snapshot_initial_load", "result", "success", "version", strconv.FormatUint(initial.Version, 10), "sentences", initial.SentenceCount, "categories", len(initial.Categories), "text_bytes", initial.TextBytes, "estimated_memory_bytes", estimateSnapshotBytes(initial), "duration_ms", float64(time.Since(initialStart).Microseconds())/1000)
	var stopping atomic.Bool
	var bg sync.WaitGroup
	controller := &refreshController{life: life, loader: loader, manager: manager, metrics: metrics, logger: logger, loadTimeout: c.SnapshotLoadTimeout, stopping: &stopping, bg: &bg}
	startReload := func(_ context.Context, force bool, cb func(bool, error)) bool { return controller.start(force, cb) }
	h := httpapi.New(httpapi.Options{Snapshots: manager, StartReload: startReload, LifecycleContext: life, Metrics: metrics, Logger: logger, TrustedProxies: c.TrustedProxyCIDRs, CORSOrigins: c.CORSAllowedOrigins, ReloadToken: c.ReloadToken, Build: httpapi.BuildInfo{Version: version, GitCommit: gitCommit, BuildTime: buildTime}, IsStopping: stopping.Load})
	srv := &http.Server{Addr: c.HTTPAddr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	controller.runPoll(c.SnapshotPollInterval)
	serveErr := make(chan error, 1)
	go func() {
		e := srv.ListenAndServe()
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		serveErr <- e
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	var listenErr error
	select {
	case e := <-serveErr:
		listenErr = e
	case <-signals:
	}
	controller.startMu.Lock()
	stopping.Store(true)
	controller.startMu.Unlock()
	deadline, cancelShutdown := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancelShutdown()
	stopLife()
	result := shutdownAll(deadline, srv, &bg, db.Close)
	if listenErr != nil {
		return listenErr
	}
	if result.http != nil {
		if errors.Is(result.http, context.DeadlineExceeded) {
			return errors.New("shutdown timeout")
		}
		return errors.New("HTTP shutdown failed")
	}
	return result.db
}

func setSnapshotMetrics(m *observability.Metrics, s *snapshot.Snapshot) {
	if s != nil {
		m.SetSnapshot(s.Version, s.SentenceCount, uint64(len(s.Categories)), s.TextBytes, s.LoadedAt)
	}
}
func safeCategory(err error) string {
	var e *importer.Error
	if errors.As(err, &e) {
		return e.Category
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "configuration-or-runtime"
}
