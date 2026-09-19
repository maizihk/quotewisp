package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentence-api/internal/database"
	"sentence-api/internal/httpapi"
	"sentence-api/internal/importer"
	"sentence-api/internal/observability"
	"sentence-api/internal/snapshot"
	"sentence-api/internal/testdb"
	"sentence-api/internal/web/store"
)

const seedDataset = `{"categories":[{"code":"original","name":"原创","sort_order":1},{"code":"other","name":"其他","sort_order":2}],"sentences":[{"uuid":"75A45FD4-4F2F-45EB-80CB-6F0A7BCDFAF2","category":"original","content":"原创句子","source":"出处","author":"作者"},{"uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","category":"other","content":"其他句子","source":"出处","author":"作者"}]}`

func TestCategoryDisableViaStoreSnapshotAndRandomAPI(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	if _, err := importer.Import(ctx, db, strings.NewReader(seedDataset), false); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	cat, err := st.GetCategory(ctx, "original")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetCategoryEnabled(ctx, "original", false, int64(cat.PublishedCount)); err != nil {
		t.Fatal(err)
	}
	snap, err := database.LoadSnapshot(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.LookupUUID("75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2"); ok {
		t.Fatal("disabled category sentence still in snapshot")
	}
	if got, ok := snap.LookupUUID("de305d54-75b4-431b-adb2-eb6b9e546014"); !ok || got.Category != "other" {
		t.Fatalf("other category sentence missing: ok=%t got=%+v", ok, got)
	}
	for _, c := range snap.Categories {
		if c.Code == "original" {
			t.Fatal("disabled category still listed in snapshot")
		}
	}
	mgr := snapshot.NewManager(database.Loader{DB: db}, time.Second)
	if err = mgr.LoadInitial(ctx); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(httpapi.Options{Snapshots: mgr, Metrics: observability.NewMetrics()})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1?categories=original&min_length=1&max_length=30", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("random with disabled category status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1?categories=other&min_length=1&max_length=30", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("random with enabled category status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestExportJSONImportUnchanged(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	if _, err := importer.Import(ctx, db, strings.NewReader(seedDataset), false); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	pub, err := st.BuildPublicData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub.ExportJSON) == 0 {
		t.Fatal("empty export json")
	}
	versionBefore, err := st.DatasetVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := importer.Import(ctx, db, strings.NewReader(string(pub.ExportJSON)), false)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Changed {
		t.Fatalf("export re-import changed data: %+v", sum)
	}
	if sum.SkippedCount == 0 {
		t.Fatalf("expected skipped rows: %+v", sum)
	}
	versionAfter, err := st.DatasetVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("export re-import bumped version before=%d after=%d", versionBefore, versionAfter)
	}
	var doc struct {
		Categories []struct {
			Code      string `json:"code"`
			Name      string `json:"name"`
			SortOrder int32  `json:"sort_order"`
		} `json:"categories"`
		Sentences []struct {
			UUID     string  `json:"uuid"`
			Category string  `json:"category"`
			Content  string  `json:"content"`
			Source   *string `json:"source"`
			Author   *string `json:"author"`
		} `json:"sentences"`
	}
	dec := json.NewDecoder(strings.NewReader(string(pub.ExportJSON)))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&doc); err != nil {
		t.Fatalf("export json invalid: %v", err)
	}
	if len(doc.Categories) != 2 || len(doc.Sentences) != 2 {
		t.Fatalf("unexpected export shape: categories=%d sentences=%d", len(doc.Categories), len(doc.Sentences))
	}
}
