package importjobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"sentence-api/internal/database"
	"sentence-api/internal/importer"
)

const managerInput = `{"categories":[{"code":"x","name":"X"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"hello"}]}`

type managerFixture struct {
	db     *sql.DB
	m      *Manager
	cancel context.CancelFunc
	wg     sync.WaitGroup
	dir    string
}

func newManagerFixture(t *testing.T, ttl time.Duration, refresh func() error) *managerFixture {
	t.Helper()
	dir := t.TempDir()
	target := database.Target{SQLitePath: filepath.Join(dir, "jobs.db")}
	return newManagerFixtureTarget(t, target, filepath.Join(dir, "uploads"), ttl, refresh)
}

func newManagerFixtureTarget(t *testing.T, target database.Target, uploadDir string, ttl time.Duration, refresh func() error) *managerFixture {
	t.Helper()
	if err := target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	db, err := target.Open(context.Background(), database.PoolConfig{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	f := &managerFixture{db: db, cancel: cancel, dir: uploadDir}
	m, err := New(life, Options{DB: db, Dir: f.dir, MaxBytes: 1 << 20, TTL: ttl, Timeout: 2 * time.Second, Refresh: func(context.Context) error {
		if refresh != nil {
			return refresh()
		}
		return nil
	}}, &f.wg)
	if err != nil {
		cancel()
		db.Close()
		t.Fatal(err)
	}
	f.m = m
	t.Cleanup(func() {
		cancel()
		f.wg.Wait()
		db.Close()
	})
	return f
}

func mysqlManagerTarget(t *testing.T) (database.Target, func()) {
	t.Helper()
	raw := os.Getenv("MYSQL_TEST_DSN")
	if raw == "" {
		t.Skip("MYSQL_TEST_DSN is not set; MySQL manager test skipped")
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var suffix [8]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "sentence_api_importjobs_" + hex.EncodeToString(suffix[:])
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("create isolated manager database:", err)
	}
	cleanup := func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanCtx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Errorf("drop isolated manager database: %v", err)
		}
	}
	cfg.DBName = name
	return database.Target{MySQLDSN: cfg.FormatDSN()}, cleanup
}

func waitJob(t *testing.T, m *Manager, owner uint64, id string, want ...string) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		j, err := m.Get(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, status := range want {
			if j.Status == status {
				return j
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("job %s did not reach %v; last status %s", id, want, j.Status)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestManagerUploadPreviewConfirmCompleteAndOwnerChecks(t *testing.T) {
	f := newManagerFixture(t, time.Minute, nil)
	j, err := f.m.Upload(context.Background(), 7, importer.FormatNative, strings.NewReader(managerInput))
	if err != nil {
		t.Fatal(err)
	}
	preview := waitJob(t, f.m, 7, j.ID, "preview")
	if preview.HasResult == false || len(preview.Categories) != 1 {
		t.Fatalf("preview=%+v", preview)
	}
	if _, err = f.m.Get(context.Background(), 8, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner get error=%v", err)
	}
	if err = f.m.Confirm(context.Background(), 8, j.ID, j.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong owner confirm error=%v", err)
	}
	if err = f.m.Confirm(context.Background(), 7, j.ID, "bad"); !errors.Is(err, ErrDigest) {
		t.Fatalf("wrong digest error=%v", err)
	}
	if err = f.m.Confirm(context.Background(), 7, j.ID, j.Digest); err != nil {
		t.Fatal(err)
	}
	complete := waitJob(t, f.m, 7, j.ID, "complete")
	if !complete.HasResult || !complete.Result.Changed {
		t.Fatalf("complete=%+v", complete)
	}
	if err = f.m.Confirm(context.Background(), 7, j.ID, j.Digest); !errors.Is(err, ErrState) {
		t.Fatalf("duplicate confirmation error=%v", err)
	}
	var n int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("sentences=%d err=%v", n, err)
	}
}

func TestManagerUploadLimitMalformedAndPollTimeout(t *testing.T) {
	f := newManagerFixture(t, time.Minute, nil)
	if _, err := f.m.Upload(context.Background(), 1, importer.FormatNative, strings.NewReader(strings.Repeat("x", 1<<20+1))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large upload error=%v", err)
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("uploads after size failure=%d err=%v", len(entries), err)
	}
	j, err := f.m.Upload(context.Background(), 1, importer.FormatNative, strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, j.ID, "failed")
	if _, err = os.Stat(f.m.path(j.ID)); !os.IsNotExist(err) {
		t.Fatalf("malformed upload retained: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	if _, err = f.m.List(ctx, 1); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("poll/list timeout error=%v", err)
	}
}

func TestManagerSingleActivePreviewBusyUntilCancelAndExpiry(t *testing.T) {
	f := newManagerFixture(t, time.Minute, nil)
	one, err := f.m.Upload(context.Background(), 1, importer.FormatNative, strings.NewReader(managerInput))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, one.ID, "preview")
	if _, err = f.m.Upload(context.Background(), 2, importer.FormatNative, strings.NewReader(managerInput)); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy upload error=%v", err)
	}
	if err = f.m.Cancel(context.Background(), 1, one.ID); err != nil {
		t.Fatal(err)
	}
	two, err := f.m.Upload(context.Background(), 2, importer.FormatNative, strings.NewReader(managerInput))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 2, two.ID, "preview")
	if _, err = f.db.Exec("UPDATE import_jobs SET expires_at=? WHERE id=?", time.Now().Add(-time.Second), two.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.m.Confirm(context.Background(), 2, two.ID, two.Digest); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired confirm error=%v", err)
	}
	waitJob(t, f.m, 2, two.ID, "expired")
}

func TestManagerTamperedUploadFailsWithoutWriting(t *testing.T) {
	f := newManagerFixture(t, time.Minute, nil)
	j, err := f.m.Upload(context.Background(), 1, importer.FormatNative, strings.NewReader(managerInput))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, j.ID, "preview")
	if err = os.WriteFile(f.m.path(j.ID), []byte(managerInput+" "), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.m.Confirm(context.Background(), 1, j.ID, j.Digest); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, j.ID, "failed")
	var n int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 0 {
		t.Fatalf("sentences after tamper=%d err=%v", n, err)
	}
}

func TestManagerRefreshFailureRetainsCommitAndRetry(t *testing.T) {
	fail := true
	f := newManagerFixture(t, time.Minute, func() error {
		if fail {
			return errors.New("refresh down")
		}
		return nil
	})
	j, err := f.m.Upload(context.Background(), 1, importer.FormatNative, strings.NewReader(managerInput))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, j.ID, "preview")
	if err = f.m.Confirm(context.Background(), 1, j.ID, j.Digest); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, j.ID, "refresh_failed")
	var n int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("committed sentences=%d err=%v", n, err)
	}
	fail = false
	if err = f.m.RetryRefresh(context.Background(), 1, j.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 1, j.ID, "complete")
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("duplicate sentences=%d err=%v", n, err)
	}
}

func TestManagerRecoveryMarksInterruptedAndRefreshesCommitted(t *testing.T) {
	dir := t.TempDir()
	target := database.Target{SQLitePath: filepath.Join(dir, "jobs.db")}
	if err := target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	db, err := target.Open(context.Background(), database.PoolConfig{})
	if err != nil {
		t.Fatal(err)
	}
	uploads := filepath.Join(dir, "uploads")
	if err = os.MkdirAll(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rows := []struct{ id, status string }{{"00000000-0000-0000-0000-000000000011", "queued_preview"}, {"00000000-0000-0000-0000-000000000012", "preview"}, {"00000000-0000-0000-0000-000000000013", "importing"}, {"00000000-0000-0000-0000-000000000014", "committed"}}
	for _, row := range rows {
		_, err = db.Exec(`INSERT INTO import_jobs (id,admin_id,digest,format,status,created_at,expires_at,updated_at) VALUES (?,?,?,?,?,?,?,?)`, row.id, 1, strings.Repeat("a", 64), importer.FormatNative, row.status, now, now.Add(time.Hour), now)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(uploads, row.id+".json"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	life, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	m, err := New(life, Options{DB: db, Dir: uploads, MaxBytes: 1 << 20, TTL: time.Minute, Timeout: time.Second, Refresh: func(context.Context) error { return nil }}, &wg)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	wg.Wait()
	defer db.Close()
	for _, row := range rows {
		var status string
		if err = db.QueryRow("SELECT status FROM import_jobs WHERE id=?", row.id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		want := "interrupted"
		if row.status == "committed" {
			want = "refresh_failed"
		}
		if status != want {
			t.Errorf("%s status=%s want=%s", row.id, status, want)
		}
		if _, err = os.Stat(filepath.Join(uploads, row.id+".json")); !os.IsNotExist(err) {
			t.Errorf("%s upload retained: %v", row.id, err)
		}
	}
	_ = m
}

func TestManagerMySQLRefreshRetryAndRecovery(t *testing.T) {
	target, drop := mysqlManagerTarget(t)
	t.Cleanup(drop)
	dir := t.TempDir()
	fail := true
	f := newManagerFixtureTarget(t, target, filepath.Join(dir, "uploads"), time.Minute, func() error {
		if fail {
			return errors.New("refresh down")
		}
		return nil
	})
	j, err := f.m.Upload(context.Background(), 9, importer.FormatNative, strings.NewReader(managerInput))
	if err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 9, j.ID, "preview")
	if err = f.m.Confirm(context.Background(), 9, j.ID, j.Digest); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 9, j.ID, "refresh_failed")
	var n int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("MySQL committed sentences=%d err=%v", n, err)
	}
	fail = false
	if err = f.m.RetryRefresh(context.Background(), 9, j.ID); err != nil {
		t.Fatal(err)
	}
	waitJob(t, f.m, 9, j.ID, "complete")
	if err = f.db.QueryRow("SELECT COUNT(*) FROM sentences").Scan(&n); err != nil || n != 1 {
		t.Fatalf("MySQL duplicate sentences=%d err=%v", n, err)
	}

	// A fresh manager reconciles committed work and removes private uploads.
	if err = os.WriteFile(f.m.path(j.ID), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE import_jobs SET status='committed' WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	f.cancel()
	f.wg.Wait()
	life, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	m2, err := New(life, Options{DB: f.db, Dir: f.dir, MaxBytes: 1 << 20, TTL: time.Minute, Timeout: time.Second, Refresh: func(context.Context) error { return nil }}, &wg)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	wg.Wait()
	_ = m2
	var status string
	if err = f.db.QueryRow("SELECT status FROM import_jobs WHERE id=?", j.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "refresh_failed" {
		t.Fatalf("MySQL recovered status=%s", status)
	}
	if _, err = os.Stat(f.m.path(j.ID)); !os.IsNotExist(err) {
		t.Fatalf("MySQL recovered upload retained: %v", err)
	}
}
