package admin

import (
	"errors"
	"net/http"

	"sentence-api/internal/web/store"
)

type sentencesListPageData struct {
	basePageData
	Items    []store.Sentence
	Category string
	Status   string
	Query    string
	UUID     string
	Page     int
	Pages    int
	Total    int
	PrevPage int
	NextPage int
}

type sentenceFormPageData struct {
	basePageData
	Item       *store.Sentence
	Categories []store.Category
	Form       sentenceFormFields
}

type sentenceFormFields struct {
	Content  string
	Category string
	Source   string
	Author   string
}

func (h *handler) getSentencesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, err := parseSentenceStatus(q.Get("status"))
	if err != nil {
		h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", "无效的状态筛选")
		return
	}
	page := parsePage(q.Get("page"))
	filter := store.SentenceFilter{
		CategoryCode: q.Get("category"),
		Status:       status,
		Query:        q.Get("q"),
		UUID:         q.Get("uuid"),
		Page:         page,
		Size:         pageSize,
	}
	items, total, err := h.store.ListSentences(r.Context(), filter)
	if err != nil {
		if isValidation(err) {
			h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", validationMessage(err))
			return
		}
		h.logger.Error("admin list sentences failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载列表失败")
		return
	}
	pages := totalPages(total, pageSize)
	h.renderPage(w, r, []string{pageFile("sentences_list")}, "sentences_list", http.StatusOK, sentencesListPageData{
		basePageData: h.baseData(r.Context()),
		Items:        items,
		Category:     filter.CategoryCode,
		Status:       q.Get("status"),
		Query:        filter.Query,
		UUID:         filter.UUID,
		Page:         page,
		Pages:        pages,
		Total:        total,
		PrevPage:     prevPage(page),
		NextPage:     nextPage(page, pages),
	})
}

func (h *handler) getSentenceNew(w http.ResponseWriter, r *http.Request) {
	cats, err := h.store.ListCategoriesAdmin(r.Context())
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载分类失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("sentence_form")}, "sentence_form", http.StatusOK, sentenceFormPageData{
		basePageData: h.baseData(r.Context()),
		Categories:   enabledCategories(cats),
	})
}

func (h *handler) postSentenceCreate(w http.ResponseWriter, r *http.Request) {
	fields := sentenceFormFields{
		Content:  r.FormValue("content"),
		Category: r.FormValue("category"),
		Source:   r.FormValue("source"),
		Author:   r.FormValue("author"),
	}
	uuid, err := h.store.CreateSentence(r.Context(), store.SentenceFields{
		Content: fields.Content, CategoryCode: fields.Category,
		Source: fields.Source, Author: fields.Author,
	})
	if err != nil {
		h.handleSentenceWriteError(w, r, "create", nil, fields, err)
		return
	}
	h.metrics.SentenceChange("create", "success")
	h.onChange()
	redirect(w, r, "/admin/sentences/"+uuid)
}

func (h *handler) getSentenceDetail(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	item, err := h.store.GetSentence(r.Context(), uuid)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		if isValidation(err) {
			h.renderer.NotFound(w, r)
			return
		}
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	cats, err := h.store.ListCategoriesAdmin(r.Context())
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载分类失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("sentence_detail")}, "sentence_detail", http.StatusOK, sentenceFormPageData{
		basePageData: h.baseData(r.Context()),
		Item:         &item,
		Categories:   enabledCategories(cats),
		Form: sentenceFormFields{
			Content: item.Content, Category: item.CategoryCode,
			Source: item.Source, Author: item.Author,
		},
	})
}

func (h *handler) postSentenceEdit(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	fields := sentenceFormFields{
		Content:  r.FormValue("content"),
		Category: r.FormValue("category"),
		Source:   r.FormValue("source"),
		Author:   r.FormValue("author"),
	}
	err := h.store.UpdateSentence(r.Context(), uuid, store.SentenceFields{
		Content: fields.Content, CategoryCode: fields.Category,
		Source: fields.Source, Author: fields.Author,
	})
	if err != nil {
		h.handleSentenceWriteError(w, r, "edit", &uuid, fields, err)
		return
	}
	h.metrics.SentenceChange("edit", "success")
	h.onChange()
	redirect(w, r, "/admin/sentences/"+uuid)
}

func (h *handler) postSentenceDisable(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	err := h.store.SetSentenceStatus(r.Context(), uuid, 1, 3)
	if err != nil {
		h.handleSentenceActionError(w, r, uuid, "disable", err)
		return
	}
	h.metrics.SentenceChange("disable", "success")
	h.onChange()
	redirect(w, r, "/admin/sentences/"+uuid)
}

func (h *handler) postSentenceEnable(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	err := h.store.SetSentenceStatus(r.Context(), uuid, 3, 1)
	if err != nil {
		h.handleSentenceActionError(w, r, uuid, "enable", err)
		return
	}
	h.metrics.SentenceChange("enable", "success")
	h.onChange()
	redirect(w, r, "/admin/sentences/"+uuid)
}

func (h *handler) handleSentenceWriteError(w http.ResponseWriter, r *http.Request, action string, uuid *string, fields sentenceFormFields, err error) {
	switch {
	case errors.Is(err, store.ErrUnchanged):
		h.metrics.SentenceChange(action, "unchanged")
		h.renderSentencePage(w, r, uuid, fields, "无变化", http.StatusOK)
	case errors.Is(err, store.ErrConflict):
		h.metrics.SentenceChange(action, "conflict")
		h.renderSentencePage(w, r, uuid, fields, "数据已变化，请刷新后重试", http.StatusConflict)
	case errors.Is(err, store.ErrDuplicate):
		h.metrics.SentenceChange(action, "duplicate")
		h.renderSentencePage(w, r, uuid, fields, "句库中已有相同语句", http.StatusConflict)
	case errors.Is(err, store.ErrCategoryDisabled), isValidation(err):
		h.metrics.SentenceChange(action, "error")
		h.renderSentencePage(w, r, uuid, fields, validationMessage(err), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		h.metrics.SentenceChange(action, "error")
		h.renderer.NotFound(w, r)
	default:
		h.metrics.SentenceChange(action, "error")
		h.logger.Error("admin sentence write failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "保存失败")
	}
}

func (h *handler) handleSentenceActionError(w http.ResponseWriter, r *http.Request, uuid, action string, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		h.metrics.SentenceChange(action, "conflict")
		h.renderSentenceDetailFlash(w, r, uuid, "已被处理，请刷新后重试", http.StatusConflict)
	case errors.Is(err, store.ErrNotFound):
		h.metrics.SentenceChange(action, "error")
		h.renderer.NotFound(w, r)
	default:
		h.metrics.SentenceChange(action, "error")
		h.logger.Error("admin sentence action failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "操作失败")
	}
}

func (h *handler) renderSentencePage(w http.ResponseWriter, r *http.Request, uuid *string, fields sentenceFormFields, flash string, status int) {
	cats, _ := h.store.ListCategoriesAdmin(r.Context())
	data := sentenceFormPageData{
		basePageData: h.baseData(r.Context()),
		Categories:   enabledCategories(cats),
		Form:         fields,
	}
	data.Flash = flash
	if uuid != nil {
		item, err := h.store.GetSentence(r.Context(), *uuid)
		if err == nil {
			data.Item = &item
		}
		h.renderPage(w, r, []string{pageFile("sentence_detail")}, "sentence_detail", status, data)
		return
	}
	h.renderPage(w, r, []string{pageFile("sentence_form")}, "sentence_form", status, data)
}

func (h *handler) renderSentenceDetailFlash(w http.ResponseWriter, r *http.Request, uuid, flash string, status int) {
	item, err := h.store.GetSentence(r.Context(), uuid)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	cats, _ := h.store.ListCategoriesAdmin(r.Context())
	data := sentenceFormPageData{
		basePageData: h.baseData(r.Context()),
		Item:         &item,
		Categories:   enabledCategories(cats),
		Form: sentenceFormFields{
			Content: item.Content, Category: item.CategoryCode,
			Source: item.Source, Author: item.Author,
		},
	}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("sentence_detail")}, "sentence_detail", status, data)
}
