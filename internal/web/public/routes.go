package public

import "net/http"

func (h *handler) registerRoutes() {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", h.renderer.Static())
	mux.Handle("HEAD /static/", h.renderer.Static())
	mux.Handle("GET /assets/random.js", assetsHandler("assets/random.js", "application/javascript; charset=utf-8", "public, max-age=86400"))
	mux.Handle("HEAD /assets/random.js", assetsHandler("assets/random.js", "application/javascript; charset=utf-8", "public, max-age=86400"))
	mux.Handle("GET /dataset/LICENSE.txt", assetsHandler("LICENSE.txt", "text/plain; charset=utf-8", "public, max-age=86400"))
	mux.Handle("HEAD /dataset/LICENSE.txt", assetsHandler("LICENSE.txt", "text/plain; charset=utf-8", "public, max-age=86400"))

	h.registerGet(mux, "GET /{$}", h.getIndex)
	h.registerGet(mux, "GET /docs", h.getDocs)
	h.registerGet(mux, "GET /submit", h.getSubmit)
	h.registerPost(mux, "POST /submit", h.postSubmit)
	h.registerGet(mux, "GET /submit/done", h.getSubmitDone)
	h.registerGet(mux, "GET /dataset", h.getDataset)
	h.registerGet(mux, "GET /dataset/sentences.json", h.getSentencesJSON)
	mux.HandleFunc("/", h.fallback)

	h.mux = mux
}

func (h *handler) registerGet(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, h.page(fn))
	mux.HandleFunc("HEAD "+pattern[4:], h.page(fn))
}

func (h *handler) registerPost(mux *http.ServeMux, pattern string, fn http.HandlerFunc) {
	mux.HandleFunc(pattern, h.page(fn))
}
