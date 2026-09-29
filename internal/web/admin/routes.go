package admin

import "net/http"

func (h *handler) registerRoutes() {
	mux := http.NewServeMux()
	h.mux = mux

	h.registerGetPublic(mux, "GET /admin/login", h.getLogin)
	h.registerLoginPost(mux, "POST /admin/login", h.postLogin)

	h.registerPost(mux, "POST /admin/logout", h.postLogout)

	h.registerGet(mux, "GET /admin/{$}", h.getOverview)
	h.registerGet(mux, "GET /admin/submissions", h.getSubmissionsList)
	h.registerGet(mux, "GET /admin/submissions/{id}", h.getSubmissionDetail)
	h.registerPost(mux, "POST /admin/submissions/{id}/approve", h.postApprove)
	h.registerPost(mux, "POST /admin/submissions/{id}/reject", h.postReject)

	h.registerGet(mux, "GET /admin/sentences", h.getSentencesList)
	h.registerGet(mux, "GET /admin/sentences/new", h.getSentenceNew)
	h.registerPost(mux, "POST /admin/sentences", h.postSentenceCreate)
	h.registerGet(mux, "GET /admin/sentences/{uuid}", h.getSentenceDetail)
	h.registerPost(mux, "POST /admin/sentences/{uuid}", h.postSentenceEdit)
	h.registerPost(mux, "POST /admin/sentences/{uuid}/disable", h.postSentenceDisable)
	h.registerPost(mux, "POST /admin/sentences/{uuid}/enable", h.postSentenceEnable)

	h.registerGet(mux, "GET /admin/categories", h.getCategoriesList)
	h.registerGet(mux, "GET /admin/categories/new", h.getCategoryNew)
	h.registerPost(mux, "POST /admin/categories", h.postCategoryCreate)
	h.registerGet(mux, "GET /admin/categories/{code}", h.getCategoryDetail)
	h.registerPost(mux, "POST /admin/categories/{code}", h.postCategoryEdit)
	h.registerPost(mux, "POST /admin/categories/{code}/disable", h.postCategoryDisable)
	h.registerPost(mux, "POST /admin/categories/{code}/enable", h.postCategoryEnable)

	h.registerGet(mux, "GET /admin/users", h.getUsersList)
	h.registerPost(mux, "POST /admin/users", h.postUserCreate)
	h.registerPost(mux, "POST /admin/users/{id}/disable", h.postUserDisable)
	h.registerPost(mux, "POST /admin/users/{id}/enable", h.postUserEnable)
	h.registerGet(mux, "GET /admin/users/{id}/reset-password", h.getUserResetPassword)
	h.registerPost(mux, "POST /admin/users/{id}/reset-password", h.postUserResetPassword)

	h.registerGet(mux, "GET /admin/password", h.getPassword)
	h.registerPost(mux, "POST /admin/password", h.postPassword)

	h.registerGet(mux, "GET /admin/settings", h.getSettings)
	h.registerPost(mux, "POST /admin/settings", h.postSettings)
	h.registerGet(mux, "GET /admin/imports", h.getImports)
	h.registerGet(mux, "GET /admin/imports/{id}", h.getImport)
	h.registerPostUpload(mux, "POST /admin/imports", h.postImport)
	h.registerPost(mux, "POST /admin/imports/{id}/confirm", h.postImportConfirm)
	h.registerPost(mux, "POST /admin/imports/{id}/cancel", h.postImportCancel)
	h.registerPost(mux, "POST /admin/imports/{id}/refresh", h.postImportRefresh)

	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		setNoStore(w)
		redirect(w, r, "/admin/")
	})
	// Catch-all so ServeMux never emits its own plain-text 404/405; known
	// paths with a wrong method get a rendered 405 with the right Allow.
	mux.HandleFunc("/admin/", h.fallback)
}

func (h *handler) fallback(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if RouteName(r.URL.Path) == "unmatched" {
		h.renderer.NotFound(w, r)
		return
	}
	h.renderer.MethodNotAllowed(w, r, h.allowForPath(r.URL.Path))
}
