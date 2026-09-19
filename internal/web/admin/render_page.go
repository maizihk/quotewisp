package admin

import (
	"fmt"
	"net/http"
)

func (h *handler) renderPage(w http.ResponseWriter, r *http.Request, files []string, entry string, status int, data any) {
	patterns := append([]string{"templates/admin_layout.html", "templates/admin_login_layout.html", "templates/admin_bar.html"}, files...)
	pages, err := h.renderer.Pages(templateFS, patterns...)
	if err != nil {
		h.logger.Error("admin_template_parse_failed", "error_category", "template", "detail", err.Error())
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "页面加载失败")
		return
	}
	h.renderer.HTML(w, r, pages, entry, status, data)
}

func pageFile(name string) string {
	return fmt.Sprintf("templates/%s.html", name)
}
