package admin

import (
	"net/http"

	"sentence-api/internal/web/store"
)

type overviewPageData struct {
	basePageData
	Overview store.Overview
	API      apiUsage
}

func (h *handler) getOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := h.store.AdminOverview(r.Context())
	if err != nil {
		h.logger.Error("admin overview failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载概况失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("overview")}, "overview", http.StatusOK, overviewPageData{
		basePageData: h.baseData(r.Context()),
		Overview:     overview,
		API:          fetchAPIUsage(r.Context(), h.apiMetricsURL),
	})
}
