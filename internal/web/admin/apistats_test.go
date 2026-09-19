package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentence-api/internal/observability"
)

func TestParseAPIRequestTotals(t *testing.T) {
	m := observability.NewMetrics()
	m.ObserveHTTP("GET", "/api/v1", 200, time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1", 400, time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1/sentences/{uuid}", 200, time.Millisecond)
	m.ObserveHTTP("GET", "/api/v1/categories", 200, time.Millisecond)
	m.ObserveHTTP("GET", "/healthz", 200, time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.WritePrometheus(w)
	}))
	t.Cleanup(srv.Close)
	got := fetchAPIUsage(context.Background(), srv.URL)
	if !got.OK || got.Random != 2 || got.UUID != 1 || got.Categories != 1 || got.Total() != 4 {
		t.Fatalf("%+v", got)
	}
	if fetchAPIUsage(context.Background(), "").OK {
		t.Fatal("empty URL should skip")
	}
}

func TestParseAPIRequestTotalsReachableWithoutSamples(t *testing.T) {
	got := parseAPIRequestTotals([]byte("# HELP sentence_api_http_requests_total HTTP requests.\n# TYPE sentence_api_http_requests_total counter\n"))
	if !got.OK || got.Total() != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestFetchAPIUsageRejectsRedirectAndOversize(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("# TYPE sentence_api_http_requests_total counter\n"))
		}))
		defer target.Close()
		srv := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
		defer srv.Close()
		if got := fetchAPIUsage(context.Background(), srv.URL); got.OK {
			t.Fatalf("redirect accepted: %+v", got)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		prefix := "# TYPE sentence_api_http_requests_total counter\n" +
			"sentence_api_http_requests_total{method=\"GET\",route=\"/api/v1\",status=\"200\"} 7\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(prefix + strings.Repeat("#", maxMetricsBody+1-len(prefix))))
		}))
		defer srv.Close()
		if got := fetchAPIUsage(context.Background(), srv.URL); got.OK || got.Random != 0 {
			t.Fatalf("oversize accepted: %+v", got)
		}
	})
}

func TestFetchAPIUsageCanceledContextDoesNotSend(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("# TYPE sentence_api_http_requests_total counter\n"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := fetchAPIUsage(ctx, srv.URL); got.OK {
		t.Fatalf("canceled request succeeded: %+v", got)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("canceled request reached metrics server %d time(s)", got)
	}
}
