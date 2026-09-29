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
	"sync"
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

var testFormTokens = render.NewFormTokens([]byte("01234567890123456789012345678901"))

type testEnv struct {
	store    *store.Store
	renderer *render.Renderer
	handler  http.Handler
	client   *http.Client
	metrics  *testMetrics
	onChange atomic.Int32
	adminID  uint64
}

func setupEnv(t *testing.T) *testEnv {
	return setupEnvWithImports(t, nil)
}

func setupEnvWithImports(t *testing.T, imports admin.ImportService) *testEnv {
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
	env := &testEnv{store: st, renderer: renderer, metrics: metrics, adminID: adminID}
	h, err := admin.New(admin.Deps{
		Store:          st,
		Renderer:       renderer,
		Metrics:        metrics,
		Logins:         auth.NewLoginLimiter(10, 5, 15*time.Minute, 100000),
		Tokens:         testFormTokens,
		CookieSecure:   false,
		OnChange:       func() { env.onChange.Add(1) },
		Imports:        imports,
		ImportMaxBytes: 1024,
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

func formTokenFromPage(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="form_token" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("form token not found")
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("form token malformed")
	}
	return rest[:j]
}

func (e *testEnv) loginFormToken(t *testing.T) string {
	t.Helper()
	page := e.do(t, http.MethodGet, "/admin/login", nil, nil)
	body, _ := io.ReadAll(page.Body)
	return formTokenFromPage(t, string(body))
}

func (e *testEnv) login(t *testing.T) {
	t.Helper()
	token := e.loginFormToken(t)
	resp := e.do(t, http.MethodPost, "/admin/login", url.Values{
		"form_token": {token},
		"username":   {"admin1"},
		"password":   {"password123"},
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
		{"/admin/users/1/reset-password", "/admin/users/{id}/reset-password"},
		{"/admin/settings", "/admin/settings"},
		{"/admin", "/admin/"},
		{"/admin/sentences/a", "unmatched"},
		{"/admin/unknown", "unmatched"},
		{"/other", "unmatched"},
	}
	for _, tt := range tests {
		if got := admin.RouteName(tt.path); got != tt.want {
			t.Fatalf("RouteName(%q)=%q want %q", tt.path, got, tt.want)
		}
	}
}

func TestUserResetPasswordPageAndSelfReset(t *testing.T) {
	env := setupEnv(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	id, err := env.store.CreateAdmin(ctx, "admin2", hash, &env.adminID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/users/" + u64(id) + "/reset-password"
	resp := env.do(t, http.MethodGet, path, nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	env.login(t)
	for _, badPath := range []string{"/admin/users/not-a-number/reset-password", "/admin/users/999999/reset-password"} {
		resp = env.do(t, http.MethodGet, badPath, nil, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status=%d", badPath, resp.StatusCode)
		}
	}
	resp = env.do(t, http.MethodGet, path, nil, nil)
	body := readBody(resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "admin2") || !strings.Contains(body, "ID "+u64(id)) || !strings.Contains(body, "现有登录会话将失效") {
		t.Fatalf("reset page status=%d body=%s", resp.StatusCode, body)
	}
	if strings.Contains(body, hash) || strings.Contains(body, `value="password123"`) {
		t.Fatal("reset page exposed password material")
	}
	csrf := csrfFromPage(t, body)
	resp = env.do(t, http.MethodPost, path, url.Values{
		"password": {"new-password-1"}, "password_confirm": {"different-password"}, "csrf_token": {csrf},
	}, nil)
	body = readBody(resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "两次输入的密码不一致") || strings.Contains(body, "new-password-1") {
		t.Fatalf("mismatch status=%d body=%s", resp.StatusCode, body)
	}
	resp = env.do(t, http.MethodPost, path, url.Values{
		"password": {"new-password-1"}, "password_confirm": {"new-password-1"},
	}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", resp.StatusCode)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	env.client.Jar = jar
	token := env.loginFormToken(t)
	resp = env.do(t, http.MethodPost, "/admin/login", url.Values{
		"form_token": {token}, "username": {"admin2"}, "password": {"password123"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin2 login status=%d", resp.StatusCode)
	}
	resp = env.do(t, http.MethodGet, path, nil, nil)
	csrf = csrfFromPage(t, readBody(resp))
	resp = env.do(t, http.MethodPost, path, url.Values{
		"csrf_token": {csrf}, "password": {"new-password-2"}, "password_confirm": {"new-password-2"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/users" {
		t.Fatalf("success status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp = env.do(t, http.MethodGet, "/admin/users", nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("self-reset session remained valid: status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, err := env.store.GetAdminByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := auth.VerifyPassword(got.PasswordHash, "new-password-2")
	if err != nil || !ok {
		t.Fatalf("new password rejected ok=%t err=%v", ok, err)
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
		token := env.loginFormToken(t)
		resp := env.do(t, http.MethodPost, "/admin/login", url.Values{
			"form_token": {token},
			"username":   {"admin1"},
			"password":   {"wrongpass"},
		}, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", i+1, resp.StatusCode)
		}
	}
	token := env.loginFormToken(t)
	resp := env.do(t, http.MethodPost, "/admin/login", url.Values{
		"form_token": {token},
		"username":   {"admin1"},
		"password":   {"wrongpass"},
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
		Tokens: testFormTokens,
	})
	if err != nil {
		t.Fatal(err)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	getRec := httptest.NewRecorder()
	secureHandler.ServeHTTP(getRec, getReq)
	getBody, _ := io.ReadAll(getRec.Result().Body)
	token := formTokenFromPage(t, string(getBody))
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(url.Values{
		"form_token": {token},
		"username":   {"admin1"},
		"password":   {"password123"},
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

func TestLoginFormTokenRequired(t *testing.T) {
	env := setupEnv(t)
	resp := env.do(t, http.MethodPost, "/admin/login", url.Values{
		"username": {"admin1"},
		"password": {"password123"},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing token status=%d", resp.StatusCode)
	}
}

func TestLoginCrossSiteDenied(t *testing.T) {
	env := setupEnv(t)
	token := env.loginFormToken(t)
	resp := env.do(t, http.MethodPost, "/admin/login", url.Values{
		"form_token": {token},
		"username":   {"admin1"},
		"password":   {"password123"},
	}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-site login status=%d", resp.StatusCode)
	}
}

func TestSentenceCreateRequiresSourceOrAuthor(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/sentences/new", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	resp := env.do(t, http.MethodPost, "/admin/sentences", url.Values{
		"csrf_token": {token},
		"content":    {"后台新增句子"},
		"category":   {"original"},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create without source/author status=%d body=%s", resp.StatusCode, readBody(resp))
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
	token = env.loginFormToken(t)
	resp = env.do(t, http.MethodPost, "/admin/login", url.Values{
		"form_token": {token},
		"username":   {"admin2"},
		"password":   {"password123"},
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

func TestAdminLoginUsesDedicatedShell(t *testing.T) {
	env := setupEnv(t)
	resp := env.do(t, http.MethodGet, "/admin/login", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, `href="/static/admin.css"`) || !strings.Contains(s, "admin-login-card") || !strings.Contains(s, `src="/static/theme.js"`) {
		t.Fatalf("login missing admin shell:\n%s", s)
	}
	for _, leak := range []string{`href="/docs"`, "接口文档", `href="/submit"`, "hitokoto-osc"} {
		if strings.Contains(s, leak) {
			t.Fatalf("login leaked public chrome %q", leak)
		}
	}
}

func TestAdminHomeUsesSidebar(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	resp := env.do(t, http.MethodGet, "/admin/", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", resp.StatusCode, s)
	}
	for _, want := range []string{`class="admin-app"`, "概况", "已发布句子", "读接口调用", `href="/admin/submissions"`, `href="/admin/sentences"`, `href="/admin/settings"`, "admin1", `href="/static/admin.css"`, `data-theme-toggle`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	for _, leak := range []string{"hitokoto-osc", `href="/docs"`, `href="/submit"`} {
		if strings.Contains(s, leak) {
			t.Fatalf("overview leaked public chrome %q", leak)
		}
	}
	pending := env.do(t, http.MethodGet, "/admin/submissions", nil, nil)
	pbody, _ := io.ReadAll(pending.Body)
	if pending.StatusCode != http.StatusOK || !strings.Contains(string(pbody), "待审投稿") {
		t.Fatalf("pending status=%d body=%s", pending.StatusCode, pbody)
	}
}

func TestSiteSettingsSave(t *testing.T) {
	env := setupEnv(t)
	resp := env.do(t, http.MethodGet, "/admin/settings", nil, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unauth status=%d", resp.StatusCode)
	}
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/settings", nil, nil)
	body, _ := io.ReadAll(page.Body)
	s := string(body)
	if page.StatusCode != http.StatusOK {
		t.Fatalf("get status=%d body=%s", page.StatusCode, s)
	}
	if !strings.Contains(s, "站点设置") || !strings.Contains(s, `name="site_name"`) {
		t.Fatalf("missing settings form: %s", s)
	}
	token := csrfFromPage(t, s)
	saved := env.do(t, http.MethodPost, "/admin/settings", url.Values{
		"csrf_token":    {token},
		"site_name":     {"测试站"},
		"english_name":  {"Test Site"},
		"slogan":        {"每日一句"},
		"contact":       {"ops@example.com"},
		"public_origin": {"https://example.com/"},
		"repo_url":      {"https://github.com/x/y"},
		"beian_text":    {"京ICP备1号"},
		"beian_url":     {"https://www.beian.gov.cn/portal/registerSystemInfo?recordcode=1"},
	}, nil)
	if saved.StatusCode != http.StatusSeeOther {
		t.Fatalf("save status=%d", saved.StatusCode)
	}
	got, err := env.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "测试站" || got.EnglishName != "Test Site" || got.Slogan != "每日一句" || got.Contact != "ops@example.com" || got.PublicOrigin != "https://example.com" || got.BeianText != "京ICP备1号" {
		t.Fatalf("stored=%+v", got)
	}
	again := env.do(t, http.MethodGet, "/admin/settings", nil, nil)
	againBody, _ := io.ReadAll(again.Body)
	out := string(againBody)
	if !strings.Contains(out, "测试站") || !strings.Contains(out, "Test Site") || !strings.Contains(out, "每日一句") || !strings.Contains(out, "ops@example.com") || !strings.Contains(out, "京ICP备1号") {
		t.Fatalf("live site not updated: %s", out)
	}
	bad := env.do(t, http.MethodPost, "/admin/settings", url.Values{
		"csrf_token": {csrfFromPage(t, out)},
		"site_name":  {"测试站"},
		"contact":    {"ops@example.com"},
		"beian_url":  {"https://beian.miit.gov.cn/"},
	}, nil)
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid save status=%d", bad.StatusCode)
	}
	if live := env.renderer.Site(); live.Name != "测试站" || live.Contact != "ops@example.com" {
		t.Fatalf("invalid save changed live site: %+v", live)
	}

	afterBad := env.do(t, http.MethodPost, "/admin/settings", url.Values{
		"csrf_token": {csrfFromPage(t, out)},
		"site_name":  {"锁已释放"},
		"contact":    {"after@example.com"},
	}, nil)
	if afterBad.StatusCode != http.StatusSeeOther {
		t.Fatalf("valid save after invalid status=%d", afterBad.StatusCode)
	}
}

func TestConcurrentSiteSettingsSaveKeepsDatabaseAndRendererTogether(t *testing.T) {
	env := setupEnv(t)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/settings", nil, nil)
	token := csrfFromPage(t, readBody(page))

	const saves = 24
	start := make(chan struct{})
	errCh := make(chan string, saves)
	var wg sync.WaitGroup
	for i := 0; i < saves; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			name := "并发站点-" + strconv.Itoa(i)
			contact := "ops-" + strconv.Itoa(i) + "@example.com"
			resp := env.do(t, http.MethodPost, "/admin/settings", url.Values{
				"csrf_token": {token},
				"site_name":  {name},
				"contact":    {contact},
			}, nil)
			if resp.StatusCode != http.StatusSeeOther {
				errCh <- "status=" + strconv.Itoa(resp.StatusCode)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	stored, err := env.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	live := env.renderer.Site()
	if live.Name != stored.Name || live.Contact != stored.Contact ||
		live.EnglishName != stored.EnglishName || live.Slogan != stored.Slogan ||
		live.PublicOrigin != stored.PublicOrigin || live.RepoURL != stored.RepoURL ||
		live.BeianText != stored.BeianText || live.BeianURL != stored.BeianURL {
		t.Fatalf("database and renderer diverged: stored=%+v live=%+v", stored, live)
	}
}

func u64(n uint64) string {
	return strconv.FormatUint(n, 10)
}
