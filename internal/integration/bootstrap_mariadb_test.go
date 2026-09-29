package integration

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"sentence-api/internal/database"
	"sentence-api/internal/testdb"
)

func mariadbTarget(t *testing.T) (database.Target, *sql.DB, context.Context) {
	t.Helper()
	db := testdb.Open(t)
	var name string
	if err := db.QueryRow("SELECT DATABASE()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("MYSQL_TEST_DSN"))
	if err != nil {
		t.Fatal("MYSQL_TEST_DSN is invalid")
	}
	cfg.DBName = name
	cfg.ParseTime = true
	return database.Target{MySQLDSN: cfg.FormatDSN()}, db, context.Background()
}

func TestMariaDBInitializeFreshConcurrentSeedAndNoReseed(t *testing.T) {
	target, db, ctx := mariadbTarget(t)
	if err := database.MigrateDown(target.MySQLDSN, int(database.CurrentSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE schema_migrations"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- target.Initialize(ctx, database.PoolConfig{}) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var categories, sentences int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if categories != 1 || sentences != 1 {
		t.Fatalf("seed counts categories=%d sentences=%d", categories, sentences)
	}
	if err := target.Initialize(ctx, database.PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if sentences != 1 {
		t.Fatalf("repeat reseeded sentences=%d", sentences)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM sentences"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM categories"); err != nil {
		t.Fatal(err)
	}
	if err := target.Initialize(ctx, database.PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if sentences != 0 {
		t.Fatalf("deleted data reseeded sentences=%d", sentences)
	}
}

func TestMariaDBInitializeExistingEmptySchemaPreservesData(t *testing.T) {
	target, db, ctx := mariadbTarget(t)
	if _, err := db.ExecContext(ctx, "INSERT INTO site_settings (id,site_name,contact) VALUES (1,?,?)", "Existing", "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO admin_users (username,password_hash) VALUES (?,?)", "admin", "existing-hash"); err != nil {
		t.Fatal(err)
	}
	if err := target.Initialize(ctx, database.PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	var categories, sentences int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&sentences); err != nil {
		t.Fatal(err)
	}
	if categories != 0 || sentences != 0 {
		t.Fatalf("existing empty schema seeded categories=%d sentences=%d", categories, sentences)
	}
	var name, hash string
	if err := db.QueryRowContext(ctx, "SELECT site_name FROM site_settings WHERE id=1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE username='admin'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if name != "Existing" || hash != "existing-hash" {
		t.Fatalf("existing data changed name=%q hash=%q", name, hash)
	}
}
