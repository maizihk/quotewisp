package public_test

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentence-api/internal/httpmw"
	"sentence-api/internal/testdb"
	"sentence-api/internal/web/public"
	"sentence-api/internal/web/publicdata"
	"sentence-api/internal/web/ratelimit"
	"sentence-api/internal/web/render"
	"sentence-api/internal/web/store"
)

type testMetrics struct {
	submissions atomic.Int32
	lastResult  atomic.Value
}

func (m *testMetrics) Submission(result string) {
	m.submissions.Add(1)
	m.lastResult.Store(result)
}

type stubSource struct {
	data    *store.PublicData
	version uint64
}

func (s *stubSource) BuildPublicData(context.Context) (*store.PublicData, error) {
	return s.data, nil
}

func (s *stubSource) DatasetVersion(context.Context) (uint64, error) {
	return s.version, nil
}

type testEnv struct {
	handler http.Handler
	store   *store.Store
	cache   *publicdata.Cache
	tokens  *render.FormTokens
	metrics *testMetrics
	limiter *ratelimit.Limiter
}

func newTestEnv(t *testing.T, limiter *ratelimit.Limiter) *testEnv {
	t.Helper()
	data := &store.PublicData{
		Version: 42,
		Categories: []store.PublicCategory{
			{Code: "original", Name: "原创", Count: 2},
		},
		Recent: []store.RecentItem{
			{Content: "最近一句", CategoryName: "原创", Nickname: "测试者"},
		},
		ExportJSON: []byte(`{"categories":[{"code":"original","name":"原创","sort_order":1}],"sentences":[]}`),
		BuiltAt:    time.Now().UTC(),
	}
	src := &stubSource{data: data, version: 42}
	cache := publicdata.New(src, nil, nil, time.Second)
	if err := cache.LoadInitial(context.Background()); err != nil {
		t.Fatal(err)
	}
	renderer, err := render.New(render.Site{
		Name:    "句子 API",
		Contact: "admin@example.com",
		RepoURL: "https://github.com/example/sentence-api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if limiter == nil {
		limiter = ratelimit.New(5, 20, 1000)
	}
	metrics := &testMetrics{}
	tokens := render.NewFormTokens([]byte("01234567890123456789012345678901"))
	h, err := public.New(public.Deps{
		Cache:        cache,
		Store:        store.New(nil),
		Renderer:     renderer,
		Tokens:       tokens,
		Limiter:      limiter,
		Logger:       nil,
		Metrics:      metrics,
		PendingLimit: 1000,
		APIBaseURL:   "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{handler: h, cache: cache, tokens: tokens, metrics: metrics, limiter: limiter}
}

func newDBTestEnv(t *testing.T, limiter *ratelimit.Limiter) *testEnv {
	t.Helper()
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	data := &store.PublicData{
		Version: 1,
		Categories: []store.PublicCategory{
			{Code: "original", Name: "原创", Count: 0},
		},
		ExportJSON: []byte(`{"categories":[{"code":"original","name":"原创","sort_order":1}],"sentences":[]}`),
		BuiltAt:    time.Now().UTC(),
	}
	src := &stubSource{data: data, version: 1}
	cache := publicdata.New(src, nil, nil, time.Second)
	if err := cache.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	renderer, err := render.New(render.Site{Name: "句子 API", Contact: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if limiter == nil {
		limiter = ratelimit.New(100, 1000, 1000)
	}
	metrics := &testMetrics{}
	tokens := render.NewFormTokens([]byte("01234567890123456789012345678901"))
	h, err := public.New(public.Deps{
		Cache:        cache,
		Store:        st,
		Renderer:     renderer,
		Tokens:       tokens,
		Limiter:      limiter,
		Metrics:      metrics,
		PendingLimit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{handler: h, store: st, cache: cache, tokens: tokens, metrics: metrics, limiter: limiter}
}

func (e *testEnv) do(t *testing.T, method, path string, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req = withMiddleware(req)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Result()
}

func withMiddleware(req *http.Request) *http.Request {
	rec := httptest.NewRecorder()
	var out *http.Request
	chain := httpmw.RequestID(httpmw.ClientIP(nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		out = r
	})))
	chain.ServeHTTP(rec, req)
	return out
}

func TestIndexRendersRecentAndNav(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodGet, "/", nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{
		"最近一句", "原创", "测试者",
		`href="/docs"`, `href="/submit"`, `href="/dataset"`,
		`src="/assets/random.js"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in index body", want)
		}
	}
}

func TestDocsShowsCategoryTableAndErrorCodes(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodGet, "/docs", nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{
		"original", "原创", "invalid-parameter", "not-found", "method-not-allowed",
		"meta.dataset_version", "42", "curl -sS 'https://example.com/api/v1/sentences/random'",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in docs body", want)
		}
	}
}

func TestGetSubmitContainsTokenAndHoneypot(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodGet, "/submit", nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if !strings.Contains(text, `name="form_token"`) {
		t.Fatal("missing form_token")
	}
	if !strings.Contains(text, `name="website"`) || !strings.Contains(text, "visually-hidden") {
		t.Fatal("missing honeypot field")
	}
}

func TestPostSubmitWithoutToken(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodPost, "/submit", url.Values{
		"content": {"hello"}, "category": {"original"}, "source": {"s"},
		"author": {"a"}, "contact": {"test@example.com"}, "agree": {"on"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "表单已过期") {
		t.Fatal("expected expired form message")
	}
	if env.metrics.lastResult.Load() != "invalid" {
		t.Fatalf("metrics result=%v", env.metrics.lastResult.Load())
	}
}

func TestPostSubmitTooYoungToken(t *testing.T) {
	env := newTestEnv(t, nil)
	now := time.Now().UTC()
	token := env.tokens.Issue(now)
	resp := env.do(t, http.MethodPost, "/submit", url.Values{
		"form_token": {token},
		"content":    {"hello"}, "category": {"original"}, "source": {"s"},
		"author": {"a"}, "contact": {"test@example.com"}, "agree": {"on"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "表单已过期") {
		t.Fatal("expected too-young token rejection")
	}
}

func TestHoneypotFilled(t *testing.T) {
	env := newDBTestEnv(t, nil)
	now := time.Now().UTC().Add(-10 * time.Second)
	token := env.tokens.Issue(now)
	resp := env.do(t, http.MethodPost, "/submit", url.Values{
		"form_token": {token},
		"website":    {"http://spam.example"},
		"content":    {"蜜罐测试"}, "category": {"original"}, "source": {"s"},
		"author": {"a"}, "contact": {"test@example.com"}, "agree": {"on"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/submit/done" {
		t.Fatalf("status=%d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	items, _, err := env.store.ListSubmissions(context.Background(), store.SubmissionPending, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatal("honeypot submission should not be stored")
	}
	if env.metrics.lastResult.Load() != "honeypot" {
		t.Fatalf("metrics=%v", env.metrics.lastResult.Load())
	}
}

func TestRateLimit(t *testing.T) {
	env := newDBTestEnv(t, ratelimit.New(2, 100, 100))
	base := url.Values{
		"content": {"内容"}, "category": {"original"}, "source": {"s"},
		"author": {"a"}, "contact": {"test@example.com"}, "agree": {"on"},
	}
	for i := 0; i < 2; i++ {
		token := env.tokens.Issue(time.Now().UTC().Add(-10 * time.Second))
		vals := cloneValues(base)
		vals.Set("form_token", token)
		vals.Set("content", "内容"+string(rune('A'+i)))
		resp := env.do(t, http.MethodPost, "/submit", vals)
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("request %d status=%d", i+1, resp.StatusCode)
		}
	}
	token := env.tokens.Issue(time.Now().UTC().Add(-10 * time.Second))
	vals := cloneValues(base)
	vals.Set("form_token", token)
	vals.Set("content", "内容C")
	resp := env.do(t, http.MethodPost, "/submit", vals)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "提交过于频繁") {
		t.Fatal("expected rate limit message")
	}
}

func TestValidationErrorsPreserveValues(t *testing.T) {
	env := newTestEnv(t, nil)
	now := time.Now().UTC().Add(-10 * time.Second)
	token := env.tokens.Issue(now)
	resp := env.do(t, http.MethodPost, "/submit", url.Values{
		"form_token": {token},
		"content":    {""},
		"category":   {"original"},
		"source":     {""},
		"author":     {""},
		"nickname":   {"保留昵称"},
		"contact":    {"ab"},
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	if !strings.Contains(text, `value="保留昵称"`) {
		t.Fatal("nickname value not preserved")
	}
	if !strings.Contains(text, "field-error") {
		t.Fatal("expected field errors")
	}
}

func TestSuccessfulSubmission(t *testing.T) {
	env := newDBTestEnv(t, nil)
	now := time.Now().UTC().Add(-10 * time.Second)
	content := "成功投稿内容"
	token := env.tokens.Issue(now)
	resp := env.do(t, http.MethodPost, "/submit", url.Values{
		"form_token": {token},
		"content":    {content},
		"category":   {"original"},
		"source":     {"出处"},
		"author":     {"作者"},
		"nickname":   {"昵称"},
		"contact":    {"user@example.com"},
		"agree":      {"on"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	items, _, err := env.store.ListSubmissions(context.Background(), store.SubmissionPending, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 submission, got %d", len(items))
	}
	item := items[0]
	if item.Content != content || item.Nickname != "昵称" || item.Contact != "user@example.com" {
		t.Fatalf("unexpected submission: %+v", item)
	}
	if item.Source != "出处" || item.Author != "作者" {
		t.Fatal("source/author mismatch")
	}
	hash := sha256.Sum256([]byte(content))
	if item.ContentSHA256 != hash {
		t.Fatal("content hash mismatch")
	}
	if !item.ClientIP.IsValid() {
		t.Fatal("expected client ip")
	}
}

func TestDuplicateSubmission(t *testing.T) {
	env := newDBTestEnv(t, ratelimit.New(100, 1000, 1000))
	vals := url.Values{
		"content": {"重复内容"}, "category": {"original"}, "source": {"s"},
		"author": {"a"}, "contact": {"test@example.com"}, "agree": {"on"},
	}
	vals.Set("form_token", env.tokens.Issue(time.Now().UTC().Add(-10*time.Second)))
	resp := env.do(t, http.MethodPost, "/submit", vals)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first submit status=%d", resp.StatusCode)
	}
	vals.Set("form_token", env.tokens.Issue(time.Now().UTC().Add(-10*time.Second)))
	resp = env.do(t, http.MethodPost, "/submit", vals)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
}

func TestQueueFull(t *testing.T) {
	env := newDBTestEnv(t, ratelimit.New(1000, 10000, 10000))
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.9")
	for i := 0; i < 1000; i++ {
		_, err := env.store.CreateSubmission(ctx, store.NewSubmission{
			Content: "queue-fill-" + strings.Repeat("x", i), CategoryCode: "original",
			Source: "s", Author: "a", Contact: "c@example.com", ClientIP: ip,
		}, 1000)
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-10 * time.Second)
	resp := env.do(t, http.MethodPost, "/submit", url.Values{
		"form_token": {env.tokens.Issue(now)},
		"content":    {"队列已满测试"}, "category": {"original"}, "source": {"s"},
		"author": {"a"}, "contact": {"test@example.com"}, "agree": {"on"},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestDatasetJSONHeadersAndETag(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodGet, "/dataset/sentences.json", nil)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type=%q", ct)
	}
	if resp.Header.Get("ETag") != `"42"` {
		t.Fatalf("etag=%q", resp.Header.Get("ETag"))
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "sentences-42.json") {
		t.Fatal("missing content disposition")
	}
	if resp.Header.Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("cache-control=%q", resp.Header.Get("Cache-Control"))
	}

	req := httptest.NewRequest(http.MethodGet, "/dataset/sentences.json", nil)
	req.Header.Set("If-None-Match", `"42"`)
	req = withMiddleware(req)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("expected 304, got %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatal("304 should have empty body")
	}

	head := env.do(t, http.MethodHead, "/dataset/sentences.json", nil)
	head.Body.Close()
	if head.Header.Get("Content-Length") == "" {
		t.Fatal("HEAD missing content length")
	}
	if head.ContentLength == 0 {
		t.Fatal("HEAD content length should be non-zero")
	}
}

func TestLicenseServed(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodGet, "/dataset/LICENSE.txt", nil)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "GNU AFFERO GENERAL PUBLIC LICENSE") {
		t.Fatal("license prefix mismatch")
	}
	if resp.Header.Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("cache-control=%q", resp.Header.Get("Cache-Control"))
	}
}

func TestUnknownPath404(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodGet, "/missing", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestPostRoot405(t *testing.T) {
	env := newTestEnv(t, nil)
	resp := env.do(t, http.MethodPost, "/", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Fatalf("allow=%q", allow)
	}
}

func TestRouteName(t *testing.T) {
	cases := map[string]string{
		"/":                       "/",
		"/docs":                   "/docs",
		"/submit":                 "/submit",
		"/submit/done":            "/submit/done",
		"/dataset":                "/dataset",
		"/dataset/sentences.json": "/dataset/sentences.json",
		"/dataset/LICENSE.txt":    "/dataset/LICENSE.txt",
		"/static/site.css":        "/static/*",
		"/nope":                   "unmatched",
	}
	for path, want := range cases {
		if got := public.RouteName(path); got != want {
			t.Fatalf("RouteName(%q)=%q want %q", path, got, want)
		}
	}
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		cp := append([]string(nil), vals...)
		out[k] = cp
	}
	return out
}
