package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
)

type SnapshotProvider interface{ Current() *snapshot.Snapshot }
type StartReloadFunc func(context.Context, bool, func(bool, error)) bool
type BuildInfo struct{ Version, GitCommit, BuildTime string }
type Options struct {
	Snapshots        SnapshotProvider
	StartReload      StartReloadFunc
	LifecycleContext context.Context
	Metrics          *observability.Metrics
	Logger           *slog.Logger
	TrustedProxies   []netip.Prefix
	CORSOrigins      []string
	ReloadToken      string
	Build            BuildInfo
	IsStopping       func() bool
	RandomOffset     func(uint64) uint64
}

type api struct {
	o       Options
	origins map[string]struct{}
}

func New(o Options) http.Handler {
	if o.LifecycleContext == nil {
		o.LifecycleContext = context.Background()
	}
	if o.Metrics == nil {
		o.Metrics = observability.NewMetrics()
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.IsStopping == nil {
		o.IsStopping = func() bool { return false }
	}
	a := &api{o: o, origins: make(map[string]struct{}, len(o.CORSOrigins))}
	for _, v := range o.CORSOrigins {
		a.origins[v] = struct{}{}
	}
	return a.requestID(a.clientIP(a.access(a.recover(a.security(a.cors(http.HandlerFunc(a.route)))))))
}

type ctxKey uint8

const (
	requestIDKey ctxKey = iota
	clientIPKey
	routeKey
)

func RequestID(ctx context.Context) string { v, _ := ctx.Value(requestIDKey).(string); return v }
func ClientIP(ctx context.Context) string  { v, _ := ctx.Value(clientIPKey).(string); return v }
func routeName(r *http.Request) string {
	v, _ := r.Context().Value(routeKey).(string)
	if v == "" {
		return "unmatched"
	}
	return v
}

func (a *api) classify(path string) string {
	switch {
	case path == "/api/v1/sentences/random":
		return path
	case strings.HasPrefix(path, "/api/v1/sentences/") && len(path) > len("/api/v1/sentences/") && !strings.Contains(strings.TrimPrefix(path, "/api/v1/sentences/"), "/"):
		return "/api/v1/sentences/{uuid}"
	case path == "/api/v1/categories" || path == "/healthz" || path == "/readyz" || path == "/metrics" || path == "/version":
		return path
	case path == "/internal/reload" && a.o.ReloadToken != "":
		return path
	}
	return "unmatched"
}

func (a *api) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newUUID()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}
func newUUID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (a *api) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				a.o.Logger.Error("http_panic", "request_id", RequestID(r.Context()), "route", routeName(r))
				if cw, ok := w.(interface{ Committed() bool }); ok && cw.Committed() {
					panic(http.ErrAbortHandler)
				}
				problem(w, r, 500, "内部错误", "internal-error", "请求处理失败")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type observedWriter struct {
	http.ResponseWriter
	status, bytes int
}

func (w *observedWriter) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
	w.ResponseWriter.WriteHeader(n)
}
func (w *observedWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, e := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, e
}
func (w *observedWriter) Committed() bool { return w.status != 0 }

type headWriter struct{ *observedWriter }

func (w headWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return len(p), nil
}
func (a *api) access(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), routeKey, a.classify(r.URL.Path)))
		ow := &observedWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			status := ow.status
			if status == 0 {
				status = 200
			}
			route := routeName(r)
			d := time.Since(start)
			a.o.Metrics.ObserveHTTP(r.Method, route, status, d)
			a.o.Logger.Info("http_request", "request_id", RequestID(r.Context()), "method", observability.MetricMethod(r.Method), "route", observability.MetricRoute(route), "status", status, "duration_ms", float64(d.Microseconds())/1000, "response_bytes", ow.bytes, "client_ip", ClientIP(r.Context()))
		}()
		if r.Method == "HEAD" {
			next.ServeHTTP(headWriter{ow}, r)
		} else {
			next.ServeHTTP(ow, r)
		}
	})
}
func (a *api) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (a *api) clientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct := remoteIP(r.RemoteAddr)
		ip := direct
		if direct.IsValid() && contains(a.o.TrustedProxies, direct) {
			if p, ok := forwardedIP(r.Header.Values("X-Forwarded-For"), a.o.TrustedProxies); ok {
				ip = p
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip.String())))
	})
}
func remoteIP(s string) netip.Addr {
	host, _, e := net.SplitHostPort(s)
	if e != nil {
		host = s
	}
	a, e := netip.ParseAddr(host)
	if e != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
func contains(ps []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
func forwardedIP(lines []string, trusted []netip.Prefix) (netip.Addr, bool) {
	var all []netip.Addr
	for _, line := range lines {
		for _, part := range strings.Split(line, ",") {
			part = strings.Trim(part, " \t")
			if part == "" || len(all) >= 32 {
				return netip.Addr{}, false
			}
			a, e := netip.ParseAddr(part)
			if e != nil || a.Zone() != "" {
				return netip.Addr{}, false
			}
			all = append(all, a.Unmap())
		}
	}
	if len(all) == 0 {
		return netip.Addr{}, false
	}
	for i := len(all) - 1; i >= 0; i-- {
		if !contains(trusted, all[i]) {
			return all[i], true
		}
	}
	return all[0], true
}

func (a *api) route(w http.ResponseWriter, r *http.Request) {
	route := routeName(r)
	if route == "/api/v1/sentences/random" || route == "/api/v1/sentences/{uuid}" || route == "/api/v1/categories" || route == "/internal/reload" {
		w.Header().Set("Cache-Control", "no-store")
	}
	if route == "unmatched" {
		problem(w, r, 404, "未找到", "not-found", "请求的资源不存在")
		return
	}
	allow := "GET, HEAD, OPTIONS"
	if route == "/internal/reload" {
		allow = "POST"
	}
	if !methodAllowed(r.Method, allow) {
		w.Header().Set("Allow", allow)
		problem(w, r, 405, "方法不允许", "method-not-allowed", "该路径不支持此方法")
		return
	}
	if r.Method == "OPTIONS" {
		w.Header().Set("Allow", allow)
		w.WriteHeader(204)
		return
	}
	if route != "/internal/reload" && r.ContentLength != 0 {
		problem(w, r, 400, "参数无效", "invalid-parameter", "读取接口不接受请求体")
		return
	}
	switch route {
	case "/api/v1/sentences/random":
		a.random(w, r)
	case "/api/v1/sentences/{uuid}":
		a.byUUID(w, r)
	case "/api/v1/categories":
		a.categories(w, r)
	case "/healthz":
		a.health(w, r)
	case "/readyz":
		a.ready(w, r)
	case "/metrics":
		a.metrics(w, r)
	case "/version":
		a.version(w, r)
	case "/internal/reload":
		a.reload(w, r)
	}
}
func methodAllowed(method, allow string) bool {
	for _, v := range strings.Split(allow, ", ") {
		if method == v {
			return true
		}
	}
	return false
}

type sentenceJSON struct {
	UUID     string `json:"uuid"`
	Content  string `json:"content"`
	Category string `json:"category"`
	Source   string `json:"source"`
	Author   string `json:"author"`
	Length   uint16 `json:"length"`
}

func sentenceOut(s *snapshot.Sentence) sentenceJSON {
	return sentenceJSON{UUID: s.UUID, Content: s.Content, Category: s.Category, Source: s.Source, Author: s.Author, Length: s.Length}
}
func (a *api) random(w http.ResponseWriter, r *http.Request) {
	q, e := parseQuery(r, 4096, map[string]bool{"categories": true, "min_length": true, "max_length": true})
	if e != nil {
		problem(w, r, 400, "参数无效", "invalid-parameter", e.Error())
		return
	}
	cats, e := parseCategories(q)
	if e != nil {
		problem(w, r, 400, "参数无效", "invalid-parameter", e.Error())
		return
	}
	min, e := parseLength(q, "min_length", 0)
	if e != nil {
		problem(w, r, 400, "参数无效", "invalid-parameter", e.Error())
		return
	}
	max, e := parseLength(q, "max_length", 30)
	if e != nil {
		problem(w, r, 400, "参数无效", "invalid-parameter", e.Error())
		return
	}
	if max < min {
		problem(w, r, 400, "参数无效", "invalid-parameter", "max_length 必须大于等于 min_length")
		return
	}
	s := a.o.Snapshots.Current()
	if s == nil {
		problem(w, r, 503, "服务未就绪", "not-ready", "当前没有可用数据")
		return
	}
	found, e := s.Select(cats, min, max, a.o.RandomOffset)
	if errors.Is(e, snapshot.ErrUnknownCategory) {
		problem(w, r, 400, "参数无效", "invalid-parameter", "包含未知分类")
		return
	}
	if errors.Is(e, snapshot.ErrNotFound) {
		problem(w, r, 404, "未找到", "not-found", "没有符合条件的语句")
		return
	}
	if e != nil {
		panic(e)
	}
	success(w, r, map[string]any{"data": sentenceOut(found), "meta": map[string]string{"dataset_version": strconv.FormatUint(s.Version, 10)}})
}
func (a *api) byUUID(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/sentences/")
	u, ok := normalizeUUID(raw)
	if !ok {
		problem(w, r, 400, "参数无效", "invalid-parameter", "UUID 格式无效")
		return
	}
	s := a.o.Snapshots.Current()
	if s == nil {
		problem(w, r, 503, "服务未就绪", "not-ready", "当前没有可用数据")
		return
	}
	v, ok := s.LookupUUID(u)
	if !ok {
		problem(w, r, 404, "未找到", "not-found", "语句不存在")
		return
	}
	success(w, r, map[string]any{"data": sentenceOut(v), "meta": map[string]string{"dataset_version": strconv.FormatUint(s.Version, 10)}})
}
func (a *api) categories(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	s := a.o.Snapshots.Current()
	if s == nil {
		problem(w, r, 503, "服务未就绪", "not-ready", "当前没有可用数据")
		return
	}
	type c struct {
		Code  string `json:"code"`
		Name  string `json:"name"`
		Count uint64 `json:"count"`
	}
	out := make([]c, len(s.Categories))
	for i, v := range s.Categories {
		out[i] = c{v.Code, v.Name, v.Count}
	}
	success(w, r, map[string]any{"data": out, "meta": map[string]string{"dataset_version": strconv.FormatUint(s.Version, 10)}})
}
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	writeJSON(w, r, 200, "application/json; charset=utf-8", map[string]string{"status": "ok"})
}
func (a *api) ready(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	s := a.o.Snapshots.Current()
	if s == nil || a.o.IsStopping() {
		problem(w, r, 503, "服务未就绪", "not-ready", "服务未就绪")
		return
	}
	writeJSON(w, r, 200, "application/json; charset=utf-8", map[string]string{"status": "ready", "dataset_version": strconv.FormatUint(s.Version, 10)})
}
func (a *api) metrics(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	a.o.Metrics.Handler().ServeHTTP(w, r)
}
func (a *api) version(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	b := a.o.Build
	if b.Version == "" {
		b.Version = "dev"
	}
	if b.GitCommit == "" {
		b.GitCommit = "unknown"
	}
	if b.BuildTime == "" {
		b.BuildTime = "unknown"
	}
	writeJSON(w, r, 200, "application/json; charset=utf-8", map[string]string{"version": b.Version, "git_commit": b.GitCommit, "build_time": b.BuildTime})
}
func (a *api) reload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !authorized(r.Header.Get("Authorization"), a.o.ReloadToken) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		problem(w, r, 401, "未授权", "unauthorized", "凭证无效")
		return
	}
	if r.URL.RawQuery != "" || r.ContentLength != 0 {
		problem(w, r, 400, "参数无效", "invalid-parameter", "刷新请求不接受查询参数或请求体")
		return
	}
	if a.o.IsStopping() {
		problem(w, r, 503, "服务未就绪", "not-ready", "服务正在退出")
		return
	}
	s := a.o.Snapshots.Current()
	version := uint64(0)
	if s != nil {
		version = s.Version
	}
	requestID := RequestID(r.Context())
	started := time.Now()
	done := func(_ bool, err error) {
		result := "success"
		level := slog.LevelInfo
		if err != nil {
			result = "failure"
			level = slog.LevelError
			if a.o.LifecycleContext.Err() != nil || errors.Is(err, context.Canceled) {
				result = "canceled"
				level = slog.LevelInfo
			}
		}
		a.o.Logger.Log(context.Background(), level, "snapshot_reload", "request_id", requestID, "result", result, "duration_ms", float64(time.Since(started).Microseconds())/1000)
	}
	if a.o.StartReload == nil || !a.o.StartReload(a.o.LifecycleContext, true, done) {
		problem(w, r, 409, "刷新正在进行", "reload-in-progress", "已有刷新任务")
		return
	}
	writeJSON(w, r, 202, "application/json; charset=utf-8", map[string]any{"data": map[string]string{"status": "accepted"}, "meta": map[string]string{"dataset_version": strconv.FormatUint(version, 10), "request_id": RequestID(r.Context())}})
}

func success(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, 200, "application/json; charset=utf-8", v)
}
func writeJSON(w http.ResponseWriter, r *http.Request, status int, ct string, v any) {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write(append(b, '\n'))
	}
}
func problem(w http.ResponseWriter, r *http.Request, status int, title, code, detail string) {
	writeJSON(w, r, status, "application/problem+json", map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail, "request_id": RequestID(r.Context()), "code": code})
}
func noQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		problem(w, r, 400, "参数无效", "invalid-parameter", "该接口不接受查询参数")
		return false
	}
	return true
}
func parseQuery(r *http.Request, max int, allowed map[string]bool) (url.Values, error) {
	if max > 0 && len(r.URL.RawQuery) > max {
		return nil, errors.New("查询字符串过长")
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return nil, errors.New("查询编码无效")
	}
	for k, v := range q {
		if !allowed[k] {
			return nil, errors.New("包含未知查询参数")
		}
		if len(v) != 1 {
			return nil, errors.New("查询参数不能重复")
		}
	}
	return q, nil
}
func parseLength(q url.Values, key string, def uint16) (uint16, error) {
	v, ok := q[key]
	if !ok {
		return def, nil
	}
	s := v[0]
	if s == "" {
		return 0, fmt.Errorf("%s 不能为空", key)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%s 必须是十进制整数", key)
		}
	}
	n, e := strconv.ParseUint(s, 10, 16)
	if e != nil || n > 1000 {
		return 0, fmt.Errorf("%s 必须在 0 到 1000 之间", key)
	}
	return uint16(n), nil
}
func parseCategories(q url.Values) ([]string, error) {
	v, ok := q["categories"]
	if !ok {
		return nil, nil
	}
	if v[0] == "" {
		return nil, errors.New("categories 不能为空")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, s := range strings.Split(v[0], ",") {
		if !validCategory(s) {
			return nil, errors.New("categories 包含非法分类")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 20 {
		return nil, errors.New("categories 去重后不能超过 20 个")
	}
	return out, nil
}
func validCategory(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (i > 0 && (c == '_' || c == '-')) {
			continue
		}
		return false
	}
	return true
}
func normalizeUUID(s string) (string, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return "", false
	}
	raw := strings.ReplaceAll(s, "-", "")
	if len(raw) != 32 {
		return "", false
	}
	if _, e := hex.DecodeString(raw); e != nil {
		return "", false
	}
	return strings.ToLower(s), true
}
func authorized(h, token string) bool {
	if !strings.HasPrefix(h, "Bearer ") || strings.Count(h, " ") != 1 {
		return false
	}
	got := strings.TrimPrefix(h, "Bearer ")
	a := sha256.Sum256([]byte(got))
	b := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
