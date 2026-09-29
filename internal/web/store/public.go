package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"sentence-api/internal/database"
)

type PublicCategory struct {
	Code  string
	Name  string
	Count uint64
}

type RecentItem struct {
	UUID, Content, CategoryCode, CategoryName, Nickname string
	ReviewedAt                                          time.Time
}

type PublicData struct {
	Version    uint64
	Categories []PublicCategory
	Recent     []RecentItem
	ExportJSON []byte
	BuiltAt    time.Time
}

type exportCategory struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	SortOrder int32  `json:"sort_order"`
}

type exportSentence struct {
	UUID     string  `json:"uuid"`
	Category string  `json:"category"`
	Content  string  `json:"content"`
	Source   *string `json:"source"`
	Author   *string `json:"author"`
}

type exportDoc struct {
	Categories []exportCategory `json:"categories"`
	Sentences  []exportSentence `json:"sentences"`
}

func (s *Store) BuildPublicData(ctx context.Context) (*PublicData, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, errors.New("begin public data transaction")
	}
	defer tx.Rollback()

	version, err := database.DatasetVersion(ctx, tx)
	if err != nil {
		return nil, err
	}

	out := &PublicData{Version: version, BuiltAt: time.Now().UTC(), Categories: []PublicCategory{}, Recent: []RecentItem{}}

	categoryOrder := "BINARY c.code ASC"
	if database.IsSQLite(s.DB) {
		categoryOrder = "c.code COLLATE BINARY ASC"
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.code, c.name, c.sort_order,
		COALESCE(SUM(CASE WHEN s.status = 1 THEN 1 ELSE 0 END), 0)
		FROM categories c
		LEFT JOIN sentences s ON s.category_id = c.id AND s.status = 1
		WHERE c.enabled = TRUE
		GROUP BY c.id, c.code, c.name, c.sort_order
		ORDER BY c.sort_order ASC, `+categoryOrder)
	if err != nil {
		return nil, errors.New("read public categories")
	}
	exportCats := make([]exportCategory, 0)
	for rows.Next() {
		var pc PublicCategory
		var sortOrder int32
		if err = rows.Scan(&pc.Code, &pc.Name, &sortOrder, &pc.Count); err != nil {
			rows.Close()
			return nil, errors.New("scan public category")
		}
		out.Categories = append(out.Categories, pc)
		exportCats = append(exportCats, exportCategory{Code: pc.Code, Name: pc.Name, SortOrder: sortOrder})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, errors.New("iterate public categories")
	}
	rows.Close()

	recentRows, err := tx.QueryContext(ctx, `SELECT t.uuid, sub.content, c.code, c.name, COALESCE(sub.nickname, ''), sub.reviewed_at
		FROM submissions sub
		JOIN sentences t ON t.id = sub.sentence_id
		JOIN categories c ON c.id = t.category_id
		WHERE sub.status = 1 AND t.status = 1 AND c.enabled = TRUE
		ORDER BY sub.reviewed_at DESC
		LIMIT 20`)
	if err != nil {
		return nil, errors.New("read recent submissions")
	}
	for recentRows.Next() {
		var item RecentItem
		if err = recentRows.Scan(&item.UUID, &item.Content, &item.CategoryCode, &item.CategoryName, &item.Nickname, &item.ReviewedAt); err != nil {
			recentRows.Close()
			return nil, errors.New("scan recent submission")
		}
		item.ReviewedAt = item.ReviewedAt.UTC()
		out.Recent = append(out.Recent, item)
	}
	if err = recentRows.Err(); err != nil {
		recentRows.Close()
		return nil, errors.New("iterate recent submissions")
	}
	recentRows.Close()

	sentenceRows, err := tx.QueryContext(ctx, `SELECT t.uuid, c.code, t.content, t.source, t.author
		FROM sentences t
		JOIN categories c ON c.id = t.category_id
		WHERE t.status = 1 AND c.enabled = TRUE
		ORDER BY t.id`)
	if err != nil {
		return nil, errors.New("read export sentences")
	}
	exportSentences := make([]exportSentence, 0)
	for sentenceRows.Next() {
		var es exportSentence
		var source, author sql.NullString
		if err = sentenceRows.Scan(&es.UUID, &es.Category, &es.Content, &source, &author); err != nil {
			sentenceRows.Close()
			return nil, errors.New("scan export sentence")
		}
		if source.Valid {
			v := source.String
			es.Source = &v
		}
		if author.Valid {
			v := author.String
			es.Author = &v
		}
		exportSentences = append(exportSentences, es)
	}
	if err = sentenceRows.Err(); err != nil {
		sentenceRows.Close()
		return nil, errors.New("iterate export sentences")
	}
	sentenceRows.Close()

	if err = tx.Commit(); err != nil {
		return nil, errors.New("commit public data transaction")
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(exportDoc{Categories: exportCats, Sentences: exportSentences}); err != nil {
		return nil, errors.New("encode export json")
	}
	out.ExportJSON = bytes.TrimSpace(buf.Bytes())
	return out, nil
}
