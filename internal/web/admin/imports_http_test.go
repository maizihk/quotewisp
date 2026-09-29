package admin_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"sentence-api/internal/importjobs"
)

type importHTTPFake struct {
	uploadErr error
	gotOwner  uint64
}

func (f *importHTTPFake) Upload(_ context.Context, owner uint64, _ string, r io.Reader) (importjobs.Job, error) {
	f.gotOwner = owner
	_, err := io.ReadAll(r)
	if f.uploadErr != nil {
		return importjobs.Job{}, f.uploadErr
	}
	if err != nil {
		return importjobs.Job{}, errors.Join(importjobs.ErrUpload, err)
	}
	return importjobs.Job{ID: "job-1", Status: "preview"}, nil
}
func (f *importHTTPFake) Get(context.Context, uint64, string) (importjobs.Job, error) {
	return importjobs.Job{}, errors.New("unused")
}
func (f *importHTTPFake) List(context.Context, uint64) ([]importjobs.Job, error) { return nil, nil }
func (f *importHTTPFake) Confirm(context.Context, uint64, string, string) error  { return nil }
func (f *importHTTPFake) Cancel(context.Context, uint64, string) error           { return nil }
func (f *importHTTPFake) RetryRefresh(context.Context, uint64, string) error     { return nil }

func multipartImport(t *testing.T, csrf, content string) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	_ = mw.WriteField("csrf_token", csrf)
	_ = mw.WriteField("format", "native")
	f, err := mw.CreateFormFile("file", "data.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, content)
	_ = mw.Close()
	return &b, mw.FormDataContentType()
}

func TestImportUploadRedirectsAndChecksCSRF(t *testing.T) {
	fake := &importHTTPFake{}
	env := setupEnvWithImports(t, fake)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/imports", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	b, ct := multipartImport(t, token, `{"sentences":[]}`)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/admin/imports", b)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp := serveImport(env, req)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Location") != "/admin/imports/job-1" {
		t.Fatalf("location=%q", resp.Header.Get("Location"))
	}
	if fake.gotOwner != env.adminID {
		t.Fatalf("owner=%d want %d", fake.gotOwner, env.adminID)
	}

	b, ct = multipartImport(t, "bad", `{}`)
	req, _ = http.NewRequest(http.MethodPost, "http://example.com/admin/imports", b)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp = serveImport(env, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("csrf status=%d", resp.StatusCode)
	}
}

func TestImportUploadTooLarge(t *testing.T) {
	fake := &importHTTPFake{}
	env := setupEnvWithImports(t, fake)
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/imports", nil, nil)
	body, _ := io.ReadAll(page.Body)
	token := csrfFromPage(t, string(body))
	b, ct := multipartImport(t, token, string(bytes.Repeat([]byte("x"), 2048)))
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/admin/imports", b)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp := serveImport(env, req)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func serveImport(env *testEnv, req *http.Request) *http.Response {
	for _, cookie := range env.client.Jar.Cookies(req.URL) {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	return rec.Result()
}

func TestImportRejectsExtraMultipartParts(t *testing.T) {
	env := setupEnvWithImports(t, &importHTTPFake{})
	env.login(t)
	page := env.do(t, http.MethodGet, "/admin/imports", nil, nil)
	raw, _ := io.ReadAll(page.Body)
	csrf := csrfFromPage(t, string(raw))
	for _, kind := range []string{"second-file", "trailing-field", "duplicate-format", "unknown-field", "cross-site"} {
		t.Run(kind, func(t *testing.T) {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			_ = w.WriteField("csrf_token", csrf)
			_ = w.WriteField("format", "native")
			if kind == "duplicate-format" {
				_ = w.WriteField("format", "hitokoto")
			}
			if kind == "unknown-field" {
				_ = w.WriteField("other", "value")
			}
			part, _ := w.CreateFormFile("file", "data.json")
			_, _ = io.WriteString(part, "{}")
			if kind == "second-file" {
				part, _ = w.CreateFormFile("file", "extra.json")
				_, _ = io.WriteString(part, "{}")
			}
			if kind == "trailing-field" {
				_ = w.WriteField("format", "native")
			}
			_ = w.Close()
			req, _ := http.NewRequest("POST", "http://example.com/admin/imports", &body)
			req.Header.Set("Content-Type", w.FormDataContentType())
			want := http.StatusBadRequest
			if kind == "cross-site" {
				req.Header.Set("Sec-Fetch-Site", "cross-site")
				want = http.StatusForbidden
			}
			if resp := serveImport(env, req); resp.StatusCode != want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, want)
			}
		})
	}
}
