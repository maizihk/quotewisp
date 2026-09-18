package admin

import (
	"errors"
	"net/http"
	"strconv"

	"sentence-api/internal/web/store"
)

type categoriesListPageData struct {
	basePageData
	Items []store.Category
}

type categoryFormPageData struct {
	basePageData
	Item *store.Category
	Form categoryFormFields
}

type categoryFormFields struct {
	Code      string
	Name      string
	SortOrder string
}

func (h *handler) getCategoriesList(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListCategoriesAdmin(r.Context())
	if err != nil {
		h.logger.Error("admin list categories failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载列表失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("categories_list")}, "categories_list", http.StatusOK, categoriesListPageData{
		basePageData: h.baseData(r.Context()),
		Items:        items,
	})
}

func (h *handler) getCategoryNew(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, []string{pageFile("category_form")}, "category_form", http.StatusOK, categoryFormPageData{
		basePageData: h.baseData(r.Context()),
	})
}

func (h *handler) postCategoryCreate(w http.ResponseWriter, r *http.Request) {
	fields := readCategoryForm(r)
	sortOrder, err := strconv.ParseInt(fields.SortOrder, 10, 32)
	if err != nil {
		h.renderCategoryForm(w, r, nil, fields, "排序必须是整数", http.StatusBadRequest)
		return
	}
	err = h.store.CreateCategory(r.Context(), fields.Code, fields.Name, int32(sortOrder))
	if err != nil {
		h.handleCategoryWriteError(w, r, "create", "", fields, err)
		return
	}
	h.metrics.CategoryChange("create", "success")
	h.onChange()
	redirect(w, r, "/admin/categories/"+fields.Code)
}

func (h *handler) getCategoryDetail(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	item, err := h.store.GetCategory(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("category_detail")}, "category_detail", http.StatusOK, categoryFormPageData{
		basePageData: h.baseData(r.Context()),
		Item:         &item,
		Form: categoryFormFields{
			Code: item.Code, Name: item.Name,
			SortOrder: strconv.FormatInt(int64(item.SortOrder), 10),
		},
	})
}

func (h *handler) postCategoryEdit(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	fields := readCategoryForm(r)
	fields.Code = code
	sortOrder, err := strconv.ParseInt(fields.SortOrder, 10, 32)
	if err != nil {
		h.renderCategoryDetail(w, r, code, fields, "排序必须是整数", http.StatusBadRequest)
		return
	}
	err = h.store.UpdateCategory(r.Context(), code, fields.Name, int32(sortOrder))
	if err != nil {
		h.handleCategoryWriteError(w, r, "edit", code, fields, err)
		return
	}
	h.metrics.CategoryChange("edit", "success")
	h.onChange()
	redirect(w, r, "/admin/categories/"+code)
}

func (h *handler) postCategoryDisable(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	confirm, err := strconv.ParseInt(r.FormValue("confirm"), 10, 64)
	if err != nil {
		h.renderCategoryDetailFlash(w, r, code, "确认参数无效", http.StatusBadRequest)
		return
	}
	err = h.store.SetCategoryEnabled(r.Context(), code, false, confirm)
	if err != nil {
		h.handleCategoryEnableError(w, r, code, "disable", err)
		return
	}
	h.metrics.CategoryChange("disable", "success")
	h.onChange()
	redirect(w, r, "/admin/categories")
}

func (h *handler) postCategoryEnable(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	err := h.store.SetCategoryEnabled(r.Context(), code, true, 0)
	if err != nil {
		h.handleCategoryEnableError(w, r, code, "enable", err)
		return
	}
	h.metrics.CategoryChange("enable", "success")
	h.onChange()
	redirect(w, r, "/admin/categories/"+code)
}

func readCategoryForm(r *http.Request) categoryFormFields {
	return categoryFormFields{
		Code:      r.FormValue("code"),
		Name:      r.FormValue("name"),
		SortOrder: r.FormValue("sort_order"),
	}
}

func (h *handler) handleCategoryWriteError(w http.ResponseWriter, r *http.Request, action, code string, fields categoryFormFields, err error) {
	switch {
	case errors.Is(err, store.ErrUnchanged):
		h.metrics.CategoryChange(action, "unchanged")
		if code != "" {
			h.renderCategoryDetail(w, r, code, fields, "无变化", http.StatusOK)
		} else {
			h.renderCategoryForm(w, r, nil, fields, "无变化", http.StatusOK)
		}
	case errors.Is(err, store.ErrConflict):
		h.metrics.CategoryChange(action, "conflict")
		if code != "" {
			h.renderCategoryDetail(w, r, code, fields, "数据已变化，请刷新后重试", http.StatusConflict)
		} else {
			h.renderCategoryForm(w, r, nil, fields, "代码已存在", http.StatusConflict)
		}
	case isValidation(err):
		h.metrics.CategoryChange(action, "error")
		if code != "" {
			h.renderCategoryDetail(w, r, code, fields, validationMessage(err), http.StatusBadRequest)
		} else {
			h.renderCategoryForm(w, r, nil, fields, validationMessage(err), http.StatusBadRequest)
		}
	case errors.Is(err, store.ErrNotFound):
		h.metrics.CategoryChange(action, "error")
		h.renderer.NotFound(w, r)
	default:
		h.metrics.CategoryChange(action, "error")
		h.logger.Error("admin category write failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "保存失败")
	}
}

func (h *handler) handleCategoryEnableError(w http.ResponseWriter, r *http.Request, code, action string, err error) {
	switch {
	case errors.Is(err, store.ErrConflict):
		h.metrics.CategoryChange(action, "conflict")
		h.renderCategoryDetailFlash(w, r, code, "数据已变化，请刷新后重试", http.StatusConflict)
	case errors.Is(err, store.ErrUnchanged):
		h.metrics.CategoryChange(action, "unchanged")
		h.renderCategoryDetailFlash(w, r, code, "无变化", http.StatusOK)
	case errors.Is(err, store.ErrNotFound):
		h.metrics.CategoryChange(action, "error")
		h.renderer.NotFound(w, r)
	default:
		h.metrics.CategoryChange(action, "error")
		h.logger.Error("admin category enable failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "操作失败")
	}
}

func (h *handler) renderCategoryForm(w http.ResponseWriter, r *http.Request, item *store.Category, fields categoryFormFields, flash string, status int) {
	data := categoryFormPageData{basePageData: h.baseData(r.Context()), Item: item, Form: fields}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("category_form")}, "category_form", status, data)
}

func (h *handler) renderCategoryDetail(w http.ResponseWriter, r *http.Request, code string, fields categoryFormFields, flash string, status int) {
	item, err := h.store.GetCategory(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	data := categoryFormPageData{basePageData: h.baseData(r.Context()), Item: &item, Form: fields}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("category_detail")}, "category_detail", status, data)
}

func (h *handler) renderCategoryDetailFlash(w http.ResponseWriter, r *http.Request, code, flash string, status int) {
	item, err := h.store.GetCategory(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	}
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载详情失败")
		return
	}
	data := categoryFormPageData{
		basePageData: h.baseData(r.Context()),
		Item:         &item,
		Form: categoryFormFields{
			Code: item.Code, Name: item.Name,
			SortOrder: strconv.FormatInt(int64(item.SortOrder), 10),
		},
	}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("category_detail")}, "category_detail", status, data)
}
