package httpmw

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"sentence-api/internal/observability"
)

func RouteName(r *http.Request) string {
	v, _ := r.Context().Value(routeKey).(string)
	if v == "" {
		return "unmatched"
	}
	return v
}

func Access(logger *slog.Logger, metrics *observability.Metrics, classify func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(context.WithValue(r.Context(), routeKey, classify(r)))
		ow := &observedWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			status := ow.status
			if status == 0 {
				status = 200
			}
			route := RouteName(r)
			d := time.Since(start)
			metrics.ObserveHTTP(r.Method, route, status, d)
			logger.Info("http_request", "request_id", RequestIDFrom(r.Context()), "method", observability.MetricMethod(r.Method), "route", observability.MetricRoute(route), "status", status, "duration_ms", float64(d.Microseconds())/1000, "response_bytes", ow.bytes, "client_ip", ClientIPFrom(r.Context()))
		}()
		if r.Method == "HEAD" {
			next.ServeHTTP(headWriter{ow}, r)
		} else {
			next.ServeHTTP(ow, r)
		}
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
