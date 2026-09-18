package render

import (
	"net/http"

	"sentence-api/internal/httpmw"
)

func SecurityHeaders(next http.Handler) http.Handler {
	extra := http.Header{
		"Content-Security-Policy": {"default-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'"},
		"Referrer-Policy":         {"same-origin"},
	}
	return httpmw.SecurityHeadersWith(extra, next)
}
