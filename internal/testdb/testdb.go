package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"sentence-api/internal/database"
)

func Open(t testing.TB) *sql.DB {
	t.Helper()
	raw := testingEnv("MYSQL_TEST_DSN")
	if raw == "" {
		t.Skip("MYSQL_TEST_DSN is not set; external database integration test skipped")
	}
	cfg, err := mysql.ParseDSN(raw)
	if err != nil {
		t.Fatal("MYSQL_TEST_DSN is invalid")
	}
	adminDB := cfg.DBName
	if adminDB == "" {
		adminDB = "mysql"
	}
	cfg.DBName = adminDB
	cfg.ParseTime = true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal("open test admin connection")
	}
	t.Cleanup(func() { _ = admin.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = admin.PingContext(ctx); err != nil {
		t.Fatal("connect test database")
	}

	var suffix [8]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		t.Fatal("generate isolated database name")
	}
	name := "sentence_api_test_" + hex.EncodeToString(suffix[:])
	if _, err = admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("create isolated test database")
	}
	created := true
	t.Cleanup(func() {
		if !created {
			return
		}
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanCancel()
		if _, e := admin.ExecContext(cleanCtx, "DROP DATABASE `"+name+"`"); e != nil {
			t.Errorf("drop isolated test database: %v", e)
		}
	})

	cfg.DBName = name
	testDSN := cfg.FormatDSN()
	if err = database.MigrateUp(testDSN); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, testDSN, database.PoolConfig{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testingEnv(key string) string {
	return os.Getenv(key)
}
