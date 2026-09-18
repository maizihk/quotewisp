package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/store"
)

func encodeCSRF(tok [32]byte) string {
	return auth.EncodeCSRF(tok)
}

func parsePage(q string) int {
	p, err := strconv.Atoi(q)
	if err != nil || p < 1 {
		return 1
	}
	return p
}

func totalPages(total, size int) int {
	if total == 0 {
		return 1
	}
	return (total + size - 1) / size
}

func prevPage(page int) int {
	if page > 1 {
		return page - 1
	}
	return 0
}

func nextPage(page, pages int) int {
	if page < pages {
		return page + 1
	}
	return 0
}

func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		return false
	}
	return true
}

func parseUint64Param(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func parseSentenceStatus(s string) (uint8, error) {
	switch s {
	case "", "0":
		return 0, nil
	case "1":
		return 1, nil
	case "3":
		return 3, nil
	default:
		return 0, errors.New("invalid status")
	}
}

func validationMessage(err error) string {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		if ve.Reason != "" {
			return ve.Reason
		}
		return "参数无效"
	}
	return "操作失败"
}

func fieldsChanged(sub store.Submission, content, category, source, author string) *store.SentenceFields {
	if sub.Content == content && sub.CategoryCode == category && sub.Source == source && sub.Author == author {
		return nil
	}
	return &store.SentenceFields{
		Content:      content,
		CategoryCode: category,
		Source:       source,
		Author:       author,
	}
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func trim(s string) string {
	return strings.TrimSpace(s)
}
