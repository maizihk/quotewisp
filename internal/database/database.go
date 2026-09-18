package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"sentence-api/internal/snapshot"
	"sentence-api/migrations"
)

type PoolConfig struct {
	MaxOpenConns, MaxIdleConns int
	ConnMaxLifetime            time.Duration
}

type safeDriverLogger struct{ logger *slog.Logger }

func (l safeDriverLogger) Print(_ ...any) {
	l.logger.Error("mysql_driver_diagnostic", "category", "driver-critical")
}

func newDriverLogger() mysql.Logger {
	return safeDriverLogger{logger: newDriverSlog(os.Stderr)}
}

func newDriverSlog(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Time(a.Key, a.Value.Time().UTC())
		}
		return a
	}}
	return slog.New(slog.NewJSONHandler(w, opts))
}

func normalizedConfig(raw string, multiStatements bool) (*mysql.Config, error) {
	c, e := mysql.ParseDSN(raw)
	if e != nil {
		return nil, errors.New("invalid MYSQL_DSN")
	}
	if c.DBName == "" {
		return nil, errors.New("MYSQL_DSN must include a database name")
	}
	// Match the driver's grammar: the final slash starts the database name and
	// the first question mark after it starts parameters. Password punctuation
	// and punctuation inside encoded parameter values must not move this split.
	if slash := strings.LastIndexByte(raw, '/'); slash >= 0 {
		if rel := strings.IndexByte(raw[slash+1:], '?'); rel >= 0 {
			i := slash + 1 + rel
			q, e := url.ParseQuery(raw[i+1:])
			if e != nil {
				return nil, errors.New("invalid MYSQL_DSN parameters")
			}
			for _, k := range []string{"timeout", "readTimeout", "writeTimeout"} {
				if v, ok := q[k]; ok {
					d, e := time.ParseDuration(v[len(v)-1])
					if e != nil || d <= 0 {
						return nil, fmt.Errorf("MYSQL_DSN %s must be positive", k)
					}
				}
			}
		}
	}
	c.ParseTime = true
	c.Loc = time.UTC
	c.Collation = "utf8mb4_unicode_ci"
	c.MultiStatements = multiStatements
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 30 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 30 * time.Second
	}
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	c.Params["time_zone"] = "'+00:00'"
	c.Params["charset"] = "utf8mb4"
	// Reparse the normalized DSN so driver-special parameters such as charset
	// populate their dedicated internal fields before NewConnector is used.
	// Logger is not serialized and is attached only after this round trip.
	normalized, e := mysql.ParseDSN(c.FormatDSN())
	if e != nil {
		return nil, errors.New("normalize MYSQL_DSN")
	}
	normalized.Logger = newDriverLogger()
	return normalized, nil
}
func NormalizeDSN(raw string, multiStatements bool) (string, error) {
	c, e := normalizedConfig(raw, multiStatements)
	if e != nil {
		return "", e
	}
	return c.FormatDSN(), nil
}
func Open(ctx context.Context, raw string, p PoolConfig) (*sql.DB, error) {
	c, e := normalizedConfig(raw, false)
	if e != nil {
		return nil, e
	}
	connector, e := mysql.NewConnector(c)
	if e != nil {
		return nil, errors.New("open database")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(p.MaxOpenConns)
	db.SetMaxIdleConns(p.MaxIdleConns)
	db.SetConnMaxLifetime(p.ConnMaxLifetime)
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, errors.New("ping database")
	}
	return db, nil
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
func CheckSchema(ctx context.Context, db *sql.DB) error {
	v, d, e := SchemaVersion(ctx, db)
	if e != nil {
		return e
	}
	if d || v != 1 {
		return fmt.Errorf("unsupported schema state: version=%d dirty=%t", v, d)
	}
	return nil
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
	rows, e := tx.QueryContext(ctx, "SELECT code, name, sort_order FROM categories WHERE enabled = TRUE ORDER BY sort_order ASC, BINARY code ASC")
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

func MigrateUp(raw string) error { return migrateRun(raw, 0, true) }
func MigrateDown(raw string, steps int) error {
	if steps <= 0 {
		return errors.New("down steps must be positive")
	}
	return migrateRun(raw, steps, false)
}
func migrateRun(raw string, steps int, up bool) error {
	c, e := normalizedConfig(raw, true)
	if e != nil {
		return e
	}
	connector, e := mysql.NewConnector(c)
	if e != nil {
		return errors.New("open migration database")
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	driver, e := migratemysql.WithInstance(db, &migratemysql.Config{})
	if e != nil {
		return errors.New("create migration database driver")
	}
	src, e := iofs.New(fs.FS(migrations.FS), ".")
	if e != nil {
		return errors.New("create migration source")
	}
	m, e := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if e != nil {
		return errors.New("create migrator")
	}
	defer m.Close()
	v, dirty, ve := m.Version()
	if ve != nil && !errors.Is(ve, migrate.ErrNilVersion) {
		return errors.New("read migration state")
	}
	if dirty {
		return errors.New("migration state is dirty")
	}
	if !errors.Is(ve, migrate.ErrNilVersion) && v > 1 {
		return errors.New("unknown migration version")
	}
	if up {
		e = m.Up()
	} else {
		e = m.Steps(-steps)
	}
	if errors.Is(e, migrate.ErrNoChange) {
		return nil
	}
	if e != nil {
		return errors.New("migration failed")
	}
	return nil
}
