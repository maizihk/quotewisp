package httpmw

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"sentence-api/internal/observability"
)

func TestForwardedIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::ffff:10.0.0.0/104")}
	tests := []struct {
		name  string
		lines []string
		want  string
		ok    bool
	}{
		{"rightmost untrusted", []string{"198.51.100.1, 10.0.0.1"}, "198.51.100.1", true},
		{"all trusted leftmost", []string{"10.0.0.5, 10.0.0.1"}, "10.0.0.5", true},
		{"empty element", []string{"198.51.100.1,,10.0.0.1"}, "", false},
		{"hostname rejected", []string{"client.example, 10.0.0.1"}, "", false},
		{"port rejected", []string{"198.51.100.1:443, 10.0.0.1"}, "", false},
		{"zone rejected", []string{"fe80::1%eth0, 10.0.0.1"}, "", false},
		{"over limit", []string{strings.Repeat("1.2.3.4, ", 33) + "1.2.3.4"}, "", false},
		{"multiline concat", []string{"198.51.100.2", "203.0.113.1, 10.0.0.1"}, "203.0.113.1", true},
		{"ipv4 mapped normalized", []string{"::ffff:198.51.100.3, 10.0.0.1"}, "198.51.100.3", true},
		{"missing header", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := forwardedIP(tt.lines, trusted)
			if ok != tt.ok {
				t.Fatalf("ok=%v want %v", ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if got.String() != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestClientIPTrustedProxy(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	var logged string
	logger := slog.New(slog.NewJSONHandler(writerFunc(func(p []byte) (int, error) {
		if strings.Contains(string(p), `"client_ip"`) {
			logged = string(p)
		}
		return len(p), nil
	}), nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := RequestID(ClientIP(trusted, Access(logger, observability.NewMetrics(), func(*http.Request) string { return "/healthz" }, inner)))
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.RemoteAddr = "10.0.0.2:123"
	r.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(logged, `"client_ip":"198.51.100.1"`) {
		t.Fatalf("log=%q", logged)
	}
	if ClientIPFrom(r.Context()) != "" {
		t.Fatal("handler context unchanged")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestRecoverUncommitted(t *testing.T) {
	var called bool
	h := Access(slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMetrics(), func(*http.Request) string { return "/healthz" },
		Recover(slog.New(slog.NewTextHandler(io.Discard, nil)), func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(500)
		})(SecurityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret") }))))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if !called || w.Code != 500 || strings.Contains(w.Body.String(), "secret") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("recovery=%d called=%v body=%q", w.Code, called, w.Body.String())
	}
}

func TestRecoverCommitted(t *testing.T) {
	h := Access(slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMetrics(), func(*http.Request) string { return "/healthz" },
		Recover(slog.New(slog.NewTextHandler(io.Discard, nil)), func(http.ResponseWriter, *http.Request) {
			t.Fatal("onPanic on committed response")
		})(SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(201)
			_, _ = w.Write([]byte("partial"))
			panic("secret")
		}))))
	w := httptest.NewRecorder()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered=%v", recovered)
	}
	if w.Code != 201 || w.Body.String() != "partial" {
		t.Fatalf("response changed: %d %q", w.Code, w.Body.String())
	}
}

func TestHeadWriter(t *testing.T) {
	h := Access(slog.New(slog.NewTextHandler(io.Discard, nil)), observability.NewMetrics(), func(*http.Request) string { return "/metrics" },
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("prometheus"))
		}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("HEAD", "/metrics", nil))
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
	}
}

func TestRouteNameDefault(t *testing.T) {
	if RouteName(httptest.NewRequest("GET", "/", nil)) != "unmatched" {
		t.Fatal("empty route")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), routeKey, ""))
	if RouteName(r) != "unmatched" {
		t.Fatal("blank route")
	}
	r = r.WithContext(context.WithValue(r.Context(), routeKey, "/healthz"))
	if RouteName(r) != "/healthz" {
		t.Fatal("set route")
	}
}
