package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sentence-api/internal/importer"
	"sentence-api/internal/testdb"
	"sentence-api/internal/web/store"
)

func seedCategory(t *testing.T, st *store.Store, ctx context.Context) {
	t.Helper()
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
}

func TestValidationHelpers(t *testing.T) {
	if err := store.ValidateCategoryCode("ab"); err != nil {
		t.Fatalf("valid code rejected: %v", err)
	}
	if err := store.ValidateCategoryCode("Bad"); err == nil {
		t.Fatal("invalid code accepted")
	}
	if err := store.ValidateCategoryName("原创"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if err := store.ValidateCategoryName("   "); err == nil {
		t.Fatal("whitespace name accepted")
	}
	if err := store.ValidateSentenceFields("hello", "src", "auth", 1000); err != nil {
		t.Fatalf("valid sentence rejected: %v", err)
	}
	if err := store.ValidateSentenceFields(strings.Repeat("界", 1001), "src", "auth", 1000); err == nil {
		t.Fatal("content over max runes accepted")
	}
	if err := store.ValidateSentenceFields("hello", "src", "auth", 0); err != nil {
		t.Fatalf("zero max runes should allow long content within DB limits: %v", err)
	}
	if err := store.ValidateWebSentenceFields("hello", "src", "auth", 0); err != nil {
		t.Fatalf("valid web sentence rejected: %v", err)
	}
	if err := store.ValidateWebSentenceFields("hello", "", "", 0); err == nil {
		t.Fatal("web sentence without source or author accepted")
	}
	if store.RuneLength("你好") != 2 {
		t.Fatalf("unexpected rune length: %d", store.RuneLength("你好"))
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "句子 API", Contact: "a@b.c"}); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "   ", Contact: "a@b.c"}); err == nil {
		t.Fatal("blank site name accepted")
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "句子 API", Contact: "a@b.c", PublicOrigin: "example.com"}); err == nil {
		t.Fatal("origin without scheme accepted")
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "句子 API", Contact: "a@b.c", PublicOrigin: "http://:80"}); err == nil {
		t.Fatal("origin with empty host accepted")
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "句子 API", Contact: "a@b.c", PublicOrigin: "https://example.com#"}); err == nil {
		t.Fatal("origin with fragment accepted")
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "句子 API", Contact: "a@b.c", PublicOrigin: "https://example.com?"}); err == nil {
		t.Fatal("origin with empty query accepted")
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{Name: "句子 API", Contact: "a@b.c", BeianURL: "https://beian.miit.gov.cn/"}); err == nil {
		t.Fatal("beian url without text accepted")
	}
	if err := store.ValidateSiteSettings(store.SiteSettings{
		Name: "句子 API", Contact: "a@b.c", BeianText: "京ICP备1号",
		BeianURL: "https://www.beian.gov.cn/portal/registerSystemInfo?recordcode=1",
	}); err != nil {
		t.Fatalf("beian url with query rejected: %v", err)
	}
	for _, tc := range []struct {
		field string
		set   func(*store.SiteSettings)
	}{
		{field: "english_name", set: func(s *store.SiteSettings) { s.EnglishName = strings.Repeat("a", 65) }},
		{field: "english_name", set: func(s *store.SiteSettings) { s.EnglishName = "bad\x00name" }},
		{field: "slogan", set: func(s *store.SiteSettings) { s.Slogan = strings.Repeat("句", 129) }},
		{field: "slogan", set: func(s *store.SiteSettings) { s.Slogan = "bad\u0085text" }},
	} {
		in := store.SiteSettings{Name: "拾句", Contact: "a@b.c"}
		tc.set(&in)
		var ve *store.ValidationError
		if err := store.ValidateSiteSettings(in); !errors.As(err, &ve) || ve.Field != tc.field {
			t.Fatalf("field=%s err=%v", tc.field, err)
		}
	}
	normalized := store.NormalizeSiteSettings(store.SiteSettings{Name: " 拾句 ", EnglishName: " Quotewisp ", Slogan: " 偶遇一句话 ", Contact: " a@b.c "})
	if normalized.Name != "拾句" || normalized.EnglishName != "Quotewisp" || normalized.Slogan != "偶遇一句话" || normalized.Contact != "a@b.c" {
		t.Fatalf("normalized=%+v", normalized)
	}
}

func TestCreateSubmissionDuplicateQueueFull(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	ip := netip.MustParseAddr("203.0.113.1")
	sub := store.NewSubmission{
		Content: "待审内容", CategoryCode: "original", Source: "出处", Author: "作者",
		Nickname: "昵称", Contact: "test@example.com", ClientIP: ip,
	}
	id, err := st.CreateSubmission(ctx, sub, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected submission id")
	}
	if _, err = st.CreateSubmission(ctx, sub, 1000); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("expected duplicate pending, got %v", err)
	}
	for i := 0; i < 999; i++ {
		s := sub
		s.Content = fmt.Sprintf("other-%d", i)
		if _, err = st.CreateSubmission(ctx, s, 1000); err != nil {
			t.Fatalf("fill queue: %v", err)
		}
	}
	if _, err = st.CreateSubmission(ctx, store.NewSubmission{
		Content: "overflow", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: ip,
	}, 1000); !errors.Is(err, store.ErrQueueFull) {
		t.Fatalf("expected queue full, got %v", err)
	}
}

func TestApproveRejectAndSentences(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	adminID, err := st.CreateAdmin(ctx, "admin1", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("203.0.113.2")
	subID, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "审核通过测试", CategoryCode: "original", Source: "出处", Author: "作者",
		Contact: "c@example.com", ClientIP: ip,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.DatasetVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	uuid, err := st.ApproveSubmission(ctx, subID, adminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(uuid) != 36 || uuid != strings.ToLower(uuid) {
		t.Fatalf("non-canonical uuid: %q", uuid)
	}
	after, err := st.DatasetVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Fatalf("version bump=%d want 1", after-before)
	}
	sent, err := st.GetSentence(ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Length != store.RuneLength("审核通过测试") {
		t.Fatalf("length=%d", sent.Length)
	}
	if _, err = st.ApproveSubmission(ctx, subID, adminID, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second approve: %v", err)
	}

	dupSub, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "审核通过测试", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: ip,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	vBeforeDup, _ := st.DatasetVersion(ctx)
	if _, err = st.ApproveSubmission(ctx, dupSub, adminID, nil); !errors.Is(err, store.ErrDuplicate) {
		t.Fatalf("duplicate approve: %v", err)
	}
	vAfterDup, _ := st.DatasetVersion(ctx)
	if vAfterDup != vBeforeDup {
		t.Fatal("duplicate approve changed version")
	}

	editSub, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "原始内容", CategoryCode: "original", Source: "旧出处", Author: "旧作者",
		Contact: "c@example.com", ClientIP: ip,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	edit := &store.SentenceFields{Content: "修正内容", CategoryCode: "original", Source: "新出处", Author: "新作者"}
	uuid2, err := st.ApproveSubmission(ctx, editSub, adminID, edit)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSubmission(ctx, editSub)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != edit.Content || got.Source != edit.Source || got.Author != edit.Author {
		t.Fatalf("edited fields not persisted: %+v", got)
	}
	s2, err := st.GetSentence(ctx, uuid2)
	if err != nil || s2.Content != edit.Content {
		t.Fatalf("sentence edit mismatch: %+v err=%v", s2, err)
	}

	rejectSub, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "拒绝测试", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: ip,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	vRejectBefore, _ := st.DatasetVersion(ctx)
	if err = st.RejectSubmission(ctx, rejectSub, adminID, "不符合要求"); err != nil {
		t.Fatal(err)
	}
	vRejectAfter, _ := st.DatasetVersion(ctx)
	if vRejectAfter != vRejectBefore {
		t.Fatal("reject bumped version")
	}

	vUnchangedBefore, _ := st.DatasetVersion(ctx)
	if err = st.UpdateSentence(ctx, uuid, store.SentenceFields{
		Content: sent.Content, CategoryCode: sent.CategoryCode, Source: sent.Source, Author: sent.Author,
	}); !errors.Is(err, store.ErrUnchanged) {
		t.Fatalf("unchanged update: %v", err)
	}
	vUnchangedAfter, _ := st.DatasetVersion(ctx)
	if vUnchangedAfter != vUnchangedBefore {
		t.Fatal("unchanged update bumped version")
	}
	if err = st.UpdateSentence(ctx, uuid, store.SentenceFields{
		Content: "更新后的内容", CategoryCode: sent.CategoryCode, Source: sent.Source, Author: sent.Author,
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetSentence(ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Length != store.RuneLength("更新后的内容") {
		t.Fatalf("length not recomputed: %d", updated.Length)
	}
	if err = st.SetSentenceStatus(ctx, uuid, 3, 1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong from status: %v", err)
	}
}

func TestConcurrentDisableAdmin(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	id1, err := st.CreateAdmin(ctx, "admin1", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := st.CreateAdmin(ctx, "admin2", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	var okCount int32
	var lastAdminCount int32
	var wg sync.WaitGroup
	for _, id := range []uint64{id1, id2} {
		wg.Add(1)
		go func(adminID uint64) {
			defer wg.Done()
			if err := st.SetAdminEnabled(ctx, adminID, false); err == nil {
				atomic.AddInt32(&okCount, 1)
			} else if errors.Is(err, store.ErrLastAdmin) {
				atomic.AddInt32(&lastAdminCount, 1)
			} else {
				t.Errorf("disable admin %d: %v", adminID, err)
			}
		}(id)
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("concurrent disable successes=%d want 1", okCount)
	}
	if lastAdminCount != 1 {
		t.Fatalf("concurrent disable last-admin=%d want 1", lastAdminCount)
	}
	admins, err := st.ListAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enabled := 0
	for _, a := range admins {
		if a.Enabled {
			enabled++
		}
	}
	if enabled != 1 {
		t.Fatalf("enabled admins=%d want 1", enabled)
	}
}

func TestConcurrentCreateSubmissionDuplicate(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	ip := netip.MustParseAddr("203.0.113.10")
	sub := store.NewSubmission{
		Content: "并发重复投稿", CategoryCode: "original", Source: "出处", Author: "作者",
		Contact: "c@example.com", ClientIP: ip,
	}
	var okCount int32
	var dupCount int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.CreateSubmission(ctx, sub, 1000); err == nil {
				atomic.AddInt32(&okCount, 1)
			} else if errors.Is(err, store.ErrDuplicate) {
				atomic.AddInt32(&dupCount, 1)
			} else {
				t.Errorf("create submission: %v", err)
			}
		}()
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("concurrent duplicate submissions=%d want 1", okCount)
	}
	if dupCount != 7 {
		t.Fatalf("concurrent duplicate rejections=%d want 7", dupCount)
	}
}

func TestConcurrentCreateSubmissionQueueLimit(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	ip := netip.MustParseAddr("203.0.113.11")
	var okCount int32
	var fullCount int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sub := store.NewSubmission{
				Content: fmt.Sprintf("并发队列-%d", n), CategoryCode: "original", Source: "s", Author: "a",
				Contact: "c@example.com", ClientIP: ip,
			}
			if _, err := st.CreateSubmission(ctx, sub, 1); err == nil {
				atomic.AddInt32(&okCount, 1)
			} else if errors.Is(err, store.ErrQueueFull) {
				atomic.AddInt32(&fullCount, 1)
			} else {
				t.Errorf("create submission: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("concurrent queue submissions=%d want 1", okCount)
	}
	if fullCount != 7 {
		t.Fatalf("concurrent queue full=%d want 7", fullCount)
	}
}

func TestWebSentenceRequiresSourceOrAuthor(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	_, err := st.CreateSentence(ctx, store.SentenceFields{
		Content: "无出处作者", CategoryCode: "original",
	})
	if err == nil {
		t.Fatal("create sentence without source/author accepted")
	}
	var ve *store.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected validation error, got %v", err)
	}
	uuid, err := st.CreateSentence(ctx, store.SentenceFields{
		Content: "有出处", CategoryCode: "original", Source: "出处",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpdateSentence(ctx, uuid, store.SentenceFields{
		Content: "有出处", CategoryCode: "original",
	}); err == nil {
		t.Fatal("update sentence clearing source/author accepted")
	}
	if !errors.As(err, &ve) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestConcurrentApprove(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	adminID, err := st.CreateAdmin(ctx, "admin1", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	subID, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "并发审核", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: netip.MustParseAddr("203.0.113.3"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var okCount int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.ApproveSubmission(ctx, subID, adminID, nil); err == nil {
				atomic.AddInt32(&okCount, 1)
			}
		}()
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("concurrent approve successes=%d want 1", okCount)
	}
}

func TestCategoriesAndAdmins(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	input := `{"categories":[{"code":"original","name":"原创","sort_order":1}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"original","content":"句库语句","source":"s","author":"a"}]}`
	if _, err := importer.Import(ctx, sqlDB, strings.NewReader(input), false); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateCategory(ctx, "extra", "额外", 2); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCategory(ctx, "extra", "额外", 2); !errors.Is(err, store.ErrUnchanged) {
		t.Fatalf("unchanged category: %v", err)
	}
	before, _ := st.DatasetVersion(ctx)
	if err := st.UpdateCategory(ctx, "extra", "额外分类", 3); err != nil {
		t.Fatal(err)
	}
	after, _ := st.DatasetVersion(ctx)
	if after != before+1 {
		t.Fatal("category update did not bump version")
	}
	cat, err := st.GetCategory(ctx, "original")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetCategoryEnabled(ctx, "original", false, int64(cat.PublishedCount)+1); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong confirm count: %v", err)
	}
	vBefore, _ := st.DatasetVersion(ctx)
	if err = st.SetCategoryEnabled(ctx, "original", false, int64(cat.PublishedCount)); err != nil {
		t.Fatal(err)
	}
	vAfter, _ := st.DatasetVersion(ctx)
	if vAfter != vBefore+1 {
		t.Fatal("disable category did not bump version")
	}
	disabled, err := st.GetCategory(ctx, "original")
	if err != nil || disabled.Enabled {
		t.Fatalf("category not disabled: %+v err=%v", disabled, err)
	}

	if _, err = st.CreateAdmin(ctx, "onlyone", "hash", nil); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAdminEnabled(ctx, 1, false); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("last admin disable: %v", err)
	}
}

func TestSessionsAndPublicDataRetention(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seedCategory(t, st, ctx)
	adminID, err := st.CreateAdmin(ctx, "sessadmin", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := st.CreateAdmin(ctx, "other", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	var tokenHash, csrf [32]byte
	tokenHash[0] = 1
	csrf[0] = 2
	now := time.Now().UTC()
	if err = st.CreateSession(ctx, store.Session{
		TokenHash: tokenHash, AdminID: adminID, Username: "sessadmin", CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetSession(ctx, tokenHash, now); err != nil {
		t.Fatal(err)
	}
	expired := [32]byte{9}
	if err = st.CreateSession(ctx, store.Session{
		TokenHash: expired, AdminID: adminID, Username: "sessadmin", CSRFToken: csrf,
		CreatedAt: now.Add(-2 * time.Hour), LastSeenAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetSession(ctx, expired, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}

	subID, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "公开数据", CategoryCode: "original", Source: "s", Author: "a",
		Nickname: "公开昵称", Contact: "c@example.com", ClientIP: netip.MustParseAddr("203.0.113.4"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	uuid, err := st.ApproveSubmission(ctx, subID, adminID, nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := st.BuildPublicData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Version == 0 || len(pub.ExportJSON) == 0 {
		t.Fatal("empty public data")
	}
	type importCategory struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		SortOrder int32  `json:"sort_order"`
	}
	type importSentence struct {
		UUID     string  `json:"uuid"`
		Category string  `json:"category"`
		Content  string  `json:"content"`
		Source   *string `json:"source"`
		Author   *string `json:"author"`
	}
	type importDoc struct {
		Categories []importCategory `json:"categories"`
		Sentences  []importSentence `json:"sentences"`
	}
	dec := json.NewDecoder(strings.NewReader(string(pub.ExportJSON)))
	dec.DisallowUnknownFields()
	var doc importDoc
	if err = dec.Decode(&doc); err != nil {
		t.Fatalf("export json invalid: %v", err)
	}
	found := false
	for _, s := range doc.Sentences {
		if s.UUID == uuid {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("export missing approved sentence: %+v", doc.Sentences)
	}

	if err = st.SetAdminEnabled(ctx, adminID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetSession(ctx, tokenHash, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled admin session: %v", err)
	}
	_ = otherID

	old := time.Now().UTC().Add(-100 * 24 * time.Hour)
	pendingID, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "保留待审", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: netip.MustParseAddr("203.0.113.5"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	rejectID, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "保留拒绝", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "c@example.com", ClientIP: netip.MustParseAddr("203.0.113.6"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RejectSubmission(ctx, rejectID, otherID, "no"); err != nil {
		t.Fatal(err)
	}
	approvedID, err := st.CreateSubmission(ctx, store.NewSubmission{
		Content: "保留通过", CategoryCode: "original", Source: "s", Author: "a",
		Contact: "keep@example.com", ClientIP: netip.MustParseAddr("203.0.113.7"),
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ApproveSubmission(ctx, approvedID, otherID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = sqlDB.ExecContext(ctx, "UPDATE submissions SET reviewed_at = ? WHERE id IN (?, ?)", old, rejectID, approvedID); err != nil {
		t.Fatal(err)
	}
	deleted, redacted, err := st.RunRetention(ctx, time.Now().UTC().Add(-90*24*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 1 || redacted < 1 {
		t.Fatalf("retention counts deleted=%d redacted=%d", deleted, redacted)
	}
	if _, err = st.GetSubmission(ctx, pendingID); err != nil {
		t.Fatal("pending submission removed")
	}
	got, err := st.GetSubmission(ctx, approvedID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Contact != "" || got.ClientIP.IsValid() {
		t.Fatal("approved submission not redacted")
	}
}

func TestListPendingSingleConn(t *testing.T) {
	sqlDB := testdb.Open(t)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	st := store.New(sqlDB)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seedCategory(t, st, ctx)
	ip := netip.MustParseAddr("203.0.113.10")
	for i := 0; i < 2; i++ {
		sub := store.NewSubmission{
			Content: fmt.Sprintf("pending-%d", i), CategoryCode: "original", Source: "出处", Author: "作者",
			Contact: "a@b.c", ClientIP: ip,
		}
		if _, err := st.CreateSubmission(ctx, sub, 1000); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := st.ListSubmissions(ctx, store.SubmissionPending, 1, 50)
	if err != nil {
		t.Fatalf("list with MaxOpenConns=1: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("got total=%d n=%d", total, len(items))
	}
}

func TestResetAdminPasswordRevokesSessions(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	id, err := st.CreateAdmin(ctx, "resetme", "oldhash", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var keep, drop, csrf [32]byte
	keep[0], drop[0], csrf[0] = 1, 2, 3
	for _, h := range [][32]byte{keep, drop} {
		if err = st.CreateSession(ctx, store.Session{
			TokenHash: h, AdminID: id, Username: "resetme", CSRFToken: csrf,
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.ResetAdminPassword(ctx, id, "newhash", &keep); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetSession(ctx, keep, now); err != nil {
		t.Fatalf("kept session: %v", err)
	}
	if _, err = st.GetSession(ctx, drop, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("dropped session: %v", err)
	}
	if err = st.ResetAdminPassword(ctx, id, "newerhash", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetSession(ctx, keep, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected all sessions revoked")
	}
}

func TestCreateLoginSessionRejectsStaleHash(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	id, err := st.CreateAdmin(ctx, "stalehash", "oldhash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.ResetAdminPassword(ctx, id, "newhash", nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var token, csrf [32]byte
	token[0], csrf[0] = 4, 5
	err = st.CreateLoginSession(ctx, store.Session{
		TokenHash: token, AdminID: id, CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}, "oldhash")
	if !errors.Is(err, store.ErrStaleAuth) {
		t.Fatalf("stale hash: %v", err)
	}
	if _, err = st.GetSession(ctx, token, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale login must not leave a session")
	}

	if err = st.CreateLoginSession(ctx, store.Session{
		TokenHash: token, AdminID: id, CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}, "newhash"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAdminByID(ctx, id)
	if err != nil || got.LastLoginAt == nil {
		t.Fatalf("last login: %v %+v", err, got)
	}
}

func TestCreateLoginSessionLosesToPasswordReset(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	id, err := st.CreateAdmin(ctx, "racereset", "oldhash", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var token, csrf [32]byte
	token[0], csrf[0] = 6, 7
	start := make(chan struct{})
	var wg sync.WaitGroup
	var loginErr, resetErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		loginErr = st.CreateLoginSession(ctx, store.Session{
			TokenHash: token, AdminID: id, CSRFToken: csrf,
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
		}, "oldhash")
	}()
	go func() {
		defer wg.Done()
		<-start
		resetErr = st.ResetAdminPassword(ctx, id, "newhash", nil)
	}()
	close(start)
	wg.Wait()

	if resetErr != nil {
		t.Fatalf("reset must succeed: %v", resetErr)
	}
	if loginErr != nil && !errors.Is(loginErr, store.ErrStaleAuth) {
		t.Fatalf("login: %v", loginErr)
	}
	admin, err := st.GetAdminByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if admin.PasswordHash != "newhash" {
		t.Fatalf("hash=%q", admin.PasswordHash)
	}
	if _, err = st.GetSession(ctx, token, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("session created with old hash after reset")
	}
}

func TestChangeAdminPasswordRejectsStaleHash(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	id, err := st.CreateAdmin(ctx, "selfchange", "oldhash", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var keep, csrf [32]byte
	keep[0], csrf[0] = 8, 9
	if err = st.CreateSession(ctx, store.Session{
		TokenHash: keep, AdminID: id, Username: "selfchange", CSRFToken: csrf,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err = st.ResetAdminPassword(ctx, id, "adminreset", nil); err != nil {
		t.Fatal(err)
	}
	if err = st.ChangeAdminPassword(ctx, id, "oldhash", "selfnew", &keep); !errors.Is(err, store.ErrStaleAuth) {
		t.Fatalf("stale self-change: %v", err)
	}
	got, err := st.GetAdminByID(ctx, id)
	if err != nil || got.PasswordHash != "adminreset" {
		t.Fatalf("hash overwritten: %v %+v", err, got)
	}
}

func TestAdminOverviewCounts(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	if err := st.CreateCategory(ctx, "original", "原创", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSentence(ctx, store.SentenceFields{
		Content: "概况测试句子", CategoryCode: "original", Source: "出处", Author: "作者",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublishedSentences != 1 || got.EnabledCategories != 1 || got.DatasetVersion == 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSiteSettingsEnsureAndUpsert(t *testing.T) {
	sqlDB := testdb.Open(t)
	st := store.New(sqlDB)
	ctx := context.Background()
	seed := store.SiteSettings{Name: "句子 API", EnglishName: "Quotewisp", Slogan: "偶遇一句话", Contact: "seed@example.com", PublicOrigin: "https://example.com/", RepoURL: "https://github.com/x/y"}
	got, err := st.EnsureSettings(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "句子 API" || got.EnglishName != "Quotewisp" || got.Slogan != "偶遇一句话" || got.Contact != "seed@example.com" || got.PublicOrigin != "https://example.com" || got.RepoURL != "https://github.com/x/y" {
		t.Fatalf("seeded=%+v", got)
	}
	again, err := st.EnsureSettings(ctx, store.SiteSettings{Name: "其他站", Contact: "other@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "句子 API" || again.Contact != "seed@example.com" {
		t.Fatalf("ensure overwrote existing row: %+v", again)
	}
	if err = st.UpsertSettings(ctx, store.SiteSettings{
		Name: "测试站", EnglishName: "Test Site", Slogan: "每日一句", Contact: "ops@example.com", BeianText: "京ICP备1号",
		BeianURL: "https://beian.miit.gov.cn/",
	}); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "测试站" || got.EnglishName != "Test Site" || got.Slogan != "每日一句" || got.Contact != "ops@example.com" || got.PublicOrigin != "" || got.BeianText != "京ICP备1号" {
		t.Fatalf("updated=%+v", got)
	}
}
