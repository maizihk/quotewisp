package public

import (
	"net/http"
)

func (h *handler) serve() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			h.handleOptions(w, r)
			return
		}
		h.mux.ServeHTTP(w, r)
	})
}

// fallback is the ServeMux catch-all so the mux never emits its own plain-text
// 404/405; known paths with a wrong method get a rendered 405 with Allow.
func (h *handler) fallback(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if RouteName(r.URL.Path) == "unmatched" {
		h.renderer.NotFound(w, r)
		return
	}
	h.renderer.MethodNotAllowed(w, r, h.allowForPath(r.URL.Path))
}

func (h *handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	w.Header().Set("Allow", h.allowForPath(r.URL.Path))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) allowForPath(path string) string {
	switch RouteName(path) {
	case "/submit":
		return "GET, HEAD, POST, OPTIONS"
	case "/":
		return "GET, HEAD, OPTIONS"
	case "/docs", "/submit/done", "/dataset":
		return "GET, HEAD, OPTIONS"
	case "/dataset/sentences.json", "/dataset/LICENSE.txt":
		return "GET, HEAD, OPTIONS"
	case "/static/*":
		return "GET, HEAD, OPTIONS"
	default:
		return "GET, HEAD, OPTIONS"
	}
}

func (h *handler) page(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		next(w, r)
	}
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}
