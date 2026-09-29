package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Target keeps the SQLite path separate from a MySQL DSN: any MySQL username
// (including "sqlite") remains valid, and a bad external database never falls back.
type Target struct{ MySQLDSN, SQLitePath string }

func (t Target) Open(ctx context.Context, pool PoolConfig) (*sql.DB, error) {
	if t.SQLitePath != "" {
		return OpenSQLite(ctx, t.SQLitePath)
	}
	return Open(ctx, t.MySQLDSN, pool)
}
func (t Target) MigrateUp() error {
	if t.SQLitePath != "" {
		return migrateSQLite(t.SQLitePath, 0, true)
	}
	return MigrateUp(t.MySQLDSN)
}
func (t Target) MigrateDown(steps int) error {
	if steps <= 0 {
		return errors.New("down steps must be positive")
	}
	if t.SQLitePath != "" {
		return migrateSQLite(t.SQLitePath, steps, false)
	}
	return MigrateDown(t.MySQLDSN, steps)
}

// StartupError exposes safe, actionable stages without DSNs or driver messages.
type StartupError struct {
	Stage, Category string
	cause           error
}

func (e *StartupError) Error() string { return e.Stage + ": " + e.Category }
func (e *StartupError) Unwrap() error { return e.cause }
func startupError(stage, category string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		category = "timeout"
	}
	if errors.Is(err, context.Canceled) {
		category = "canceled"
	}
	return &StartupError{Stage: stage, Category: category, cause: err}
}

// Initialize persists the fresh-database decision BEFORE migration. If startup
// is interrupted between migration and seeding, the pending marker survives.
// A locked transaction consumes it with the seed, so concurrent starters cannot
// duplicate examples, and deleting data later never reactivates the seed.
func (t Target) Initialize(ctx context.Context, pool PoolConfig) error {
	if t.SQLitePath == "" {
		unlock, err := t.lockInitialization(ctx)
		if err != nil {
			return startupError("database-initialization-lock", "database", err)
		}
		defer unlock()
	}
	db, err := t.Open(ctx, pool)
	if err != nil {
		return startupError("database-open", "database", err)
	}
	defer db.Close()
	if err = prepareInitialization(ctx, db); err != nil {
		return startupError("database-initialization", "schema-state", err)
	}
	if err = t.MigrateUp(); err != nil {
		return startupError("database-migration", "migration", err)
	}
	if err = CheckWriteSchema(ctx, db); err != nil {
		return startupError("database-schema", "schema-state", err)
	}
	if err = finishInitialization(ctx, db); err != nil {
		return startupError("database-seed", "database", err)
	}
	return nil
}

func existingSchema(ctx context.Context, db *sql.DB) (bool, error) {
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='schema_migrations'"
	if IsSQLite(db) {
		query = "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'"
	}
	var count int
	if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&rows); err != nil {
		return false, err
	}
	if rows > 1 {
		return true, errors.New("multiple schema state rows")
	}
	if rows == 0 {
		return true, nil
	}
	version, dirty, err := SchemaVersion(ctx, db)
	if err != nil {
		return true, err
	}
	if dirty || version > CurrentSchemaVersion {
		return true, errors.New("unsupported schema state")
	}
	return true, nil
}

func prepareInitialization(ctx context.Context, db *sql.DB) error {
	// Refuse unknown/dirty schemas before adding our initialization metadata.
	if _, err := existingSchema(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_initialization (
        id INTEGER PRIMARY KEY, seed_pending BOOLEAN NOT NULL, CHECK (id = 1)
    )`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insert := "INSERT IGNORE INTO app_initialization (id, seed_pending) VALUES (1, FALSE)"
	if IsSQLite(db) {
		insert = "INSERT OR IGNORE INTO app_initialization (id, seed_pending) VALUES (1, FALSE)"
	}
	result, err := tx.ExecContext(ctx, insert)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		// The first writer holds this marker's lock until the decision commits.
		// Other initializers cannot start migrations before that point.
		query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name <> 'app_initialization'"
		if IsSQLite(db) {
			query = "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'app_initialization'"
		}
		var tables int
		if err = tx.QueryRowContext(ctx, query).Scan(&tables); err != nil {
			return err
		}
		if tables == 0 {
			if _, err = tx.ExecContext(ctx, "UPDATE app_initialization SET seed_pending=TRUE WHERE id=1"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func finishInitialization(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pending bool
	if err = tx.QueryRowContext(ctx, "SELECT seed_pending FROM app_initialization WHERE id=1"+ForUpdate(db)).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return tx.Commit()
	}
	// Respect an explicit import performed after schema creation but before
	// startup finished. The durable pending marker, not row counts, controls
	// whether an initialization attempt is eligible to seed at all.
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM categories) + (SELECT COUNT(*) FROM sentences)").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		result, err := tx.ExecContext(ctx, "INSERT INTO categories (code,name,enabled,sort_order) VALUES ('original','原创',TRUE,0)")
		if err != nil {
			return err
		}
		category, err := result.LastInsertId()
		if err != nil {
			return err
		}
		const content = "给今天留一点空白，让新的想法慢慢长出来。"
		now := time.Now().UTC()
		if _, err = tx.ExecContext(ctx, `INSERT INTO sentences
            (uuid,category_id,content,source,author,length,status,published_at)
            VALUES (?,?,?,?,?,?,1,?)`, "710b0812-e252-4b13-8bdf-5357e4b5c9ce", category, content, "拾句示例", nil, utf8.RuneCountInString(content), now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE dataset_versions SET version=version+1,published_at=? WHERE id=1", now); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE app_initialization SET seed_pending=FALSE WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}

// MySQL DDL auto-commits, so an advisory lock spans schema inspection, migration
// and seeding. It prevents another first starter from seeing an in-flight dirty
// migration. A dedicated connection owns and releases the lock.
func (t Target) lockInitialization(ctx context.Context) (func(), error) {
	cfg, err := normalizedConfig(t.MySQLDSN, false)
	if err != nil {
		return nil, err
	}
	lockDB, err := Open(ctx, t.MySQLDSN, PoolConfig{MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return nil, err
	}
	conn, err := lockDB.Conn(ctx)
	if err != nil {
		lockDB.Close()
		return nil, err
	}
	digest := sha256.Sum256([]byte(cfg.DBName))
	name := fmt.Sprintf("quotewisp:init:%x", digest[:20])
	var acquired sql.NullInt64
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", name).Scan(&acquired); err != nil || !acquired.Valid || acquired.Int64 != 1 {
		conn.Close()
		lockDB.Close()
		if err == nil {
			err = errors.New("initialization lock timeout")
		}
		return nil, err
	}
	return func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(release, "SELECT RELEASE_LOCK(?)", name)
		conn.Close()
		lockDB.Close()
	}, nil
}
