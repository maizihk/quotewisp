package httpmw

import (
	"log/slog"
	"net/http"
)

func Recover(logger *slog.Logger, onPanic func(w http.ResponseWriter, r *http.Request)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					logger.Error("http_panic", "request_id", RequestIDFrom(r.Context()), "route", RouteName(r))
					if cw, ok := w.(interface{ Committed() bool }); ok && cw.Committed() {
						panic(http.ErrAbortHandler)
					}
					onPanic(w, r)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
