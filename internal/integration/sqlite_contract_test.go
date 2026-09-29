package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentence-api/internal/database"
	"sentence-api/internal/httpapi"
	"sentence-api/internal/importer"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
	"sentence-api/internal/web/store"
)

func openSQLiteContract(t *testing.T) (*sql.DB, *store.Store, context.Context, string) {
	t.Helper()
	ctx := context.Background()
	raw := filepath.Join(t.TempDir(), "quotewisp.db")
	if err := (database.Target{SQLitePath: raw}).MigrateUp(); err != nil {
		t.Fatal(err)
	}
	db, err := database.OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, store.New(db), ctx, raw
}

func runSQLiteContract(t *testing.T, fn func(*testing.T, *sql.DB, *store.Store, context.Context, string)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		db, st, ctx, raw := openSQLiteContract(t)
		fn(t, db, st, ctx, raw)
	})

}

func TestSQLiteCategoryAndSentenceCRUDContracts(t *testing.T) {
	runSQLiteContract(t, testCategoryAndSentenceCRUDContracts)
}

func testCategoryAndSentenceCRUDContracts(t *testing.T, _ *sql.DB, st *store.Store, ctx context.Context, _ string) {
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateCategory(ctx, "original", "重复", 2); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate category error=%v", err)
	}
	uuid, err := st.CreateSentence(ctx, store.SentenceFields{Content: "literal % marker", CategoryCode: "original", Source: "出处"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSentence(ctx, store.SentenceFields{Content: "literal % marker", CategoryCode: "original", Source: "出处"}); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate sentence error=%v", err)
	}
	if _, err = st.CreateSentence(ctx, store.SentenceFields{Content: "ordinary marker", CategoryCode: "original", Source: "出处"}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSentence(ctx, store.SentenceFields{Content: `literal _ slash \ marker`, CategoryCode: "original", Source: "出处"}); err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateSentence(ctx, uuid, store.SentenceFields{Content: "literal % revised", CategoryCode: "original", Author: "作者"}); err != nil {
		t.Fatal(err)
	}
	items, total, err := st.ListSentences(ctx, store.SentenceFilter{Query: "%", Page: 1, Size: 20})
	if err != nil || total != 1 || len(items) != 1 || items[0].Content != "literal % revised" {
		t.Fatalf("escaped LIKE query total=%d items=%+v err=%v", total, items, err)
	}
	for _, query := range []string{"_", `\`} {
		items, total, err = st.ListSentences(ctx, store.SentenceFilter{Query: query, Page: 1, Size: 20})
		if err != nil || total != 1 || len(items) != 1 || items[0].Content != `literal _ slash \ marker` {
			t.Fatalf("escaped LIKE query=%q total=%d items=%+v err=%v", query, total, items, err)
		}
	}
	if err = st.UpdateCategory(ctx, "original", "原创更新", 3); err != nil {
		t.Fatal(err)
	}
	cat, err := st.GetCategory(ctx, "original")
	if err != nil || cat.Name != "原创更新" || cat.SortOrder != 3 || cat.PublishedCount != 3 {
		t.Fatalf("category=%+v err=%v", cat, err)
	}
	if err = st.SetCategoryEnabled(ctx, "original", false, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong disable confirmation error=%v", err)
	}
	if err = st.SetCategoryEnabled(ctx, "original", false, 3); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSentence(ctx, store.SentenceFields{Content: "disabled", CategoryCode: "original", Source: "出处"}); !errors.Is(err, store.ErrCategoryDisabled) {
		t.Fatalf("disabled category create error=%v", err)
	}
}

func TestSQLiteSubmissionReviewDuplicateAndSettingsContracts(t *testing.T) {
	runSQLiteContract(t, testSubmissionReviewDuplicateAndSettingsContracts)
}

func testSubmissionReviewDuplicateAndSettingsContracts(t *testing.T, _ *sql.DB, st *store.Store, ctx context.Context, _ string) {
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	adminID, err := st.CreateAdmin(ctx, "reviewer", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	n := store.NewSubmission{Content: "待审内容", CategoryCode: "original", Source: "出处", Contact: "a@example.com", ClientIP: netip.MustParseAddr("203.0.113.10")}
	id, err := st.CreateSubmission(ctx, n, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateSubmission(ctx, n, 10); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("pending duplicate error=%v", err)
	}
	if err = st.RejectSubmission(ctx, id, adminID, "不采用"); err != nil {
		t.Fatal(err)
	}
	if err = st.RejectSubmission(ctx, id, adminID, "再次拒绝"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reviewed duplicate action error=%v", err)
	}
	approved, err := st.CreateSubmission(ctx, store.NewSubmission{Content: "通过内容", CategoryCode: "original", Author: "作者", Contact: "b@example.com", ClientIP: netip.MustParseAddr("203.0.113.11")}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ApproveSubmission(ctx, approved, adminID, nil); err != nil {
		t.Fatal(err)
	}
	dup, err := st.CreateSubmission(ctx, store.NewSubmission{Content: "通过内容", CategoryCode: "original", Source: "出处"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ApproveSubmission(ctx, dup, adminID, nil); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("published duplicate review error=%v", err)
	}
	if err = st.UpsertSettings(ctx, store.SiteSettings{Name: "拾句", EnglishName: "Quotewisp", Contact: "ops@example.com", PublicOrigin: "https://example.com/"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSettings(ctx)
	if err != nil || got.Name != "拾句" || got.PublicOrigin != "https://example.com" || got.EnglishName != "Quotewisp" {
		t.Fatalf("settings=%+v err=%v", got, err)
	}
}

func TestSQLiteAdminSessionAuthRetentionAndLastAdminContracts(t *testing.T) {
	runSQLiteContract(t, testAdminSessionAuthRetentionAndLastAdminContracts)
}

func testAdminSessionAuthRetentionAndLastAdminContracts(t *testing.T, _ *sql.DB, st *store.Store, ctx context.Context, raw string) {
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateAdmin(ctx, "adminone", "oldhash", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateAdmin(ctx, "admintwo", "hash2", &first)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var token, csrf [32]byte
	token[0], csrf[0] = 1, 2
	sess := store.Session{TokenHash: token, AdminID: first, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = st.CreateLoginSession(ctx, sess, "oldhash"); err != nil {
		t.Fatal(err)
	}
	if err = st.ResetAdminPassword(ctx, first, "newhash", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetSession(ctx, token, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("password reset left session: %v", err)
	}
	var stale [32]byte
	stale[0] = 3
	if err = st.CreateLoginSession(ctx, store.Session{TokenHash: stale, AdminID: first, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, "oldhash"); !errors.Is(err, store.ErrStaleAuth) {
		t.Fatalf("stale login error=%v", err)
	}
	if err = st.SetAdminEnabled(ctx, first, false); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetAdminByID(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAdminEnabled(ctx, second, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("last admin error=%v", err)
	}

	// Two simultaneous disable attempts must leave exactly one enabled admin.
	third, err := st.CreateAdmin(ctx, "adminthree", "hash3", nil)
	if err != nil {
		t.Fatal(err)
	}
	var concurrentStore = st
	var secondDB *sql.DB
	if raw != "" {
		var err error
		secondDB, err = database.OpenSQLite(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = secondDB.Close() })
		concurrentStore = store.New(secondDB)
	}
	var wg sync.WaitGroup
	var e1, e2 error
	concurrentCtx, cancelConcurrent := context.WithTimeout(ctx, 3*time.Second)
	defer cancelConcurrent()
	wg.Add(2)
	go func() { defer wg.Done(); e1 = st.SetAdminEnabled(concurrentCtx, second, false) }()
	go func() { defer wg.Done(); e2 = concurrentStore.SetAdminEnabled(concurrentCtx, third, false) }()
	wg.Wait()
	last := 0
	for _, e := range []error{e1, e2} {
		if errors.Is(e, store.ErrLastAdmin) {
			last++
		} else if e != nil {
			t.Fatalf("concurrent disable error=%v", e)
		}
	}
	if last != 1 {
		t.Fatalf("concurrent last-admin errors=%d e1=%v e2=%v", last, e1, e2)
	}

	rejected, err := st.CreateSubmission(ctx, store.NewSubmission{Content: "待清理拒绝", CategoryCode: "original", Contact: "r@example.com", ClientIP: netip.MustParseAddr("203.0.113.12")}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RejectSubmission(ctx, rejected, first, "清理"); err != nil {
		t.Fatal(err)
	}
	approved, err := st.CreateSubmission(ctx, store.NewSubmission{Content: "待清理通过", CategoryCode: "original", Contact: "p@example.com", ClientIP: netip.MustParseAddr("203.0.113.13")}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ApproveSubmission(ctx, approved, first, nil); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if _, err = st.DB.ExecContext(ctx, "UPDATE submissions SET reviewed_at = ? WHERE id IN (?, ?)", old, rejected, approved); err != nil {
		t.Fatal(err)
	}
	deleted, redacted, err := st.RunRetention(ctx, time.Now().UTC().Add(-24*time.Hour), 10)
	if err != nil || deleted != 1 || redacted != 1 {
		t.Fatalf("retention deleted=%d redacted=%d err=%v", deleted, redacted, err)
	}
	got, err := st.GetSubmission(ctx, approved)
	if err != nil || got.Contact != "" || got.ClientIP.IsValid() {
		t.Fatalf("approved submission not redacted: %+v err=%v", got, err)
	}
}

func TestSQLiteExactDuplicateBytewiseContracts(t *testing.T) {
	runSQLiteContract(t, testExactDuplicateBytewiseContracts)
}

func testExactDuplicateBytewiseContracts(t *testing.T, _ *sql.DB, st *store.Store, ctx context.Context, _ string) {
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"Case Sensitive", "case sensitive", "Case Sensitive "} {
		if _, err := st.CreateSentence(ctx, store.SentenceFields{Content: content, CategoryCode: "original", Source: "出处"}); err != nil {
			t.Fatalf("content %q rejected as duplicate: %v", content, err)
		}
	}
}

func TestSQLiteDuplicateAdminUsernameContract(t *testing.T) {
	runSQLiteContract(t, testDuplicateAdminUsernameContract)
}

func testDuplicateAdminUsernameContract(t *testing.T, _ *sql.DB, st *store.Store, ctx context.Context, _ string) {
	if _, err := st.CreateAdmin(ctx, "duplicate", "hash", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAdmin(ctx, "duplicate", "other", nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate admin error=%v", err)
	}
}

func TestSQLiteEmptyPublicContracts(t *testing.T) {
	runSQLiteContract(t, testEmptyPublicContracts)
}

func testEmptyPublicContracts(t *testing.T, db *sql.DB, st *store.Store, ctx context.Context, _ string) {
	pub, err := st.BuildPublicData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Categories == nil || pub.Recent == nil {
		t.Fatalf("empty public slices must be non-nil: categories=%#v recent=%#v", pub.Categories, pub.Recent)
	}
	var exported struct {
		Categories []json.RawMessage `json:"categories"`
		Sentences  []json.RawMessage `json:"sentences"`
	}
	if err := json.Unmarshal(pub.ExportJSON, &exported); err != nil {
		t.Fatalf("empty export JSON: %v", err)
	}
	if exported.Categories == nil || exported.Sentences == nil || len(exported.Categories) != 0 || len(exported.Sentences) != 0 {
		t.Fatalf("empty export arrays: categories=%v sentences=%v", exported.Categories, exported.Sentences)
	}
	manager := snapshot.NewManager(database.Loader{DB: db}, time.Second)
	if err := manager.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(httpapi.Options{Snapshots: manager, Metrics: observability.NewMetrics()})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1?min_length=1&max_length=30", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("empty random status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("empty readiness status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSQLiteImportAtomicityAndVersion(t *testing.T) {
	runSQLiteContract(t, func(t *testing.T, db *sql.DB, st *store.Store, ctx context.Context, _ string) {
		const original = `{"categories":[{"code":"original","name":"原创"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"original","content":"导入内容"}]}`
		preview, err := importer.Import(ctx, db, strings.NewReader(original), true)
		if err != nil || preview.NewSentences != 1 {
			t.Fatalf("preview: %+v %v", preview, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM categories").Scan(&count); err != nil || count != 0 {
			t.Fatalf("preview wrote data: %d %v", count, err)
		}
		first, err := importer.Import(ctx, db, strings.NewReader(original), false)
		if err != nil || !first.Changed || first.NewSentences != 1 {
			t.Fatalf("import: %+v %v", first, err)
		}
		version, err := st.DatasetVersion(ctx)
		if err != nil {
			t.Fatal(err)
		}
		duplicate, err := importer.Import(ctx, db, strings.NewReader(original), false)
		if err != nil || duplicate.Changed || duplicate.SkippedCount != 1 {
			t.Fatalf("repeat: %+v %v", duplicate, err)
		}
		const conflict = `{"categories":[{"code":"rollback","name":"不能保留"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"original","content":"冲突内容"}]}`
		if _, err := importer.Import(ctx, db, strings.NewReader(conflict), false); err == nil {
			t.Fatal("accepted UUID conflict")
		}
		if _, err := st.GetCategory(ctx, "rollback"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("conflicting import left category: %v", err)
		}
		if after, err := st.DatasetVersion(ctx); err != nil || after != version {
			t.Fatalf("no-op/conflict changed version: %d %v", after, err)
		}
	})
}

func TestInitialAdminConcurrentOnceContract(t *testing.T) {
	runSQLiteContract(t, func(t *testing.T, db *sql.DB, st *store.Store, ctx context.Context, raw string) {
		var before uint64
		if err := db.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id=1").Scan(&before); err != nil {
			t.Fatal(err)
		}
		// Separate SQLite handles model separate CLI processes, not a Go mutex.
		other := st
		if raw != "" {
			second, err := database.OpenSQLite(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			other = store.New(second)
		}
		start := make(chan struct{})
		results := make(chan error, 8)
		for i := 0; i < 8; i++ {
			go func(i int) {
				<-start
				current := st
				if i%2 == 1 {
					current = other
				}
				_, err := current.CreateInitialAdmin(ctx, fmt.Sprintf("first%d", i), "initialhash")
				results <- err
			}(i)
		}
		close(start)
		successes := 0
		for i := 0; i < 8; i++ {
			err := <-results
			if err == nil {
				successes++
			} else if !errors.Is(err, store.ErrAlreadyInitialized) {
				t.Fatalf("unexpected bootstrap error: %v", err)
			}
		}
		if successes != 1 {
			t.Fatalf("successful initializations=%d", successes)
		}
		users, err := st.ListAdmins(ctx)
		if err != nil || len(users) != 1 {
			t.Fatalf("admins=%d error=%v", len(users), err)
		}
		if users[0].PasswordHash != "initialhash" || !users[0].Enabled || users[0].CreatedBy != nil {
			t.Fatal("unexpected initial administrator")
		}
		// Normal authenticated administration remains available after bootstrap.
		if _, err = st.CreateAdmin(ctx, "secondadmin", "secondhash", &users[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err = st.CreateInitialAdmin(ctx, "thirdadmin", "replacement"); !errors.Is(err, store.ErrAlreadyInitialized) {
			t.Fatalf("repeat=%v", err)
		}
		// Disabled accounts still mean the installation was initialized.
		if _, err = db.ExecContext(ctx, "UPDATE admin_users SET enabled=FALSE"); err != nil {
			t.Fatal(err)
		}
		if _, err = st.CreateInitialAdmin(ctx, "recovery", "replacement"); !errors.Is(err, store.ErrAlreadyInitialized) {
			t.Fatalf("disabled bypass=%v", err)
		}
		var after uint64
		if err = db.QueryRowContext(ctx, "SELECT version FROM dataset_versions WHERE id=1").Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("bootstrap changed dataset version: %d -> %d", before, after)
		}
	})
}

func TestPasswordResetAndConcurrentLoginContract(t *testing.T) {
	runSQLiteContract(t, func(t *testing.T, _ *sql.DB, st *store.Store, ctx context.Context, raw string) {
		other := st
		if raw != "" {
			db, err := database.OpenSQLite(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			other = store.New(db)
		}
		id, err := st.CreateInitialAdmin(ctx, "passwordrace", "oldhash")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		var token, csrf [32]byte
		token[0] = 5
		csrf[0] = 9
		start := make(chan struct{})
		var wg sync.WaitGroup
		var loginErr, resetErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			loginErr = st.CreateLoginSession(ctx, store.Session{TokenHash: token, AdminID: id, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, "oldhash")
		}()
		go func() { defer wg.Done(); <-start; resetErr = other.ResetAdminPassword(ctx, id, "newhash", nil) }()
		close(start)
		wg.Wait()
		if resetErr != nil || (loginErr != nil && !errors.Is(loginErr, store.ErrStaleAuth)) {
			t.Fatalf("login=%v reset=%v", loginErr, resetErr)
		}
		if _, err = st.GetSession(ctx, token, now); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old credentials left a valid session: %v", err)
		}
		if err = st.ChangeAdminPassword(ctx, id, "oldhash", "stalechange", nil); !errors.Is(err, store.ErrStaleAuth) {
			t.Fatalf("stale change=%v", err)
		}
		admin, err := st.GetAdminByID(ctx, id)
		if err != nil || admin.PasswordHash != "newhash" {
			t.Fatalf("reset overwritten: %v", err)
		}
	})
}

func TestImportIndexedLookupPreservesCorruptUUIDConflict(t *testing.T) {
	runSQLiteContract(t, func(t *testing.T, db *sql.DB, _ *store.Store, ctx context.Context, _ string) {
		canonical := "abcdef00-0000-4000-8000-000000000001"
		source := `{"categories":[{"code":"lookup","name":"查重","sort_order":0}],"sentences":[{"uuid":"` + canonical + `","category":"lookup","content":"查重测试"}]}`
		if _, err := importer.Import(ctx, db, strings.NewReader(source), false); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{strings.ToUpper(canonical), strings.ReplaceAll(canonical, "-", ""), "abcdef0-00000-4000-8000-000000000001"} {
			if _, err := db.ExecContext(ctx, "UPDATE sentences SET uuid=? WHERE uuid=?", raw, canonical); err != nil {
				t.Fatal(err)
			}
			for _, dry := range []bool{true, false} {
				_, err := importer.Import(ctx, db, strings.NewReader(source), dry)
				var detail *importer.Error
				if !errors.As(err, &detail) || detail.Category != "conflict" {
					t.Fatalf("raw=%q dry=%v error=%v", raw, dry, err)
				}
			}
			unrelated := strings.ReplaceAll(source, canonical, "abcdef00-0000-4000-8000-000000000002")
			if _, err := importer.Import(ctx, db, strings.NewReader(unrelated), true); err != nil {
				t.Fatalf("unrelated corrupt UUID blocks import: %v", err)
			}
			if _, err := db.ExecContext(ctx, "UPDATE sentences SET uuid=? WHERE uuid=?", canonical, raw); err != nil {
				t.Fatal(err)
			}
		}
		sum, err := importer.Import(ctx, db, strings.NewReader(source), false)
		if err != nil || sum.SkippedCount != 1 || sum.Changed {
			t.Fatalf("exact duplicate=%+v error=%v", sum, err)
		}
	})
}
