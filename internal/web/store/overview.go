package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Overview struct {
	PublishedSentences  int64
	DisabledSentences   int64
	EnabledCategories   int64
	DisabledCategories  int64
	PendingSubmissions  int64
	ApprovedSubmissions int64
	RejectedSubmissions int64
	RecentSubmissions   int64
	DatasetVersion      uint64
	DatasetPublishedAt  time.Time
}

func (s *Store) AdminOverview(ctx context.Context) (Overview, error) {
	var out Overview
	var published sql.NullTime
	recent := "(SELECT COUNT(*) FROM submissions WHERE julianday(created_at) >= julianday('now','-24 hours'))"

	err := s.DB.QueryRowContext(ctx, `
SELECT
	(SELECT COUNT(*) FROM sentences WHERE status = 1),
	(SELECT COUNT(*) FROM sentences WHERE status = 3),
	(SELECT COUNT(*) FROM categories WHERE enabled = TRUE),
	(SELECT COUNT(*) FROM categories WHERE enabled = FALSE),
	(SELECT COUNT(*) FROM submissions WHERE status = 0),
	(SELECT COUNT(*) FROM submissions WHERE status = 1),
	(SELECT COUNT(*) FROM submissions WHERE status = 2),
	`+recent+`,
	(SELECT version FROM dataset_versions WHERE id = 1),
	(SELECT published_at FROM dataset_versions WHERE id = 1)`).Scan(
		&out.PublishedSentences,
		&out.DisabledSentences,
		&out.EnabledCategories,
		&out.DisabledCategories,
		&out.PendingSubmissions,
		&out.ApprovedSubmissions,
		&out.RejectedSubmissions,
		&out.RecentSubmissions,
		&out.DatasetVersion,
		&published,
	)
	if err != nil {
		return Overview{}, errors.New("load admin overview")
	}
	if published.Valid {
		out.DatasetPublishedAt = published.Time.UTC()
	}
	return out, nil
}
