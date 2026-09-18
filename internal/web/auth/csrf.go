package auth

import (
	"crypto/subtle"
	"net/http"
)

// CheckCSRF validates Sec-Fetch-Site and the csrf_token form field.
func CheckCSRF(r *http.Request, sessionCSRF [32]byte) error {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		if site != "same-origin" && site != "none" {
			return ErrCSRF
		}
	}

	got, ok := DecodeCSRF(r.PostFormValue("csrf_token"))
	if !ok {
		return ErrCSRF
	}
	if subtle.ConstantTimeCompare(got[:], sessionCSRF[:]) != 1 {
		return ErrCSRF
	}
	return nil
}
