package store

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"sentence-api/internal/database"
	"strings"
)

func lockSuffix(db *sql.DB) string { return database.ForUpdate(db) }

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func coalesceString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func scanClientIP(raw []byte) netip.Addr {
	if len(raw) == 0 {
		return netip.Addr{}
	}
	if len(raw) == 16 {
		return netip.AddrFrom16([16]byte(raw))
	}
	return netip.Addr{}
}

func clientIPArg(addr netip.Addr) any {
	if !addr.IsValid() {
		return nil
	}
	b := addr.As16()
	return b[:]
}

type categoryRow struct {
	id      uint64
	code    string
	name    string
	enabled bool
}

func lookupCategory(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, code string) (categoryRow, error) {
	var c categoryRow
	err := q.QueryRowContext(ctx, "SELECT id, code, name, enabled FROM categories WHERE code = ?", code).
		Scan(&c.id, &c.code, &c.name, &c.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, errors.New("read category")
	}
	return c, nil
}

func lookupCategoryByID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id uint64) (categoryRow, error) {
	var c categoryRow
	err := q.QueryRowContext(ctx, "SELECT id, code, name, enabled FROM categories WHERE id = ?", id).
		Scan(&c.id, &c.code, &c.name, &c.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, errors.New("read category")
	}
	return c, nil
}

func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func pageOffset(page, size int) int {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 1
	}
	return (page - 1) * size
}
