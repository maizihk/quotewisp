package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// NewToken returns a random session token and its SHA-256 hash.
func NewToken() (raw string, hash [32]byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", hash, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	hash = HashToken(raw)
	return raw, hash, nil
}

// HashToken returns SHA-256 of the raw token string bytes.
func HashToken(raw string) [32]byte {
	return sha256.Sum256([]byte(raw))
}

// NewCSRFToken returns 32 random bytes for CSRF protection.
func NewCSRFToken() ([32]byte, error) {
	var tok [32]byte
	_, err := rand.Read(tok[:])
	return tok, err
}

// EncodeCSRF hex-encodes a CSRF token for form fields.
func EncodeCSRF(tok [32]byte) string {
	return hex.EncodeToString(tok[:])
}

// DecodeCSRF parses a hex CSRF token from a form field.
func DecodeCSRF(s string) ([32]byte, bool) {
	var tok [32]byte
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return tok, false
	}
	copy(tok[:], b)
	return tok, true
}
