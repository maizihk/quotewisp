package admin

import (
	"errors"
	"net/http"

	"sentence-api/internal/web/store"
)

type settingsPageData struct {
	basePageData
	Form store.SiteSettings
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	form, err := h.loadSettingsForm(r)
	if err != nil {
		h.logger.Error("admin load settings failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载站点设置失败")
		return
	}
	h.renderSettings(w, r, form, "", http.StatusOK)
}

func (h *handler) postSettings(w http.ResponseWriter, r *http.Request) {
	form := store.NormalizeSiteSettings(store.SiteSettings{
		Name:         r.FormValue("site_name"),
		EnglishName:  r.FormValue("english_name"),
		Slogan:       r.FormValue("slogan"),
		Contact:      r.FormValue("contact"),
		PublicOrigin: r.FormValue("public_origin"),
		RepoURL:      r.FormValue("repo_url"),
		BeianText:    r.FormValue("beian_text"),
		BeianURL:     r.FormValue("beian_url"),
	})
	h.settingsMu.Lock()
	defer h.settingsMu.Unlock()
	if r.Context().Err() != nil {
		return
	}
	if err := h.store.UpsertSettings(r.Context(), form); err != nil {
		var ve *store.ValidationError
		if errors.As(err, &ve) {
			h.renderSettings(w, r, form, validationMessage(err), http.StatusBadRequest)
			return
		}
		h.logger.Error("admin save settings failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "保存站点设置失败")
		return
	}
	cur := h.renderer.Site()
	h.renderer.ReplaceSite(cur.Overlay(form.Name, form.EnglishName, form.Slogan, form.Contact, form.PublicOrigin, form.RepoURL, form.BeianText, form.BeianURL))
	redirect(w, r, "/admin/settings")
}

func (h *handler) loadSettingsForm(r *http.Request) (store.SiteSettings, error) {
	form, err := h.store.GetSettings(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		s := h.renderer.Site()
		return store.SiteSettings{
			Name:         s.Name,
			EnglishName:  s.EnglishName,
			Slogan:       s.Slogan,
			Contact:      s.Contact,
			PublicOrigin: s.PublicOrigin,
			RepoURL:      s.RepoURL,
			BeianText:    s.BeianText,
			BeianURL:     s.BeianURL,
		}, nil
	}
	return form, err
}

func (h *handler) renderSettings(w http.ResponseWriter, r *http.Request, form store.SiteSettings, flash string, status int) {
	data := settingsPageData{basePageData: h.baseData(r.Context()), Form: form}
	data.Flash = flash
	h.renderPage(w, r, []string{pageFile("settings")}, "settings", status, data)
}
