package render

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"time"
)

const (
	tokenTimeSize  = 8
	tokenNonceSize = 8
	tokenMACSize   = 16
	tokenSize      = tokenTimeSize + tokenNonceSize + tokenMACSize
)

type FormTokens struct {
	secret []byte
}

func NewFormTokens(secret []byte) *FormTokens {
	return &FormTokens{secret: append([]byte(nil), secret...)}
}

func (f *FormTokens) Issue(now time.Time) string {
	ts := make([]byte, tokenTimeSize)
	binary.BigEndian.PutUint64(ts, uint64(now.Unix()))

	nonce := make([]byte, tokenNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}

	payload := append(ts, nonce...)
	mac := hmac.New(sha256.New, f.secret)
	_, _ = mac.Write(payload)

	token := append(payload, mac.Sum(nil)[:tokenMACSize]...)
	return base64.RawURLEncoding.EncodeToString(token)
}

func (f *FormTokens) Verify(token string, now time.Time, minAge, maxAge time.Duration) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenSize {
		return false
	}

	tsBytes := raw[:tokenTimeSize]
	nonce := raw[tokenTimeSize : tokenTimeSize+tokenNonceSize]
	gotMAC := raw[tokenTimeSize+tokenNonceSize:]

	payload := append(append([]byte(nil), tsBytes...), nonce...)
	mac := hmac.New(sha256.New, f.secret)
	_, _ = mac.Write(payload)
	wantMAC := mac.Sum(nil)[:tokenMACSize]
	if subtle.ConstantTimeCompare(gotMAC, wantMAC) != 1 {
		return false
	}

	ts := time.Unix(int64(binary.BigEndian.Uint64(tsBytes)), 0).UTC()
	age := now.UTC().Sub(ts)
	if age < minAge || age > maxAge {
		return false
	}
	return true
}
