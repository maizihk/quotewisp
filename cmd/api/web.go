package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sentence-api/internal/config"
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
	return runService()
}

// buildWebHandler constructs the site against the caller's database and
// lifecycle. It is shared by the combined server so the process has one pool
// and one set of metrics.
func buildWebHandler(c config.Config, db *sql.DB, life context.Context, metrics *observability.Metrics, logger *slog.Logger, bg *sync.WaitGroup, stopping *atomic.Bool, onChange func(), apiUsage func(context.Context) admin.APIUsage) (http.Handler, *publicdata.Cache, error) {
	st := store.New(db)
	metrics.EnableWeb()
	initCtx, cancel := context.WithTimeout(life, c.SnapshotLoadTimeout)
	defer cancel()
	settings, err := st.EnsureSettings(initCtx, store.SiteSettings{Name: siteName, EnglishName: siteEnglishName, Slogan: siteSlogan, Contact: c.SiteContact, PublicOrigin: c.APIBaseURL, RepoURL: c.SiteRepoURL})
	if err != nil {
		return nil, nil, errors.New("site settings seed failed")
	}
	cache := publicdata.New(st, logger, webPublicDataMetrics{metrics}, c.SnapshotLoadTimeout)
	if err := cache.LoadInitial(initCtx); err != nil {
		return nil, nil, errors.New("initial public data load failed")
	}
	renderer, err := render.New(render.Site{Name: settings.Name, EnglishName: settings.EnglishName, Slogan: settings.Slogan, Contact: settings.Contact, RepoURL: settings.RepoURL, PublicOrigin: settings.PublicOrigin, BeianText: settings.BeianText, BeianURL: settings.BeianURL, Version: version, AssetRev: version + "-" + gitCommit + "-" + buildTime})
	if err != nil {
		return nil, nil, err
	}
	tokens := render.NewFormTokens([]byte(c.WebSecretKey))
	limiter := ratelimit.New(c.SubmissionRatePerHour, c.SubmissionRatePerDay, 100000)
	logins := auth.NewLoginLimiter(10, 5, 15*time.Minute, 100000)
	pub, err := public.New(public.Deps{Cache: cache, Store: st, Renderer: renderer, Tokens: tokens, Limiter: limiter, Logger: logger, Metrics: webPublicMetrics{metrics}, PendingLimit: c.SubmissionPendingLimit})
	if err != nil {
		return nil, nil, err
	}
	adm, err := admin.New(admin.Deps{Store: st, Renderer: renderer, Logger: logger, Metrics: webAdminMetrics{metrics}, Logins: logins, Tokens: tokens, CookieSecure: c.CookieSecure, OnChange: func() {
		cache.Refresh()
		if onChange != nil {
			onChange()
		}
	}, APIMetricsURL: c.APIMetricsURL, APIUsage: apiUsage})
	if err != nil {
		return nil, nil, err
	}
	ctl := &webController{cache: cache, metrics: metrics, stopping: stopping, build: buildInfo{Version: version, GitCommit: gitCommit, BuildTime: buildTime}}
	root := http.NewServeMux()
	root.HandleFunc("/healthz", ctl.health)
	root.HandleFunc("/readyz", ctl.ready)
	root.Handle("/metrics", metrics.Handler())
	root.HandleFunc("/version", ctl.version)
	root.Handle("/admin/", adm)
	root.Handle("/admin", adm)
	root.Handle("/", pub)
	cache.Bind(life, bg)
	cache.RunPoll(life, c.SnapshotPollInterval, bg)
	ctl.runRetention(life, c, st, limiter, logger, bg)
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
	return handler, cache, nil
}

func combinedHandler(api, web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/healthz" || p == "/readyz" || p == "/metrics" || p == "/version" || p == "/internal/reload" {
			api.ServeHTTP(w, r)
			return
		}
		web.ServeHTTP(w, r)
	})
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
