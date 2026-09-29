package store

import (
	"context"
	"database/sql"
	"errors"
	"sentence-api/internal/database"
	"time"

	"github.com/google/uuid"
)

type SentenceFilter struct {
	CategoryCode string
	Status       uint8
	Query        string
	UUID         string
	Page, Size   int
}

type Sentence struct {
	ID                uint64
	UUID              string
	Content           string
	CategoryCode      string
	CategoryName      string
	Source            string
	Author            string
	Length            uint16
	Status            uint8
	PublishedAt       *time.Time
	CreatedAt         time.Time
	SubmissionID      *uint64
	SubmitterNickname string
}

func (s *Store) ListSentences(ctx context.Context, f SentenceFilter) ([]Sentence, int, error) {
	if f.Query != "" {
		if err := validateQuery(f.Query); err != nil {
			return nil, 0, err
		}
	}
	if f.UUID != "" {
		if err := validateUUIDFilter(f.UUID); err != nil {
			return nil, 0, err
		}
	}
	where := "s.status IN (1, 3)"
	args := []any{}
	if f.CategoryCode != "" {
		where += " AND c.code = ?"
		args = append(args, f.CategoryCode)
	}
	if f.Status != 0 {
		where += " AND s.status = ?"
		args = append(args, f.Status)
	}
	if f.Query != "" {
		escape := " ESCAPE '\\\\'"
		if database.IsSQLite(s.DB) {
			escape = " ESCAPE '\\'"
		}
		where += " AND s.content LIKE ?" + escape
		args = append(args, "%"+escapeLike(f.Query)+"%")
	}
	if f.UUID != "" {
		where += " AND s.uuid = ?"
		args = append(args, f.UUID)
	}
	countQ := "SELECT COUNT(*) FROM sentences s JOIN categories c ON c.id = s.category_id WHERE " + where
	var total int
	if err := s.DB.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, errors.New("count sentences")
	}
	offset := pageOffset(f.Page, f.Size)
	listArgs := append(append([]any{}, args...), f.Size, offset)
	rows, err := s.DB.QueryContext(ctx, `SELECT s.id, s.uuid, s.content, c.code, c.name,
		COALESCE(s.source, ''), COALESCE(s.author, ''), s.length, s.status, s.published_at, s.created_at
		FROM sentences s JOIN categories c ON c.id = s.category_id
		WHERE `+where+` ORDER BY s.id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, errors.New("list sentences")
	}
	defer rows.Close()
	var items []Sentence
	for rows.Next() {
		item, err := scanSentence(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, errors.New("iterate sentences")
	}
	return items, total, nil
}

func (s *Store) GetSentence(ctx context.Context, uuid string) (Sentence, error) {
	if err := validateUUIDFilter(uuid); err != nil {
		return Sentence{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT s.id, s.uuid, s.content, c.code, c.name,
		COALESCE(s.source, ''), COALESCE(s.author, ''), s.length, s.status, s.published_at, s.created_at,
		sub.id, COALESCE(sub.nickname, '')
		FROM sentences s
		JOIN categories c ON c.id = s.category_id
		LEFT JOIN submissions sub ON sub.sentence_id = s.id
		WHERE s.uuid = ?`, uuid)
	item, err := scanSentenceWithSubmission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Sentence{}, ErrNotFound
	}
	return item, err
}

func (s *Store) CreateSentence(ctx context.Context, f SentenceFields) (string, error) {
	var outUUID string
	err := s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		if err := ValidateWebSentenceFields(f.Content, f.Source, f.Author, 0); err != nil {
			return false, err
		}
		cat, err := lookupCategory(ctx, tx, f.CategoryCode)
		if err != nil {
			return false, err
		}
		if !cat.enabled {
			return false, ErrCategoryDisabled
		}
		if err = s.exactDuplicateSentence(ctx, tx, cat.id, f.Content, 0); err != nil {
			return false, err
		}
		u := uuid.New()
		outUUID = u.String()
		length := RuneLength(f.Content)
		now := time.Now().UTC()
		if _, err = tx.ExecContext(ctx, `INSERT INTO sentences
			(uuid, category_id, content, source, author, length, status, published_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
			outUUID, cat.id, f.Content, nullString(f.Source), nullString(f.Author), length, now); err != nil {
			return false, errors.New("insert sentence")
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return outUUID, nil
}

func (s *Store) UpdateSentence(ctx context.Context, uuid string, f SentenceFields) error {
	if err := validateUUIDFilter(uuid); err != nil {
		return err
	}
	return s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		var cur struct {
			id         uint64
			content    string
			source     sql.NullString
			author     sql.NullString
			categoryID uint64
			code       string
		}
		err := tx.QueryRowContext(ctx, `SELECT s.id, s.content, s.source, s.author, s.category_id, c.code
		FROM sentences s JOIN categories c ON c.id = s.category_id WHERE s.uuid = ?`+lockSuffix(s.DB), uuid).
			Scan(&cur.id, &cur.content, &cur.source, &cur.author, &cur.categoryID, &cur.code)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, errors.New("read sentence")
		}
		if err = ValidateWebSentenceFields(f.Content, f.Source, f.Author, 0); err != nil {
			return false, err
		}
		cat, err := lookupCategory(ctx, tx, f.CategoryCode)
		if err != nil {
			return false, err
		}
		if !cat.enabled {
			return false, ErrCategoryDisabled
		}
		oldSource := cur.source.String
		oldAuthor := cur.author.String
		if cur.content == f.Content && cur.code == f.CategoryCode && oldSource == f.Source && oldAuthor == f.Author {
			return false, ErrUnchanged
		}
		if cur.content != f.Content || cur.categoryID != cat.id {
			if err = s.exactDuplicateSentence(ctx, tx, cat.id, f.Content, cur.id); err != nil {
				return false, err
			}
		}
		length := RuneLength(f.Content)
		if _, err = tx.ExecContext(ctx, `UPDATE sentences SET content = ?, category_id = ?, source = ?, author = ?, length = ? WHERE id = ?`,
			f.Content, cat.id, nullString(f.Source), nullString(f.Author), length, cur.id); err != nil {
			return false, errors.New("update sentence")
		}
		return true, nil
	})
}

func (s *Store) SetSentenceStatus(ctx context.Context, uuid string, from, to uint8) error {
	if err := validateUUIDFilter(uuid); err != nil {
		return err
	}
	return s.withVersionTx(ctx, func(tx *sql.Tx) (bool, error) {
		var status uint8
		err := tx.QueryRowContext(ctx, "SELECT status FROM sentences WHERE uuid = ?"+lockSuffix(s.DB), uuid).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		if err != nil {
			return false, errors.New("read sentence")
		}
		if status != from {
			return false, ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "UPDATE sentences SET status = ? WHERE uuid = ?", to, uuid); err != nil {
			return false, errors.New("update sentence status")
		}
		return true, nil
	})
}

type sentenceScanner interface {
	Scan(dest ...any) error
}

func scanSentence(row sentenceScanner) (Sentence, error) {
	var item Sentence
	var published sql.NullTime
	if err := row.Scan(&item.ID, &item.UUID, &item.Content, &item.CategoryCode, &item.CategoryName,
		&item.Source, &item.Author, &item.Length, &item.Status, &published, &item.CreatedAt); err != nil {
		return Sentence{}, errors.New("scan sentence")
	}
	if published.Valid {
		t := published.Time.UTC()
		item.PublishedAt = &t
	}
	item.CreatedAt = item.CreatedAt.UTC()
	return item, nil
}

func scanSentenceWithSubmission(row sentenceScanner) (Sentence, error) {
	var item Sentence
	var published sql.NullTime
	var submissionID sql.NullInt64
	if err := row.Scan(&item.ID, &item.UUID, &item.Content, &item.CategoryCode, &item.CategoryName,
		&item.Source, &item.Author, &item.Length, &item.Status, &published, &item.CreatedAt,
		&submissionID, &item.SubmitterNickname); err != nil {
		return Sentence{}, errors.New("scan sentence")
	}
	if published.Valid {
		t := published.Time.UTC()
		item.PublishedAt = &t
	}
	if submissionID.Valid {
		v := uint64(submissionID.Int64)
		item.SubmissionID = &v
	}
	item.CreatedAt = item.CreatedAt.UTC()
	return item, nil
}
