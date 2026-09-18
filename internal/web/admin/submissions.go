package admin

import (
	"errors"
	"net/http"

	"sentence-api/internal/web/store"
)

type pendingPageData struct {
	basePageData
	Items    []store.Submission
	Page     int
	Pages    int
	Total    int
	PrevPage int
	NextPage int
}

type submissionsListPageData struct {
	basePageData
	Items    []store.Submission
	Status   string
	Page     int
	Pages    int
	Total    int
	PrevPage int
	NextPage int
}

type submissionDetailPageData struct {
	basePageData
	Item       store.Submission
	Categories []store.Category
}

func (h *handler) getPending(w http.ResponseWriter, r *http.Request) {
	page := parsePage(r.URL.Query().Get("page"))
	items, total, err := h.store.ListSubmissions(r.Context(), store.SubmissionPending, page, pageSize)
	if err != nil {
		h.logger.Error("admin list pending failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载待审列表失败")
		return
	}
	pages := totalPages(total, pageSize)
	h.renderPage(w, r, []string{pageFile("pending")}, "pending", http.StatusOK, pendingPageData{
		basePageData: h.baseData(r.Context()),
		Items:        items,
		Page:         page,
		Pages:        pages,
		Total:        total,
		PrevPage:     prevPage(page),
		NextPage:     nextPage(page, pages),
	})
}

func (h *handler) getSubmissionsList(w http.ResponseWriter, r *http.Request) {
	statusParam := r.URL.Query().Get("status")
	var status uint8
	var label string
	switch statusParam {
	case "approved":
		status = store.SubmissionApproved
		label = "approved"
	case "rejected":
		status = store.SubmissionRejected
		label = "rejected"
	default:
		h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", "无效的状态筛选")
		return
	}
	page := parsePage(r.URL.Query().Get("page"))
	items, total, err := h.store.ListSubmissions(r.Context(), status, page, pageSize)
	if err != nil {
		h.logger.Error("admin list submissions failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载列表失败")
		return
	}
	pages := totalPages(total, pageSize)
	h.renderPage(w, r, []string{pageFile("submissions_list")}, "submissions_list", http.StatusOK, submissionsListPageData{
		basePageData: h.baseData(r.Context()),
		Items:        items,
		Status:       label,
		Page:         page,
		Pages:        pages,
		Total:        total,
		PrevPage:     prevPage(page),
		NextPage:     nextPage(page, pages),
	})
}

func (h *handler) getSubmissionDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUint64Param(r.PathValue("id"))
	if !ok {
		h.renderer.NotFound(w, r)
		return
	}
	item, err := h.store.GetSubmission(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("admin get submission failed", "admin_id", sessionAdminID(r), "submission_id", id)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	var cats []store.Category
	if item.Status == store.SubmissionPending {
		cats, err = h.store.ListCategoriesAdmin(r.Context())
		if err != nil {
			h.logger.Error("admin list categories failed", "admin_id", sessionAdminID(r))
			h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载分类失败")
			return
		}
		cats = enabledCategories(cats)
	}
	h.renderPage(w, r, []string{pageFile("submission_detail")}, "submission_detail", http.StatusOK, submissionDetailPageData{
		basePageData: h.baseData(r.Context()),
		Item:         item,
		Categories:   cats,
	})
}

func (h *handler) postApprove(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	id, ok := parseUint64Param(r.PathValue("id"))
	if !ok {
		h.renderer.NotFound(w, r)
		return
	}
	sub, err := h.store.GetSubmission(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("admin approve read failed", "admin_id", sess.AdminID, "submission_id", id)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载投稿失败")
		return
	}

	content := r.FormValue("content")
	category := r.FormValue("category")
	source := r.FormValue("source")
	author := r.FormValue("author")
	edit := fieldsChanged(sub, content, category, source, author)

	_, err = h.store.ApproveSubmission(r.Context(), id, sess.AdminID, edit)
	if err != nil {
		h.handleApproveError(w, r, id, sub, err)
		return
	}
	h.metrics.Review("approve", "success")
	h.logger.Info("admin approve success", "admin_id", sess.AdminID, "submission_id", id)
	h.onChange()
	redirect(w, r, "/admin/")
}

func (h *handler) handleApproveError(w http.ResponseWriter, r *http.Request, id uint64, sub store.Submission, err error) {
	sess, _ := sessionFrom(r.Context())
	switch {
	case errors.Is(err, store.ErrConflict):
		h.metrics.Review("approve", "conflict")
		h.renderSubmissionDetail(w, r, id, "已被其他管理员处理", http.StatusConflict)
	case errors.Is(err, store.ErrDuplicate):
		h.metrics.Review("approve", "duplicate")
		h.renderSubmissionDetail(w, r, id, "句库中已有相同语句，请拒绝", http.StatusConflict)
	case errors.Is(err, store.ErrCategoryDisabled), isValidation(err):
		h.metrics.Review("approve", "error")
		h.renderSubmissionDetail(w, r, id, validationMessage(err), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		h.metrics.Review("approve", "error")
		h.renderer.NotFound(w, r)
	default:
		h.metrics.Review("approve", "error")
		h.logger.Error("admin approve failed", "admin_id", sess.AdminID, "submission_id", id)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "审核通过失败")
	}
}

func (h *handler) postReject(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	id, ok := parseUint64Param(r.PathValue("id"))
	if !ok {
		h.renderer.NotFound(w, r)
		return
	}
	reason := r.FormValue("reject_reason")
	err := h.store.RejectSubmission(r.Context(), id, sess.AdminID, reason)
	if err != nil {
		h.handleRejectError(w, r, id, err)
		return
	}
	h.metrics.Review("reject", "success")
	h.logger.Info("admin reject success", "admin_id", sess.AdminID, "submission_id", id)
	redirect(w, r, "/admin/submissions?status=rejected")
}

func (h *handler) handleRejectError(w http.ResponseWriter, r *http.Request, id uint64, err error) {
	sess, _ := sessionFrom(r.Context())
	switch {
	case errors.Is(err, store.ErrConflict):
		h.metrics.Review("reject", "conflict")
		h.renderSubmissionDetail(w, r, id, "已被其他管理员处理", http.StatusConflict)
	case isValidation(err):
		h.metrics.Review("reject", "error")
		h.renderSubmissionDetail(w, r, id, validationMessage(err), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		h.metrics.Review("reject", "error")
		h.renderer.NotFound(w, r)
	default:
		h.metrics.Review("reject", "error")
		h.logger.Error("admin reject failed", "admin_id", sess.AdminID, "submission_id", id)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "拒绝失败")
	}
}

func (h *handler) renderSubmissionDetail(w http.ResponseWriter, r *http.Request, id uint64, flash string, status int) {
	item, err := h.store.GetSubmission(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	cats, _ := h.store.ListCategoriesAdmin(r.Context())
	data := submissionDetailPageData{
		basePageData: h.baseData(r.Context()),
		Item:         item,
		Categories:   enabledCategories(cats),
	}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("submission_detail")}, "submission_detail", status, data)
}

func sessionAdminID(r *http.Request) uint64 {
	if s, ok := sessionFrom(r.Context()); ok {
		return s.AdminID
	}
	return 0
}

func isValidation(err error) bool {
	var ve *store.ValidationError
	return errors.As(err, &ve)
}
