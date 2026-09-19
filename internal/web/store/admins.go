package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type AdminUser struct {
	ID           uint64
	Username     string
	PasswordHash string
	Enabled      bool
	CreatedBy    *uint64
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string, createdBy *uint64) (uint64, error) {
	username = strings.ToLower(username)
	if err := validateAdminUsername(username); err != nil {
		return 0, err
	}
	res, err := s.DB.ExecContext(ctx,
		"INSERT INTO admin_users (username, password_hash, enabled, created_by) VALUES (?, ?, TRUE, ?)",
		username, passwordHash, createdBy)
	if err != nil {
		if isDuplicateKey(err) {
			return 0, ErrConflict
		}
		return 0, errors.New("insert admin")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, errors.New("read admin id")
	}
	return uint64(id), nil
}

func (s *Store) GetAdminByUsername(ctx context.Context, username string) (AdminUser, error) {
	return s.getAdmin(ctx, "username = ?", strings.ToLower(username))
}

func (s *Store) GetAdminByID(ctx context.Context, id uint64) (AdminUser, error) {
	return s.getAdmin(ctx, "id = ?", id)
}

func (s *Store) getAdmin(ctx context.Context, where string, arg any) (AdminUser, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT id, username, password_hash, enabled, created_by, created_at, last_login_at
		FROM admin_users WHERE `+where, arg)
	var u AdminUser
	var createdBy sql.NullInt64
	var lastLogin sql.NullTime
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Enabled, &createdBy, &u.CreatedAt, &lastLogin); errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	} else if err != nil {
		return AdminUser{}, errors.New("read admin")
	}
	if createdBy.Valid {
		v := uint64(createdBy.Int64)
		u.CreatedBy = &v
	}
	if lastLogin.Valid {
		t := lastLogin.Time.UTC()
		u.LastLoginAt = &t
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, nil
}

func (s *Store) ListAdmins(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, username, password_hash, enabled, created_by, created_at, last_login_at
		FROM admin_users ORDER BY username ASC`)
	if err != nil {
		return nil, errors.New("list admins")
	}
	defer rows.Close()
	var items []AdminUser
	for rows.Next() {
		var u AdminUser
		var createdBy sql.NullInt64
		var lastLogin sql.NullTime
		if err = rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Enabled, &createdBy, &u.CreatedAt, &lastLogin); err != nil {
			return nil, errors.New("scan admin")
		}
		if createdBy.Valid {
			v := uint64(createdBy.Int64)
			u.CreatedBy = &v
		}
		if lastLogin.Valid {
			t := lastLogin.Time.UTC()
			u.LastLoginAt = &t
		}
		u.CreatedAt = u.CreatedAt.UTC()
		items = append(items, u)
	}
	if err = rows.Err(); err != nil {
		return nil, errors.New("iterate admins")
	}
	return items, nil
}

func (s *Store) SetAdminEnabled(ctx context.Context, id uint64, enabled bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin transaction")
	}
	defer tx.Rollback()
	if !enabled {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM admin_users WHERE enabled = TRUE ORDER BY id FOR UPDATE")
		if err != nil {
			return errors.New("lock enabled admins")
		}
		enabledCount := 0
		found := false
		for rows.Next() {
			var eid uint64
			if err = rows.Scan(&eid); err != nil {
				rows.Close()
				return errors.New("lock enabled admins")
			}
			enabledCount++
			if eid == id {
				found = true
			}
		}
		if err = rows.Close(); err != nil {
			return errors.New("lock enabled admins")
		}
		if err = rows.Err(); err != nil {
			return errors.New("lock enabled admins")
		}
		if !found {
			var exists int
			if err = tx.QueryRowContext(ctx, "SELECT 1 FROM admin_users WHERE id = ?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return errors.New("read admin")
			}
			if err = tx.Commit(); err != nil {
				return errors.New("commit transaction")
			}
			return nil
		}
		if enabledCount <= 1 {
			return ErrLastAdmin
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE admin_id = ?", id); err != nil {
			return errors.New("delete admin sessions")
		}
	} else {
		var curEnabled bool
		if err = tx.QueryRowContext(ctx, "SELECT enabled FROM admin_users WHERE id = ? FOR UPDATE", id).Scan(&curEnabled); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return errors.New("read admin")
		}
		if curEnabled {
			if err = tx.Commit(); err != nil {
				return errors.New("commit transaction")
			}
			return nil
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE admin_users SET enabled = ? WHERE id = ?", enabled, id); err != nil {
		return errors.New("update admin")
	}
	return tx.Commit()
}

func (s *Store) ResetAdminPassword(ctx context.Context, id uint64, hash string, keepSession *[32]byte) error {
	return s.resetAdminPassword(ctx, id, hash, keepSession, "")
}

func (s *Store) ChangeAdminPassword(ctx context.Context, id uint64, expectHash, hash string, keepSession *[32]byte) error {
	if expectHash == "" {
		return errors.New("expected password hash required")
	}
	return s.resetAdminPassword(ctx, id, hash, keepSession, expectHash)
}

func (s *Store) resetAdminPassword(ctx context.Context, id uint64, hash string, keepSession *[32]byte, expectHash string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin transaction")
	}
	defer tx.Rollback()
	var currentHash string
	if err = tx.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE id = ? FOR UPDATE", id).Scan(&currentHash); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return errors.New("lock admin")
	}
	if expectHash != "" && !sameSecret(currentHash, expectHash) {
		return ErrStaleAuth
	}
	if _, err = tx.ExecContext(ctx, "UPDATE admin_users SET password_hash = ? WHERE id = ?", hash, id); err != nil {
		return errors.New("update admin password")
	}
	if keepSession == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE admin_id = ?", id)
	} else {
		_, err = tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE admin_id = ? AND token_hash <> ?", id, keepSession[:])
	}
	if err != nil {
		return errors.New("delete admin sessions")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("commit transaction")
	}
	return nil
}

func (s *Store) SetAdminPasswordHash(ctx context.Context, id uint64, hash string) error {
	return s.ResetAdminPassword(ctx, id, hash, nil)
}

func (s *Store) TouchAdminLogin(ctx context.Context, id uint64) error {
	now := time.Now().UTC()
	res, err := s.DB.ExecContext(ctx, "UPDATE admin_users SET last_login_at = ? WHERE id = ?", now, id)
	if err != nil {
		return errors.New("update admin login")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errors.New("update admin login")
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") || strings.Contains(msg, "duplicate key")
}
