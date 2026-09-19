package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sentence-api/internal/config"
	"sentence-api/internal/database"
	"sentence-api/internal/httpmw"
	"sentence-api/internal/observability"
	"sentence-api/internal/web/admin"
	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/public"
	"sentence-api/internal/web/publicdata"
	"sentence-api/internal/web/ratelimit"
	"sentence-api/internal/web/render"
	"sentence-api/internal/web/store"
)

const (
	siteName        = "拾句"
	siteEnglishName = "Quotewisp"
	siteSlogan      = "偶遇一句话。"
)

func runWeb(args []string) error {
	if len(args) > 0 {
		if args[0] == "admin" {
			return runWebAdmin(args[1:])
		}
		return errors.New("unknown web command")
	}
	return runWebServer()
}

func runWebServer() error {
	c, err := config.Load(config.ModeWeb)
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

	startCtx, cancelStart := context.WithTimeout(life, 30*time.Second)
	defer cancelStart()
	db, err := database.Open(startCtx, c.MYSQLDSN, pool(c))
	if err == nil {
		err = database.CheckWriteSchema(startCtx, db)
	}
	if err != nil {
		if db != nil {
			db.Close()
		}
		return err
	}

	st := store.New(db)
	metrics := observability.NewMetrics()
	metrics.EnableWeb()

	settings, err := st.EnsureSettings(startCtx, store.SiteSettings{
		Name:         siteName,
		EnglishName:  siteEnglishName,
		Slogan:       siteSlogan,
		Contact:      c.SiteContact,
		PublicOrigin: c.APIBaseURL,
		RepoURL:      c.SiteRepoURL,
	})
	if err != nil {
		logger.Error("site_settings_seed", "result", "failure", "error_category", operationCategory(err))
		db.Close()
		return errors.New("site settings seed failed")
	}

	cache := publicdata.New(st, logger, webPublicDataMetrics{metrics}, 30*time.Second)
	if err = cache.LoadInitial(startCtx); err != nil {
		logger.Error("public_data_initial_load", "result", "failure", "error_category", operationCategory(err))
		db.Close()
		return errors.New("initial public data load failed")
	}

	site := render.Site{
		Name:         settings.Name,
		EnglishName:  settings.EnglishName,
		Slogan:       settings.Slogan,
		Contact:      settings.Contact,
		RepoURL:      settings.RepoURL,
		PublicOrigin: settings.PublicOrigin,
		BeianText:    settings.BeianText,
		BeianURL:     settings.BeianURL,
		Version:      version,
		AssetRev:     version + "-" + gitCommit + "-" + buildTime,
	}
	renderer, err := render.New(site)
	if err != nil {
		db.Close()
		return err
	}
	tokens := render.NewFormTokens([]byte(c.WebSecretKey))
	limiter := ratelimit.New(c.SubmissionRatePerHour, c.SubmissionRatePerDay, 100000)
	logins := auth.NewLoginLimiter(10, 5, 15*time.Minute, 100000)

	publicHandler, err := public.New(public.Deps{
		Cache:        cache,
		Store:        st,
		Renderer:     renderer,
		Tokens:       tokens,
		Limiter:      limiter,
		Logger:       logger,
		Metrics:      webPublicMetrics{metrics},
		PendingLimit: c.SubmissionPendingLimit,
	})
	if err != nil {
		db.Close()
		return err
	}
	adminHandler, err := admin.New(admin.Deps{
		Store:         st,
		Renderer:      renderer,
		Logger:        logger,
		Metrics:       webAdminMetrics{metrics},
		Logins:        logins,
		Tokens:        tokens,
		CookieSecure:  c.CookieSecure,
		OnChange:      cache.Refresh,
		APIMetricsURL: c.APIMetricsURL,
	})
	if err != nil {
		db.Close()
		return err
	}

	var stopping atomic.Bool
	webCtl := &webController{
		cache:    cache,
		metrics:  metrics,
		stopping: &stopping,
		build: buildInfo{
			Version:   version,
			GitCommit: gitCommit,
			BuildTime: buildTime,
		},
	}

	root := http.NewServeMux()
	root.HandleFunc("/healthz", webCtl.health)
	root.HandleFunc("/readyz", webCtl.ready)
	root.Handle("/metrics", metrics.Handler())
	root.HandleFunc("/version", webCtl.version)
	root.Handle("/admin/", adminHandler)
	root.Handle("/admin", adminHandler)
	root.Handle("/", publicHandler)

	classify := func(r *http.Request) string {
		path := r.URL.Path
		if strings.HasPrefix(path, "/admin") {
			return admin.RouteName(path)
		}
		switch path {
		case "/healthz", "/readyz", "/metrics", "/version":
			return path
		default:
			return public.RouteName(path)
		}
	}
	onPanic := func(w http.ResponseWriter, r *http.Request) {
		renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "请求处理失败")
	}
	handler := httpmw.RequestID(httpmw.ClientIP(c.TrustedProxyCIDRs, httpmw.Access(logger, metrics, classify, httpmw.Recover(logger, onPanic)(render.SecurityHeaders(root)))))

	srv := &http.Server{
		Addr:              c.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	var bg sync.WaitGroup
	cache.Bind(life, &bg)
	cache.RunPoll(life, c.SnapshotPollInterval, &bg)
	webCtl.runRetention(life, c, st, limiter, logger, &bg)

	serveErr := make(chan error, 1)
	go func() {
		e := srv.ListenAndServe()
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		serveErr <- e
	}()
	logger.Info("web_started", "addr", c.HTTPAddr, "version", version)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	var listenErr error
	select {
	case e := <-serveErr:
		listenErr = e
	case <-signals:
	}

	stopping.Store(true)
	deadline, cancelShutdown := context.WithTimeout(context.Background(), c.ShutdownTimeout)
	defer cancelShutdown()
	stopLife()
	result := shutdownAll(deadline, srv, &bg, db.Close)
	logger.Info("web_stopped")
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

type buildInfo struct {
	Version, GitCommit, BuildTime string
}

type webController struct {
	cache    *publicdata.Cache
	metrics  *observability.Metrics
	stopping *atomic.Bool
	build    buildInfo
}

func (c *webController) health(w http.ResponseWriter, r *http.Request) {
	if !webNoQuery(w, r) {
		return
	}
	webWriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *webController) ready(w http.ResponseWriter, r *http.Request) {
	if !webNoQuery(w, r) {
		return
	}
	if c.stopping.Load() || c.cache.Current() == nil {
		webProblem(w, r, http.StatusServiceUnavailable, "not-ready", "服务未就绪")
		return
	}
	data := c.cache.Current()
	webWriteJSON(w, r, http.StatusOK, map[string]string{
		"status":          "ready",
		"dataset_version": strconv.FormatUint(data.Version, 10),
	})
}

func (c *webController) version(w http.ResponseWriter, r *http.Request) {
	if !webNoQuery(w, r) {
		return
	}
	b := c.build
	if b.Version == "" {
		b.Version = "dev"
	}
	if b.GitCommit == "" {
		b.GitCommit = "unknown"
	}
	if b.BuildTime == "" {
		b.BuildTime = "unknown"
	}
	webWriteJSON(w, r, http.StatusOK, map[string]string{
		"version":    b.Version,
		"git_commit": b.GitCommit,
		"build_time": b.BuildTime,
	})
}

func (c *webController) runRetention(life context.Context, cfg config.Config, st *store.Store, limiter *ratelimit.Limiter, logger *slog.Logger, bg *sync.WaitGroup) {
	bg.Add(1)
	go func() {
		defer bg.Done()
		run := func() {
			now := time.Now().UTC()
			deleted, redacted, err := st.RunRetention(life, now.Add(-cfg.SubmissionRetention), 500)
			result := "success"
			if err != nil {
				result = "failure"
				logger.Warn("retention_run", "result", result, "error_category", operationCategory(err))
			} else {
				c.metrics.AddWebRetentionRows("deleted", deleted)
				c.metrics.AddWebRetentionRows("redacted", redacted)
			}
			sessions, serr := st.DeleteExpiredSessions(life, now)
			if serr != nil {
				result = "failure"
				logger.Warn("retention_run", "result", result, "error_category", operationCategory(serr))
			}
			limiter.Sweep(now)
			if pending, perr := st.PendingCount(life); perr == nil {
				c.metrics.SetWebPendingSubmissions(pending)
			}
			if err == nil && serr == nil {
				logger.Info("retention_run", "deleted", deleted, "redacted", redacted, "sessions", sessions, "result", result)
			}
		}
		if pending, err := st.PendingCount(life); err == nil {
			c.metrics.SetWebPendingSubmissions(pending)
		}
		first := time.NewTimer(5 * time.Minute)
		defer first.Stop()
		select {
		case <-life.Done():
			return
		case <-first.C:
			run()
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

func webNoQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		webProblem(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效")
		return false
	}
	return true
}

func webWriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
	}
}

func webProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	webWriteJSON(w, r, status, map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"code":   code,
		"detail": detail,
	})
}
