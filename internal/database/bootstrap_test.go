package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func sqliteTarget(t *testing.T, name string) Target {
	t.Helper()
	return Target{SQLitePath: filepath.Join(t.TempDir(), name)}
}

func openTarget(t *testing.T, target Target) (*sql.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := target.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, ctx
}

func TestSQLiteInitializeSeedsOnceAndPreservesEmptyDelete(t *testing.T) {
	target := sqliteTarget(t, "quote?name#x.db")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	db, _ := openTarget(t, target)
	var categories, sentences, pending int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_initialization WHERE seed_pending").Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if categories != 1 || sentences != 1 || pending != 0 {
		t.Fatalf("initial seed categories=%d sentences=%d pending=%d", categories, sentences, pending)
	}
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var again int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&again); err != nil || again != 1 {
		t.Fatalf("repeated seed sentences=%d err=%v", again, err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM sentences"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM categories"); err != nil {
		t.Fatal(err)
	}
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if categories != 0 || sentences != 0 {
		t.Fatalf("deleted data was reseeded categories=%d sentences=%d", categories, sentences)
	}
}

func TestSQLiteExplicitMigrateUpDoesNotSeed(t *testing.T) {
	target := sqliteTarget(t, "explicit.db")
	if err := target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	db, ctx := openTarget(t, target)
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("MigrateUp seeded categories=%d", n)
	}
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&n); err != nil || n != 0 {
		t.Fatalf("existing empty schema seeded: %d %v", n, err)
	}
}

func TestSQLiteInitializePreservesExistingSettingsAndAdmin(t *testing.T) {
	target := sqliteTarget(t, "preserve.db")
	ctx := context.Background()
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	db, _ := openTarget(t, target)
	if _, err := db.ExecContext(ctx, "INSERT INTO site_settings (id,site_name,contact) VALUES (1,'existing','ops@example.com')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE site_settings SET site_name='existing' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO admin_users (username,password_hash) VALUES ('admin','hash')"); err != nil {
		t.Fatal(err)
	}
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var name, hash string
	if err := db.QueryRowContext(ctx, "SELECT site_name FROM site_settings WHERE id=1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE username='admin'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if name != "existing" || hash != "hash" {
		t.Fatalf("existing data changed name=%q hash=%q", name, hash)
	}
}

func TestSQLiteConcurrentInitializeSameFile(t *testing.T) {
	target := sqliteTarget(t, "concurrent.db")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- target.Initialize(ctx)
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent initialize: %v", err)
		}
	}
	db, _ := openTarget(t, target)
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("concurrent seed count=%d err=%v", n, err)
	}
}

func TestSQLiteInitializeRejectsFutureAndDirtySchemaUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate string
	}{
		{name: "future", mutate: "UPDATE schema_migrations SET version = ?"},
		{name: "dirty", mutate: "UPDATE schema_migrations SET dirty = 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := sqliteTarget(t, tc.name+".db")
			if err := target.MigrateUp(); err != nil {
				t.Fatal(err)
			}
			db, ctx := openTarget(t, target)
			if tc.name == "future" {
				if _, err := db.ExecContext(ctx, tc.mutate, CurrentSchemaVersion+1); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.ExecContext(ctx, tc.mutate); err != nil {
				t.Fatal(err)
			}
			if err := target.Initialize(ctx); err == nil {
				t.Fatal("accepted unsupported schema")
			}
			var version uint
			var dirty bool
			if err := db.QueryRowContext(ctx, "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
				t.Fatal(err)
			}
			if tc.name == "future" && version != CurrentSchemaVersion+1 || tc.name == "dirty" && !dirty {
				t.Fatalf("schema state changed version=%d dirty=%t", version, dirty)
			}
		})
	}
}

func TestSQLiteInterruptedInitializationResumesPendingSeed(t *testing.T) {
	target := sqliteTarget(t, "resume.db")
	db, ctx := openTarget(t, target)
	if err := prepareInitialization(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("resumed seed count=%d err=%v", n, err)
	}
}

func TestSQLiteInitializeStartupErrorStageAndCategory(t *testing.T) {
	target := Target{SQLitePath: "/dev/null/quotewisp.db"}
	err := target.Initialize(context.Background())
	var startup *StartupError
	if !errors.As(err, &startup) {
		t.Fatalf("error type=%T err=%v", err, err)
	}
	if startup.Stage != "database-open" || startup.Category != "database" {
		t.Fatalf("startup error stage=%q category=%q", startup.Stage, startup.Category)
	}
}
