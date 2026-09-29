package admin

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"

	"sentence-api/internal/importer"
	"sentence-api/internal/importjobs"
	"sentence-api/internal/web/auth"
)

type ImportService interface {
	Upload(context.Context, uint64, string, io.Reader) (importjobs.Job, error)
	Get(context.Context, uint64, string) (importjobs.Job, error)
	List(context.Context, uint64) ([]importjobs.Job, error)
	Confirm(context.Context, uint64, string, string) error
	Cancel(context.Context, uint64, string) error
	RetryRefresh(context.Context, uint64, string) error
}

type importsPageData struct {
	basePageData
	Jobs     []importView
	MaxBytes int64
	Error    string
}
type importPageData struct {
	basePageData
	Job                                  importView
	Error                                string
	InFileDuplicates, DatabaseDuplicates uint64
}

func importData(ctx context.Context, d basePageData, job importjobs.Job) importPageData {
	p := importPageData{basePageData: d, Job: importView{job}}
	if job.HasResult {
		p.InFileDuplicates = job.Result.InputCount - minUint(job.Result.InputCount, job.Result.DeduplicatedCount)
		p.DatabaseDuplicates = job.Result.DeduplicatedCount - minUint(job.Result.DeduplicatedCount, job.Result.NewSentences)
	}
	return p
}
func minUint(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func (h *handler) getImports(w http.ResponseWriter, r *http.Request) {
	if h.imports == nil {
		h.renderer.Error(w, r, http.StatusNotImplemented, "unavailable", "暂不可用", "导入功能尚未启用")
		return
	}
	jobs, err := h.imports.List(r.Context(), sessionAdminID(r))
	if err != nil {
		h.importError(w, r, err, "加载导入记录失败")
		return
	}
	h.renderPage(w, r, []string{pageFile("imports_list")}, "imports_list", http.StatusOK, importsPageData{basePageData: h.baseData(r.Context()), Jobs: importViews(jobs), MaxBytes: h.importMaxBytes})
}

func (h *handler) getImport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.imports == nil {
		h.renderer.Error(w, r, http.StatusNotImplemented, "unavailable", "暂不可用", "导入功能尚未启用")
		return
	}
	job, err := h.imports.Get(r.Context(), sessionAdminID(r), id)
	if err != nil {
		h.importError(w, r, err, "加载导入详情失败")
		return
	}
	if importActive(job.Status) {
		w.Header().Set("Refresh", "2")
	}
	h.renderPage(w, r, []string{pageFile("import_detail")}, "import_detail", http.StatusOK, importData(r.Context(), h.baseData(r.Context()), job))
}

func (h *handler) postImport(w http.ResponseWriter, r *http.Request) {
	if h.imports == nil {
		h.renderer.Error(w, r, http.StatusNotImplemented, "unavailable", "暂不可用", "导入功能尚未启用")
		return
	}
	sess, _ := sessionFrom(r.Context())
	format, file, cleanup, err := h.readUpload(w, r, sess)
	if err != nil {
		h.uploadError(w, r, err)
		return
	}
	defer cleanup()
	job, err := h.imports.Upload(r.Context(), sess.AdminID, format, file)
	if err != nil {
		h.importError(w, r, err, "导入失败")
		return
	}
	redirect(w, r, "/admin/imports/"+url.PathEscape(job.ID))
}

func (h *handler) postImportConfirm(w http.ResponseWriter, r *http.Request) {
	h.importAction(w, r, "confirm", func(ctx context.Context, s uint64, id, d string) error { return h.imports.Confirm(ctx, s, id, d) })
}
func (h *handler) postImportCancel(w http.ResponseWriter, r *http.Request) {
	h.importAction(w, r, "cancel", func(ctx context.Context, s uint64, id, d string) error { return h.imports.Cancel(ctx, s, id) })
}
func (h *handler) postImportRefresh(w http.ResponseWriter, r *http.Request) {
	h.importAction(w, r, "refresh", func(ctx context.Context, s uint64, id, d string) error { return h.imports.RetryRefresh(ctx, s, id) })
}
func (h *handler) importAction(w http.ResponseWriter, r *http.Request, action string, fn func(context.Context, uint64, string, string) error) {
	if h.imports == nil {
		h.renderer.Error(w, r, http.StatusNotImplemented, "unavailable", "暂不可用", "导入功能尚未启用")
		return
	}
	sess, _ := sessionFrom(r.Context())
	id := r.PathValue("id")
	digest := r.FormValue("digest")
	if err := fn(r.Context(), sess.AdminID, id, digest); err != nil {
		h.importError(w, r, err, "操作导入失败")
		return
	}
	redirect(w, r, "/admin/imports/"+url.PathEscape(id))
}

func (h *handler) readUpload(w http.ResponseWriter, r *http.Request, sess Session) (string, io.Reader, func(), error) {
	max := h.importMaxBytes
	if max <= 0 {
		max = 64 << 20
	}
	if r.Body == nil {
		return "", nil, func() {}, errors.New("bad request")
	}
	r.Body = http.MaxBytesReader(w, r.Body, max+64<<10)
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" || params["boundary"] == "" {
		return "", nil, func() {}, errors.New("bad request")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, func() {}, err
	}
	vals := url.Values{}
	for {
		p, e := mr.NextPart()
		if e != nil {
			return "", nil, func() {}, errors.New("missing or invalid upload file")
		}
		name := p.FormName()
		if p.FileName() != "" {
			if name != "file" || vals.Get("csrf_token") == "" || vals.Get("format") == "" {
				return "", nil, func() {}, errors.New("invalid multipart order")
			}
			r.PostForm = vals
			if err := auth.CheckCSRF(r, sess.CSRFToken); err != nil {
				return "", nil, func() {}, err
			}
			format := vals.Get("format")
			if format != importer.FormatNative && format != importer.FormatHitokoto {
				return "", nil, func() {}, errors.New("invalid format")
			}
			return format, &maxReader{r: &uploadPartReader{part: p, next: mr}, max: max + 1}, func() {}, nil
		}
		if name != "csrf_token" && name != "format" {
			return "", nil, func() {}, errors.New("invalid multipart field")
		}
		if _, exists := vals[name]; exists {
			return "", nil, func() {}, errors.New("duplicate multipart field")
		}
		b, e := io.ReadAll(io.LimitReader(p, 4097))
		if e != nil {
			return "", nil, func() {}, e
		}
		if len(b) > 4096 {
			return "", nil, func() {}, errors.New("field too large")
		}
		vals.Set(name, string(b))
	}
}

type maxReader struct {
	r      io.Reader
	max, n int64
}

// uploadPartReader keeps the multipart stream attached to the upload. Once
// the file reaches EOF, it rejects trailing fields/files before Upload can
// finish successfully.
type uploadPartReader struct {
	part *multipart.Part
	next *multipart.Reader
	done bool
}

func (r *uploadPartReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.part.Read(p)
	if err != io.EOF {
		return n, err
	}
	r.done = true
	nextPart, nextErr := r.next.NextPart()
	if nextErr == io.EOF {
		return n, io.EOF
	}
	if nextErr != nil {
		return n, nextErr
	}
	if nextPart.FileName() != "" {
		return n, errors.New("multiple upload files")
	}
	return n, errors.New("multipart fields must precede file")
}

func (m *maxReader) Read(p []byte) (int, error) {
	if m.n >= m.max {
		return 0, importjobs.ErrTooLarge
	}
	if int64(len(p)) > m.max-m.n {
		p = p[:m.max-m.n]
	}
	n, e := m.r.Read(p)
	m.n += int64(n)
	return n, e
}

func importActive(s string) bool {
	switch s {
	case "queued_preview", "previewing", "queued_import", "importing", "committed", "queued_refresh", "refreshing":
		return true
	}
	return false
}
func (h *handler) importError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		h.renderer.Error(w, r, http.StatusRequestEntityTooLarge, "too-large", "文件过大", "上传文件超过大小限制")
		return
	}
	switch {
	case errors.Is(err, importjobs.ErrUpload):
		h.uploadError(w, r, err)
	case errors.Is(err, importjobs.ErrNotFound), errors.Is(err, importjobs.ErrExpired):
		h.renderer.Error(w, r, http.StatusNotFound, "not-found", "未找到", "导入记录不存在或已过期")
	case errors.Is(err, importjobs.ErrBusy), errors.Is(err, importjobs.ErrState), errors.Is(err, importjobs.ErrDigest):
		h.renderer.Error(w, r, http.StatusConflict, "conflict", "无法操作", "导入状态已变化，请刷新后重试")
	case errors.Is(err, importjobs.ErrTooLarge):
		h.renderer.Error(w, r, http.StatusRequestEntityTooLarge, "too-large", "文件过大", "上传文件超过大小限制")
	default:
		h.logger.Error("admin import failed", "admin_id", sessionAdminID(r), "error_category", "storage")
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", fallback)
	}
}
func (h *handler) uploadError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		h.renderer.Error(w, r, http.StatusRequestEntityTooLarge, "too-large", "文件过大", "上传文件超过大小限制")
		return
	}
	if errors.Is(err, auth.ErrCSRF) {
		h.renderer.Error(w, r, http.StatusForbidden, "csrf-denied", "请求被拒绝", "CSRF 校验失败")
		return
	}
	if errors.Is(err, importjobs.ErrTooLarge) {
		h.renderer.Error(w, r, http.StatusRequestEntityTooLarge, "too-large", "文件过大", "上传文件超过大小限制")
		return
	}
	h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", "上传请求无效")
}

func formatLabel(s string) string {
	if s == "hitokoto" {
		return "Hitokoto JSON"
	}
	return "原生 JSON"
}
func statusLabel(s string) string {
	labels := map[string]string{"queued_preview": "等待检查", "previewing": "检查中", "preview": "待确认", "queued_import": "等待导入", "importing": "导入中", "committed": "已提交", "complete": "已完成", "refresh_failed": "刷新失败", "queued_refresh": "等待刷新", "refreshing": "刷新中", "failed": "失败", "expired": "已过期", "interrupted": "已中断", "canceled": "已取消"}
	if v := labels[s]; v != "" {
		return v
	}
	return s
}

// importView keeps workflow presentation separate from persistent task states.
type importView struct{ importjobs.Job }

func (v importView) StatusLabel() string { return statusLabel(v.Status) }
func (v importView) FormatLabel() string { return formatLabel(v.Format) }
func (v importView) Committed() bool {
	switch v.Status {
	case "committed", "complete", "refresh_failed", "queued_refresh", "refreshing":
		return true
	}
	return false
}
func importViews(jobs []importjobs.Job) []importView {
	views := make([]importView, len(jobs))
	for i, j := range jobs {
		views[i] = importView{j}
	}
	return views
}
