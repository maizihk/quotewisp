package testdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"sentence-api/internal/database"
	"testing"
)

// Open returns an isolated migrated SQLite database; no external service or secret is required.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	target := database.Target{SQLitePath: filepath.Join(t.TempDir(), "test.db")}
	if err := target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	db, err := target.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
