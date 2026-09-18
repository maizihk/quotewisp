package auth

import (
	"net/http"
	"time"
)

const SessionCookieName = "admin_session"

const (
	SessionAbsoluteTTL   = 24 * time.Hour
	SessionIdleTTL       = 2 * time.Hour
	SessionTouchInterval = 5 * time.Minute
)

// SessionCookie builds the admin session Set-Cookie value.
func SessionCookie(raw string, secure bool, maxAge time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    raw,
		Path:     "/admin",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}

// ClearSessionCookie returns a cookie that deletes the session.
func ClearSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/admin",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	}
}
