package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"sentence-api/internal/database"
)

func receiptTestDB(t *testing.T) *sql.DB {
	t.Helper()
	target := database.Target{SQLitePath: filepath.Join(t.TempDir(), "receipt.db")}
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

func receiptDataset(uuid string) Dataset {
	return Dataset{
		InputCount: 1, DeduplicatedCount: 1,
		Categories: []Category{{Code: "x", Name: "X"}},
		Sentences:  []Sentence{{UUID: uuid, Category: "x", Content: "a", Length: 1}},
	}
}

func TestRunWithReceiptCommitsSummaryAndReceipt(t *testing.T) {
	db := receiptTestDB(t)
	data := receiptDataset("75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2")
	var got Summary
	sum, err := RunWithReceipt(context.Background(), db, data, func(tx *sql.Tx, received Summary) error {
		got = received
		encoded, err := json.Marshal(received)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO import_jobs
			(id,admin_id,digest,format,status,result_json,created_at,expires_at,updated_at)
			VALUES (?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			"00000000-0000-0000-0000-000000000001", 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "json", "completed", encoded)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Changed || sum.DatasetVersionBefore != "1" || sum.DatasetVersionAfter == nil || *sum.DatasetVersionAfter != "2" {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if got != sum {
		t.Fatalf("receipt summary differs: got %+v want %+v", got, sum)
	}
	var result string
	if err := db.QueryRow("SELECT result_json FROM import_jobs WHERE id=?", "00000000-0000-0000-0000-000000000001").Scan(&result); err != nil {
		t.Fatal(err)
	}
	var stored Summary
	if err := json.Unmarshal([]byte(result), &stored); err != nil || !sameSummary(stored, sum) {
		t.Fatalf("stored summary=%+v err=%v", stored, err)
	}
}

func sameSummary(a, b Summary) bool {
	if a.DryRun != b.DryRun || a.InputCount != b.InputCount || a.DeduplicatedCount != b.DeduplicatedCount || a.SkippedCount != b.SkippedCount || a.NewSentences != b.NewSentences || a.NewCategories != b.NewCategories || a.Changed != b.Changed || a.DatasetVersionBefore != b.DatasetVersionBefore {
		return false
	}
	if a.DatasetVersionAfter == nil || b.DatasetVersionAfter == nil {
		return a.DatasetVersionAfter == nil && b.DatasetVersionAfter == nil
	}
	return *a.DatasetVersionAfter == *b.DatasetVersionAfter
}

func TestRunWithReceiptInvokesHookOnNoop(t *testing.T) {
	db := receiptTestDB(t)
	data := receiptDataset("75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2")
	if _, err := Run(context.Background(), db, data, false); err != nil {
		t.Fatal(err)
	}
	called := false
	sum, err := RunWithReceipt(context.Background(), db, data, func(tx *sql.Tx, received Summary) error {
		called = true
		if received.Changed || received.NewSentences != 0 || received.NewCategories != 0 || received.DatasetVersionAfter == nil || *received.DatasetVersionAfter != "2" {
			t.Errorf("unexpected noop summary: %+v", received)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || sum.Changed || sum.DatasetVersionAfter == nil || *sum.DatasetVersionAfter != "2" {
		t.Fatalf("hook=%t summary=%+v", called, sum)
	}
}

func TestRunWithReceiptHookFailureRollsBackImportAndReceipt(t *testing.T) {
	db := receiptTestDB(t)
	failure := errors.New("receipt failed")
	data := receiptDataset("85a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2")
	_, err := RunWithReceipt(context.Background(), db, data, func(tx *sql.Tx, _ Summary) error {
		if _, err := tx.Exec(`INSERT INTO import_jobs
			(id,admin_id,digest,format,status,created_at,expires_at,updated_at)
			VALUES (?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			"00000000-0000-0000-0000-000000000002", 1, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "json", "failed"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM categories WHERE code='x'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("category persisted after receipt failure: %d", count)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM import_jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("receipt persisted after failure: %d", count)
	}
	var version uint64
	if err := db.QueryRow("SELECT version FROM dataset_versions WHERE id=1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version persisted after receipt failure: %d", version)
	}
}
