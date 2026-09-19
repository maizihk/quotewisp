package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

type Session struct {
	TokenHash  [32]byte
	AdminID    uint64
	Username   string
	CSRFToken  [32]byte
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO admin_sessions
		(token_hash, admin_id, csrf_token, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sess.TokenHash[:], sess.AdminID, sess.CSRFToken[:],
		sess.CreatedAt.UTC(), sess.LastSeenAt.UTC(), sess.ExpiresAt.UTC())
	if err != nil {
		return errors.New("insert session")
	}
	return nil
}

// CreateLoginSession inserts a session only if the account is still enabled and
// still uses passwordHash. The row lock serializes this with password reset.
func (s *Store) CreateLoginSession(ctx context.Context, sess Session, passwordHash string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin transaction")
	}
	defer tx.Rollback()

	var currentHash string
	var enabled bool
	err = tx.QueryRowContext(ctx, "SELECT password_hash, enabled FROM admin_users WHERE id = ? FOR UPDATE", sess.AdminID).
		Scan(&currentHash, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return errors.New("lock admin")
	}
	if !enabled || !sameSecret(currentHash, passwordHash) {
		return ErrStaleAuth
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO admin_sessions
		(token_hash, admin_id, csrf_token, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sess.TokenHash[:], sess.AdminID, sess.CSRFToken[:],
		sess.CreatedAt.UTC(), sess.LastSeenAt.UTC(), sess.ExpiresAt.UTC())
	if err != nil {
		return errors.New("insert session")
	}
	if _, err = tx.ExecContext(ctx, "UPDATE admin_users SET last_login_at = ? WHERE id = ?", sess.CreatedAt.UTC(), sess.AdminID); err != nil {
		return errors.New("update admin login")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("commit transaction")
	}
	return nil
}

func sameSecret(a, b string) bool {
	ab, bb := []byte(a), []byte(b)
	if len(ab) != len(bb) {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}

func (s *Store) GetSession(ctx context.Context, tokenHash [32]byte, now time.Time) (Session, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT s.token_hash, s.admin_id, u.username, s.csrf_token,
		s.created_at, s.last_seen_at, s.expires_at, u.enabled
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_id
		WHERE s.token_hash = ?`, tokenHash[:])
	var sess Session
	var hash, csrf []byte
	var enabled bool
	if err := row.Scan(&hash, &sess.AdminID, &sess.Username, &csrf,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt, &enabled); errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	} else if err != nil {
		return Session{}, errors.New("read session")
	}
	if len(hash) == 32 {
		copy(sess.TokenHash[:], hash)
	}
	if len(csrf) == 32 {
		copy(sess.CSRFToken[:], csrf)
	}
	sess.CreatedAt = sess.CreatedAt.UTC()
	sess.LastSeenAt = sess.LastSeenAt.UTC()
	sess.ExpiresAt = sess.ExpiresAt.UTC()
	now = now.UTC()
	if !enabled || !sess.ExpiresAt.After(now) {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *Store) TouchSession(ctx context.Context, tokenHash [32]byte, now time.Time) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE admin_sessions SET last_seen_at = ? WHERE token_hash = ?", now.UTC(), tokenHash[:])
	if err != nil {
		return errors.New("update session")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errors.New("update session")
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash [32]byte) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM admin_sessions WHERE token_hash = ?", tokenHash[:])
	if err != nil {
		return errors.New("delete session")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return errors.New("delete session")
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteSessionsForAdmin(ctx context.Context, adminID uint64, except *[32]byte) error {
	var err error
	if except == nil {
		_, err = s.DB.ExecContext(ctx, "DELETE FROM admin_sessions WHERE admin_id = ?", adminID)
	} else {
		_, err = s.DB.ExecContext(ctx, "DELETE FROM admin_sessions WHERE admin_id = ? AND token_hash <> ?", adminID, except[:])
	}
	if err != nil {
		return errors.New("delete admin sessions")
	}
	return nil
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM admin_sessions WHERE expires_at < ?", now.UTC())
	if err != nil {
		return 0, errors.New("delete expired sessions")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, errors.New("delete expired sessions")
	}
	return n, nil
}
