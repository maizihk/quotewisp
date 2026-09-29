package importer

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"sentence-api/internal/database"
)

func TestSQLiteMultiBatchPreviewAndImport(t *testing.T) {
	ctx := context.Background()
	target := database.Target{SQLitePath: filepath.Join(t.TempDir(), "scale.db")}
	if err := target.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := target.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	data := Dataset{Categories: []Category{{Code: "scale", Name: "规模测试", SortOrder: 0}}}
	for i := 0; i < 12000; i++ {
		data.Sentences = append(data.Sentences, Sentence{UUID: fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1), Category: "scale", Content: "测试", Length: 2})
	}
	data.InputCount = uint64(len(data.Sentences))
	data.DeduplicatedCount = data.InputCount
	for _, dry := range []bool{true, false} {
		sum, err := Run(ctx, db, data, dry)
		if err != nil {
			t.Fatalf("dry=%v: %v", dry, err)
		}
		if sum.NewSentences != 12000 {
			t.Fatalf("summary=%+v", sum)
		}
	}
}
