package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

const (
	SubmissionPending  uint8 = 0
	SubmissionApproved uint8 = 1
	SubmissionRejected uint8 = 2
)

type NewSubmission struct {
	Content, CategoryCode, Source, Author, Nickname, Contact string
	ClientIP                                                 netip.Addr
}

type Submission struct {
	ID                 uint64
	Content            string
	CategoryCode       string
	CategoryName       string
	Source             string
	Author             string
	Nickname           string
	Contact            string
	ClientIP           netip.Addr
	ContentSHA256      [32]byte
	Status             uint8
	RejectReason       string
	ReviewedBy         *uint64
	ReviewedByUsername string
	ReviewedAt         *time.Time
	SentenceID         *uint64
	SentenceUUID       string
	CreatedAt          time.Time
	DuplicatePending   bool
}

type SentenceFields struct {
	Content, CategoryCode, Source, Author string
}

func (s *Store) PendingCount(ctx context.Context) (int, error) {
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions WHERE status = 0").Scan(&n); err != nil {
		return 0, errors.New("count pending submissions")
	}
	return n, nil
}

func (s *Store) CreateSubmission(ctx context.Context, n NewSubmission, pendingLimit int) (uint64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.New("begin transaction")
	}
	defer tx.Rollback()

	var version uint64
	if err = tx.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id = 1 FOR UPDATE").Scan(&version); err != nil || version == 0 {
		return 0, errors.New("dataset version is missing or invalid")
	}

	cat, err := lookupCategory(ctx, tx, n.CategoryCode)
	if err != nil {
		return 0, err
	}
	if !cat.enabled {
		return 0, ErrCategoryDisabled
	}
	hash := sha256.Sum256([]byte(n.Content))
	var dup int
	if err = tx.QueryRowContext(ctx,
		"SELECT 1 FROM submissions WHERE status = 0 AND category_id = ? AND content_sha256 = ? LIMIT 1",
		cat.id, hash[:]).Scan(&dup); err == nil {
		return 0, ErrDuplicate
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("check pending duplicate")
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions WHERE status = 0").Scan(&count); err != nil {
		return 0, errors.New("count pending submissions")
	}
	if count >= pendingLimit {
		return 0, ErrQueueFull
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO submissions
		(content, category_id, source, author, nickname, contact, client_ip, content_sha256, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		n.Content, cat.id, nullString(n.Source), nullString(n.Author), nullString(n.Nickname),
		n.Contact, clientIPArg(n.ClientIP), hash[:])
	if err != nil {
		return 0, errors.New("insert submission")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, errors.New("read submission id")
	}
	if err = tx.Commit(); err != nil {
		return 0, errors.New("commit transaction")
	}
	return uint64(id), nil
}

func (s *Store) ListSubmissions(ctx context.Context, status uint8, page, size int) ([]Submission, int, error) {
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM submissions WHERE status = ?", status).Scan(&total); err != nil {
		return nil, 0, errors.New("count submissions")
	}
	offset := pageOffset(page, size)
	selectList := `SELECT s.id, s.content, c.code, c.name,
		COALESCE(s.source, ''), COALESCE(s.author, ''), COALESCE(s.nickname, ''), COALESCE(s.contact, ''),
		s.client_ip, s.content_sha256, s.status, COALESCE(s.reject_reason, ''), s.reviewed_by,
		COALESCE(u.username, ''), s.reviewed_at, s.sentence_id, COALESCE(t.uuid, ''), s.created_at`
	if status == SubmissionPending {
		selectList += `, EXISTS (
			SELECT 1 FROM submissions d
			WHERE d.status = 0 AND d.category_id = s.category_id
			  AND d.content_sha256 = s.content_sha256 AND d.id <> s.id
		)`
	}
	from := ` FROM submissions s
		JOIN categories c ON c.id = s.category_id
		LEFT JOIN admin_users u ON u.id = s.reviewed_by
		LEFT JOIN sentences t ON t.id = s.sentence_id
		WHERE s.status = ?`
	var rows *sql.Rows
	var err error
	if status == SubmissionPending {
		rows, err = s.DB.QueryContext(ctx, selectList+from+` ORDER BY s.created_at ASC LIMIT ? OFFSET ?`, status, size, offset)
	} else {
		rows, err = s.DB.QueryContext(ctx, selectList+from+` ORDER BY s.reviewed_at DESC LIMIT ? OFFSET ?`, status, size, offset)
	}
	if err != nil {
		return nil, 0, errors.New("list submissions")
	}
	defer rows.Close()
	var items []Submission
	for rows.Next() {
		var item Submission
		var err error
		if status == SubmissionPending {
			item, err = scanSubmission(rows, true)
		} else {
			item, err = scanSubmission(rows, false)
		}
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, errors.New("iterate submissions")
	}
	return items, total, nil
}

func (s *Store) GetSubmission(ctx context.Context, id uint64) (Submission, error) {
	row := s.DB.QueryRowContext(ctx, `SELECT s.id, s.content, c.code, c.name,
		COALESCE(s.source, ''), COALESCE(s.author, ''), COALESCE(s.nickname, ''), COALESCE(s.contact, ''),
		s.client_ip, s.content_sha256, s.status, COALESCE(s.reject_reason, ''), s.reviewed_by,
		COALESCE(u.username, ''), s.reviewed_at, s.sentence_id, COALESCE(t.uuid, ''), s.created_at
		FROM submissions s
		JOIN categories c ON c.id = s.category_id
		LEFT JOIN admin_users u ON u.id = s.reviewed_by
		LEFT JOIN sentences t ON t.id = s.sentence_id
		WHERE s.id = ?`, id)
	item, err := scanSubmission(row, false)
	if errors.Is(err, sql.ErrNoRows) {
		return Submission{}, ErrNotFound
	}
	if err != nil {
		return Submission{}, err
	}
	if item.Status == SubmissionPending {
		dup, err := s.hasPendingDuplicate(ctx, item.ID, item.CategoryCode, item.ContentSHA256)
		if err != nil {
			return Submission{}, err
		}
		item.DuplicatePending = dup
	}
	return item, nil
}

func (s *Store) ApproveSubmission(ctx context.Context, id, adminID uint64, edit *SentenceFields) (string, error) {
	var outUUID string
	err := s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		var sub struct {
			status                     uint8
			content, source, author    sql.NullString
			categoryID                 uint64
			categoryCode, categoryName string
		}
		err := tx.QueryRowContext(ctx, `SELECT s.status, s.content, s.source, s.author, s.category_id, c.code, c.name
			FROM submissions s JOIN categories c ON c.id = s.category_id WHERE s.id = ? FOR UPDATE`, id).
			Scan(&sub.status, &sub.content, &sub.source, &sub.author, &sub.categoryID, &sub.categoryCode, &sub.categoryName)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, errors.New("read submission")
		}
		if sub.status != SubmissionPending {
			return false, ErrConflict
		}
		fields := SentenceFields{
			Content:      sub.content.String,
			CategoryCode: sub.categoryCode,
			Source:       sub.source.String,
			Author:       sub.author.String,
		}
		if edit != nil {
			fields = *edit
		}
		if err = ValidateSentenceFields(fields.Content, fields.Source, fields.Author, 0); err != nil {
			return false, err
		}
		cat, err := lookupCategory(ctx, tx, fields.CategoryCode)
		if err != nil {
			return false, err
		}
		if !cat.enabled {
			return false, ErrCategoryDisabled
		}
		if err = exactDuplicateSentence(ctx, tx, cat.id, fields.Content, 0); err != nil {
			return false, err
		}
		u := uuid.New()
		outUUID = u.String()
		length := RuneLength(fields.Content)
		now := time.Now().UTC()
		res, err := tx.ExecContext(ctx, `INSERT INTO sentences
			(uuid, category_id, content, source, author, length, status, published_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
			outUUID, cat.id, fields.Content, nullString(fields.Source), nullString(fields.Author), length, now)
		if err != nil {
			return false, errors.New("insert sentence")
		}
		sentenceID, err := res.LastInsertId()
		if err != nil {
			return false, errors.New("read sentence id")
		}
		if edit != nil {
			_, err = tx.ExecContext(ctx, `UPDATE submissions SET status = 1, reviewed_by = ?, reviewed_at = ?,
				sentence_id = ?, content = ?, category_id = ?, source = ?, author = ? WHERE id = ?`,
				adminID, now, sentenceID, fields.Content, cat.id, nullString(fields.Source), nullString(fields.Author), id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE submissions SET status = 1, reviewed_by = ?, reviewed_at = ?, sentence_id = ? WHERE id = ?`,
				adminID, now, sentenceID, id)
		}
		if err != nil {
			return false, errors.New("update submission")
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return outUUID, nil
}

func (s *Store) RejectSubmission(ctx context.Context, id, adminID uint64, reason string) error {
	if err := validateRejectReason(reason); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("begin transaction")
	}
	defer tx.Rollback()
	var status uint8
	if err = tx.QueryRowContext(ctx, "SELECT status FROM submissions WHERE id = ? FOR UPDATE", id).Scan(&status); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return errors.New("read submission")
	}
	if status != SubmissionPending {
		return ErrConflict
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE submissions SET status = 2, reject_reason = ?, reviewed_by = ?, reviewed_at = ? WHERE id = ?`,
		reason, adminID, now, id); err != nil {
		return errors.New("update submission")
	}
	if err = tx.Commit(); err != nil {
		return errors.New("commit transaction")
	}
	return nil
}

func (s *Store) hasPendingDuplicate(ctx context.Context, id uint64, categoryCode string, hash [32]byte) (bool, error) {
	cat, err := lookupCategory(ctx, s.DB, categoryCode)
	if err != nil {
		return false, err
	}
	var other uint64
	err = s.DB.QueryRowContext(ctx,
		"SELECT id FROM submissions WHERE status = 0 AND category_id = ? AND content_sha256 = ? AND id <> ? LIMIT 1",
		cat.id, hash[:], id).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("check pending duplicate")
	}
	return true, nil
}

type submissionScanner interface {
	Scan(dest ...any) error
}

func scanSubmission(row submissionScanner, withDup bool) (Submission, error) {
	var item Submission
	var clientIP []byte
	var hash []byte
	var reviewedBy sql.NullInt64
	var reviewedAt sql.NullTime
	var sentenceID sql.NullInt64
	dest := []any{&item.ID, &item.Content, &item.CategoryCode, &item.CategoryName,
		&item.Source, &item.Author, &item.Nickname, &item.Contact, &clientIP, &hash,
		&item.Status, &item.RejectReason, &reviewedBy, &item.ReviewedByUsername, &reviewedAt,
		&sentenceID, &item.SentenceUUID, &item.CreatedAt}
	if withDup {
		dest = append(dest, &item.DuplicatePending)
	}
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Submission{}, ErrNotFound
		}
		return Submission{}, errors.New("scan submission")
	}
	item.ClientIP = scanClientIP(clientIP)
	if len(hash) == 32 {
		copy(item.ContentSHA256[:], hash)
	}
	if reviewedBy.Valid {
		v := uint64(reviewedBy.Int64)
		item.ReviewedBy = &v
	}
	if reviewedAt.Valid {
		t := reviewedAt.Time.UTC()
		item.ReviewedAt = &t
	}
	if sentenceID.Valid {
		v := uint64(sentenceID.Int64)
		item.SentenceID = &v
	}
	item.CreatedAt = item.CreatedAt.UTC()
	return item, nil
}

func exactDuplicateSentence(ctx context.Context, tx *sql.Tx, categoryID uint64, content string, excludeID uint64) error {
	var q string
	var args []any
	if excludeID == 0 {
		q = "SELECT 1 FROM sentences WHERE category_id = ? AND BINARY content = ? AND status = 1 LIMIT 1"
		args = []any{categoryID, content}
	} else {
		q = "SELECT 1 FROM sentences WHERE category_id = ? AND BINARY content = ? AND status = 1 AND id <> ? LIMIT 1"
		args = []any{categoryID, content, excludeID}
	}
	var one int
	err := tx.QueryRowContext(ctx, q, args...).Scan(&one)
	if err == nil {
		return ErrDuplicate
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return errors.New("check duplicate sentence")
}
