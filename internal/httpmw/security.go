package httpmw

import "net/http"

func SecurityHeaders(next http.Handler) http.Handler {
	return securityHeaders(nil, next)
}

func SecurityHeadersWith(extra http.Header, next http.Handler) http.Handler {
	return securityHeaders(extra, next)
}

func securityHeaders(extra http.Header, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		for k, vs := range extra {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		next.ServeHTTP(w, r)
	})
}
