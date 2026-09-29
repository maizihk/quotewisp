package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentence-api/internal/database"
	"sentence-api/internal/httpapi"
	"sentence-api/internal/importer"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
)

func TestSQLiteMigrationImportAndSnapshot(t *testing.T) {
	ctx := context.Background()
	target := database.Target{SQLitePath: filepath.Join(t.TempDir(), "migration.db")}
	if err := target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	db, err := target.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.CheckWriteSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if v, dirty, e := database.SchemaVersion(ctx, db); e != nil || dirty || v != database.CurrentSchemaVersion {
		t.Fatalf("schema version=%d dirty=%t err=%v", v, dirty, e)
	}
	var webTables int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','submissions','admin_sessions')").Scan(&webTables); err != nil || webTables != 3 {
		t.Fatalf("web tables missing count=%d err=%v", webTables, err)
	}
	var settingsTables int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = 'site_settings'").Scan(&settingsTables); err != nil || settingsTables != 1 {
		t.Fatalf("site_settings missing count=%d err=%v", settingsTables, err)
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO site_settings (id, site_name, english_name, slogan, contact) VALUES (1, ?, ?, ?, ?)", "Onword\u200b", "Quotewisp", "偶遇一句话", "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if err = target.MigrateDown(int(database.CurrentSchemaVersion) - 3); err != nil {
		t.Fatal(err)
	}
	if v, dirty, e := database.SchemaVersion(ctx, db); e != nil || dirty || v != 3 {
		t.Fatalf("after brand down version=%d dirty=%t err=%v", v, dirty, e)
	}
	var preservedName string
	if err = db.QueryRowContext(ctx, "SELECT site_name FROM site_settings WHERE id = 1").Scan(&preservedName); err != nil || preservedName != "Onword\u200b" {
		t.Fatalf("site_name not preserved after brand down name=%q err=%v", preservedName, err)
	}
	var brandColumns int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('site_settings') WHERE name IN ('english_name','slogan')").Scan(&brandColumns); err != nil || brandColumns != 0 {
		t.Fatalf("brand columns remain after down count=%d err=%v", brandColumns, err)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','submissions','admin_sessions')").Scan(&webTables); err != nil || webTables != 3 {
		t.Fatalf("web tables missing after brand down count=%d err=%v", webTables, err)
	}
	if err = database.CheckWriteSchema(ctx, db); err == nil {
		t.Fatal("CheckWriteSchema accepted version 3")
	}
	if err = database.CheckReadSchema(ctx, db); err != nil {
		t.Fatal("CheckReadSchema rejected version 3")
	}
	if err = target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var migratedName string
	var migratedEnglish, migratedSlogan sql.NullString
	if err = db.QueryRowContext(ctx, "SELECT site_name, english_name, slogan FROM site_settings WHERE id = 1").Scan(&migratedName, &migratedEnglish, &migratedSlogan); err != nil {
		t.Fatal(err)
	}
	if migratedName != "Onword\u200b" || migratedEnglish.Valid || migratedSlogan.Valid {
		t.Fatalf("3 to 4 migration changed settings name=%q english=%+v slogan=%+v", migratedName, migratedEnglish, migratedSlogan)
	}
	if err = target.MigrateDown(int(database.CurrentSchemaVersion) - 3); err != nil {
		t.Fatal(err)
	}
	if err = target.MigrateDown(1); err != nil {
		t.Fatal(err)
	}
	if v, dirty, e := database.SchemaVersion(ctx, db); e != nil || dirty || v != 2 {
		t.Fatalf("after settings down version=%d dirty=%t err=%v", v, dirty, e)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = 'site_settings'").Scan(&settingsTables); err != nil || settingsTables != 0 {
		t.Fatalf("site_settings remain after down count=%d err=%v", settingsTables, err)
	}
	if err = target.MigrateDown(1); err != nil {
		t.Fatal(err)
	}
	if v, dirty, e := database.SchemaVersion(ctx, db); e != nil || dirty || v != 1 {
		t.Fatalf("after down version=%d dirty=%t err=%v", v, dirty, e)
	}
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('admin_users','submissions','admin_sessions')").Scan(&webTables); err != nil || webTables != 0 {
		t.Fatalf("web tables remain after down count=%d err=%v", webTables, err)
	}
	if err = database.CheckWriteSchema(ctx, db); err == nil {
		t.Fatal("CheckWriteSchema accepted version 1")
	}
	if err = database.CheckReadSchema(ctx, db); err != nil {
		t.Fatal("CheckReadSchema rejected version 1")
	}
	if err = target.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	if err = database.CheckWriteSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if v, dirty, e := database.SchemaVersion(ctx, db); e != nil || dirty || v != database.CurrentSchemaVersion {
		t.Fatalf("after re-up version=%d dirty=%t err=%v", v, dirty, e)
	}
	input := `{"categories":[{"code":"original","name":"原创","sort_order":1},{"code":"empty","name":"空分类"}],"sentences":[{"uuid":"75A45FD4-4F2F-45EB-80CB-6F0A7BCDFAF2","category":"original","content":"今天也要认真写代码。","source":"项目自编示例","author":null}]}`
	sum, err := importer.Import(ctx, db, strings.NewReader(input), false)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Changed || sum.NewCategories != 2 || sum.NewSentences != 1 || sum.DatasetVersionAfter == nil {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	snap, err := database.LoadSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SentenceCount != 1 || len(snap.Categories) != 2 || snap.Version != 2 {
		t.Fatalf("unexpected snapshot: version=%d sentences=%d categories=%d", snap.Version, snap.SentenceCount, len(snap.Categories))
	}
	dry, err := importer.Import(ctx, db, strings.NewReader(input), true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Changed || dry.SkippedCount != 1 || dry.DatasetVersionAfter != nil {
		t.Fatalf("unexpected dry run: %+v", dry)
	}
	again, err := importer.Import(ctx, db, strings.NewReader(input), false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed || again.SkippedCount != 1 || again.DatasetVersionAfter == nil || *again.DatasetVersionAfter != "2" {
		t.Fatalf("unexpected idempotent import: %+v", again)
	}
	longContent := strings.Repeat("界", 1001)
	longRaw, _ := json.Marshal(map[string]any{"sentences": []map[string]any{{"uuid": "de305d54-75b4-431b-adb2-eb6b9e546014", "category": "original", "content": longContent}}})
	if _, err = importer.Import(ctx, db, strings.NewReader(string(longRaw)), false); err != nil {
		t.Fatal(err)
	}
	snap, err = database.LoadSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := snap.LookupUUID("de305d54-75b4-431b-adb2-eb6b9e546014"); !ok || got.Length != 1001 {
		t.Fatal("sentence longer than random-query range was not loaded for UUID lookup")
	}
	if _, err = db.ExecContext(ctx, "UPDATE categories SET enabled=FALSE WHERE code='original'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE dataset_versions SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if empty, loadErr := database.LoadSnapshot(ctx, db); loadErr != nil || empty.SentenceCount != 0 {
		t.Fatalf("disabled category must produce valid empty snapshot: %v", loadErr)
	}
	if _, err = db.ExecContext(ctx, "UPDATE categories SET enabled=TRUE WHERE code='original'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE dataset_versions SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.LoadSnapshot(ctx, db); err != nil {
		t.Fatal("snapshot did not recover after category repair")
	}
	if _, err = db.ExecContext(ctx, "DELETE FROM dataset_versions WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.LoadSnapshot(ctx, db); err == nil {
		t.Fatal("expected missing version failure")
	}
	if _, err = db.ExecContext(ctx, "INSERT INTO dataset_versions(id,version) VALUES(1,10)"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.LoadSnapshot(ctx, db); err != nil {
		t.Fatal("snapshot did not recover after restoring version")
	}
	mgr := snapshot.NewManager(database.Loader{DB: db}, 5*time.Second)
	if err = mgr.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(httpapi.Options{Snapshots: mgr, Metrics: observability.NewMetrics()})
	for i, path := range []string{"/api/v1?categories=original&min_length=1&max_length=30", "/api/v1/sentences/de305d54-75b4-431b-adb2-eb6b9e546014", "/api/v1/categories"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("HTTP integration %s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err = json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["meta"] == nil {
			t.Fatalf("HTTP integration %s missing valid meta", path)
		}
		if i < 2 {
			data, ok := body["data"].(map[string]any)
			if !ok {
				t.Fatalf("HTTP integration %s invalid sentence data", path)
			}
			for _, key := range []string{"uuid", "content", "category", "source", "author", "length"} {
				if _, ok = data[key]; !ok {
					t.Fatalf("HTTP integration %s missing %s", path, key)
				}
			}
			for _, legacy := range []string{"id", "hitokoto", "type", "from", "from_who"} {
				if _, ok = data[legacy]; ok {
					t.Fatalf("HTTP integration %s exposed legacy field %s", path, legacy)
				}
			}
		}
	}
	old := mgr.Current()
	if _, err = db.ExecContext(ctx, "UPDATE sentences SET length=length+1 WHERE uuid='de305d54-75b4-431b-adb2-eb6b9e546014'"); err != nil {
		t.Fatal(err)
	}
	if _, err = mgr.Refresh(ctx, true); err == nil {
		t.Fatal("expected refresh validation failure")
	}
	if mgr.Current() != old {
		t.Fatal("failed refresh replaced the previous snapshot")
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/sentences/de305d54-75b4-431b-adb2-eb6b9e546014", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("old snapshot unavailable over HTTP during failed refresh: %d", rr.Code)
	}
	if _, err = db.ExecContext(ctx, "UPDATE sentences SET length=1001 WHERE uuid='de305d54-75b4-431b-adb2-eb6b9e546014'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE dataset_versions SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if changed, e := mgr.Refresh(ctx, false); e != nil || !changed || mgr.Current() == old {
		t.Fatalf("refresh did not recover changed=%t err=%v", changed, e)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before uint64
	if err = tx.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id=1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sentences SET source='并发发布' WHERE uuid='de305d54-75b4-431b-adb2-eb6b9e546014'"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE dataset_versions SET version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	loaded := make(chan *snapshot.Snapshot, 1)
	loadErr := make(chan error, 1)
	go func() {
		s, e := database.LoadSnapshot(ctx, db)
		if e != nil {
			loadErr <- e
			return
		}
		loaded <- s
	}()
	time.Sleep(20 * time.Millisecond)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-loadErr:
		t.Fatal(e)
	case s := <-loaded:
		got, _ := s.LookupUUID("de305d54-75b4-431b-adb2-eb6b9e546014")
		if !((s.Version == before && got.Source == "") || (s.Version == before+1 && got.Source == "并发发布")) {
			t.Fatalf("incoherent concurrent snapshot version=%d source=%q", s.Version, got.Source)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err = db.ExecContext(ctx, "CREATE TRIGGER fail_batch BEFORE INSERT ON sentences WHEN NEW.uuid='00000000-0000-0000-0000-000000000250' BEGIN SELECT RAISE(ABORT, 'test failure'); END"); err != nil {
		t.Fatal(err)
	}
	many := importer.Dataset{Categories: []importer.Category{{Code: "batchnew", Name: "批量回滚"}}, InputCount: 251}
	for i := 0; i < 251; i++ {
		many.Sentences = append(many.Sentences, importer.Sentence{UUID: fmt.Sprintf("00000000-0000-0000-0000-%012d", i), Category: "batchnew", Content: "batch", Length: 5})
	}
	if _, err = importer.Run(ctx, db, many, false); err == nil {
		t.Fatal("expected second-batch failure")
	}
	var batchCount int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences WHERE content='batch'").Scan(&batchCount); err != nil || batchCount != 0 {
		t.Fatalf("batch rollback failed count=%d err=%v", batchCount, err)
	}
	var categoryCount int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories WHERE code='batchnew'").Scan(&categoryCount); err != nil || categoryCount != 0 {
		t.Fatalf("new category was not rolled back count=%d err=%v", categoryCount, err)
	}
	var afterFailure uint64
	if err = db.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id=1").Scan(&afterFailure); err != nil || afterFailure != before+1 {
		t.Fatalf("version changed on rollback before=%d after=%d err=%v", before, afterFailure, err)
	}
	_, _ = db.ExecContext(ctx, "DROP TRIGGER fail_batch")
	if _, err = db.ExecContext(ctx, "UPDATE sentences SET content='broken' WHERE uuid='75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2'"); err != nil {
		t.Fatal(err)
	}
	if _, err = importer.Import(ctx, db, strings.NewReader(input), false); err == nil {
		t.Fatal("expected conflict")
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sentences").Scan(&count); err != nil || count != 2 {
		t.Fatalf("transaction changed rows: count=%d err=%v", count, err)
	}
}

var _ snapshot.Loader = database.Loader{}
