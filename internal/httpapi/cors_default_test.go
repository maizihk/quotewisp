package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestDefaultPublicCORS(t *testing.T) {
	h := handler(t, &provider{s: fixture(t)}, nil)
	for _, target := range []string{"/api/v1", "/api/v1/categories"} {
		for _, method := range []string{"GET", "HEAD"} {
			r := httptest.NewRequest(method, target, nil)
			r.Header.Set("Origin", "https://any.example")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%s %s: got %d", method, target, w.Code)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("%s headers: %#v", method, w.Header())
			}
		}
	}
	r := httptest.NewRequest("OPTIONS", "/api/v1/categories", nil)
	r.Header.Set("Origin", "https://any.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Headers", "Accept, Content-Type")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight %d %#v", w.Code, w.Header())
	}
}

func TestDefaultPublicCORSErrorsAndPrivateRoutes(t *testing.T) {
	h := handler(t, &provider{s: fixture(t)}, nil)
	for _, tc := range []struct {
		target string
		status int
	}{{"/api/v1/categories?bad=1", 400}, {"/api/v1/sentences/00000000-0000-4000-8000-000000000000", 404}} {
		r := httptest.NewRequest("GET", tc.target, nil)
		r.Header.Set("Origin", "https://any.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s got %d %#v", tc.target, w.Code, w.Header())
		}
	}
	for _, target := range []string{"/healthz", "/metrics", "/internal/reload", "/admin/login"} {
		r := httptest.NewRequest("GET", target, nil)
		r.Header.Set("Origin", "https://any.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s unexpectedly has CORS", target)
		}
	}
	r := httptest.NewRequest("OPTIONS", "/api/v1/categories", nil)
	r.Header.Set("Origin", "https://any.example")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("invalid preflight %d %#v", w.Code, w.Header())
	}
	r = httptest.NewRequest("OPTIONS", "/api/v1/categories", nil)
	r.Header.Set("Origin", "https://any.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Headers", "Authorization")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("invalid header preflight %d", w.Code)
	}
	h = handler(t, &provider{s: fixture(t)}, func(o *Options) { o.ReloadToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" })
	r = httptest.NewRequest("OPTIONS", "/internal/reload", nil)
	r.Header.Set("Origin", "https://any.example")
	r.Header.Set("Access-Control-Request-Method", "GET")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("internal reload unexpectedly has CORS")
	}
}
