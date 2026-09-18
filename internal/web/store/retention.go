package store

import (
	"context"
	"errors"
	"time"
)

func (s *Store) RunRetention(ctx context.Context, before time.Time, batch int) (deleted, redacted int64, err error) {
	if batch < 1 {
		batch = 1
	}
	before = before.UTC()
	for {
		res, e := s.DB.ExecContext(ctx,
			"DELETE FROM submissions WHERE status = 2 AND reviewed_at < ? LIMIT ?", before, batch)
		if e != nil {
			return deleted, redacted, errors.New("delete rejected submissions")
		}
		n, e := res.RowsAffected()
		if e != nil {
			return deleted, redacted, errors.New("delete rejected submissions")
		}
		deleted += n
		if n < int64(batch) {
			break
		}
	}
	for {
		res, e := s.DB.ExecContext(ctx, `UPDATE submissions SET contact = NULL, client_ip = NULL
			WHERE status = 1 AND reviewed_at < ? AND (contact IS NOT NULL OR client_ip IS NOT NULL)
			LIMIT ?`, before, batch)
		if e != nil {
			return deleted, redacted, errors.New("redact approved submissions")
		}
		n, e := res.RowsAffected()
		if e != nil {
			return deleted, redacted, errors.New("redact approved submissions")
		}
		redacted += n
		if n < int64(batch) {
			break
		}
	}
	return deleted, redacted, nil
}
