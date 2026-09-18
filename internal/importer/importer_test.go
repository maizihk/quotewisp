package importer

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/DATA-DOG/go-sqlmock"
)

const validInput = `{"categories":[{"code":"original","name":"原创"}],"sentences":[{"uuid":"75A45FD4-4F2F-45EB-80CB-6F0A7BCDFAF2","category":"original","content":"今天也要认真写代码。","source":null}]}`

func TestParseValidAndNormalize(t *testing.T) {
	d, e := Parse(context.Background(), strings.NewReader(validInput))
	if e != nil {
		t.Fatal(e)
	}
	if d.InputCount != 1 || d.Sentences[0].UUID != "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2" || d.Sentences[0].Length != uint16(utf8.RuneCountInString("今天也要认真写代码。")) {
		t.Fatalf("unexpected dataset: %+v", d)
	}
}

func TestParseDuplicateSentenceDeduplicates(t *testing.T) {
	item := `{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"original","content":"x"}`
	raw := `{"categories":[{"code":"original","name":"x"}],"sentences":[` + item + `,` + item + `]}`
	d, e := Parse(context.Background(), strings.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	if d.InputCount != 2 || d.DeduplicatedCount != 1 || len(d.Sentences) != 1 {
		t.Fatalf("unexpected counts: %+v", d)
	}
}

func TestParseStrictJSON(t *testing.T) {
	tests := map[string]string{
		"duplicate key":   `{"sentences":[],"sentences":[]}`,
		"unknown field":   `{"sentences":[],"extra":1}`,
		"null array":      `{"sentences":null}`,
		"trailing":        validInput + ` {}`,
		"bom":             "\xef\xbb\xbf" + validInput,
		"bad utf8":        `{"sentences":["` + string([]byte{0xff}) + `"]}`,
		"high surrogate":  `{"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"\ud800"}]}`,
		"low surrogate":   `{"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"\udc00"}]}`,
		"high then basic": `{"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"\ud800\u0041"}]}`,
		"two highs":       `{"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"\ud800\ud800\udc00"}]}`,
		"high then utf8":  `{"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"\ud800中\udc00"}]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, e := Parse(context.Background(), strings.NewReader(raw)); e == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type oneByteReader struct{ r *strings.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.r.Read(p)
}
func TestParseChunkedReader(t *testing.T) {
	raw := strings.Replace(validInput, "今天也要认真写代码。", `\ud83d\ude00 �`, 1)
	if _, e := Parse(context.Background(), oneByteReader{strings.NewReader(raw)}); e != nil {
		t.Fatal(e)
	}
}

func TestParseAllowsReplacementRuneAndPair(t *testing.T) {
	for _, content := range []string{"�", `\ud83d\ude00`, `\ufffd`} {
		raw := `{"categories":[{"code":"x","name":"x"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"` + content + `"}]}`
		if _, e := Parse(context.Background(), strings.NewReader(raw)); e != nil {
			t.Fatalf("content %q: %v", content, e)
		}
	}
}

func TestParseValidationAndConflictingDuplicate(t *testing.T) {
	tests := []string{
		`{"categories":[{"code":"Bad","name":"x"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"Bad","content":"x"}]}`,
		`{"categories":[{"code":"x","name":"   "}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"x"}]}`,
		`{"categories":[{"code":"x","name":"x"}],"sentences":[{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"x"},{"uuid":"75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2","category":"x","content":"y"}]}`,
	}
	for _, raw := range tests {
		if _, e := Parse(context.Background(), strings.NewReader(raw)); e == nil {
			t.Fatal("expected validation error")
		}
	}
}

func TestParseCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := Parse(ctx, strings.NewReader(validInput))
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("got %v", e)
	}
}

func importDataset() Dataset {
	return Dataset{InputCount: 1, Sentences: []Sentence{{UUID: "75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2", Category: "x", Content: "a", Length: 1}}}
}
func expectVersion(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT version FROM dataset_versions WHERE id=1 FOR UPDATE")).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(1))
}
func expectCategory(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id,code,name,enabled,sort_order FROM categories WHERE code IN (?)")).WithArgs("x").WillReturnRows(rows)
}

func TestRunCommitErrorIsOutcomeUnknown(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	mock.ExpectBegin()
	expectVersion(mock)
	expectCategory(mock, sqlmock.NewRows([]string{"id", "code", "name", "enabled", "sort_order"}).AddRow(1, "x", "X", true, 0))
	mock.ExpectQuery("SELECT s\\.uuid.*REPLACE").WithArgs("75a45fd44f2f45eb80cb6f0a7bcdfaf2").WillReturnRows(sqlmock.NewRows([]string{"uuid", "code", "content", "source", "author", "length", "status", "enabled", "published_at"}))
	mock.ExpectExec("INSERT INTO sentences").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE dataset_versions").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("connection lost"))
	_, e = Run(context.Background(), db, importDataset(), false)
	var ie *Error
	if !errors.As(e, &ie) || ie.Category != "commit-outcome-unknown" {
		t.Fatalf("unexpected error: %v", e)
	}
	if strings.Contains(e.Error(), "connection lost") {
		t.Fatal("raw commit error leaked")
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestRunCategoryRowsErrorStopsBeforeWrite(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	mock.ExpectBegin()
	expectVersion(mock)
	rows := sqlmock.NewRows([]string{"id", "code", "name", "enabled", "sort_order"}).AddRow(1, "x", "X", true, 0).RowError(0, errors.New("network read"))
	expectCategory(mock, rows)
	mock.ExpectRollback()
	_, e = Run(context.Background(), db, importDataset(), false)
	var ie *Error
	if !errors.As(e, &ie) || ie.Category != "database" || ie.Err.Error() != "category lookup failed" {
		t.Fatalf("unexpected error: %v", e)
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}

func TestRunSentenceRowsErrorStopsBeforeWrite(t *testing.T) {
	db, mock, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	mock.ExpectBegin()
	expectVersion(mock)
	expectCategory(mock, sqlmock.NewRows([]string{"id", "code", "name", "enabled", "sort_order"}).AddRow(1, "x", "X", true, 0))
	rows := sqlmock.NewRows([]string{"uuid", "code", "content", "source", "author", "length", "status", "enabled", "published_at"}).AddRow("75a45fd4-4f2f-45eb-80cb-6f0a7bcdfaf2", "x", "a", nil, nil, 1, 1, true, nil).RowError(0, errors.New("network read"))
	mock.ExpectQuery("SELECT s\\.uuid.*REPLACE").WithArgs("75a45fd44f2f45eb80cb6f0a7bcdfaf2").WillReturnRows(rows)
	mock.ExpectRollback()
	_, e = Run(context.Background(), db, importDataset(), false)
	var ie *Error
	if !errors.As(e, &ie) || ie.Category != "database" || ie.Err.Error() != "sentence lookup failed" {
		t.Fatalf("unexpected error: %v", e)
	}
	if e = mock.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
