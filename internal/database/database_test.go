package database

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCheckSchema(t *testing.T) {
	q := regexp.QuoteMeta("SELECT version, dirty FROM schema_migrations LIMIT 1")
	cases := []struct {
		name     string
		version  uint
		dirty    bool
		min, max uint
		ok       bool
	}{
		{name: "version2", version: CurrentSchemaVersion, dirty: false, min: CurrentSchemaVersion, max: CurrentSchemaVersion, ok: true},
		{name: "version1Write", version: 1, dirty: false, min: CurrentSchemaVersion, max: CurrentSchemaVersion, ok: false},
		{name: "version1Read", version: 1, dirty: false, min: MinReadSchemaVersion, max: CurrentSchemaVersion, ok: true},
		{name: "dirty", version: CurrentSchemaVersion, dirty: true, min: CurrentSchemaVersion, max: CurrentSchemaVersion, ok: false},
		{name: "version4", version: CurrentSchemaVersion + 1, dirty: false, min: MinReadSchemaVersion, max: CurrentSchemaVersion, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, e := sqlmock.New()
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectQuery(q).WillReturnRows(sqlmock.NewRows([]string{"version", "dirty"}).AddRow(tc.version, tc.dirty))
			err := CheckSchema(context.Background(), db, tc.min, tc.max)
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
