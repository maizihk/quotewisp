package admin

import (
	"context"
	"net/http"

	"sentence-api/internal/web/store"
)

type overviewPageData struct {
	basePageData
	Overview store.Overview
	API      APIUsage
}

func (h *handler) getOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := h.store.AdminOverview(r.Context())
	if err != nil {
		h.logger.Error("admin overview failed", "admin_id", sessionAdminID(r))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "加载概况失败")
		return
	}
	usage := h.apiUsageFor(r.Context())
	h.renderPage(w, r, []string{pageFile("overview")}, "overview", http.StatusOK, overviewPageData{
		basePageData: h.baseData(r.Context()),
		Overview:     overview,
		API:          usage,
	})
}

func (h *handler) apiUsageFor(ctx context.Context) APIUsage {
	if h.apiUsage != nil {
		return h.apiUsage(ctx)
	}
	return fetchAPIUsage(ctx, h.apiMetricsURL)
}
