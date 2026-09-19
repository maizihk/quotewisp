package public

import (
	"errors"
	"net/http"
	"net/netip"
	"time"
	"unicode/utf8"

	"sentence-api/internal/httpmw"
	"sentence-api/internal/web/store"
)

type submitFormData struct {
	Categories  []categoryView
	Recent      []recentView
	FormToken   string
	Values      submitValues
	FieldErrors map[string]string
	FormError   string
}

type submitValues struct {
	Content, Category, Source, Author, Nickname, Contact string
	Agree                                                bool
}

func (h *handler) getSubmit(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	h.renderSubmitForm(w, r, http.StatusOK, submitValues{}, nil, "", h.tokens.Issue(now))
}

func (h *handler) postSubmit(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.renderer.Error(w, r, http.StatusBadRequest, "invalid-parameter", "参数无效", "请求无效")
		return
	}

	values := submitValues{
		Content:  r.FormValue("content"),
		Category: r.FormValue("category"),
		Source:   r.FormValue("source"),
		Author:   r.FormValue("author"),
		Nickname: r.FormValue("nickname"),
		Contact:  r.FormValue("contact"),
		Agree:    r.FormValue("agree") == "on",
	}
	token := r.FormValue("form_token")

	if !h.tokens.Verify(token, now, 5*time.Second, 2*time.Hour) {
		h.logSubmission("invalid", r)
		h.metrics.Submission("invalid")
		h.renderSubmitForm(w, r, http.StatusBadRequest, values, nil, "表单已过期，请刷新后重试", h.tokens.Issue(now))
		return
	}

	if r.FormValue("website") != "" {
		h.logSubmission("honeypot", r)
		h.metrics.Submission("honeypot")
		redirect(w, r, "/submit/done")
		return
	}

	ipStr := httpmw.ClientIPFrom(r.Context())
	ip, _ := netip.ParseAddr(ipStr)
	if !h.limiter.Allow(ipStr, now) {
		h.logSubmission("rate_limited", r)
		h.metrics.Submission("rate_limited")
		h.renderer.Error(w, r, http.StatusTooManyRequests, "rate-limited", "提交过于频繁", "提交过于频繁，请稍后再试")
		return
	}

	fieldErrors := validateSubmission(values, h.cache.Current())
	if len(fieldErrors) > 0 {
		h.logSubmission("invalid", r)
		h.metrics.Submission("invalid")
		h.renderSubmitForm(w, r, http.StatusBadRequest, values, fieldErrors, "", h.tokens.Issue(now))
		return
	}

	id, err := h.store.CreateSubmission(r.Context(), store.NewSubmission{
		Content:      values.Content,
		CategoryCode: values.Category,
		Source:       values.Source,
		Author:       values.Author,
		Nickname:     values.Nickname,
		Contact:      values.Contact,
		ClientIP:     ip,
	}, h.pendingLimit)
	if errors.Is(err, store.ErrQueueFull) {
		h.logSubmission("queue_full", r)
		h.metrics.Submission("queue_full")
		h.renderer.Error(w, r, http.StatusServiceUnavailable, "queue-full", "审核队列已满", "审核队列已满，请稍后再试")
		return
	}
	if errors.Is(err, store.ErrDuplicate) {
		h.logSubmission("duplicate", r)
		h.metrics.Submission("duplicate")
		h.renderer.Error(w, r, http.StatusConflict, "duplicate", "重复投稿", "相同内容已在审核中")
		return
	}
	if errors.Is(err, store.ErrCategoryDisabled) || errors.Is(err, store.ErrNotFound) {
		fieldErrors = map[string]string{"category": "所选分类不可用"}
		h.logSubmission("invalid", r)
		h.metrics.Submission("invalid")
		h.renderSubmitForm(w, r, http.StatusBadRequest, values, fieldErrors, "", h.tokens.Issue(now))
		return
	}
	if err != nil {
		h.logger.Error("submission", "result", "error", "request_id", httpmw.RequestIDFrom(r.Context()))
		h.renderer.Error(w, r, http.StatusInternalServerError, "internal-error", "内部错误", "提交失败，请稍后再试")
		return
	}
	_ = id
	h.logSubmission("accepted", r)
	h.metrics.Submission("accepted")
	redirect(w, r, "/submit/done")
}

func (h *handler) renderSubmitForm(w http.ResponseWriter, r *http.Request, status int, values submitValues, fieldErrors map[string]string, formError, token string) {
	data := h.cache.Current()
	cats := make([]categoryView, len(data.Categories))
	for i, c := range data.Categories {
		cats[i] = categoryView{Code: c.Code, Name: c.Name, Count: c.Count}
	}
	h.renderer.HTML(w, r, h.submitPages, "submit", status, submitFormData{
		Categories:  cats,
		Recent:      recentViews(data),
		FormToken:   token,
		Values:      values,
		FieldErrors: fieldErrors,
		FormError:   formError,
	})
}

func validateSubmission(values submitValues, data *store.PublicData) map[string]string {
	errs := map[string]string{}

	if err := store.ValidateSentenceFields(values.Content, values.Source, values.Author, 1000); err != nil {
		if ve, ok := err.(*store.ValidationError); ok {
			errs[ve.Field] = fieldMessage(ve.Field)
		} else {
			errs["content"] = "内容无效"
		}
	}
	if values.Source == "" && values.Author == "" {
		errs["source"] = "出处与作者至少填写一项"
		errs["author"] = "出处与作者至少填写一项"
	}
	if values.Nickname != "" {
		if !utf8.ValidString(values.Nickname) || utf8.RuneCountInString(values.Nickname) > 32 {
			errs["nickname"] = "昵称长度应为 1–32 个字符"
		}
	}
	if !utf8.ValidString(values.Contact) {
		errs["contact"] = "联系方式无效"
	} else {
		n := utf8.RuneCountInString(values.Contact)
		if n < 3 || n > 255 {
			errs["contact"] = "联系方式长度应为 3–255 个字符"
		}
	}
	if !categoryEnabled(data, values.Category) {
		errs["category"] = "请选择有效分类"
	}
	if !values.Agree {
		errs["agree"] = "请确认并同意投稿条款"
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func categoryEnabled(data *store.PublicData, code string) bool {
	for _, c := range data.Categories {
		if c.Code == code {
			return true
		}
	}
	return false
}

func fieldMessage(field string) string {
	switch field {
	case "content":
		return "内容长度应为 1–1000 个字符且不能为空"
	case "source":
		return "出处过长或无效"
	case "author":
		return "作者过长或无效"
	default:
		return "字段无效"
	}
}

func (h *handler) logSubmission(result string, r *http.Request) {
	h.logger.Info("submission", "result", result, "request_id", httpmw.RequestIDFrom(r.Context()))
}
