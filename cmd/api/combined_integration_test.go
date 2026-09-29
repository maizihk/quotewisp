package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sentence-api/internal/config"
	"sentence-api/internal/database"
	"sentence-api/internal/httpapi"
	"sentence-api/internal/importer"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
	"sentence-api/internal/testdb"
	"sentence-api/internal/web/admin"
)

func TestCombinedHandlerSharesAPIAndWebMetrics(t *testing.T) {
	db := testdb.Open(t)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx, cancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		bg.Wait()
	})
	seed := `{"categories":[{"code":"original","name":"原创","sort_order":1}],"sentences":[{"uuid":"75A45FD4-4F2F-45EB-80CB-6F0A7BCDFAF2","category":"original","content":"组合测试句子","source":"测试","author":"测试"}]}`
	if _, err := importer.Import(ctx, db, strings.NewReader(seed), false); err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewMetrics()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := snapshot.NewManager(database.Loader{DB: db}, time.Second)
	if err := manager.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	var stopping atomic.Bool
	c := testCombinedConfig()
	c.DataDir = t.TempDir()
	c.ImportMaxUploadBytes = 32 << 20
	c.ImportUploadTTL = time.Minute
	c.ImportTimeout = time.Minute
	web, _, err := buildWebHandler(c, db, ctx, metrics, logger, &bg, &stopping, nil, func(context.Context) admin.APIUsage {
		v := metrics.APIRequestTotals()
		return admin.APIUsage{OK: v.OK, Random: v.Random, UUID: v.UUID, Categories: v.Categories}
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(httpapi.Options{Snapshots: manager, LifecycleContext: ctx, Metrics: metrics, Logger: logger, Build: httpapi.BuildInfo{Version: version, GitCommit: gitCommit, BuildTime: buildTime}})
	h := combinedHandler(api, web)

	apiResp := requestCombined(h, http.MethodGet, "/api/v1?categories=original&min_length=1&max_length=30")
	if apiResp.Code != http.StatusOK {
		t.Fatalf("API status=%d body=%s", apiResp.Code, apiResp.Body.String())
	}
	if apiResp.Header().Get("X-Request-ID") == "" {
		t.Fatal("API request ID missing")
	}
	webResp := requestCombined(h, http.MethodGet, "/")
	if webResp.Code != http.StatusOK {
		t.Fatalf("web status=%d", webResp.Code)
	}
	if webResp.Header().Get("X-Request-ID") == "" {
		t.Fatal("web request ID missing")
	}
	adminResp := requestCombined(h, http.MethodGet, "/admin")
	if adminResp.Code != http.StatusSeeOther {
		t.Fatalf("admin status=%d", adminResp.Code)
	}
	got := metrics.APIRequestTotals()
	if !got.OK || got.Random != 1 || got.UUID != 0 || got.Categories != 0 {
		t.Fatalf("web/admin requests contaminated API totals: %+v", got)
	}

}

func requestCombined(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func testCombinedConfig() config.Config {
	return config.Config{
		SnapshotPollInterval: time.Hour, SnapshotLoadTimeout: 5 * time.Second,
		WebSecretKey: "combined-integration-test-secret-key-32chars", SiteContact: "test@example.invalid",
		SiteRepoURL: "https://example.invalid/repo", APIBaseURL: "https://example.invalid",
		CookieSecure: false, SubmissionRatePerHour: 100, SubmissionRatePerDay: 100,
		SubmissionPendingLimit: 10, SubmissionRetention: time.Hour,
	}
}
