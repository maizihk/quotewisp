package render

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sentence-api/internal/httpmw"
)

//go:embed templates/*
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Site struct {
	Name, Contact, RepoURL, Version string
}

type View struct {
	Site      Site
	RequestID string
	Data      any
}

type errorViewData struct {
	Status int
	Title  string
	Detail string
	Code   string
}

type Renderer struct {
	site Site
	base *template.Template
}

func New(site Site) (*Renderer, error) {
	base := template.New("base").Funcs(templateFuncs())
	if _, err := base.ParseFS(templateFS, "templates/layout.html", "templates/error.html"); err != nil {
		return nil, err
	}
	return &Renderer{site: site, base: base}, nil
}

func (r *Renderer) Pages(fsys fs.FS, patterns ...string) (*template.Template, error) {
	t, err := r.base.Clone()
	if err != nil {
		return nil, err
	}
	return t.ParseFS(fsys, patterns...)
}

func (r *Renderer) HTML(w http.ResponseWriter, req *http.Request, t *template.Template, name string, status int, data any) {
	view := View{
		Site:      r.site,
		RequestID: httpmw.RequestIDFrom(req.Context()),
		Data:      data,
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, view); err != nil {
		r.Error(w, req, http.StatusInternalServerError, "internal-error", "内部错误", "页面渲染失败")
		return
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if req.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

func (r *Renderer) Error(w http.ResponseWriter, req *http.Request, status int, code, title, detail string) {
	if prefersJSON(req) {
		writeProblem(w, req, status, title, code, detail)
		return
	}
	view := View{
		Site:      r.site,
		RequestID: httpmw.RequestIDFrom(req.Context()),
		Data: errorViewData{
			Status: status,
			Title:  title,
			Detail: detail,
			Code:   code,
		},
	}
	var buf bytes.Buffer
	if err := r.base.ExecuteTemplate(&buf, "error", view); err != nil {
		writeProblem(w, req, http.StatusInternalServerError, "内部错误", "internal-error", "错误页面渲染失败")
		return
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if req.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

func (r *Renderer) NotFound(w http.ResponseWriter, req *http.Request) {
	r.Error(w, req, http.StatusNotFound, "not-found", "未找到", "请求的资源不存在")
}

func (r *Renderer) MethodNotAllowed(w http.ResponseWriter, req *http.Request, allow string) {
	w.Header().Set("Allow", allow)
	r.Error(w, req, http.StatusMethodNotAllowed, "method-not-allowed", "方法不允许", "该路径不支持此方法")
}

func (r *Renderer) Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return &staticHandler{fs: http.FS(sub)}
}

func writeProblem(w http.ResponseWriter, req *http.Request, status int, title, code, detail string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{
		"type":       "about:blank",
		"title":      title,
		"status":     status,
		"detail":     detail,
		"request_id": httpmw.RequestIDFrom(req.Context()),
		"code":       code,
	}); err != nil {
		panic(err)
	}
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if req.Method != http.MethodHead {
		_, _ = w.Write(buf.Bytes())
	}
}

func prefersJSON(req *http.Request) bool {
	accept := req.Header.Get("Accept")
	if accept == "" {
		return false
	}
	var htmlQ, jsonQ float64
	var hasHTML, hasJSON bool
	for _, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		typ := part
		q := 1.0
		if i := strings.Index(part, ";"); i >= 0 {
			typ = strings.TrimSpace(part[:i])
			for _, param := range strings.Split(part[i+1:], ";") {
				param = strings.TrimSpace(param)
				if len(param) > 2 && strings.EqualFold(param[:2], "q=") {
					if parsed, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64); err == nil && parsed >= 0 && parsed <= 1 {
						q = parsed
					}
				}
			}
		}
		switch strings.ToLower(typ) {
		case "text/html":
			hasHTML = true
			if q > htmlQ {
				htmlQ = q
			}
		case "application/json", "application/problem+json":
			hasJSON = true
			if q > jsonQ {
				jsonQ = q
			}
		}
	}
	if !hasJSON {
		return false
	}
	if !hasHTML {
		return true
	}
	return jsonQ > htmlQ
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"formatTime": func(t time.Time) string {
			return t.UTC().Format("2006-01-02 15:04") + " UTC"
		},
		"truncate": func(n int, s string) string {
			if n <= 0 {
				return "…"
			}
			runes := []rune(s)
			if len(runes) <= n {
				return s
			}
			return string(runes[:n]) + "…"
		},
		"submissionStatus": func(status uint8) string {
			switch status {
			case 0:
				return "待审"
			case 1:
				return "已通过"
			case 2:
				return "已拒绝"
			default:
				return "未知"
			}
		},
		"sentenceStatus": func(status uint8) string {
			switch status {
			case 1:
				return "已发布"
			case 3:
				return "已停用"
			default:
				return "未知"
			}
		},
	}
}
