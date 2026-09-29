package store

import (
	"context"
	"database/sql"
	"errors"

	"sentence-api/internal/database"
)

func (s *Store) DatasetVersion(ctx context.Context) (uint64, error) {
	return database.DatasetVersion(ctx, s.DB)
}

func (s *Store) withVersionTx(ctx context.Context, fn func(tx *sql.Tx) (changed bool, err error)) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin transaction")
	}
	defer tx.Rollback()

	var version uint64
	if err = tx.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id = 1").Scan(&version); err != nil || version == 0 {
		return errors.New("dataset version is missing or invalid")
	}
	changed, err := fn(tx)
	if err != nil {
		return err
	}
	if changed {
		if version >= uint64(1<<63-1) {
			return errors.New("dataset version exhausted")
		}
		stamp := "CURRENT_TIMESTAMP"

		res, err := tx.ExecContext(ctx, "UPDATE dataset_versions SET version = version + 1, published_at = "+stamp+" WHERE id = 1")
		if err != nil {
			return errors.New("dataset version update failed")
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("dataset version update affected no row")
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.New("commit transaction")
	}
	return nil
}
