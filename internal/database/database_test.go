package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestSafeDriverLoggerDoesNotForwardArguments(t *testing.T) {
	var b bytes.Buffer
	l := safeDriverLogger{logger: slog.New(slog.NewJSONHandler(&b, nil))}
	l.Print("password=secret", errors.New("dsn secret"))
	got := b.String()
	if strings.Contains(got, "secret") || !strings.Contains(got, "mysql_driver_diagnostic") || !strings.Contains(got, "driver-critical") {
		t.Fatalf("unsafe log: %s", got)
	}
}

func TestDriverLoggerUsesUTC(t *testing.T) {
	var b bytes.Buffer
	safeDriverLogger{logger: newDriverSlog(&b)}.Print("ignored")
	var record map[string]any
	if e := json.Unmarshal(b.Bytes(), &record); e != nil {
		t.Fatal(e)
	}
	stamp, ok := record["time"].(string)
	if !ok {
		t.Fatalf("missing time: %s", b.String())
	}
	parsed, e := time.Parse(time.RFC3339Nano, stamp)
	if e != nil {
		t.Fatal(e)
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		t.Fatalf("driver log is not UTC: %s", stamp)
	}
}

func TestNormalizeDSN(t *testing.T) {
	d, e := NormalizeDSN("u:p@tcp(localhost:3306)/db", false)
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range []string{"parseTime=true", "timeout=5s", "readTimeout=30s", "writeTimeout=30s", "charset=utf8mb4"} {
		if !strings.Contains(d, x) {
			t.Errorf("missing %s in %s", x, d)
		}
	}
}
func TestNormalizeDSNRejects(t *testing.T) {
	for _, d := range []string{"u:p@tcp(localhost:3306)/", "u:p@tcp(localhost:3306)/db?timeout=0s", "u:p@tcp(localhost:3306)/db?readTimeout=-1s"} {
		if _, e := NormalizeDSN(d, false); e == nil {
			t.Errorf("accepted %s", d)
		}
	}
}

func TestNormalizeDSNQuestionMarkInPassword(t *testing.T) {
	if _, e := NormalizeDSN("u:p?word@tcp(localhost:3306)/db", false); e != nil {
		t.Fatal(e)
	}
	if _, e := NormalizeDSN("u:p?word@tcp(localhost:3306)/db?timeout=0s", false); e == nil {
		t.Fatal("accepted explicit zero timeout")
	}
}

func TestCheckSchema(t *testing.T) {
	q := regexp.QuoteMeta("SELECT version, dirty FROM schema_migrations LIMIT 1")
	cases := []struct {
		name    string
		version uint
		dirty   bool
		ok      bool
	}{
		{name: "version2", version: RequiredSchemaVersion, dirty: false, ok: true},
		{name: "version1", version: 1, dirty: false, ok: false},
		{name: "dirty", version: RequiredSchemaVersion, dirty: true, ok: false},
		{name: "version3", version: 3, dirty: false, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, e := sqlmock.New()
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectQuery(q).WillReturnRows(sqlmock.NewRows([]string{"version", "dirty"}).AddRow(tc.version, tc.dirty))
			err := CheckSchema(context.Background(), db)
			if tc.ok && err != nil {
				t.Fatalf("expected success: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected failure")
			}
			if e = mock.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
