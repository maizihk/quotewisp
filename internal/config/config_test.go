package config

import (
	"strings"
	"testing"
	"time"
)

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

func TestCombinedDefaultsAndWebRequirements(t *testing.T) {
	base := map[string]string{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}
	c, err := load(ModeCombined, env(base))
	if err != nil || c.HTTPAddr != ":8080" || c.MySQLMaxOpenConns != 10 || c.SnapshotLoadTimeout != 30*time.Second {
		t.Fatalf("combined defaults: %v %#v", err, c)
	}
	for _, key := range []string{"WEB_SECRET_KEY", "SITE_CONTACT"} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		delete(m, key)
		if _, err := load(ModeCombined, env(m)); err == nil {
			t.Fatalf("combined accepted missing %s", key)
		}
	}
	bad := map[string]string{}
	for k, v := range base {
		bad[k] = v
	}
	bad["CORS_ALLOWED_ORIGINS"] = "https://*.example.com"
	if _, err := load(ModeCombined, env(bad)); err == nil {
		t.Fatal("combined accepted invalid CORS")
	}
}

func TestCombinedServiceValidation(t *testing.T) {
	base := map[string]string{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}
	for _, patch := range []map[string]string{
		{"SNAPSHOT_LOAD_TIMEOUT": "0s"}, {"SNAPSHOT_LOAD_TIMEOUT": "garbage"},
		{"RELOAD_TOKEN": "contains whitespace and is long enough 12345678901234567890"},
		{"MYSQL_MAX_IDLE_CONNS": "11", "MYSQL_MAX_OPEN_CONNS": "10"},
	} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range patch {
			m[k] = v
		}
		if _, err := load(ModeCombined, env(m)); err == nil {
			t.Fatalf("combined accepted %#v", patch)
		}
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

var webSecret = strings.Repeat("a", 32)

func TestWebRequiredMissing(t *testing.T) {
	for _, x := range []map[string]string{
		{"WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"},
		{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "SITE_CONTACT": "contact@example.com"},
		{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "WEB_SECRET_KEY": webSecret},
	} {
		if _, e := load(ModeWeb, env(x)); e == nil {
			t.Fatalf("accepted %#v", x)
		}
	}
}
func TestWebDefaults(t *testing.T) {
	c, e := load(ModeWeb, env(map[string]string{
		"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com",
	}))
	if e != nil {
		t.Fatal(e)
	}
	if c.HTTPAddr != ":8081" || !c.CookieSecure || c.SubmissionRatePerHour != 5 || c.SubmissionRatePerDay != 20 ||
		c.SubmissionPendingLimit != 1000 || c.SubmissionRetention != 2160*time.Hour || c.SnapshotPollInterval != time.Minute ||
		c.MySQLMaxOpenConns != 10 || c.ShutdownTimeout != 10*time.Second || c.APIMetricsURL != "" {
		t.Fatalf("bad defaults: %#v", c)
	}
}
func TestWebValidation(t *testing.T) {
	base := map[string]string{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}
	cases := []map[string]string{
		{"HTTP_ADDR": "nope"},
		{"HTTP_ADDR": ":0"},
		{"WEB_SECRET_KEY": "short"},
		{"SITE_CONTACT": ""},
		{"SITE_CONTACT": strings.Repeat("x", 257)},
		{"SITE_CONTACT": "\xff"},
		{"COOKIE_SECURE": "yes"},
		{"SUBMISSION_RATE_PER_HOUR": "0"},
		{"SUBMISSION_RATE_PER_HOUR": ""},
		{"SUBMISSION_RATE_PER_DAY": "0"},
		{"SUBMISSION_RATE_PER_DAY": ""},
		{"SUBMISSION_RATE_PER_DAY": "4"},
		{"SUBMISSION_PENDING_LIMIT": "0"},
		{"SUBMISSION_PENDING_LIMIT": ""},
		{"SUBMISSION_RETENTION": "0s"},
		{"SUBMISSION_RETENTION": ""},
		{"SNAPSHOT_POLL_INTERVAL": "0s"},
		{"SNAPSHOT_POLL_INTERVAL": ""},
		{"TRUSTED_PROXY_CIDRS": "bad"},
		{"LOG_LEVEL": "trace"},
		{"SHUTDOWN_TIMEOUT": ""},
		{"SITE_REPO_URL": "ftp://x"},
		{"SITE_REPO_URL": "https://u:p@example.com/x"},
		{"API_BASE_URL": "/api"},
		{"API_BASE_URL": "https://example.com/?x=1"},
		{"API_BASE_URL": "http://:80"},
		{"SITE_REPO_URL": "http://:80"},
		{"API_BASE_URL": "https://example.com#"},
		{"API_BASE_URL": "https://example.com?"},
		{"SITE_REPO_URL": "https://example.com#"},
		{"API_METRICS_URL": "https://example.com/healthz"},
		{"API_METRICS_URL": "https://example.com#"},
	}
	for _, patch := range cases {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range patch {
			m[k] = v
		}
		if _, e := load(ModeWeb, env(m)); e == nil {
			t.Fatalf("accepted %#v", patch)
		}
	}
	m := map[string]string{"API_BASE_URL": "https://example.com/", "SITE_REPO_URL": "https://github.com/x/y", "API_METRICS_URL": "http://127.0.0.1:8080"}
	for k, v := range base {
		m[k] = v
	}
	c, e := load(ModeWeb, env(m))
	if e != nil || c.APIBaseURL != "https://example.com" || c.SiteRepoURL != "https://github.com/x/y" || c.APIMetricsURL != "http://127.0.0.1:8080/metrics" {
		t.Fatalf("urls: %v %#v", e, c)
	}
}
func TestWebAdminRequiredMissing(t *testing.T) {
	if _, e := load(ModeWebAdmin, env(map[string]string{})); e == nil {
		t.Fatal("accepted missing MYSQL_DSN")
	}
}
func TestWebAdminDefaultsAndPool(t *testing.T) {
	c, e := load(ModeWebAdmin, env(map[string]string{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db"}))
	if e != nil {
		t.Fatal(e)
	}
	if c.MySQLMaxOpenConns != 10 || c.MySQLMaxIdleConns != 5 {
		t.Fatalf("bad defaults: %#v", c)
	}
	if _, e = load(ModeWebAdmin, env(map[string]string{"MYSQL_DSN": "u:p@tcp(localhost:3306)/db", "MYSQL_MAX_OPEN_CONNS": "0"})); e == nil {
		t.Fatal("accepted invalid pool")
	}
}
