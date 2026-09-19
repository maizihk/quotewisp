package admin

import (
	"errors"
	"net/http"
	"time"

	"sentence-api/internal/httpmw"
	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

type loginPageData struct {
	Flash     string
	Error     string
	FormToken string
}

func (h *handler) getLogin(w http.ResponseWriter, r *http.Request) {
	now := nowUTC()
	h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusOK, loginPageData{
		FormToken: h.tokens.Issue(now),
	})
}

func (h *handler) postLogin(w http.ResponseWriter, r *http.Request) {
	now := nowUTC()
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		h.renderLoginForm(w, r, http.StatusBadRequest, "", now)
		return
	}
	token := r.FormValue("form_token")
	if !h.tokens.Verify(token, now, 0, 2*time.Hour) {
		h.renderLoginForm(w, r, http.StatusBadRequest, "", now)
		return
	}

	usernameRaw := trim(r.FormValue("username"))
	password := r.FormValue("password")
	ip := httpmw.ClientIPFrom(r.Context())

	username, uerr := auth.ValidateUsername(usernameRaw)
	if uerr != nil {
		username = usernameRaw
	}

	if !h.logins.Reserve(ip, username, now) {
		h.metrics.LoginAttempt("rate_limited")
		h.renderer.Error(w, r, http.StatusTooManyRequests, "rate-limited", "尝试过于频繁", "请稍后再试")
		return
	}
	reserved := true
	defer func() {
		if reserved {
			h.logins.Release(ip, username)
		}
	}()
	if !h.logins.TryAcquireVerify() {
		h.metrics.LoginAttempt("rate_limited")
		h.renderer.Error(w, r, http.StatusTooManyRequests, "rate-limited", "尝试过于频繁", "请稍后再试")
		return
	}
	defer h.logins.ReleaseVerify()

	admin, err := h.store.GetAdminByUsername(r.Context(), username)
	if err != nil || !admin.Enabled {
		_, _ = auth.VerifyPassword(h.dummyHash, password)
		h.logins.Failure(ip, username, now)
		reserved = false
		h.metrics.LoginAttempt("failure")
		h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusUnauthorized, loginPageData{
			Error:     "用户名或密码错误",
			FormToken: h.tokens.Issue(now),
		})
		return
	}

	ok, verr := auth.VerifyPassword(admin.PasswordHash, password)
	if verr != nil || !ok {
		h.logins.Failure(ip, username, now)
		reserved = false
		h.metrics.LoginAttempt("failure")
		h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusUnauthorized, loginPageData{
			Error:     "用户名或密码错误",
			FormToken: h.tokens.Issue(now),
		})
		return
	}

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
	if err := h.store.CreateLoginSession(r.Context(), sess, admin.PasswordHash); errors.Is(err, store.ErrStaleAuth) || errors.Is(err, store.ErrNotFound) {
		h.metrics.LoginAttempt("failure")
		h.renderPage(w, r, []string{pageFile("login")}, "login", http.StatusUnauthorized, loginPageData{
			Error:     "用户名或密码错误",
			FormToken: h.tokens.Issue(now),
		})
		return
	} else if err != nil {
		h.logger.Error("admin login session failed", "admin_id", admin.ID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "登录失败")
		return
	}

	h.logins.Success(ip, username)
	reserved = false
	h.metrics.LoginAttempt("success")

	http.SetCookie(w, auth.SessionCookie(raw, h.cookieSecure, auth.SessionAbsoluteTTL))
	h.logger.Info("admin login success", "admin_id", admin.ID)
	redirect(w, r, "/admin/")
}

func (h *handler) renderLoginForm(w http.ResponseWriter, r *http.Request, status int, errMsg string, now time.Time) {
	h.renderPage(w, r, []string{pageFile("login")}, "login", status, loginPageData{
		Error:     errMsg,
		FormToken: h.tokens.Issue(now),
	})
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
