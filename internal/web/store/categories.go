package store

import (
	"context"
	"database/sql"
	"errors"
)

type Category struct {
	ID             uint64
	Code           string
	Name           string
	SortOrder      int32
	Enabled        bool
	PublishedCount uint64
	DisabledCount  uint64
}

func (s *Store) ListCategoriesAdmin(ctx context.Context) ([]Category, error) {
	categoryOrder := "c.code COLLATE BINARY ASC"

	rows, err := s.DB.QueryContext(ctx, `SELECT c.id, c.code, c.name, c.sort_order, c.enabled,
		COALESCE(SUM(CASE WHEN s.status = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN s.status = 3 THEN 1 ELSE 0 END), 0)
		FROM categories c
		LEFT JOIN sentences s ON s.category_id = c.id AND s.status IN (1, 3)
		GROUP BY c.id, c.code, c.name, c.sort_order, c.enabled
		ORDER BY c.sort_order ASC, `+categoryOrder)
	if err != nil {
		return nil, errors.New("list categories")
	}
	defer rows.Close()
	var items []Category
	for rows.Next() {
		var c Category
		if err = rows.Scan(&c.ID, &c.Code, &c.Name, &c.SortOrder, &c.Enabled, &c.PublishedCount, &c.DisabledCount); err != nil {
			return nil, errors.New("scan category")
		}
		items = append(items, c)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.New("iterate categories")
	}
	return items, nil
}

func (s *Store) GetCategory(ctx context.Context, code string) (Category, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT c.id, c.code, c.name, c.sort_order, c.enabled,
		COALESCE(SUM(CASE WHEN s.status = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN s.status = 3 THEN 1 ELSE 0 END), 0)
		FROM categories c
		LEFT JOIN sentences s ON s.category_id = c.id AND s.status IN (1, 3)
		WHERE c.code = ?
		GROUP BY c.id, c.code, c.name, c.sort_order, c.enabled`, code)
	var c Category
	if err := row.Scan(&c.ID, &c.Code, &c.Name, &c.SortOrder, &c.Enabled, &c.PublishedCount, &c.DisabledCount); errors.Is(err, sql.ErrNoRows) {
		return Category{}, ErrNotFound
	} else if err != nil {
		return Category{}, errors.New("read category")
	}
	return c, nil
}

func (s *Store) CreateCategory(ctx context.Context, code, name string, sortOrder int32) error {
	if err := ValidateCategoryCode(code); err != nil {
		return err
	}
	if err := ValidateCategoryName(name); err != nil {
		return err
	}
	return s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		var one int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM categories WHERE code = ? LIMIT 1", code).Scan(&one)
		if err == nil {
			return false, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, errors.New("check category")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO categories (code, name, enabled, sort_order) VALUES (?, ?, TRUE, ?)", code, name, sortOrder); err != nil {
			return false, errors.New("insert category")
		}
		return true, nil
	})
}

func (s *Store) UpdateCategory(ctx context.Context, code, name string, sortOrder int32) error {
	if err := ValidateCategoryName(name); err != nil {
		return err
	}
	return s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		var curName string
		var curOrder int32
		err := tx.QueryRowContext(ctx, "SELECT name, sort_order FROM categories WHERE code = ?", code).
			Scan(&curName, &curOrder)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, errors.New("read category")
		}
		if curName == name && curOrder == sortOrder {
			return false, ErrUnchanged
		}
		if _, err = tx.ExecContext(ctx, "UPDATE categories SET name = ?, sort_order = ? WHERE code = ?", name, sortOrder, code); err != nil {
			return false, errors.New("update category")
		}
		return true, nil
	})
}

func (s *Store) SetCategoryEnabled(ctx context.Context, code string, enabled bool, confirmPublishedCount int64) error {
	return s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		var curEnabled bool
		var id uint64
		err := tx.QueryRowContext(ctx, "SELECT id, enabled FROM categories WHERE code = ?", code).Scan(&id, &curEnabled)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, errors.New("read category")
		}
		if curEnabled == enabled {
			return false, ErrUnchanged
		}
		if !enabled {
			var published int64
			if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences WHERE category_id = ? AND status = 1", id).Scan(&published); err != nil {
				return false, errors.New("count published sentences")
			}
			if published != confirmPublishedCount {
				return false, ErrConflict
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE categories SET enabled = ? WHERE id = ?", enabled, id); err != nil {
			return false, errors.New("update category")
		}
		return true, nil
	})
}
