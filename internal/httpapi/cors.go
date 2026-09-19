package httpapi

import (
	"net/http"
	"strings"
)

func (a *api) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := routeName(r)
		if route != "/api/v1" && route != "/api/v1/sentences/{uuid}" && route != "/api/v1/categories" {
			next.ServeHTTP(w, r)
			return
		}
		addVary(w.Header(), "Origin")
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		_, allowed := a.origins[origin]
		preflight := r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != ""
		if preflight {
			addVary(w.Header(), "Access-Control-Request-Method")
			addVary(w.Header(), "Access-Control-Request-Headers")
			if !allowed || !allowedPreflight(r) {
				problem(w, r, 403, "跨域请求被拒绝", "cors-denied", "跨域预检不被允许")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
			next.ServeHTTP(w, r)
			return
		}
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		}
		next.ServeHTTP(w, r)
	})
}
func allowedPreflight(r *http.Request) bool {
	m := r.Header.Get("Access-Control-Request-Method")
	if m != "GET" && m != "HEAD" {
		return false
	}
	for _, line := range r.Header.Values("Access-Control-Request-Headers") {
		for _, v := range strings.Split(line, ",") {
			v = strings.TrimSpace(v)
			if !strings.EqualFold(v, "Accept") && !strings.EqualFold(v, "Content-Type") {
				return false
			}
		}
	}
	return true
}
func addVary(h http.Header, v string) {
	for _, line := range h.Values("Vary") {
		for _, x := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(x), v) {
				return
			}
		}
	}
	h.Add("Vary", v)
}
