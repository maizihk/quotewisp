package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestWritePrometheusDoesNotHoldLockWhileWriting(t *testing.T) {
	m := NewMetrics()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() { m.WritePrometheus(blockWriter{entered, release}); close(done) }()
	<-entered
	observed := make(chan struct{})
	go func() { m.ObserveHTTP("GET", "/healthz", 200, time.Millisecond); close(observed) }()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("slow metrics writer blocked observations")
	}
	close(release)
	<-done
}

type blockWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w blockWriter) Write(p []byte) (int, error) {
	select {
	case <-w.entered:
	default:
		close(w.entered)
	}
	<-w.release
	return len(p), nil
}

func TestMetricsBoundLabelsAndRuntime(t *testing.T) {
	m := NewMetrics()
	m.ObserveHTTP("PATCH", "/attacker/uuid", 418, time.Second)
	var b bytes.Buffer
	m.WritePrometheus(&b)
	s := b.String()
	if !strings.Contains(s, `method="OTHER",route="unmatched",status="418"`) {
		t.Fatalf("bounded labels missing: %s", s)
	}
	if strings.Contains(s, "attacker") {
		t.Fatal("raw path leaked into metric")
	}
	if !strings.Contains(s, "go_goroutines") {
		t.Fatal("runtime metric missing")
	}
}

func TestAPIRequestTotalsExcludeWebRoutes(t *testing.T) {
	m := NewMetrics()
	m.ObserveHTTP("GET", "/api/v1", 200, time.Millisecond)
	m.ObserveHTTP("POST", "/api/v1", 400, time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1/sentences/{uuid}", 404, time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1/categories", 200, time.Millisecond)
	m.ObserveHTTP("GET", "/", 200, time.Millisecond)
	m.ObserveHTTP("GET", "/admin/", 200, time.Millisecond)
	got := m.APIRequestTotals()
	if !got.OK || got.Random != 2 || got.UUID != 1 || got.Categories != 1 {
		t.Fatalf("unexpected API totals: %+v", got)
	}
}

func TestWebMetricsBoundedAndOptional(t *testing.T) {
	read := NewMetrics()
	var readBuf bytes.Buffer
	read.WritePrometheus(&readBuf)
	if strings.Contains(readBuf.String(), "web_submissions_total") {
		t.Fatal("read API metrics must not include web families")
	}

	web := NewMetrics()
	web.EnableWeb()
	web.WebSubmission("accepted")
	web.WebSubmission("bogus")
	web.WebAdminLogin("success")
	web.WebReview("approve", "success")
	web.WebSentenceChange("create", "duplicate")
	web.WebCategoryChange("disable", "error")
	web.WebPublicDataBuilt("success", 42, 1000)
	web.WebPublicDataBuilt("failure", 0, 0)

	var webBuf bytes.Buffer
	web.WritePrometheus(&webBuf)
	s := webBuf.String()
	if !strings.Contains(s, `web_submissions_total{result="accepted"} 1`) {
		t.Fatalf("accepted counter missing: %s", s)
	}
	if !strings.Contains(s, `web_submissions_total{result="other"} 1`) {
		t.Fatalf("unknown submission mapped to other: %s", s)
	}
	if !strings.Contains(s, `web_admin_login_total{result="success"} 1`) {
		t.Fatalf("login counter missing: %s", s)
	}
	if !strings.Contains(s, `web_reviews_total{action="approve",result="success"} 1`) {
		t.Fatalf("review counter missing: %s", s)
	}
	if !strings.Contains(s, "web_public_data_version 42") {
		t.Fatalf("public data version missing: %s", s)
	}
	if !strings.Contains(s, "web_public_data_export_bytes 1000") {
		t.Fatalf("public data bytes missing: %s", s)
	}
	if !strings.Contains(s, `web_public_data_builds_total{result="success"} 1`) {
		t.Fatalf("build success counter missing: %s", s)
	}
	if !strings.Contains(s, `web_public_data_builds_total{result="failure"} 1`) {
		t.Fatalf("build failure counter missing: %s", s)
	}
	if !strings.Contains(s, "web_pending_submissions 0") {
		t.Fatalf("pending gauge should start at zero: %s", s)
	}
	web.SetWebPendingSubmissions(7)
	web.AddWebRetentionRows("deleted", 3)
	web.AddWebRetentionRows("redacted", 2)
	web.AddWebRetentionRows("bogus", 99)
	var webBuf2 bytes.Buffer
	web.WritePrometheus(&webBuf2)
	s2 := webBuf2.String()
	if !strings.Contains(s2, "web_pending_submissions 7") {
		t.Fatalf("pending gauge missing: %s", s2)
	}
	if !strings.Contains(s2, `web_retention_rows_total{action="deleted"} 3`) {
		t.Fatalf("retention deleted counter missing: %s", s2)
	}
	if !strings.Contains(s2, `web_retention_rows_total{action="redacted"} 2`) {
		t.Fatalf("retention redacted counter missing: %s", s2)
	}
	if strings.Contains(s2, `action="other"`) {
		t.Fatalf("unknown retention action leaked: %s", s2)
	}
}

func TestLoggerUsesUTC(t *testing.T) {
	var b bytes.Buffer
	l, e := NewJSONLogger(&b, "info")
	if e != nil {
		t.Fatal(e)
	}
	l.Info("event")
	var v map[string]any
	if e = json.Unmarshal(b.Bytes(), &v); e != nil {
		t.Fatal(e)
	}
	ts := v[slog.TimeKey].(string)
	if !strings.HasSuffix(ts, "Z") {
		t.Fatalf("time is not UTC: %q", ts)
	}
}
