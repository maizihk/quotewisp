package admin

import (
	"errors"
	"net/http"

	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

func (h *handler) serve() http.Handler {
	return headAsGet(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			h.handleOptions(w, r)
			return
		}
		h.mux.ServeHTTP(w, r)
	}))
}

// headAsGet routes HEAD through GET patterns and strips response bodies.
func headAsGet(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		r2 := *r
		r2.Method = http.MethodGet
		next.ServeHTTP(headBodyWriter{ResponseWriter: w}, &r2)
	})
}

type headBodyWriter struct{ http.ResponseWriter }

func (w headBodyWriter) Write(p []byte) (int, error) { return len(p), nil }

func (h *handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	w.Header().Set("Allow", h.allowForPath(r.URL.Path))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) allowForPath(path string) string {
	switch RouteName(path) {
	case "/admin/login":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/logout":
		return "POST, OPTIONS"
	case "/admin/password":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/":
		return "GET, HEAD, OPTIONS"
	case "/admin/submissions":
		return "GET, HEAD, OPTIONS"
	case "/admin/submissions/{id}":
		return "GET, HEAD, OPTIONS"
	case "/admin/submissions/{id}/approve", "/admin/submissions/{id}/reject":
		return "POST, OPTIONS"
	case "/admin/sentences/new":
		return "GET, HEAD, OPTIONS"
	case "/admin/sentences":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/sentences/{uuid}":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/sentences/{uuid}/disable", "/admin/sentences/{uuid}/enable":
		return "POST, OPTIONS"
	case "/admin/categories/new":
		return "GET, HEAD, OPTIONS"
	case "/admin/categories":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/categories/{code}":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/categories/{code}/disable", "/admin/categories/{code}/enable":
		return "POST, OPTIONS"
	case "/admin/users":
		return "GET, HEAD, POST, OPTIONS"
	case "/admin/users/{id}/disable", "/admin/users/{id}/enable", "/admin/users/{id}/reset-password":
		return "POST, OPTIONS"
	default:
		return "GET, HEAD, OPTIONS"
	}
}

func (h *handler) public(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		next(w, r)
	}
}

func (h *handler) protected(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		sess, ok := h.loadSession(w, r)
		if !ok {
			return
		}
		if r.Method == http.MethodPost {
			if !parseForm(w, r) {
				h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", "请求无效")
				return
			}
			if err := auth.CheckCSRF(r, sess.CSRFToken); err != nil {
				h.renderer.Error(w, r, http.StatusForbidden, "csrf-denied", "请求被拒绝", "CSRF 校验失败")
				return
			}
		}
		next(w, r.WithContext(withSession(r.Context(), sess)))
	}
}

func (h *handler) loadSession(w http.ResponseWriter, r *http.Request) (Session, bool) {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil || c.Value == "" {
		h.clearAndRedirectLogin(w, r)
		return Session{}, false
	}
	now := nowUTC()
	hash := auth.HashToken(c.Value)
	sess, err := h.store.GetSession(r.Context(), hash, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.clearAndRedirectLogin(w, r)
			return Session{}, false
		}
		h.logger.Error("admin session lookup failed", "admin_id", uint64(0))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "会话读取失败")
		return Session{}, false
	}
	if now.Sub(sess.LastSeenAt) > auth.SessionIdleTTL {
		_ = h.store.DeleteSession(r.Context(), hash)
		h.clearAndRedirectLogin(w, r)
		return Session{}, false
	}
	if now.Sub(sess.LastSeenAt) > auth.SessionTouchInterval {
		if err := h.store.TouchSession(r.Context(), hash, now); err != nil {
			h.logger.Error("admin session touch failed", "admin_id", sess.AdminID)
		}
	}
	return Session{
		AdminID:   sess.AdminID,
		Username:  sess.Username,
		CSRFToken: sess.CSRFToken,
		TokenHash: hash,
		RawToken:  c.Value,
	}, true
}

func (h *handler) clearAndRedirectLogin(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, auth.ClearSessionCookie(h.cookieSecure))
	redirect(w, r, "/admin/login")
}

func (h *handler) getOrRedirect(w http.ResponseWriter, r *http.Request) (Session, bool) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		h.clearAndRedirectLogin(w, r)
		return Session{}, false
	}
	return sess, true
}

func (h *handler) registerGet(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, h.protected(fn))
}

func (h *handler) registerGetPublic(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, h.public(fn))
}

func (h *handler) registerPost(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, h.protected(fn))
}

func (h *handler) registerPostPublic(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, h.public(fn))
}

// loginOnly skips CSRF on login POST but still uses public wrapper.
func (h *handler) registerLoginPost(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		if !parseForm(w, r) {
			h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", "请求无效")
			return
		}
		fn(w, r)
	})
}
