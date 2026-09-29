package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sentence-api/internal/database"
	"sentence-api/internal/httpapi"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
	"sentence-api/internal/web/admin"
	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

// Exercises the real HTTP/session/worker/transaction/API and public-cache path.
func TestSQLiteAdminImportRefreshesCombinedSite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var bg sync.WaitGroup
	dir := t.TempDir()
	target := database.Target{SQLitePath: filepath.Join(dir, "site.db")}
	if err := target.Initialize(ctx, database.PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	db, err := target.Open(ctx, database.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); bg.Wait(); db.Close() })
	st := store.New(db)
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateAdmin(ctx, "admin1", hash, nil); err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewMetrics()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	loader := database.Loader{DB: db}
	snapshots := snapshot.NewManager(loader, 5*time.Second)
	if err = snapshots.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	var stopping atomic.Bool
	controller := &refreshController{life: ctx, loader: loader, manager: snapshots, metrics: metrics, logger: logger, loadTimeout: 5 * time.Second, stopping: &stopping, bg: &bg}
	cfg := testCombinedConfig()
	cfg.DataDir = dir
	cfg.ImportMaxUploadBytes = 1 << 20
	cfg.ImportUploadTTL = time.Minute
	cfg.ImportTimeout = 10 * time.Second
	web, cache, err := buildWebHandler(cfg, db, ctx, metrics, logger, &bg, &stopping, nil, func(context.Context) admin.APIUsage { return admin.APIUsage{} }, controller.refreshAndWait)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(httpapi.Options{Snapshots: snapshots, LifecycleContext: ctx, Metrics: metrics, Logger: logger})
	handler := combinedHandler(api, web)
	jar, _ := cookiejar.New(nil)
	request := func(method, path, ctype string, body io.Reader) *httptest.ResponseRecorder {
		t.Helper()
		u, _ := url.Parse("http://example.com" + path)
		req := httptest.NewRequest(method, u.String(), body)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		for _, cookie := range jar.Cookies(u) {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		jar.SetCookies(u, rec.Result().Cookies())
		return rec
	}
	token := func(body, name string) string {
		t.Helper()
		m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(body)
		if len(m) != 2 {
			t.Fatalf("missing %s in %s", name, body)
		}
		return m[1]
	}
	post := func(path string, values url.Values) *httptest.ResponseRecorder {
		return request("POST", path, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
	}
	login := request("GET", "/admin/login", "", nil)
	resp := post("/admin/login", url.Values{"form_token": {token(login.Body.String(), "form_token")}, "username": {"admin1"}, "password": {"password123"}})
	if resp.Code != 303 {
		t.Fatalf("login=%d %s", resp.Code, resp.Body.String())
	}
	for _, format := range []string{"native", "hitokoto"} {
		page := request("GET", "/admin/imports", "", nil)
		csrf := token(page.Body.String(), "csrf_token")
		id := "930bb316-a56b-49b5-879e-a322271c91de"
		data := `{"categories":[{"code":"import_test","name":"测试导入","sort_order":0}],"sentences":[{"uuid":"` + id + `","category":"import_test","content":"后台导入端到端测试"}]}`
		if format == "hitokoto" {
			id = "930bb316-a56b-49b5-879e-a322271c91df"
			data = `[{"uuid":"` + id + `","hitokoto":"适配格式端到端测试","type":"import_test","from":null,"from_who":null}]`
		}
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("csrf_token", csrf)
		_ = writer.WriteField("format", format)
		part, _ := writer.CreateFormFile("file", "sentences.json")
		_, _ = io.WriteString(part, data)
		_ = writer.Close()
		upload := request("POST", "/admin/imports", writer.FormDataContentType(), &body)
		if upload.Code != 303 {
			t.Fatalf("upload=%d %s", upload.Code, upload.Body.String())
		}
		location := upload.Header().Get("Location")
		jobID := strings.TrimPrefix(location, "/admin/imports/")
		wait := func(want string) {
			t.Helper()
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				var status string
				if err := db.QueryRow("SELECT status FROM import_jobs WHERE id=?", jobID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status == want {
					return
				}
				if status == "failed" || status == "refresh_failed" {
					t.Fatalf("job status=%s", status)
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("timed out waiting for %s", want)
		}
		wait("preview")
		preview := request("GET", location, "", nil)
		values := url.Values{"csrf_token": {csrf}, "digest": {token(preview.Body.String(), "digest")}}
		confirmed := post(location+"/confirm", values)
		if confirmed.Code != 303 {
			t.Fatalf("confirm=%d %s", confirmed.Code, confirmed.Body.String())
		}
		wait("complete")
		completed := request("GET", location, "", nil)
		if completed.Code != 200 || !strings.Contains(completed.Body.String(), "API 与前台快照刷新已完成") {
			t.Fatalf("completion page=%d %s", completed.Code, completed.Body.String())
		}
		if duplicate := post(location+"/confirm", values); duplicate.Code != 409 {
			t.Fatalf("repeat confirm=%d", duplicate.Code)
		}
		// Query through the immutable API snapshot, not directly through the database.
		result := request("GET", "/api/v1/sentences/"+id, "", nil)
		if result.Code != 200 || !strings.Contains(result.Body.String(), id) {
			t.Fatalf("API=%d %s", result.Code, result.Body.String())
		}
		var version uint64
		if err := db.QueryRow("SELECT version FROM dataset_versions WHERE id=1").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if cache.Current().Version != version {
			t.Fatalf("public cache version=%d database=%d", cache.Current().Version, version)
		}
	}
}
