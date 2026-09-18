package render

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentence-api/internal/httpmw"
)

//go:embed testdata/*
var testdataFS embed.FS

func testRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(Site{
		Name:    "句子 API",
		Contact: "admin@example.com",
		RepoURL: "https://github.com/example/sentence-api",
		Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func withRequestID(req *http.Request) *http.Request {
	rec := httptest.NewRecorder()
	var out *http.Request
	h := httpmw.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		out = r
	}))
	h.ServeHTTP(rec, req)
	return out
}

func TestEmbeddedAssetsParse(t *testing.T) {
	r := testRenderer(t)
	if _, err := r.Pages(templateFS, "templates/layout.html", "templates/error.html"); err != nil {
		t.Fatal(err)
	}
	css, err := staticFS.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(css))) == 0 {
		t.Fatal("site.css is empty")
	}
}

func TestLayoutRendersPage(t *testing.T) {
	r := testRenderer(t)
	pages, err := r.Pages(testdataFS, "testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	req := withRequestID(httptest.NewRequest(http.MethodGet, "/", nil))
	w := httptest.NewRecorder()
	r.HTML(w, req, pages, "page", http.StatusOK, nil)

	body := w.Body.String()
	for _, want := range []string{
		"测试页 · 句子 API",
		`href="/"`, "首页",
		`href="/docs"`, "接口文档",
		`href="/submit"`, "投稿",
		`href="/dataset"`, "数据",
		"hitokoto-osc/sentences-bundle",
		`href="/dataset/LICENSE.txt"`, "AGPL v3",
		`href="/dataset/sentences.json"`, "本站句子库（含用户投稿）在相同条件下开放下载",
		`href="https://github.com/example/sentence-api"`, "源代码",
		"admin@example.com",
		"页面正文",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type=%q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control=%q", cc)
	}
}

func TestLayoutOmitsRepoWhenEmpty(t *testing.T) {
	r, err := New(Site{Name: "句子 API", Contact: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	pages, err := r.Pages(testdataFS, "testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.HTML(w, req, pages, "page", http.StatusOK, nil)
	body := w.Body.String()
	if strings.Contains(body, "源代码") {
		t.Fatalf("repo link should be omitted: %s", body)
	}
	if !strings.Contains(body, "程序为独立实现。") {
		t.Fatal("expected standalone implementation text")
	}
}

func TestErrorNegotiationJSON(t *testing.T) {
	r := testRenderer(t)
	req := withRequestID(httptest.NewRequest(http.MethodGet, "/", nil))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.Error(w, req, http.StatusNotFound, "not-found", "未找到", "请求的资源不存在")

	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type=%q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type":   "about:blank",
		"title":  "未找到",
		"status": float64(404),
		"detail": "请求的资源不存在",
		"code":   "not-found",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s=%v want %v", k, got[k], v)
		}
	}
	if rid, _ := got["request_id"].(string); rid == "" {
		t.Fatal("missing request_id")
	}
	if !strings.HasSuffix(w.Body.String(), "\n") {
		t.Fatal("expected trailing newline")
	}
}

func TestErrorNegotiationHTML(t *testing.T) {
	r := testRenderer(t)
	req := withRequestID(httptest.NewRequest(http.MethodGet, "/", nil))
	w := httptest.NewRecorder()
	r.Error(w, req, http.StatusNotFound, "not-found", "未找到", "请求的资源不存在")

	body := w.Body.String()
	if !strings.Contains(body, "404") || !strings.Contains(body, "未找到") {
		t.Fatalf("unexpected body: %s", body)
	}
	if !strings.Contains(body, httpmw.RequestIDFrom(req.Context())) {
		t.Fatal("missing request id")
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type=%q", ct)
	}
}

func TestErrorHEAD(t *testing.T) {
	r := testRenderer(t)
	req := withRequestID(httptest.NewRequest(http.MethodHead, "/", nil))
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	r.Error(w, req, http.StatusNotFound, "not-found", "未找到", "请求的资源不存在")
	if w.Body.Len() != 0 {
		t.Fatalf("body=%q", w.Body.String())
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestHTMLTemplateFailureNoPartialOutput(t *testing.T) {
	r := testRenderer(t)
	pages, err := r.Pages(testdataFS, "testdata/bad_page.html")
	if err != nil {
		t.Fatal(err)
	}
	req := withRequestID(httptest.NewRequest(http.MethodGet, "/", nil))
	w := httptest.NewRecorder()
	r.HTML(w, req, pages, "bad_page", http.StatusOK, nil)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "坏页") {
		t.Fatalf("partial page title leaked: %s", body)
	}
	if !strings.Contains(body, "内部错误") {
		t.Fatalf("unexpected error body: %s", body)
	}
}

func TestHTMLHEAD(t *testing.T) {
	r := testRenderer(t)
	pages, err := r.Pages(testdataFS, "testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodHead, "/", nil)
	w := httptest.NewRecorder()
	r.HTML(w, req, pages, "page", http.StatusOK, nil)
	if w.Body.Len() != 0 {
		t.Fatalf("body=%q", w.Body.String())
	}
	if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("missing content type")
	}
}

func TestStaticHandler(t *testing.T) {
	r := testRenderer(t)
	h := r.Static()

	req := httptest.NewRequest(http.MethodGet, "/static/site.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("content-type=%q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("cache-control=%q", w.Header().Get("Cache-Control"))
	}
	if !strings.Contains(w.Body.String(), "system-ui") {
		t.Fatal("css body missing expected content")
	}

	req = httptest.NewRequest(http.MethodGet, "/static/missing.css", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/static/site.css", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("allow=%q", w.Header().Get("Allow"))
	}
}

func TestNotFoundAndMethodNotAllowed(t *testing.T) {
	r := testRenderer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.NotFound(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "未找到") {
		t.Fatal(w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	r.MethodNotAllowed(w, req, "GET, HEAD")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("allow=%q", w.Header().Get("Allow"))
	}
	if !strings.Contains(w.Body.String(), "方法不允许") {
		t.Fatal(w.Body.String())
	}
}

func TestFormTokenRoundtrip(t *testing.T) {
	secret := []byte("test-secret-key-for-form-token!!")
	ft := NewFormTokens(secret)
	now := time.Unix(1_700_000_000, 0).UTC()
	minAge := 5 * time.Second
	maxAge := 2 * time.Hour

	token := ft.Issue(now)
	check := now.Add(minAge)
	if !ft.Verify(token, check, minAge, maxAge) {
		t.Fatal("valid token rejected")
	}
}

func TestFormTokenTooYoung(t *testing.T) {
	ft := NewFormTokens([]byte("secret"))
	now := time.Unix(1_700_000_000, 0).UTC()
	token := ft.Issue(now)
	if ft.Verify(token, now.Add(2*time.Second), 5*time.Second, 2*time.Hour) {
		t.Fatal("too young token accepted")
	}
}

func TestFormTokenExpired(t *testing.T) {
	ft := NewFormTokens([]byte("secret"))
	now := time.Unix(1_700_000_000, 0).UTC()
	token := ft.Issue(now)
	if ft.Verify(token, now.Add(3*time.Hour), 5*time.Second, 2*time.Hour) {
		t.Fatal("expired token accepted")
	}
}

func TestFormTokenTampered(t *testing.T) {
	ft := NewFormTokens([]byte("secret"))
	now := time.Unix(1_700_000_000, 0).UTC()
	token := ft.Issue(now)
	tampered := token[:len(token)-1] + "X"
	if ft.Verify(tampered, now.Add(minAge()), 5*time.Second, 2*time.Hour) {
		t.Fatal("tampered token accepted")
	}
}

func TestFormTokenWrongSecret(t *testing.T) {
	ft1 := NewFormTokens([]byte("secret-one"))
	ft2 := NewFormTokens([]byte("secret-two"))
	now := time.Unix(1_700_000_000, 0).UTC()
	token := ft1.Issue(now)
	if ft2.Verify(token, now.Add(minAge()), 5*time.Second, 2*time.Hour) {
		t.Fatal("wrong secret accepted")
	}
}

func TestFormTokenMalformed(t *testing.T) {
	ft := NewFormTokens([]byte("secret"))
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, bad := range []string{"", "!!!", "AQID"} {
		if ft.Verify(bad, now, 0, time.Hour) {
			t.Fatalf("malformed token %q accepted", bad)
		}
	}
}

func minAge() time.Duration { return 5 * time.Second }

func TestTemplateFuncs(t *testing.T) {
	r := testRenderer(t)
	pages, err := r.Pages(testdataFS, "testdata/funcs_page.html")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	r.HTML(w, req, pages, "funcs_page", http.StatusOK, map[string]any{
		"When": time.Date(2026, 3, 19, 8, 30, 0, 0, time.FixedZone("CST", 8*3600)),
	})
	body := w.Body.String()
	for _, want := range []string{
		"2026-03-19 00:30 UTC",
		"你好…",
		"待审",
		"已通过",
		"已拒绝",
		"已发布",
		"已停用",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	var seenCSP, seenReferrer, seenNoSniff bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seenCSP = w.Header().Get("Content-Security-Policy") != ""
		seenReferrer = w.Header().Get("Referrer-Policy") == "same-origin"
		seenNoSniff = w.Header().Get("X-Content-Type-Options") == "nosniff"
		w.WriteHeader(http.StatusNoContent)
	})
	h := SecurityHeaders(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !seenCSP || !seenReferrer || !seenNoSniff {
		t.Fatalf("csp=%v referrer=%v nosniff=%v", seenCSP, seenReferrer, seenNoSniff)
	}
}

func TestPrefersJSON(t *testing.T) {
	tests := []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"text/html", false},
		{"application/json", true},
		{"application/problem+json", true},
		{"text/html, application/json;q=0.8", false},
		{"application/json, text/html;q=0.5", true},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept", tt.accept)
		if got := prefersJSON(req); got != tt.want {
			t.Fatalf("accept=%q got=%v want=%v", tt.accept, got, tt.want)
		}
	}
}

func TestPagesCloneIsolation(t *testing.T) {
	r := testRenderer(t)
	pages, err := r.Pages(testdataFS, "testdata/page.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pages.Parse(`{{define "extra"}}{{end}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.base.Clone(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestIDFromMiddleware(t *testing.T) {
	r := testRenderer(t)
	var captured string
	inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.Error(w, req, http.StatusBadRequest, "invalid-parameter", "参数无效", "bad")
		captured = httpmw.RequestIDFrom(req.Context())
	})
	h := httpmw.RequestID(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if captured == "" {
		t.Fatal("missing request id from middleware")
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["request_id"] != captured {
		t.Fatalf("request_id=%v context=%q", got["request_id"], captured)
	}
}

func TestStaticFSRoot(t *testing.T) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	f, err := sub.Open("site.css")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty css")
	}
}
