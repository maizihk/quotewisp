package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
)

type provider struct {
	s     *snapshot.Snapshot
	calls atomic.Int64
}

func (p *provider) Current() *snapshot.Snapshot { p.calls.Add(1); return p.s }

func fixture(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	b := snapshot.NewBuilder(9007199254740993, time.Unix(100, 0))
	for _, c := range []snapshot.CategoryRow{{Code: "a", Name: "A"}, {Code: "b", Name: "B"}, {Code: "empty", Name: "Empty"}} {
		if e := b.AddCategory(c); e != nil {
			t.Fatal(e)
		}
	}
	rows := []snapshot.Sentence{{ID: 1, UUID: "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", Content: "x", Category: "a", Length: 1}, {ID: 2, UUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Content: "yy", Category: "b", Length: 2}}
	for _, r := range rows {
		r.UUID = strings.ToLower(r.UUID)
		if e := b.AddSentence(r); e != nil {
			t.Fatal(e)
		}
	}
	s, e := b.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func handler(t *testing.T, p *provider, mutate func(*Options)) http.Handler {
	t.Helper()
	o := Options{Snapshots: p, Metrics: observability.NewMetrics(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Build: BuildInfo{Version: "1"}, RandomOffset: func(n uint64) uint64 { return n - 1 }}
	if mutate != nil {
		mutate(&o)
	}
	return New(o)
}
func request(h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRandomValidation(t *testing.T) {
	p := &provider{s: fixture(t)}
	h := handler(t, p, nil)
	tests := []struct {
		q    string
		want int
	}{{"", 200}, {"?categories=", 400}, {"?categories=a,,b", 400}, {"?categories=A", 400}, {"?categories=nope", 400}, {"?min_length=0&max_length=0", 404}, {"?min_length=1000&max_length=1000", 404}, {"?min_length=-1", 400}, {"?min_length=%2B1", 400}, {"?min_length=", 400}, {"?min_length=x", 400}, {"?min_length=999999999999999999999", 400}, {"?min_length=50", 400}, {"?min_length=1&min_length=2", 400}, {"?wat=1", 400}, {"?categories=a%ZZ", 400}, {"?callback=x", 400}}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			w := request(h, "GET", "/api/v1/sentences/random"+tt.q, nil)
			if w.Code != tt.want {
				t.Fatalf("got %d body %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
}

func TestRandomCandidateBoundariesAndSingleSnapshotRead(t *testing.T) {
	for off, want := range []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"} {
		p := &provider{s: fixture(t)}
		h := handler(t, p, func(o *Options) { o.RandomOffset = func(uint64) uint64 { return uint64(off) } })
		w := request(h, "GET", "/api/v1/sentences/random?categories=a,b&max_length=2", nil)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		var out struct {
			Data struct {
				UUID string `json:"uuid"`
			} `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if out.Data.UUID != want {
			t.Fatalf("offset %d got %s", off, out.Data.UUID)
		}
		assertSentenceContract(t, w.Body.Bytes())
		if p.calls.Load() != 1 {
			t.Fatalf("Current called %d times", p.calls.Load())
		}
	}
}

func assertSentenceContract(t *testing.T, b []byte) {
	t.Helper()
	var envelope map[string]json.RawMessage
	if e := json.Unmarshal(b, &envelope); e != nil {
		t.Fatal(e)
	}
	var data map[string]json.RawMessage
	if e := json.Unmarshal(envelope["data"], &data); e != nil {
		t.Fatal(e)
	}
	want := map[string]bool{"uuid": true, "content": true, "category": true, "source": true, "author": true, "length": true}
	if len(data) != len(want) {
		t.Fatalf("sentence fields=%v", data)
	}
	for k := range data {
		if !want[k] {
			t.Fatalf("legacy or unknown sentence field %q", k)
		}
	}
	for k := range want {
		if _, ok := data[k]; !ok {
			t.Fatalf("missing sentence field %q", k)
		}
	}
}

func TestUUIDCategoriesHEADAndMethods(t *testing.T) {
	p := &provider{s: fixture(t)}
	h := handler(t, p, nil)
	cases := []struct {
		method, path string
		status       int
		body         bool
	}{{"GET", "/api/v1/sentences/AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", 200, true}, {"GET", "/api/v1/sentences/bad", 400, true}, {"GET", "/api/v1/sentences/a/b", 404, true}, {"GET", "/api/v1/sentences/bbbbbbbb-bbbb-4bbb-8bbb-000000000000", 404, true}, {"GET", "/api/v1/categories", 200, true}, {"HEAD", "/api/v1/categories", 200, false}, {"HEAD", "/api/v1/sentences/bad", 400, false}, {"OPTIONS", "/api/v1/categories", 204, false}, {"POST", "/api/v1/categories", 405, true}, {"GET", "/", 404, true}}
	for _, c := range cases {
		w := request(h, c.method, c.path, nil)
		if w.Code != c.status {
			t.Errorf("%s %s got %d", c.method, c.path, w.Code)
		}
		if (w.Body.Len() > 0) != c.body {
			t.Errorf("%s %s body=%q", c.method, c.path, w.Body.String())
		}
		if w.Header().Get("X-Request-ID") == "" {
			t.Error("request id missing")
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Error("security header missing")
		}
		if c.status == 200 && c.method == "GET" && strings.HasPrefix(c.path, "/api/v1/sentences/") {
			assertSentenceContract(t, w.Body.Bytes())
		}
	}
}

func TestQueryLimitBodyAndProblem(t *testing.T) {
	h := handler(t, &provider{s: fixture(t)}, nil)
	w := request(h, "GET", "/api/v1/sentences/random?x="+strings.Repeat("a", 4096), nil)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = request(h, "GET", "/api/v1/categories", strings.NewReader("x"))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	var p map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
		t.Fatal(e)
	}
	if p["type"] != "about:blank" || p["code"] != "invalid-parameter" {
		t.Fatalf("bad problem: %v", p)
	}
}

func TestCategoryParsingLimits(t *testing.T) {
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = "c" + string(rune('a'+i))
	}
	q := map[string][]string{"categories": {strings.Join(append(twenty, twenty[0]), ",")}}
	out, e := parseCategories(q)
	if e != nil || len(out) != 20 {
		t.Fatalf("dedup: %v %d", e, len(out))
	}
	q["categories"] = []string{strings.Join(append(twenty, "overflow"), ",")}
	if _, e = parseCategories(q); e == nil {
		t.Fatal("accepted more than 20 categories")
	}
}

func TestCORS(t *testing.T) {
	h := handler(t, &provider{s: fixture(t)}, func(o *Options) { o.CORSOrigins = []string{"https://allowed.example"} })
	r := httptest.NewRequest("OPTIONS", "/api/v1/categories", nil)
	r.Header.Set("Origin", "https://allowed.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Headers", "Accept, Content-Type")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("preflight %d %#v", w.Code, w.Header())
	}
	r = httptest.NewRequest("OPTIONS", "/api/v1/categories", nil)
	r.Header.Set("Origin", "https://bad.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("OPTIONS", "/api/v1/unknown", nil)
	r.Header.Set("Origin", "https://bad.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("unknown API path preflight got %d", w.Code)
	}
	w = request(h, "GET", "/api/v1/categories", nil)
	if !strings.Contains(w.Header().Get("Vary"), "Origin") {
		t.Fatal("Vary Origin missing without Origin request")
	}
}

func TestTrustedProxy(t *testing.T) {
	var got string
	h := handler(t, &provider{s: fixture(t)}, func(o *Options) {
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
		o.Logger = slog.New(slog.NewJSONHandler(writerFunc(func(p []byte) (int, error) {
			var v map[string]any
			_ = json.Unmarshal(p, &v)
			if x, ok := v["client_ip"].(string); ok {
				got = x
			}
			return len(p), nil
		}), nil))
	})
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.RemoteAddr = "10.0.0.2:123"
	r.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got != "198.51.100.1" {
		t.Fatalf("got %q", got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestReload(t *testing.T) {
	p := &provider{s: fixture(t)}
	var done func(bool, error)
	accepted := true
	h := handler(t, p, func(o *Options) {
		o.ReloadToken = strings.Repeat("x", 32)
		o.StartReload = func(_ context.Context, force bool, d func(bool, error)) bool { done = d; return accepted }
	})
	r := httptest.NewRequest("POST", "/internal/reload", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	done(true, nil)
	accepted = false
	r = httptest.NewRequest("POST", "/internal/reload", nil)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	w = request(h, "POST", "/internal/reload", nil)
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal(w.Code)
	}
}

func TestReloadNotRegistered(t *testing.T) {
	h := handler(t, &provider{s: fixture(t)}, nil)
	if w := request(h, "POST", "/internal/reload", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestReloadAuthenticatesBeforeInputAndReportsStopping(t *testing.T) {
	token := strings.Repeat("x", 32)
	p := &provider{s: fixture(t)}
	h := handler(t, p, func(o *Options) {
		o.ReloadToken = token
		o.StartReload = func(context.Context, bool, func(bool, error)) bool { return true }
	})
	w := request(h, "POST", "/internal/reload?x=1", strings.NewReader("body"))
	if w.Code != 401 {
		t.Fatalf("unauthenticated malformed reload=%d", w.Code)
	}
	r := httptest.NewRequest("POST", "/internal/reload?x=1", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("authenticated malformed reload=%d", w.Code)
	}
	h = handler(t, p, func(o *Options) {
		o.ReloadToken = token
		o.IsStopping = func() bool { return true }
		o.StartReload = func(context.Context, bool, func(bool, error)) bool { return false }
	})
	r = httptest.NewRequest("POST", "/internal/reload", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("stopping reload=%d", w.Code)
	}
}

func TestMetricsHEADHasNoBody(t *testing.T) {
	h := handler(t, &provider{s: fixture(t)}, nil)
	w := request(h, "HEAD", "/metrics", nil)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
	}
}

func TestPanicRecoveryBeforeAndAfterCommit(t *testing.T) {
	a := &api{o: Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Metrics: observability.NewMetrics()}}
	before := a.access(a.recover(a.security(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret") }))))
	w := httptest.NewRecorder()
	before.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("bad recovery: %d %q", w.Code, w.Body.String())
	}
	after := a.access(a.recover(a.security(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte("partial"))
		panic("secret")
	}))))
	w = httptest.NewRecorder()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		after.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("committed panic recovery=%v", recovered)
	}
	if w.Code != 201 || w.Body.String() != "partial" {
		t.Fatalf("committed response changed: %d %q", w.Code, w.Body.String())
	}
	var mb strings.Builder
	a.o.Metrics.WritePrometheus(&mb)
	if !strings.Contains(mb.String(), `route="/healthz",status="201"} 1`) {
		t.Fatalf("panic request metric missing: %s", mb.String())
	}
}

func BenchmarkRandomRequest(b *testing.B) {
	build := func(n int) *snapshot.Snapshot {
		sb := snapshot.NewBuilder(1, time.Now())
		_ = sb.AddCategory(snapshot.CategoryRow{Code: "a", Name: "A"})
		for i := 1; i <= n; i++ {
			id := fmt.Sprintf("00000000-0000-4000-8000-%012x", i)
			_ = sb.AddSentence(snapshot.Sentence{ID: uint64(i), UUID: id, Content: "x", Category: "a", Length: 1})
		}
		s, _ := sb.Build(context.Background())
		return s
	}
	for _, n := range []int{100, 10000} {
		b.Run(fmt.Sprintf("sentences_%d", n), func(b *testing.B) {
			h := handler(&testing.T{}, &provider{s: build(n)}, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/sentences/random?categories=a&min_length=1&max_length=1", nil))
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
	}
}

func FuzzQueryParameters(f *testing.F) {
	f.Add("categories=a&min_length=0&max_length=1")
	f.Add("%zz")
	f.Fuzz(func(t *testing.T, q string) {
		r := httptest.NewRequest("GET", "/api/v1/sentences/random", nil)
		r.URL.RawQuery = q
		vals, e := parseQuery(r, 4096, map[string]bool{"categories": true, "min_length": true, "max_length": true})
		if e != nil {
			return
		}
		cats, e := parseCategories(vals)
		if e != nil {
			return
		}
		if len(cats) > 20 {
			t.Fatal("too many categories")
		}
		min, e := parseLength(vals, "min_length", 0)
		if e != nil {
			return
		}
		max, e := parseLength(vals, "max_length", 30)
		if e == nil && (min > 1000 || max > 1000) {
			t.Fatalf("length out of range: %d %d", min, max)
		}
	})
}
func FuzzUUIDPath(f *testing.F) {
	f.Add("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	f.Add("bad")
	f.Fuzz(func(t *testing.T, s string) {
		u, ok := normalizeUUID(s)
		if ok {
			parsed, e := uuid.Parse(s)
			if e != nil || parsed.String() != u || len(u) != 36 || u != strings.ToLower(u) {
				t.Fatalf("invalid normalized UUID %q -> %q (%v)", s, u, e)
			}
		}
	})
}
func FuzzCategories(f *testing.F) {
	f.Add("a,b")
	f.Add("a,,b")
	f.Fuzz(func(t *testing.T, s string) {
		v := make([]string, 1)
		v[0] = s
		out, e := parseCategories(map[string][]string{"categories": v})
		if e == nil {
			if len(out) > 20 {
				t.Fatal("too many")
			}
			for _, c := range out {
				if !validCategory(c) {
					t.Fatal("invalid category accepted")
				}
			}
		}
	})
}
func FuzzProblem(f *testing.F) {
	f.Add("detail")
	f.Add("\xffsecret")
	f.Fuzz(func(t *testing.T, d string) {
		r := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		problem(w, r, 400, "bad", "invalid-parameter", d)
		if !json.Valid(w.Body.Bytes()) {
			t.Fatal("invalid JSON")
		}
		var got map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &got); e != nil {
			t.Fatal(e)
		}
		if got["type"] != "about:blank" || got["code"] != "invalid-parameter" || got["status"] != float64(400) || (utf8.ValidString(d) && got["detail"] != d) {
			t.Fatalf("problem fields changed: %#v", got)
		}
		body := w.Body.String()
		for _, secret := range []string{"MYSQL_DSN", "runtime/debug.Stack", "/home/andan"} {
			if !strings.Contains(d, secret) && strings.Contains(body, secret) {
				t.Fatalf("internal value leaked: %s", secret)
			}
		}
	})
}
