package public

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"sentence-api/internal/web/store"
)

type indexData struct {
	APIURL       string
	CategoryJSON template.JS
	Categories   []categoryView
}

type recentView struct {
	Content, CategoryName, Nickname string
}

type docsData struct {
	Version        string
	APIBase        string
	APIBaseDisplay string
	Categories     []categoryView
}

type categoryView struct {
	Code, Name string
	Count      uint64
}

type datasetPageData struct {
	Version       string
	SentenceCount uint64
}

func recentViews(data *store.PublicData) []recentView {
	if data == nil {
		return nil
	}
	recent := make([]recentView, 0, len(data.Recent))
	for _, item := range data.Recent {
		nick := item.Nickname
		if nick == "" {
			nick = "匿名"
		}
		recent = append(recent, recentView{
			Content:      item.Content,
			CategoryName: item.CategoryName,
			Nickname:     nick,
		})
	}
	return recent
}

func (h *handler) getIndex(w http.ResponseWriter, r *http.Request) {
	data := h.cache.Current()
	names := make(map[string]string, len(data.Categories))
	categories := make([]categoryView, 0, len(data.Categories))
	for _, c := range data.Categories {
		names[c.Code] = c.Name
		if c.Count > 0 {
			categories = append(categories, categoryView{Code: c.Code, Name: c.Name, Count: c.Count})
		}
	}
	raw, err := json.Marshal(names)
	if err != nil {
		raw = []byte("{}")
	}
	base := strings.TrimRight(h.renderer.Site().PublicOrigin, "/")
	apiURL := "/api/v1"
	if base != "" {
		apiURL = base + "/api/v1"
	}
	h.renderer.HTML(w, r, h.indexPages, "index", http.StatusOK, indexData{
		APIURL:       apiURL,
		CategoryJSON: template.JS(raw),
		Categories:   categories,
	})
}

func (h *handler) getDocs(w http.ResponseWriter, r *http.Request) {
	data := h.cache.Current()
	cats := make([]categoryView, len(data.Categories))
	for i, c := range data.Categories {
		cats[i] = categoryView{Code: c.Code, Name: c.Name, Count: c.Count}
	}
	base := strings.TrimRight(h.renderer.Site().PublicOrigin, "/")
	display := base
	if display == "" {
		display = "（当前站点，相对路径）"
	}
	h.renderer.HTML(w, r, h.docsPages, "docs", http.StatusOK, docsData{
		Version:        strconv.FormatUint(data.Version, 10),
		APIBase:        base,
		APIBaseDisplay: display,
		Categories:     cats,
	})
}

func (h *handler) getDataset(w http.ResponseWriter, r *http.Request) {
	data := h.cache.Current()
	var count uint64
	for _, c := range data.Categories {
		count += c.Count
	}
	h.renderer.HTML(w, r, h.datasetPages, "dataset", http.StatusOK, datasetPageData{
		Version:       strconv.FormatUint(data.Version, 10),
		SentenceCount: count,
	})
}

func (h *handler) getSubmitDone(w http.ResponseWriter, r *http.Request) {
	h.renderer.HTML(w, r, h.donePages, "submit_done", http.StatusOK, nil)
}

func (h *handler) getSentencesJSON(w http.ResponseWriter, r *http.Request) {
	data := h.cache.Current()
	etag := fmt.Sprintf(`"%d"`, data.Version)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sentences-%d.json"`, data.Version))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(data.ExportJSON)))

	if inm := r.Header.Get("If-None-Match"); matchesETag(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data.ExportJSON)
	}
}

func matchesETag(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == etag || part == "*" {
			return true
		}
	}
	return false
}
