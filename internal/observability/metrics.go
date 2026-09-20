package observability

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metrics contains the bounded-cardinality application metrics. It deliberately
// has no dependency on a Prometheus client so the HTTP stack remains standard-library only.
type Metrics struct {
	mu                    sync.RWMutex
	webEnabled            bool
	httpRequests          map[httpKey]uint64
	httpDuration          map[routeKey]*histogram
	refreshTotal          map[string]uint64
	refreshDuration       histogram
	snapshotVersion       uint64
	snapshotSentences     uint64
	snapshotCategories    uint64
	snapshotLoaded        float64
	snapshotTextBytes     uint64
	refreshInProgress     bool
	mysqlLastSuccess      bool
	mysqlLastCheck        float64
	mysqlLastSuccessAt    float64
	webSubmissions        map[string]uint64
	webAdminLogin         map[string]uint64
	webReviews            map[webActionKey]uint64
	webSentenceChanges    map[webActionKey]uint64
	webCategoryChanges    map[webActionKey]uint64
	webPublicVersion      uint64
	webPublicBytes        uint64
	webPublicBuilds       map[string]uint64
	webPendingSubmissions int
	webRetentionRows      map[string]uint64
}

// APIRequestTotals is the in-process view of API request counters used by the
// admin overview when API and web handlers share one process. It intentionally
// contains only API routes; web and admin requests are not folded into it.
type APIRequestTotals struct {
	OK         bool
	Random     uint64
	UUID       uint64
	Categories uint64
}

type httpKey struct {
	method, route string
	status        int
}
type routeKey struct{ method, route string }
type webActionKey struct{ action, result string }

var buckets = [...]float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

type histogram struct {
	Counts [len(buckets) + 1]uint64
	Count  uint64
	Sum    float64
}

func NewMetrics() *Metrics {
	return &Metrics{
		httpRequests:       make(map[httpKey]uint64),
		httpDuration:       make(map[routeKey]*histogram),
		refreshTotal:       map[string]uint64{"success": 0, "failure": 0, "canceled": 0},
		webSubmissions:     make(map[string]uint64),
		webAdminLogin:      make(map[string]uint64),
		webReviews:         make(map[webActionKey]uint64),
		webSentenceChanges: make(map[webActionKey]uint64),
		webCategoryChanges: make(map[webActionKey]uint64),
		webPublicBuilds:    make(map[string]uint64),
		webRetentionRows:   map[string]uint64{"deleted": 0, "redacted": 0},
	}
}

// EnableWeb activates web-only Prometheus families for this process.
func (m *Metrics) EnableWeb() { m.mu.Lock(); m.webEnabled = true; m.mu.Unlock() }

func (m *Metrics) ObserveHTTP(method, route string, status int, duration time.Duration) {
	method = MetricMethod(method)
	route = MetricRoute(route)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.httpRequests[httpKey{method, route, status}]++
	h := m.httpDuration[routeKey{method, route}]
	if h == nil {
		h = &histogram{}
		m.httpDuration[routeKey{method, route}] = h
	}
	h.observe(duration.Seconds())
}

// APIRequestTotals returns counters for all observed statuses on the public API
// routes. This preserves the Prometheus-based overview semantics without an
// in-process HTTP round trip.
func (m *Metrics) APIRequestTotals() APIRequestTotals {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := APIRequestTotals{OK: true}
	for key, count := range m.httpRequests {
		switch key.route {
		case "/api/v1":
			out.Random += count
		case "/api/v1/sentences/{uuid}":
			out.UUID += count
		case "/api/v1/categories":
			out.Categories += count
		}
	}
	return out
}
func (h *histogram) observe(v float64) {
	h.Count++
	h.Sum += v
	for i, b := range buckets {
		if v <= b {
			h.Counts[i]++
		}
	}
	h.Counts[len(buckets)]++
}

func MetricMethod(method string) string {
	switch method {
	case "GET", "HEAD", "OPTIONS", "POST":
		return method
	default:
		return "OTHER"
	}
}
func MetricRoute(route string) string {
	switch route {
	case "/api/v1", "/api/v1/sentences/{uuid}", "/api/v1/categories", "/healthz", "/readyz", "/metrics", "/version", "/internal/reload":
		return route
	default:
		if webMetricRoute(route) {
			return route
		}
		return "unmatched"
	}
}

func webMetricRoute(route string) bool {
	switch route {
	case "/", "/docs", "/submit", "/submit/done", "/dataset", "/dataset/sentences.json", "/dataset/LICENSE.txt", "/static/*":
		return true
	case "/admin/", "/admin/login", "/admin/logout", "/admin/submissions", "/admin/submissions/{id}", "/admin/submissions/{id}/approve", "/admin/submissions/{id}/reject", "/admin/sentences", "/admin/sentences/new", "/admin/sentences/{uuid}", "/admin/sentences/{uuid}/disable", "/admin/sentences/{uuid}/enable", "/admin/categories", "/admin/categories/new", "/admin/categories/{code}", "/admin/categories/{code}/disable", "/admin/categories/{code}/enable", "/admin/users", "/admin/users/{id}/reset-password", "/admin/users/{id}/disable", "/admin/users/{id}/enable", "/admin/password", "/admin/settings":
		return true
	default:
		return false
	}
}

var (
	webSubmissionResults = map[string]struct{}{"accepted": {}, "invalid": {}, "rate_limited": {}, "queue_full": {}, "duplicate": {}, "honeypot": {}}
	webLoginResults      = map[string]struct{}{"success": {}, "failure": {}, "rate_limited": {}}
	webReviewActions     = map[string]struct{}{"approve": {}, "reject": {}}
	webChangeActions     = map[string]struct{}{"create": {}, "edit": {}, "disable": {}, "enable": {}}
	webChangeResults     = map[string]struct{}{"success": {}, "conflict": {}, "duplicate": {}, "unchanged": {}, "error": {}}
	webBuildResults      = map[string]struct{}{"success": {}, "failure": {}}
	webRetentionActions  = map[string]struct{}{"deleted": {}, "redacted": {}}
)

func webLabel(v string, allowed map[string]struct{}) string {
	if _, ok := allowed[v]; ok {
		return v
	}
	return "other"
}

func (m *Metrics) WebSubmission(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webSubmissions[webLabel(result, webSubmissionResults)]++
}
func (m *Metrics) WebAdminLogin(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webAdminLogin[webLabel(result, webLoginResults)]++
}
func (m *Metrics) WebReview(action, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webReviews[webActionKey{webLabel(action, webReviewActions), webLabel(result, webChangeResults)}]++
}
func (m *Metrics) WebSentenceChange(action, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webSentenceChanges[webActionKey{webLabel(action, webChangeActions), webLabel(result, webChangeResults)}]++
}
func (m *Metrics) WebCategoryChange(action, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webCategoryChanges[webActionKey{webLabel(action, webChangeActions), webLabel(result, webChangeResults)}]++
}
func (m *Metrics) WebPublicDataBuilt(result string, version uint64, bytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webPublicBuilds[webLabel(result, webBuildResults)]++
	if result == "success" {
		m.webPublicVersion = version
		if bytes >= 0 {
			m.webPublicBytes = uint64(bytes)
		}
	}
}
func (m *Metrics) SetWebPendingSubmissions(n int) {
	if n < 0 {
		n = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webPendingSubmissions = n
}
func (m *Metrics) AddWebRetentionRows(action string, n int64) {
	if n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webRetentionRows[webLabel(action, webRetentionActions)] += uint64(n)
}

func (m *Metrics) SetSnapshot(version, sentences, categories, textBytes uint64, loadedAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotVersion = version
	m.snapshotSentences = sentences
	m.snapshotCategories = categories
	m.snapshotTextBytes = textBytes
	m.snapshotLoaded = float64(loadedAt.UnixNano()) / 1e9
}
func (m *Metrics) SetRefreshInProgress(v bool) { m.mu.Lock(); m.refreshInProgress = v; m.mu.Unlock() }
func (m *Metrics) ObserveRefresh(result string, d time.Duration) {
	if result != "success" && result != "failure" && result != "canceled" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshTotal[result]++
	m.refreshDuration.observe(d.Seconds())
}
func (m *Metrics) ObserveMySQL(success bool, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mysqlLastSuccess = success
	m.mysqlLastCheck = float64(at.UnixNano()) / 1e9
	if success {
		m.mysqlLastSuccessAt = m.mysqlLastCheck
	}
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.WritePrometheus(w)
	})
}
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.mu.RLock()
	var out bytes.Buffer
	m.writePrometheusLocked(&out)
	m.mu.RUnlock()
	_, _ = io.Copy(w, &out)
}

func (m *Metrics) writePrometheusLocked(w io.Writer) {
	writeHelp(w, "sentence_api_http_requests_total", "counter", "HTTP requests.")
	keys := make([]httpKey, 0, len(m.httpRequests))
	for k := range m.httpRequests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		fmt.Fprintf(w, "sentence_api_http_requests_total{method=%s,route=%s,status=%q} %d\n", quote(k.method), quote(k.route), strconv.Itoa(k.status), m.httpRequests[k])
	}
	writeHelp(w, "sentence_api_http_request_duration_seconds", "histogram", "HTTP request duration.")
	rks := make([]routeKey, 0, len(m.httpDuration))
	for k := range m.httpDuration {
		rks = append(rks, k)
	}
	sort.Slice(rks, func(i, j int) bool { return fmt.Sprint(rks[i]) < fmt.Sprint(rks[j]) })
	for _, k := range rks {
		writeHistogram(w, "sentence_api_http_request_duration_seconds", `method=`+quote(k.method)+`,route=`+quote(k.route), m.httpDuration[k])
	}
	gauge(w, "sentence_api_snapshot_version", float64(m.snapshotVersion))
	gauge(w, "sentence_api_snapshot_sentences", float64(m.snapshotSentences))
	gauge(w, "sentence_api_snapshot_categories", float64(m.snapshotCategories))
	gauge(w, "sentence_api_snapshot_loaded_timestamp_seconds", m.snapshotLoaded)
	gauge(w, "sentence_api_snapshot_text_bytes", float64(m.snapshotTextBytes))
	writeHelp(w, "sentence_api_snapshot_refresh_total", "counter", "Snapshot refresh attempts.")
	for _, r := range []string{"success", "failure", "canceled"} {
		fmt.Fprintf(w, "sentence_api_snapshot_refresh_total{result=%q} %d\n", r, m.refreshTotal[r])
	}
	writeHelp(w, "sentence_api_snapshot_refresh_duration_seconds", "histogram", "Snapshot refresh duration.")
	writeHistogram(w, "sentence_api_snapshot_refresh_duration_seconds", "", &m.refreshDuration)
	if m.refreshInProgress {
		gauge(w, "sentence_api_snapshot_refresh_in_progress", 1)
	} else {
		gauge(w, "sentence_api_snapshot_refresh_in_progress", 0)
	}
	if m.mysqlLastSuccess {
		gauge(w, "sentence_api_mysql_last_operation_success", 1)
	} else {
		gauge(w, "sentence_api_mysql_last_operation_success", 0)
	}
	gauge(w, "sentence_api_mysql_last_check_timestamp_seconds", m.mysqlLastCheck)
	gauge(w, "sentence_api_mysql_last_success_timestamp_seconds", m.mysqlLastSuccessAt)
	if m.webEnabled {
		writeHelp(w, "web_submissions_total", "counter", "Web submission outcomes.")
		for _, r := range []string{"accepted", "invalid", "rate_limited", "queue_full", "duplicate", "honeypot", "other"} {
			if n := m.webSubmissions[r]; n > 0 || r != "other" {
				fmt.Fprintf(w, "web_submissions_total{result=%q} %d\n", r, n)
			}
		}
		writeHelp(w, "web_admin_login_total", "counter", "Admin login outcomes.")
		for _, r := range []string{"success", "failure", "rate_limited", "other"} {
			if n := m.webAdminLogin[r]; n > 0 || r != "other" {
				fmt.Fprintf(w, "web_admin_login_total{result=%q} %d\n", r, n)
			}
		}
		writeWebActionCounter(w, "web_reviews_total", "Web review outcomes.", m.webReviews, webReviewActions, webChangeResults)
		writeWebActionCounter(w, "web_sentence_changes_total", "Web sentence changes.", m.webSentenceChanges, webChangeActions, webChangeResults)
		writeWebActionCounter(w, "web_category_changes_total", "Web category changes.", m.webCategoryChanges, webChangeActions, webChangeResults)
		gauge(w, "web_public_data_version", float64(m.webPublicVersion))
		gauge(w, "web_public_data_export_bytes", float64(m.webPublicBytes))
		writeHelp(w, "web_public_data_builds_total", "counter", "Public data cache builds.")
		for _, r := range []string{"success", "failure", "other"} {
			if n := m.webPublicBuilds[r]; n > 0 || r != "other" {
				fmt.Fprintf(w, "web_public_data_builds_total{result=%q} %d\n", r, n)
			}
		}
		gauge(w, "web_pending_submissions", float64(m.webPendingSubmissions))
		writeHelp(w, "web_retention_rows_total", "counter", "Retention cleanup rows.")
		for _, action := range []string{"deleted", "redacted"} {
			fmt.Fprintf(w, "web_retention_rows_total{action=%q} %d\n", action, m.webRetentionRows[action])
		}
	}
	writeRuntime(w)
}

func writeWebActionCounter(w io.Writer, name, help string, counts map[webActionKey]uint64, actions, results map[string]struct{}) {
	writeHelp(w, name, "counter", help)
	emitted := make(map[webActionKey]bool, len(counts))
	keys := make([]webActionKey, 0, len(counts))
	for k, n := range counts {
		if n == 0 {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].action == keys[j].action {
			return keys[i].result < keys[j].result
		}
		return keys[i].action < keys[j].action
	})
	for _, k := range keys {
		fmt.Fprintf(w, "%s{action=%q,result=%q} %d\n", name, k.action, k.result, counts[k])
		emitted[k] = true
	}
	actionList := make([]string, 0, len(actions))
	for action := range actions {
		actionList = append(actionList, action)
	}
	sort.Strings(actionList)
	resultList := make([]string, 0, len(results))
	for result := range results {
		resultList = append(resultList, result)
	}
	sort.Strings(resultList)
	for _, action := range actionList {
		for _, result := range resultList {
			k := webActionKey{action, result}
			if !emitted[k] {
				fmt.Fprintf(w, "%s{action=%q,result=%q} 0\n", name, action, result)
			}
		}
	}
}
func writeHelp(w io.Writer, n, t, h string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", n, h, n, t)
}
func gauge(w io.Writer, n string, v float64) {
	writeHelp(w, n, "gauge", strings.ReplaceAll(n, "_", " "))
	fmt.Fprintf(w, "%s %g\n", n, v)
}
func quote(v string) string { return strconv.Quote(v) }
func writeHistogram(w io.Writer, n, labels string, h *histogram) {
	for i, b := range buckets {
		l := labels
		if l != "" {
			l += ","
		}
		fmt.Fprintf(w, "%s_bucket{%sle=%q} %d\n", n, l, strconv.FormatFloat(b, 'g', -1, 64), h.Counts[i])
	}
	l := labels
	if l != "" {
		l += ","
	}
	fmt.Fprintf(w, "%s_bucket{%sle=%q} %d\n", n, l, "+Inf", h.Counts[len(buckets)])
	suffix := ""
	if labels != "" {
		suffix = "{" + labels + "}"
	}
	fmt.Fprintf(w, "%s_sum%s %g\n%s_count%s %d\n", n, suffix, h.Sum, n, suffix, h.Count)
}

func writeRuntime(w io.Writer) {
	samples := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}, {Name: "/memory/classes/heap/objects:bytes"}, {Name: "/sched/goroutines:goroutines"}}
	metrics.Read(samples)
	names := map[string]string{"/gc/heap/allocs:bytes": "go_gc_heap_allocs_bytes", "/memory/classes/heap/objects:bytes": "go_memory_heap_objects_bytes", "/sched/goroutines:goroutines": "go_goroutines"}
	for _, s := range samples {
		n := names[s.Name]
		var v float64
		switch s.Value.Kind() {
		case metrics.KindUint64:
			v = float64(s.Value.Uint64())
		case metrics.KindFloat64:
			v = s.Value.Float64()
		default:
			continue
		}
		gauge(w, n, v)
	}
}
