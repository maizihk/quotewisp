package auth

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestHashVerifyRoundtrip(t *testing.T) {
	phc, err := HashPassword("validpass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected PHC prefix: %s", phc)
	}
	ok, err := VerifyPassword(phc, "validpass")
	if err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
}

func TestVerifyWrongPassword(t *testing.T) {
	phc, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword(phc, "wrong horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password should not verify")
	}
}

func TestVerifyMalformedPHC(t *testing.T) {
	cases := []string{
		"",
		"not-a-phc",
		"$argon2id$v=19$m=2147483647,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=100000,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=18$m=65536,t=3,p=2$abc$abc",
		"$argon2i$v=19$m=65536,t=3,p=2$abc$abc",
	}
	for _, phc := range cases {
		_, err := VerifyPassword(phc, "x")
		if err != ErrBadHash {
			t.Fatalf("phc %q: want ErrBadHash, got %v", phc, err)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	short7 := strings.Repeat("a", 7)
	if _, err := HashPassword(short7); err != ErrWeakPassword {
		t.Fatalf("7 runes: got %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 8)); err != nil {
		t.Fatalf("8 runes should pass: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 128)); err != nil {
		t.Fatalf("128 runes should pass: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 129)); err != ErrWeakPassword {
		t.Fatalf("129 runes: got %v", err)
	}
	if _, err := HashPassword("valid\xff\xfe"); err != ErrWeakPassword {
		t.Fatalf("invalid UTF-8: got %v", err)
	}
}

func TestVerifyDifferentPHCParams(t *testing.T) {
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i + 1)
	}
	password := "testpass12"
	hash := argon2.IDKey([]byte(password), salt, 2, 32*1024, 1, 32)
	phc := fmt.Sprintf("$argon2id$v=19$m=32768,t=2,p=1$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash))
	ok, err := VerifyPassword(phc, password)
	if err != nil || !ok {
		t.Fatalf("alt params verify failed: ok=%v err=%v", ok, err)
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		in   string
		want string
		err  error
	}{
		{"Admin", "admin", nil},
		{"a_b-c", "a_b-c", nil},
		{"ab", "", ErrInvalidUsername},
		{"-bad", "", ErrInvalidUsername},
		{"UPPER", "upper", nil},
		{strings.Repeat("a", 32), strings.Repeat("a", 32), nil},
		{strings.Repeat("a", 33), "", ErrInvalidUsername},
	}
	for _, tc := range tests {
		got, err := ValidateUsername(tc.in)
		if got != tc.want || err != tc.err {
			t.Fatalf("ValidateUsername(%q) = (%q, %v), want (%q, %v)", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestTokenUniquenessAndHash(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 20; i++ {
		raw, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[raw]; dup {
			t.Fatal("duplicate token")
		}
		seen[raw] = struct{}{}
		if HashToken(raw) != hash {
			t.Fatal("HashToken mismatch")
		}
	}
}

func TestCSRFTokenEncodeDecode(t *testing.T) {
	tok, err := NewCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	enc := EncodeCSRF(tok)
	got, ok := DecodeCSRF(enc)
	if !ok || got != tok {
		t.Fatal("roundtrip failed")
	}
	_, ok = DecodeCSRF("not-hex")
	if ok {
		t.Fatal("invalid hex should fail")
	}
}

func TestCheckCSRF(t *testing.T) {
	tok, _ := NewCSRFToken()

	mkReq := func(site, csrf string) *http.Request {
		form := url.Values{}
		if csrf != "" {
			form.Set("csrf_token", csrf)
		}
		r := httptest.NewRequest(http.MethodPost, "/admin/x", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.PostForm = form
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		return r
	}

	if CheckCSRF(mkReq("", ""), tok) != ErrCSRF {
		t.Fatal("missing field should fail")
	}
	if CheckCSRF(mkReq("", "deadbeef"), tok) != ErrCSRF {
		t.Fatal("wrong value should fail")
	}
	if CheckCSRF(mkReq("cross-site", EncodeCSRF(tok)), tok) != ErrCSRF {
		t.Fatal("cross-site should fail")
	}
	if err := CheckCSRF(mkReq("same-origin", EncodeCSRF(tok)), tok); err != nil {
		t.Fatalf("same-origin + correct: %v", err)
	}
	if err := CheckCSRF(mkReq("none", EncodeCSRF(tok)), tok); err != nil {
		t.Fatalf("none + correct: %v", err)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	c := SessionCookie("tok123", true, SessionAbsoluteTTL)
	if c.Name != SessionCookieName || c.Value != "tok123" || c.Path != "/admin" ||
		!c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie attrs: %+v", c)
	}
	if c.MaxAge != int(SessionAbsoluteTTL.Seconds()) {
		t.Fatalf("maxAge = %d", c.MaxAge)
	}

	cl := ClearSessionCookie(true)
	if cl.MaxAge != -1 || cl.Value != "" {
		t.Fatalf("clear cookie: %+v", cl)
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter(3, 2, 15*time.Minute, 1000)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	if !l.Reserve("1.2.3.4", "alice", now) {
		t.Fatal("initial reserve")
	}
	l.Failure("1.2.3.4", "alice", now)
	if !l.Reserve("1.2.3.4", "alice", now) {
		t.Fatal("second reserve")
	}
	l.Failure("1.2.3.4", "alice", now)
	if l.Reserve("1.2.3.4", "alice", now.Add(time.Minute)) {
		t.Fatal("user limit should block")
	}
	if l.Reserve("5.6.7.8", "alice", now.Add(time.Minute)) {
		t.Fatal("user limit applies across IPs")
	}
	if !l.Reserve("1.2.3.4", "bob", now.Add(time.Minute)) {
		t.Fatal("different user on same IP should pass user check")
	}
	l.Release("1.2.3.4", "bob")

	l2 := NewLoginLimiter(3, 10, 15*time.Minute, 1000)
	for i := 0; i < 3; i++ {
		if !l2.Reserve("9.9.9.9", "carol", now) {
			t.Fatal("reserve before IP limit")
		}
		l2.Failure("9.9.9.9", "carol", now)
	}
	if l2.Reserve("9.9.9.9", "dave", now.Add(time.Minute)) {
		t.Fatal("IP limit should block")
	}

	l3 := NewLoginLimiter(10, 5, 15*time.Minute, 1000)
	for i := 0; i < 4; i++ {
		if !l3.Reserve("ip", "eve", now.Add(time.Duration(i)*time.Minute)) {
			t.Fatal("reserve eve")
		}
		l3.Failure("ip", "eve", now.Add(time.Duration(i)*time.Minute))
	}
	if !l3.Reserve("ip", "eve", now.Add(5*time.Minute)) {
		t.Fatal("reserve after four failures")
	}
	l3.Success("ip", "eve")
	if !l3.Reserve("ip", "eve", now.Add(5*time.Minute)) {
		t.Fatal("Success should reset user failures")
	}
	l3.Release("ip", "eve")
}

func TestLoginLimiterReserveOccupiesBudget(t *testing.T) {
	l := NewLoginLimiter(2, 2, 15*time.Minute, 1000)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if !l.Reserve("1.1.1.1", "a", now) || !l.Reserve("1.1.1.1", "b", now) {
		t.Fatal("two in-flight reserves")
	}
	if l.Reserve("1.1.1.1", "c", now) {
		t.Fatal("in-flight should occupy IP budget")
	}
	l.Release("1.1.1.1", "b")
	if !l.Reserve("1.1.1.1", "c", now) {
		t.Fatal("release should free IP budget")
	}
}

func TestLoginLimiterVerifySlots(t *testing.T) {
	l := NewLoginLimiter(10, 5, 15*time.Minute, 1000)
	l.verifySem = make(chan struct{}, 1)
	if !l.TryAcquireVerify() {
		t.Fatal("first verify slot")
	}
	if l.TryAcquireVerify() {
		t.Fatal("second verify slot should be refused")
	}
	l.ReleaseVerify()
	if !l.TryAcquireVerify() {
		t.Fatal("slot after release")
	}
}
