package config

import "testing"

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}
func TestModesAndDefaults(t *testing.T) {
	c, e := load(ModeService, env(map[string]string{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db"}))
	if e != nil {
		t.Fatal(e)
	}
	if c.HTTPAddr != ":8080" || c.MySQLMaxOpenConns != 10 {
		t.Fatalf("bad defaults: %#v", c)
	}
}
func TestImportValidatesPool(t *testing.T) {
	_, e := load(ModeImport, env(map[string]string{"MYSQL_DSN": "x/db", "MYSQL_MAX_OPEN_CONNS": "0"}))
	if e == nil {
		t.Fatal("accepted invalid import pool")
	}
}
func TestServiceValidation(t *testing.T) {
	cases := []map[string]string{{"MYSQL_DSN": "x/db", "HTTP_ADDR": "nope"}, {"MYSQL_DSN": "x/db", "RELOAD_TOKEN": "short"}, {"MYSQL_DSN": "x/db", "CORS_ALLOWED_ORIGINS": "https://*.example.com"}, {"MYSQL_DSN": "x/db", "CORS_ALLOWED_ORIGINS": "https://example.com?"}, {"MYSQL_DSN": "x/db", "TRUSTED_PROXY_CIDRS": "bad"}}
	cases = append(cases, map[string]string{"MYSQL_DSN": "x/db", "HTTP_ADDR": ":0"}, map[string]string{"MYSQL_DSN": "x/db", "HTTP_ADDR": ":65536"}, map[string]string{"MYSQL_DSN": "x/db", "CORS_ALLOWED_ORIGINS": "https://example.com#"}, map[string]string{"MYSQL_DSN": "x/db", "CORS_ALLOWED_ORIGINS": "http://:80"}, map[string]string{"MYSQL_DSN": "x/db", "TRUSTED_PROXY_CIDRS": "::ffff:0:0/80"})
	for _, x := range cases {
		if _, e := load(ModeService, env(x)); e == nil {
			t.Fatalf("accepted %#v", x)
		}
	}
}
