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
	c, e := load(ModeService, env(map[string]string{}))
	if e != nil {
		t.Fatal(e)
	}
	if c.HTTPAddr != ":8080" {
		t.Fatalf("bad defaults: %#v", c)
	}
}

func TestSQLiteDefaultsAllModesAndDataDir(t *testing.T) {
	for _, mode := range []Mode{ModeService, ModeImport, ModeMigrate, ModeWeb, ModeWebAdmin, ModeCombined} {
		m := map[string]string{}
		if mode == ModeWeb || mode == ModeCombined {
			m["SITE_CONTACT"] = "ops@example.com"
		}
		c, err := load(mode, env(m))
		if err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		if !strings.HasSuffix(c.SQLitePath, "/quotewisp.db") {
			t.Fatalf("mode %d path=%q", mode, c.SQLitePath)
		}
	}
	c, err := load(ModeService, env(map[string]string{"DATA_DIR": "/tmp/custom data"}))
	if err != nil || c.SQLitePath != "/tmp/custom data/quotewisp.db" {
		t.Fatalf("custom data dir: %v %#v", err, c)
	}
}

func TestCombinedDefaultsAndWebRequirements(t *testing.T) {
	base := map[string]string{"WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}
	c, err := load(ModeCombined, env(base))
	if err != nil || c.HTTPAddr != ":8080" || c.SnapshotLoadTimeout != 30*time.Second {
		t.Fatalf("combined defaults: %v %#v", err, c)
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
	base := map[string]string{"WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}
	for _, patch := range []map[string]string{
		{"SNAPSHOT_LOAD_TIMEOUT": "0s"}, {"SNAPSHOT_LOAD_TIMEOUT": "garbage"},
		{"RELOAD_TOKEN": "contains whitespace and is long enough 12345678901234567890"},
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

func TestServiceValidation(t *testing.T) {
	cases := []map[string]string{{"HTTP_ADDR": "nope"}, {"RELOAD_TOKEN": "short"}, {"CORS_ALLOWED_ORIGINS": "https://*.example.com"}, {"CORS_ALLOWED_ORIGINS": "https://example.com?"}, {"TRUSTED_PROXY_CIDRS": "bad"}}
	cases = append(cases, map[string]string{"HTTP_ADDR": ":0"}, map[string]string{"HTTP_ADDR": ":65536"}, map[string]string{"CORS_ALLOWED_ORIGINS": "https://example.com#"}, map[string]string{"CORS_ALLOWED_ORIGINS": "http://:80"}, map[string]string{"TRUSTED_PROXY_CIDRS": "::ffff:0:0/80"})
	for _, x := range cases {
		if _, e := load(ModeService, env(x)); e == nil {
			t.Fatalf("accepted %#v", x)
		}
	}
}

var webSecret = strings.Repeat("a", 32)

func TestWebRequiredMissing(t *testing.T) {
	for _, x := range []map[string]string{{"WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}} {
		if c, e := load(ModeWeb, env(x)); e != nil || c.SQLitePath == "" {
			t.Fatalf("default SQLite rejected: %v %#v", e, c)
		}
	}
}
func TestWebDefaults(t *testing.T) {
	c, e := load(ModeWeb, env(map[string]string{
		"WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com",
	}))
	if e != nil {
		t.Fatal(e)
	}
	if c.HTTPAddr != ":8081" || !c.CookieSecure || c.SubmissionRatePerHour != 5 || c.SubmissionRatePerDay != 20 ||
		c.SubmissionPendingLimit != 1000 || c.SubmissionRetention != 2160*time.Hour || c.SnapshotPollInterval != time.Minute ||
		c.ShutdownTimeout != 10*time.Second || c.APIMetricsURL != "" {
		t.Fatalf("bad defaults: %#v", c)
	}
}
func TestWebValidation(t *testing.T) {
	base := map[string]string{"WEB_SECRET_KEY": webSecret, "SITE_CONTACT": "contact@example.com"}
	cases := []map[string]string{
		{"HTTP_ADDR": "nope"},
		{"HTTP_ADDR": ":0"},
		{"WEB_SECRET_KEY": "short"},
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
	if c, e := load(ModeWebAdmin, env(map[string]string{})); e != nil || c.SQLitePath == "" {
		t.Fatalf("default SQLite rejected: %v %#v", e, c)
	}
}

func TestWebImportLimits(t *testing.T) {
	for _, mode := range []Mode{ModeWeb, ModeCombined} {
		values := map[string]string{"SITE_CONTACT": "test@example.com"}
		c, err := load(mode, env(values))
		if err != nil || c.ImportMaxUploadBytes != 32<<20 || c.ImportUploadTTL != 30*time.Minute || c.ImportTimeout != 120*time.Second {
			t.Fatalf("defaults: %v %+v", err, c)
		}
		for key, bad := range map[string][]string{"IMPORT_MAX_UPLOAD_BYTES": {"0", "-1", "1073741825", "oops"}, "IMPORT_UPLOAD_TTL": {"0s", "-1s", "oops"}, "IMPORT_TIMEOUT": {"0s", "oops"}} {
			for _, v := range bad {
				values[key] = v
				if _, err := load(mode, env(values)); err == nil {
					t.Fatalf("accepted %s=%s", key, v)
				}
			}
			delete(values, key)
		}
		values["IMPORT_MAX_UPLOAD_BYTES"] = "1234"
		values["IMPORT_UPLOAD_TTL"] = "5m"
		values["IMPORT_TIMEOUT"] = "10s"
		c, err = load(mode, env(values))
		if err != nil || c.ImportMaxUploadBytes != 1234 || c.ImportUploadTTL != 5*time.Minute || c.ImportTimeout != 10*time.Second {
			t.Fatalf("custom limits: %v %+v", err, c)
		}
	}
}

func TestRemovedDatabaseConfigurationRejected(t *testing.T) {
	for _, mode := range []Mode{ModeService, ModeImport, ModeMigrate, ModeWeb, ModeWebAdmin, ModeCombined} {
		for _, key := range []string{"MYSQL_DSN", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD", "DB_TLS", "MYSQL_MAX_OPEN_CONNS", "MYSQL_MAX_IDLE_CONNS", "MYSQL_CONN_MAX_LIFETIME"} {
			for _, value := range []string{"", "private-value"} {
				_, err := load(mode, env(map[string]string{key: value}))
				if err == nil || !strings.Contains(err.Error(), "no longer supported") || strings.Contains(err.Error(), "private-value") {
					t.Fatalf("mode=%d key=%s err=%v", mode, key, err)
				}
			}
		}
	}
}
