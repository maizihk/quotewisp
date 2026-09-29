package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"sentence-api/internal/snapshot"
)

const (
	MinReadSchemaVersion  uint = 1
	CurrentSchemaVersion  uint = 5
	RequiredSchemaVersion      = CurrentSchemaVersion
)

func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("open sqlite database")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errors.New("create database directory")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("resolve database path")
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite", uri.String()+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate&_time_format=sqlite")
	if err != nil {
		return nil, errors.New("open sqlite database")
	}
	// WAL readers (sessions and import status) must remain available during a
	// long import. BEGIN IMMEDIATE still serializes all writing transactions.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err = pingSQLite(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Concurrent first connections can contend while enabling WAL, before SQLite's
// busy handler can wait. Retry only lock contention within a bounded deadline.
func pingSQLite(ctx context.Context, db *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		err := db.PingContext(ctx)
		if err == nil {
			return nil
		}
		var coded interface{ Code() int }
		if !errors.As(err, &coded) || (coded.Code()&255 != 5 && coded.Code()&255 != 6) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("ping database")
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type Queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func DatasetVersion(ctx context.Context, q Queryer) (uint64, error) {
	var v uint64
	if e := q.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id = 1").Scan(&v); errors.Is(e, sql.ErrNoRows) {
		return 0, &ValidationError{Reason: "dataset version row is missing"}
	} else if e != nil {
		return 0, errors.New("read dataset version")
	}
	if v == 0 {
		return 0, &ValidationError{Reason: "dataset version is zero"}
	}
	return v, nil
}
func SchemaVersion(ctx context.Context, db *sql.DB) (uint, bool, error) {
	var v uint
	var dirty bool
	e := db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&v, &dirty)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, false, nil
	}
	if e != nil {
		return 0, false, errors.New("read schema version")
	}
	return v, dirty, nil
}
func CheckSchema(ctx context.Context, db *sql.DB, min, max uint) error {
	if min == 0 || max < min {
		return errors.New("invalid schema version range")
	}
	v, d, e := SchemaVersion(ctx, db)
	if e != nil {
		return e
	}
	if d || v < min || v > max {
		return fmt.Errorf("unsupported schema state: version=%d dirty=%t", v, d)
	}
	return nil
}

func CheckReadSchema(ctx context.Context, db *sql.DB) error {
	return CheckSchema(ctx, db, MinReadSchemaVersion, CurrentSchemaVersion)
}

func CheckWriteSchema(ctx context.Context, db *sql.DB) error {
	return CheckSchema(ctx, db, CurrentSchemaVersion, CurrentSchemaVersion)
}

type Loader struct{ DB *sql.DB }

func (l Loader) Version(ctx context.Context) (uint64, error)          { return DatasetVersion(ctx, l.DB) }
func (l Loader) Load(ctx context.Context) (*snapshot.Snapshot, error) { return LoadSnapshot(ctx, l.DB) }
func LoadSnapshot(ctx context.Context, db *sql.DB) (*snapshot.Snapshot, error) {
	tx, e := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, errors.New("begin snapshot transaction")
	}
	defer tx.Rollback()
	v, e := DatasetVersion(ctx, tx)
	if e != nil {
		return nil, e
	}
	b := snapshot.NewBuilder(v, time.Time{})
	categoryOrder := "code COLLATE BINARY ASC"

	rows, e := tx.QueryContext(ctx, "SELECT code, name, sort_order FROM categories WHERE enabled = TRUE ORDER BY sort_order ASC, "+categoryOrder)
	if e != nil {
		return nil, errors.New("read categories")
	}
	for rows.Next() {
		var r snapshot.CategoryRow
		if e = rows.Scan(&r.Code, &r.Name, &r.SortOrder); e != nil {
			rows.Close()
			return nil, errors.New("scan category")
		}
		if e = b.AddCategory(r); e != nil {
			rows.Close()
			return nil, &ValidationError{Reason: e.Error()}
		}
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, errors.New("iterate categories")
	}
	rows.Close()
	rows, e = tx.QueryContext(ctx, "SELECT s.id, s.uuid, s.content, c.code, COALESCE(s.source,''), COALESCE(s.author,''), s.length, s.published_at FROM sentences s JOIN categories c ON c.id=s.category_id WHERE s.status=1 AND c.enabled=TRUE ORDER BY s.id")
	if e != nil {
		return nil, errors.New("read sentences")
	}
	for rows.Next() {
		var r snapshot.SentenceRow
		var published sql.NullTime
		if e = rows.Scan(&r.ID, &r.UUID, &r.Content, &r.Category, &r.Source, &r.Author, &r.Length, &published); e != nil {
			rows.Close()
			return nil, errors.New("scan sentence")
		}
		if !published.Valid {
			rows.Close()
			return nil, &ValidationError{Reason: "published sentence lacks published_at"}
		}
		if e = b.AddSentence(r); e != nil {
			rows.Close()
			return nil, &ValidationError{Reason: e.Error()}
		}
		if e = ctx.Err(); e != nil {
			rows.Close()
			return nil, e
		}
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, errors.New("iterate sentences")
	}
	rows.Close()
	if e = tx.Commit(); e != nil {
		return nil, errors.New("commit read transaction")
	}
	s, e := b.Build(ctx)
	if e != nil {
		return nil, &ValidationError{Reason: e.Error()}
	}
	return s, nil
}

type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "snapshot validation failed: " + e.Reason }

// migrateSQLite commits each schema version independently and atomically.
// the state row is updated in the same transaction, so concurrent starters are
// serialized by SQLite's write lock.
func migrateSQLite(raw string, steps int, up bool) error {
	ctx := context.Background()
	db, err := OpenSQLite(ctx, raw)
	if err != nil {
		return errors.New("migration open failed")
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (id INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1), version INTEGER NOT NULL, dirty BOOLEAN NOT NULL DEFAULT 0)`); err != nil {
		return errors.New("migration state failed")
	}
	var v uint
	var dirty bool
	err = db.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&v, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		v = 0
	} else if err != nil {
		return errors.New("migration state failed")
	}
	if dirty {
		return errors.New("migration state is dirty")
	}
	if v > CurrentSchemaVersion {
		return errors.New("unknown migration version")
	}
	if !up {
		if steps <= 0 {
			return errors.New("down steps must be positive")
		}
		for i := 0; i < steps && v > 0; i++ {
			if err = sqliteMigration(db, v, false); err != nil {
				return errors.New("migration failed")
			}
			v--
		}
		return nil
	}
	for v < CurrentSchemaVersion {
		next := v + 1
		if err = sqliteMigration(db, next, true); err != nil {
			return errors.New("migration failed")
		}
		v = next
	}
	return nil
}

func sqliteMigration(db *sql.DB, version uint, up bool) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current uint
	var dirty bool
	if err = tx.QueryRow("SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&current, &dirty); errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		return err
	}
	if dirty {
		return errors.New("migration state is dirty")
	}
	if current > CurrentSchemaVersion {
		return errors.New("unknown migration version")
	}
	if up && current >= version {
		return tx.Commit()
	}
	if !up && current != version {
		return errors.New("migration state changed concurrently")
	}
	if _, err = tx.Exec("DELETE FROM schema_migrations"); err != nil {
		return err
	}
	var stmts []string
	if up {
		switch version {
		case 1:
			stmts = []string{
				`CREATE TABLE categories (id INTEGER PRIMARY KEY AUTOINCREMENT, code TEXT NOT NULL COLLATE BINARY UNIQUE, name TEXT NOT NULL, enabled BOOLEAN NOT NULL DEFAULT 1, sort_order INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
				`CREATE TABLE sentences (id INTEGER PRIMARY KEY AUTOINCREMENT, uuid TEXT NOT NULL COLLATE BINARY UNIQUE, category_id INTEGER NOT NULL, content TEXT NOT NULL, source TEXT, author TEXT, length INTEGER NOT NULL, status INTEGER NOT NULL DEFAULT 0, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, published_at DATETIME, FOREIGN KEY(category_id) REFERENCES categories(id))`,
				`CREATE INDEX idx_categories_enabled_sort ON categories(enabled,sort_order)`, `CREATE INDEX idx_sentences_publish_query ON sentences(status,category_id,length,id)`,
				`CREATE TABLE dataset_versions (id INTEGER PRIMARY KEY, version INTEGER NOT NULL DEFAULT 1, published_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`, `INSERT INTO dataset_versions(id,version) VALUES(1,1)`,
			}
		case 2:
			stmts = []string{`CREATE TABLE admin_users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL COLLATE BINARY UNIQUE, password_hash BLOB NOT NULL, enabled BOOLEAN NOT NULL DEFAULT 1, created_by INTEGER, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, last_login_at DATETIME)`, `CREATE TABLE submissions (id INTEGER PRIMARY KEY AUTOINCREMENT, content TEXT NOT NULL, category_id INTEGER NOT NULL, source TEXT, author TEXT, nickname TEXT, contact TEXT, client_ip BLOB, content_sha256 BLOB NOT NULL, status INTEGER NOT NULL DEFAULT 0, reject_reason TEXT, reviewed_by INTEGER, reviewed_at DATETIME, sentence_id INTEGER UNIQUE, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY(category_id) REFERENCES categories(id), FOREIGN KEY(reviewed_by) REFERENCES admin_users(id), FOREIGN KEY(sentence_id) REFERENCES sentences(id))`, `CREATE TABLE admin_sessions (token_hash BLOB PRIMARY KEY, admin_id INTEGER NOT NULL, csrf_token BLOB NOT NULL, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, last_seen_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, FOREIGN KEY(admin_id) REFERENCES admin_users(id))`, `CREATE INDEX idx_submissions_status_created ON submissions(status,created_at)`, `CREATE INDEX idx_submissions_pending_hash ON submissions(status,content_sha256)`, `CREATE INDEX idx_submissions_reviewed ON submissions(status,reviewed_at)`, `CREATE INDEX idx_admin_sessions_admin ON admin_sessions(admin_id)`, `CREATE INDEX idx_admin_sessions_expires ON admin_sessions(expires_at)`}
		case 3:
			stmts = []string{`CREATE TABLE site_settings (id INTEGER PRIMARY KEY, site_name TEXT NOT NULL, contact TEXT NOT NULL, public_origin TEXT, repo_url TEXT, beian_text TEXT, beian_url TEXT, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`}
		case 4:
			stmts = []string{`ALTER TABLE site_settings ADD COLUMN english_name TEXT`, `ALTER TABLE site_settings ADD COLUMN slogan TEXT`}
		case 5:
			stmts = []string{`CREATE TABLE import_jobs (id TEXT PRIMARY KEY COLLATE BINARY NOT NULL, admin_id INTEGER NOT NULL, digest TEXT NOT NULL COLLATE BINARY, format TEXT NOT NULL, status TEXT NOT NULL, result_json TEXT, error_text TEXT, categories_json TEXT, created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`, `CREATE INDEX idx_import_jobs_admin_created ON import_jobs(admin_id,created_at)`, `CREATE INDEX idx_import_jobs_status_expires ON import_jobs(status,expires_at)`}
		}
	} else {
		switch version {
		case 5:
			stmts = []string{`DROP TABLE import_jobs`}
		case 4:
			stmts = []string{`ALTER TABLE site_settings DROP COLUMN slogan`, `ALTER TABLE site_settings DROP COLUMN english_name`}
		case 3:
			stmts = []string{`DROP TABLE site_settings`}
		case 2:
			stmts = []string{`DROP TABLE admin_sessions`, `DROP TABLE submissions`, `DROP TABLE admin_users`}
		case 1:
			stmts = []string{`DROP TABLE sentences`, `DROP TABLE categories`, `DROP TABLE dataset_versions`}
		}
	}
	for _, s := range stmts {
		if _, err = tx.Exec(s); err != nil {
			return err
		}
	}
	if up {
		_, err = tx.Exec("INSERT INTO schema_migrations(version,dirty) VALUES(?,0)", version)
	} else {
		_, err = tx.Exec("INSERT INTO schema_migrations(version,dirty) VALUES(?,0)", version-1)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
