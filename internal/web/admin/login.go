package admin

import (
	"errors"
	"net/http"

	"sentence-api/internal/httpmw"
	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

type loginPageData struct {
	Flash string
	Error string
}

func (h *handler) getLogin(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusOK, loginPageData{})
}

func (h *handler) postLogin(w http.ResponseWriter, r *http.Request) {
	usernameRaw := trim(r.FormValue("username"))
	password := r.FormValue("password")
	now := nowUTC()
	ip := httpmw.ClientIPFrom(r.Context())

	username, uerr := auth.ValidateUsername(usernameRaw)
	if uerr != nil {
		username = usernameRaw
	}

	if !h.logins.Allow(ip, username, now) {
		h.metrics.LoginAttempt("rate_limited")
		h.renderer.Error(w, r, http.StatusTooManyRequests, "rate-limited", "尝试过于频繁", "请稍后再试")
		return
	}

	admin, err := h.store.GetAdminByUsername(r.Context(), username)
	if err != nil || !admin.Enabled {
		_, _ = auth.VerifyPassword(h.dummyHash, password)
		h.logins.Failure(ip, username, now)
		h.metrics.LoginAttempt("failure")
		h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusUnauthorized, loginPageData{
			Error: "用户名或密码错误",
		})
		return
	}

	ok, verr := auth.VerifyPassword(admin.PasswordHash, password)
	if verr != nil || !ok {
		h.logins.Failure(ip, username, now)
		h.metrics.LoginAttempt("failure")
		h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusUnauthorized, loginPageData{
			Error: "用户名或密码错误",
		})
		return
	}

	h.logins.Success(username)
	h.metrics.LoginAttempt("success")

	raw, hash, err := auth.NewToken()
	if err != nil {
		h.logger.Error("admin login token failed", "admin_id", admin.ID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "登录失败")
		return
	}
	csrf, err := auth.NewCSRFToken()
	if err != nil {
		h.logger.Error("admin login csrf failed", "admin_id", admin.ID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "登录失败")
		return
	}

	expires := now.Add(auth.SessionAbsoluteTTL)
	sess := store.Session{
		TokenHash:  hash,
		AdminID:    admin.ID,
		CSRFToken:  csrf,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expires,
	}
	if err := h.store.CreateSession(r.Context(), sess); err != nil {
		h.logger.Error("admin login session failed", "admin_id", admin.ID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "登录失败")
		return
	}
	if err := h.store.TouchAdminLogin(r.Context(), admin.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logger.Error("admin login touch failed", "admin_id", admin.ID)
	}

	http.SetCookie(w, auth.SessionCookie(raw, h.cookieSecure, auth.SessionAbsoluteTTL))
	h.logger.Info("admin login success", "admin_id", admin.ID)
	redirect(w, r, "/admin/")
}

func (h *handler) postLogout(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	if err := h.store.DeleteSession(r.Context(), sess.TokenHash); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.logger.Error("admin logout failed", "admin_id", sess.AdminID)
	}
	http.SetCookie(w, auth.ClearSessionCookie(h.cookieSecure))
	redirect(w, r, "/admin/login")
}
