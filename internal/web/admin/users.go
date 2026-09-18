package admin

import (
	"errors"
	"net/http"

	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

type usersListPageData struct {
	basePageData
	Items    []store.AdminUser
	Username string
}

type passwordPageData struct {
	basePageData
	Flash string
}

func (h *handler) getUsersList(w http.ResponseWriter, r *http.Request) {
	items, err := h.store.ListAdmins(r.Context())
	if err != nil {
		h.logger.Error("admin list users failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载列表失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("users_list")}, "users_list", http.StatusOK, usersListPageData{
		basePageData: h.baseData(r.Context()),
		Items:        items,
	})
}

func (h *handler) postUserCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	username := trim(r.FormValue("username"))
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	if password != confirm {
		h.renderUserCreate(w, r, username, "两次输入的密码不一致", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		msg := "密码需 8–128 个字符"
		h.renderUserCreate(w, r, username, msg, http.StatusBadRequest)
		return
	}
	createdBy := sess.AdminID
	_, err = h.store.CreateAdmin(r.Context(), username, hash, &createdBy)
	if err != nil {
		if isValidation(err) {
			h.renderUserCreate(w, r, username, validationMessage(err), http.StatusBadRequest)
			return
		}
		if errors.Is(err, store.ErrConflict) {
			h.renderUserCreate(w, r, username, "用户名已存在", http.StatusConflict)
			return
		}
		h.logger.Error("admin create user failed", "admin_id", sess.AdminID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "创建失败")
		return
	}
	redirect(w, r, "/admin/users")
}

func (h *handler) postUserDisable(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	id, ok := parseUint64Param(r.PathValue("id"))
	if !ok {
		h.renderer.NotFound(w, r)
		return
	}
	if id == sess.AdminID {
		h.renderUsersListFlash(w, r, "不能停用自己", http.StatusBadRequest)
		return
	}
	err := h.store.SetAdminEnabled(r.Context(), id, false)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrLastAdmin):
			h.renderUsersListFlash(w, r, "不能停用最后一个管理员", http.StatusConflict)
		case errors.Is(err, store.ErrNotFound):
			h.renderer.NotFound(w, r)
		default:
			h.logger.Error("admin disable user failed", "admin_id", sess.AdminID)
			h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "停用失败")
		}
		return
	}
	redirect(w, r, "/admin/users")
}

func (h *handler) postUserEnable(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUint64Param(r.PathValue("id"))
	if !ok {
		h.renderer.NotFound(w, r)
		return
	}
	if err := h.store.SetAdminEnabled(r.Context(), id, true); errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	} else if err != nil {
		h.logger.Error("admin enable user failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "启用失败")
		return
	}
	redirect(w, r, "/admin/users")
}

func (h *handler) postUserResetPassword(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	id, ok := parseUint64Param(r.PathValue("id"))
	if !ok {
		h.renderer.NotFound(w, r)
		return
	}
	password := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	if password != confirm {
		h.renderUsersListFlash(w, r, "两次输入的密码不一致", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.renderUsersListFlash(w, r, "密码需 8–128 个字符", http.StatusBadRequest)
		return
	}
	if err := h.store.SetAdminPasswordHash(r.Context(), id, hash); errors.Is(err, store.ErrNotFound) {
		h.renderer.NotFound(w, r)
		return
	} else if err != nil {
		h.logger.Error("admin reset password failed", "admin_id", sess.AdminID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "重置密码失败")
		return
	}
	if err := h.store.DeleteSessionsForAdmin(r.Context(), id, nil); err != nil {
		h.logger.Error("admin delete sessions failed", "admin_id", sess.AdminID)
	}
	redirect(w, r, "/admin/users")
}

func (h *handler) getPassword(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, []string{pageFile("password")}, "password", http.StatusOK, passwordPageData{
		basePageData: h.baseData(r.Context()),
	})
}

func (h *handler) postPassword(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.getOrRedirect(w, r)
	if !ok {
		return
	}
	current := r.FormValue("current_password")
	newPass := r.FormValue("password")
	confirm := r.FormValue("password_confirm")
	if newPass != confirm {
		h.renderPassword(w, r, "两次输入的新密码不一致", http.StatusBadRequest)
		return
	}
	admin, err := h.store.GetAdminByID(r.Context(), sess.AdminID)
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载账户失败")
		return
	}
	okPass, verr := auth.VerifyPassword(admin.PasswordHash, current)
	if verr != nil || !okPass {
		h.renderPassword(w, r, "当前密码错误", http.StatusUnauthorized)
		return
	}
	hash, err := auth.HashPassword(newPass)
	if err != nil {
		h.renderPassword(w, r, "密码需 8–128 个字符", http.StatusBadRequest)
		return
	}
	if err := h.store.SetAdminPasswordHash(r.Context(), sess.AdminID, hash); err != nil {
		h.logger.Error("admin change password failed", "admin_id", sess.AdminID)
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "修改密码失败")
		return
	}
	except := sess.TokenHash
	if err := h.store.DeleteSessionsForAdmin(r.Context(), sess.AdminID, &except); err != nil {
		h.logger.Error("admin delete other sessions failed", "admin_id", sess.AdminID)
	}
	redirect(w, r, "/admin/password")
}

func (h *handler) renderUserCreate(w http.ResponseWriter, r *http.Request, username, flash string, status int) {
	items, err := h.store.ListAdmins(r.Context())
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载列表失败")
		return
	}
	data := usersListPageData{basePageData: h.baseData(r.Context()), Items: items, Username: username}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("users_list")}, "users_list", status, data)
}

func (h *handler) renderUsersListFlash(w http.ResponseWriter, r *http.Request, flash string, status int) {
	items, err := h.store.ListAdmins(r.Context())
	if err != nil {
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载列表失败")
		return
	}
	data := usersListPageData{basePageData: h.baseData(r.Context()), Items: items}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("users_list")}, "users_list", status, data)
}

func (h *handler) renderPassword(w http.ResponseWriter, r *http.Request, flash string, status int) {
	data := passwordPageData{basePageData: h.baseData(r.Context())}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("password")}, "password", status, data)
}
