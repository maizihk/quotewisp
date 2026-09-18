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
