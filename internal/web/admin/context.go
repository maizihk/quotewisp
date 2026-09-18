package admin

import (
	"context"

	"sentence-api/internal/web/store"
)

type ctxKey int

const sessionKey ctxKey = 1

// Session holds authenticated admin session data attached to a request.
type Session struct {
	AdminID   uint64
	Username  string
	CSRFToken [32]byte
	TokenHash [32]byte
	RawToken  string
}

func withSession(ctx context.Context, sess Session) context.Context {
	return context.WithValue(ctx, sessionKey, sess)
}

func sessionFrom(ctx context.Context) (Session, bool) {
	s, ok := ctx.Value(sessionKey).(Session)
	return s, ok
}

type basePageData struct {
	CSRF     string
	Username string
	Flash    string
}

func (h *handler) baseData(ctx context.Context) basePageData {
	d := basePageData{}
	if sess, ok := sessionFrom(ctx); ok {
		d.CSRF = encodeCSRF(sess.CSRFToken)
		d.Username = sess.Username
	}
	return d
}

func enabledCategories(cats []store.Category) []store.Category {
	var out []store.Category
	for _, c := range cats {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out
}
