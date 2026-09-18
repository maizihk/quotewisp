package admin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentence-api/internal/httpmw"
	"sentence-api/internal/testdb"
	"sentence-api/internal/web/admin"
	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/render"
	"sentence-api/internal/web/store"
)

type testMetrics struct {
	logins    atomic.Int32
	reviews   atomic.Int32
	sentences atomic.Int32
}

func (m *testMetrics) LoginAttempt(string)           { m.logins.Add(1) }
func (m *testMetrics) Review(string, string)         { m.reviews.Add(1) }
func (m *testMetrics) SentenceChange(string, string) { m.sentences.Add(1) }
func (m *testMetrics) CategoryChange(string, string) {}

type testEnv struct {
	store    *store.Store
	handler  http.Handler
	client   *http.Client
	metrics  *testMetrics
	onChange atomic.Int32
	adminID  uint64
}

func setupEnv(t *testing.T) *testEnv {
	t.Helper()
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := st.CreateAdmin(ctx, "admin1", hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := render.New(render.Site{Name: "测试", Contact: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := &testMetrics{}
	env := &testEnv{store: st, metrics: metrics, adminID: adminID}
	h, err := admin.New(admin.Deps{
		Store:        st,
		Renderer:     renderer,
		Metrics:      metrics,
		Logins:       auth.NewLoginLimiter(10, 5, 15*time.Minute, 100000),
		CookieSecure: false,
		OnChange:     func() { env.onChange.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	env.handler = httpmw.RequestID(h)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	env.client = &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return env
}

func (e *testEnv) do(t *testing.T, method, path string, body url.Values, headers map[string]string) *http.Response {
	t.Helper()
	u := reqURL(t, path)
	var r io.Reader
	if body != nil {
		r = strings.NewReader(body.Encode())
	}
	req := httptest.NewRequest(method, u.String(), r)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range e.client.Jar.Cookies(u) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	e.client.Jar.SetCookies(u, resp.Cookies())
	return resp
}

func (e *testEnv) login(t *testing.T) {
	t.Helper()
	resp := e.do(t, http.MethodPost, "/admin/login", url.Values{
		"username": {"admin1"},
		"password": {"password123"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status=%d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/" {
		t.Fatalf("login location=%q", loc)
	}
}

func reqURL(t *testing.T, path string) *url.URL {
	t.Helper()
	u, err := url.Parse("http://example.com" + path)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func csrfFromPage(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="csrf_token" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("csrf token not found")
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("csrf token malformed")
	}
	return rest[:j]
}

func TestRouteName(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/admin/login", "/admin/login"},
		{"/admin/submissions/42", "/admin/submissions/{id}"},
		{"/admin/submissions/42/approve", "/admin/submissions/{id}/approve"},
		{"/admin/sentences/new", "/admin/sentences/new"},
		{"/admin/sentences/550e8400-e29b-41d4-a716-446655440000", "/admin/sentences/{uuid}"},
		{"/admin/categories/original", "/admin/categories/{code}"},
		{"/admin/users/1/disable", "/admin/users/{id}/disable"},
		{"/admin", "/admin/"},
		{"/admin/unknown", "unmatched"},
		{"/other", "unmatched"},
	}
	for _, tt := range tests {
		if got := admin.RouteName(tt.path); got != tt.want {
			t.Fatalf("RouteName(%q)=%q want %q", tt.path, got, tt.want)
		}
	}
}

func TestUnauthenticatedRedirect(t *testing.T) {
	env := setupEnv(t)
	resp := env.do(t, http.MethodGet, "/admin/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/login" {
		t.Fatalf("location=%q", loc)
	}
}

func TestLoginWrongPasswordAndRateLimit(t *testing.T) {
	env := setupEnv(t)
	for i := 0; i < 5; i++ {
		resp := env.do(t, http.MethodPost, "/admin/login", url.Values{
			"username": {"admin1"},
			"password": {"wrongpass"},
		}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", i+1, resp.StatusCode)
		}
	}
	resp := env.do(t, http.MethodPost, "/admin/login", url.Values{
		"username": {"admin1"},
		"password": {"wrongpass"},
	}, nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limit status=%d", resp.StatusCode)
	}
}

func TestLoginSuccessCookie(t *testing.T) {
	env := setupEnv(t)
	renderer, err := render.New(render.Site{Name: "测试", Contact: "admin@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	secureHandler, err := admin.New(admin.Deps{
		Store: env.store, Renderer: renderer, CookieSecure: true,
		Logins: auth.NewLoginLimiter(10, 5, 15*time.Minute, 100000),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(url.Values{
		"username": {"admin1"},
		"password": {"password123"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	secureHandler.ServeHTTP(rec, req)
	resp := rec.Result()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("missing session cookie")
	}
	if !session.HttpOnly || session.Path != "/admin" || !session.Secure {
		t.Fatalf("cookie attrs: HttpOnly=%v Path=%q Secure=%v SameSite=%v",
			session.HttpOnly, session.Path, session.Secure, session.SameSite)
	}
	if session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SameSite=%v", session.SameSite)
	}
}

func TestCSRFRequired(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	resp := env.do(t, http.MethodPost, "/admin/logout", url.Values{}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestCSRFCrossSite(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/logout", url.Values{
		"csrf_token": {token},
	}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestApproveSubmission(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()
	subID, err := env.store.CreateSubmission(ctx, store.NewSubmission{
		Content: "审核测试内容", CategoryCode: "original", Source: "出处", Author: "作者",
		Nickname: "昵称", Contact: "c@example.com", ClientIP: netip.MustParseAddr("203.0.113.1"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	env.login(t)
	detail := env.do(t, http.MethodGet, "/admin/submissions/"+u64(subID), nil, nil)
	body, _ := io.ReadAll(detail.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/submissions/"+u64(subID)+"/approve", url.Values{
		"csrf_token": {token},
		"content":    {"审核测试内容"},
		"category":   {"original"},
		"source":     {"出处"},
		"author":     {"作者"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve status=%d", resp.StatusCode)
	}
	got, err := env.store.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.SubmissionApproved {
		t.Fatalf("status=%d", got.Status)
	}
	if env.onChange.Load() != 1 {
		t.Fatalf("onChange=%d", env.onChange.Load())
	}
	resp = env.do(t, http.MethodPost, "/admin/submissions/"+u64(subID)+"/approve", url.Values{
		"csrf_token": {token},
		"content":    {"审核测试内容"},
		"category":   {"original"},
	}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second approve status=%d", resp.StatusCode)
	}
}

func TestRejectRequiresReason(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()
	subID, err := env.store.CreateSubmission(ctx, store.NewSubmission{
		Content: "拒绝测试", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: netip.MustParseAddr("203.0.113.2"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	env.login(t)
	detail := env.do(t, http.MethodGet, "/admin/submissions/"+u64(subID), nil, nil)
	body, _ := io.ReadAll(detail.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/submissions/"+u64(subID)+"/reject", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("reject without reason status=%d", resp.StatusCode)
	}
}

func TestSentenceEditUnchanged(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()
	uuid, err := env.store.CreateSentence(ctx, store.SentenceFields{
		Content: "句子内容", CategoryCode: "original", Source: "出处", Author: "作者",
	})
	if err != nil {
		t.Fatal(err)
	}
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/sentences/"+uuid, nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/sentences/"+uuid, url.Values{
		"csrf_token": {token},
		"content":    {"句子内容"},
		"category":   {"original"},
		"source":     {"出处"},
		"author":     {"作者"},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "无变化") {
		t.Fatalf("body=%s", string(out))
	}
}

func TestCategoryDisableConfirm(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()
	uuid, err := env.store.CreateSentence(ctx, store.SentenceFields{
		Content: "分类停用测试", CategoryCode: "original", Source: "s", Author: "a",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = uuid
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/categories/original", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/categories/original/disable", url.Values{
		"csrf_token": {token},
		"confirm":    {"999"},
	}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("wrong confirm status=%d", resp.StatusCode)
	}
	resp = env.do(t, http.MethodPost, "/admin/categories/original/disable", url.Values{
		"csrf_token": {token},
		"confirm":    {"1"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable status=%d", resp.StatusCode)
	}
	cat, err := env.store.GetCategory(ctx, "original")
	if err != nil {
		t.Fatal(err)
	}
	if cat.Enabled {
		t.Fatal("category still enabled")
	}
}

func TestDisableSelf(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/users", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/users/"+u64(env.adminID)+"/disable", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestDisableLastAdmin(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := env.store.CreateAdmin(ctx, "admin2", hash, &env.adminID)
	if err != nil {
		t.Fatal(err)
	}
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/users", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/users/"+u64(id2)+"/disable", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable admin2 status=%d", resp.StatusCode)
	}
	if err := env.store.SetAdminEnabled(ctx, env.adminID, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("store last admin: %v", err)
	}
	page = env.do(t, http.MethodGet, "/admin/users", nil, nil)
	body, _ = io.ReadAll(page.Body)
	token = csrfFromPage(t, string(body))
	resp = env.do(t, http.MethodPost, "/admin/users/"+u64(env.adminID)+"/disable", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disable self as last admin status=%d body=%s", resp.StatusCode, readBody(resp))
	}
	if err := env.store.SetAdminEnabled(ctx, id2, true); err != nil {
		t.Fatal(err)
	}
	resp = env.do(t, http.MethodPost, "/admin/login", url.Values{
		"username": {"admin2"},
		"password": {"password123"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login admin2 status=%d", resp.StatusCode)
	}
	page = env.do(t, http.MethodGet, "/admin/users", nil, nil)
	body, _ = io.ReadAll(page.Body)
	token = csrfFromPage(t, string(body))
	resp = env.do(t, http.MethodPost, "/admin/users/"+u64(env.adminID)+"/disable", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable admin1 as admin2 status=%d", resp.StatusCode)
	}
	page = env.do(t, http.MethodGet, "/admin/users", nil, nil)
	body, _ = io.ReadAll(page.Body)
	token = csrfFromPage(t, string(body))
	resp = env.do(t, http.MethodPost, "/admin/users/"+u64(id2)+"/disable", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disable self as sole admin status=%d body=%s", resp.StatusCode, readBody(resp))
	}
}

func readBody(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestLogout(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/logout", url.Values{
		"csrf_token": {token},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status=%d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge >= 0 && c.Value != "" {
			t.Fatal("session cookie not cleared")
		}
	}
	resp = env.do(t, http.MethodGet, "/admin/", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("after logout status=%d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func u64(n uint64) string {
	return strconv.FormatUint(n, 10)
}
