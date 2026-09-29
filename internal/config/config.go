package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

type Mode uint8

const (
	ModeService Mode = iota
	ModeImport
	ModeMigrate
	ModeWeb
	ModeWebAdmin
	ModeCombined
)

type Config struct {
	HTTPAddr               string
	MYSQLDSN               string
	SQLitePath             string
	DataDir                string
	MySQLMaxOpenConns      int
	MySQLMaxIdleConns      int
	MySQLConnMaxLifetime   time.Duration
	SnapshotPollInterval   time.Duration
	SnapshotLoadTimeout    time.Duration
	ImportTimeout          time.Duration
	ReloadToken            string
	CORSAllowedOrigins     []string
	TrustedProxyCIDRs      []netip.Prefix
	LogLevel               slog.Level
	ShutdownTimeout        time.Duration
	WebSecretKey           string
	SiteContact            string
	SiteRepoURL            string
	APIBaseURL             string
	APIMetricsURL          string
	CookieSecure           bool
	SubmissionRatePerHour  int
	SubmissionRatePerDay   int
	SubmissionPendingLimit int
	SubmissionRetention    time.Duration
}

func Load(mode Mode) (Config, error) { return load(mode, os.LookupEnv) }

func load(mode Mode, lookup func(string) (string, bool)) (Config, error) {
	c := Config{HTTPAddr: ":8080", MySQLMaxOpenConns: 10, MySQLMaxIdleConns: 5, MySQLConnMaxLifetime: 30 * time.Minute, SnapshotPollInterval: time.Minute, SnapshotLoadTimeout: 30 * time.Second, ImportTimeout: 120 * time.Second, LogLevel: slog.LevelInfo, ShutdownTimeout: 10 * time.Second}
	var err error
	c.DataDir = "/var/lib/quotewisp"
	if v, ok := lookup("DATA_DIR"); ok {
		if v == "" {
			return c, fmt.Errorf("DATA_DIR must not be empty")
		}
		c.DataDir = v
	}
	if c.MYSQLDSN, err = mysqlDSN(lookup); err != nil {
		return c, err
	}
	if c.MYSQLDSN == "" {
		c.SQLitePath = filepath.Join(c.DataDir, "quotewisp.db")
	}
	if mode == ModeService || mode == ModeCombined {
		if c.HTTPAddr, err = str(lookup, "HTTP_ADDR", c.HTTPAddr, false); err != nil {
			return c, err
		}
		if _, port, e := net.SplitHostPort(c.HTTPAddr); e != nil {
			return c, fmt.Errorf("HTTP_ADDR is invalid")
		} else if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("HTTP_ADDR port is invalid")
		}
	}
	if mode == ModeService || mode == ModeCombined || mode == ModeImport || mode == ModeWeb || mode == ModeWebAdmin {
		if c.MySQLMaxOpenConns, err = integer(lookup, "MYSQL_MAX_OPEN_CONNS", 10, 1); err != nil {
			return c, err
		}
		if c.MySQLMaxIdleConns, err = integer(lookup, "MYSQL_MAX_IDLE_CONNS", 5, 0); err != nil {
			return c, err
		}
		if c.MySQLMaxIdleConns > c.MySQLMaxOpenConns {
			return c, fmt.Errorf("MYSQL_MAX_IDLE_CONNS must not exceed MYSQL_MAX_OPEN_CONNS")
		}
		if c.MySQLConnMaxLifetime, err = duration(lookup, "MYSQL_CONN_MAX_LIFETIME", 30*time.Minute); err != nil {
			return c, err
		}
	}
	if mode == ModeWeb || mode == ModeCombined {
		defaultAddr := ":8081"
		if mode == ModeCombined {
			defaultAddr = ":8080"
		}
		if c.HTTPAddr, err = str(lookup, "HTTP_ADDR", defaultAddr, false); err != nil {
			return c, err
		}
		if _, port, e := net.SplitHostPort(c.HTTPAddr); e != nil {
			return c, fmt.Errorf("HTTP_ADDR is invalid")
		} else if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("HTTP_ADDR port is invalid")
		}
		c.WebSecretKey, _ = optional(lookup, "WEB_SECRET_KEY")
		if c.WebSecretKey != "" && !validToken(c.WebSecretKey) {
			return c, fmt.Errorf("WEB_SECRET_KEY must be 32-256 printable non-whitespace ASCII bytes")
		}
		if c.SiteContact, err = siteContact(lookup); err != nil {
			return c, err
		}
		if c.SiteRepoURL, err = siteRepoURL(lookup); err != nil {
			return c, err
		}
		if c.APIBaseURL, err = apiBaseURL(lookup); err != nil {
			return c, err
		}
		if c.APIMetricsURL, err = apiMetricsURL(lookup); err != nil {
			return c, err
		}
		if c.CookieSecure, err = boolEnv(lookup, "COOKIE_SECURE", true); err != nil {
			return c, err
		}
		if c.SubmissionRatePerHour, err = integer(lookup, "SUBMISSION_RATE_PER_HOUR", 5, 1); err != nil {
			return c, err
		}
		if c.SubmissionRatePerDay, err = integer(lookup, "SUBMISSION_RATE_PER_DAY", 20, 1); err != nil {
			return c, err
		}
		if c.SubmissionRatePerDay < c.SubmissionRatePerHour {
			return c, fmt.Errorf("SUBMISSION_RATE_PER_DAY must be greater than or equal to SUBMISSION_RATE_PER_HOUR")
		}
		if c.SubmissionPendingLimit, err = integer(lookup, "SUBMISSION_PENDING_LIMIT", 1000, 1); err != nil {
			return c, err
		}
		if c.SubmissionRetention, err = duration(lookup, "SUBMISSION_RETENTION", 2160*time.Hour); err != nil {
			return c, err
		}
		if c.SnapshotPollInterval, err = duration(lookup, "SNAPSHOT_POLL_INTERVAL", time.Minute); err != nil {
			return c, err
		}
		if c.TrustedProxyCIDRs, err = prefixes(lookup); err != nil {
			return c, err
		}
		if c.LogLevel, err = level(lookup); err != nil {
			return c, err
		}
		if c.ShutdownTimeout, err = duration(lookup, "SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
			return c, err
		}
	}
	if mode == ModeService || mode == ModeCombined {
		if c.SnapshotPollInterval, err = duration(lookup, "SNAPSHOT_POLL_INTERVAL", time.Minute); err != nil {
			return c, err
		}
		if c.SnapshotLoadTimeout, err = duration(lookup, "SNAPSHOT_LOAD_TIMEOUT", 30*time.Second); err != nil {
			return c, err
		}
		if c.ShutdownTimeout, err = duration(lookup, "SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
			return c, err
		}
		c.ReloadToken, _ = optional(lookup, "RELOAD_TOKEN")
		if c.ReloadToken != "" && !validToken(c.ReloadToken) {
			return c, fmt.Errorf("RELOAD_TOKEN must be 32-256 printable non-whitespace ASCII bytes")
		}
		if c.CORSAllowedOrigins, err = origins(lookup); err != nil {
			return c, err
		}
		if c.TrustedProxyCIDRs, err = prefixes(lookup); err != nil {
			return c, err
		}
		if c.LogLevel, err = level(lookup); err != nil {
			return c, err
		}
	} else if mode == ModeImport {
		if c.ImportTimeout, err = duration(lookup, "IMPORT_TIMEOUT", 120*time.Second); err != nil {
			return c, err
		}
	}
	return c, nil
}

func required(l func(string) (string, bool), k string) (string, error) {
	v, ok := l(k)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required", k)
	}
	return v, nil
}

func mysqlDSN(l func(string) (string, bool)) (string, error) {
	keys := []string{"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD"}
	present := 0
	vals := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := l(k); ok {
			present++
			vals[k] = v
		}
	}
	for _, k := range []string{"DB_PORT", "DB_TLS"} {
		if _, ok := l(k); ok {
			present++
		}
	}
	legacy, legacyOK := l("MYSQL_DSN")
	if present == 0 {
		if legacyOK && legacy == "" {
			return "", fmt.Errorf("MYSQL_DSN must not be empty")
		}
		if !legacyOK {
			return "", nil
		}
		return legacy, nil
	}
	basePresent := 0
	for _, k := range keys {
		if _, ok := l(k); ok {
			basePresent++
		}
	}
	if basePresent != len(keys) {
		return "", fmt.Errorf("DB_HOST, DB_NAME, DB_USER, and DB_PASSWORD must be provided together")
	}
	if legacyOK {
		return "", fmt.Errorf("MYSQL_DSN cannot be combined with DB_* configuration")
	}
	for _, k := range keys {
		if vals[k] == "" || ((k == "DB_HOST" || k == "DB_USER" || k == "DB_NAME") && strings.TrimSpace(vals[k]) == "") {
			if k == "DB_USER" || k == "DB_NAME" {
				return "", fmt.Errorf("%s must not be empty", k)
			}
			return "", fmt.Errorf("%s is required", k)
		}
	}
	host := vals["DB_HOST"]
	if strings.ContainsAny(host, " \t\r\n[]") || strings.Contains(host, "://") {
		return "", fmt.Errorf("DB_HOST is invalid")
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", fmt.Errorf("DB_HOST is invalid")
	}
	port := "3306"
	if v, ok := l("DB_PORT"); ok {
		if v == "" {
			return "", fmt.Errorf("DB_PORT must not be empty")
		}
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("DB_PORT is invalid")
		}
		port = v
	}
	tls, err := boolEnv(l, "DB_TLS", false)
	if err != nil {
		return "", err
	}
	mc := mysql.NewConfig()
	mc.User, mc.Passwd, mc.DBName = vals["DB_USER"], vals["DB_PASSWORD"], vals["DB_NAME"]
	mc.Net, mc.Addr = "tcp", net.JoinHostPort(host, port)
	mc.ParseTime, mc.Loc = true, time.UTC
	mc.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	if tls {
		mc.TLSConfig = "true"
	}
	return mc.FormatDSN(), nil
}

func defaultDataDir(l func(string) (string, bool)) string {
	if v, ok := l("DATA_DIR"); ok && v != "" {
		return v
	}
	return "/var/lib/quotewisp"
}
func optional(l func(string) (string, bool), k string) (string, bool) { return l(k) }
func str(l func(string) (string, bool), k, d string, empty bool) (string, error) {
	v, ok := l(k)
	if !ok {
		return d, nil
	}
	if v == "" && !empty {
		return "", fmt.Errorf("%s must not be empty", k)
	}
	return v, nil
}
func integer(l func(string) (string, bool), k string, d, min int) (int, error) {
	v, ok := l(k)
	if !ok {
		return d, nil
	}
	if v == "" {
		return 0, fmt.Errorf("%s must not be empty", k)
	}
	n, e := strconv.ParseInt(v, 10, 32)
	if e != nil || n < int64(min) {
		return 0, fmt.Errorf("%s is invalid", k)
	}
	return int(n), nil
}
func duration(l func(string) (string, bool), k string, d time.Duration) (time.Duration, error) {
	v, ok := l(k)
	if !ok {
		return d, nil
	}
	x, e := time.ParseDuration(v)
	if e != nil || x <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", k)
	}
	return x, nil
}
func list(l func(string) (string, bool), k string) ([]string, error) {
	v, ok := l(k)
	if !ok || v == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.Trim(p, " \t\r\n")
		if p == "" {
			return nil, fmt.Errorf("%s contains an empty item", k)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}
func origins(l func(string) (string, bool)) ([]string, error) {
	xs, e := list(l, "CORS_ALLOWED_ORIGINS")
	if e != nil {
		return nil, e
	}
	for _, x := range xs {
		u, e := url.Parse(x)
		bad := e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(x, "#") || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || strings.Contains(u.Hostname(), "*") || x == "null" || x == "*"
		if !bad {
			if p := safePort(u); p == "!invalid" {
				bad = true
			} else if p != "" {
				n, e := strconv.Atoi(p)
				bad = e != nil || n < 1 || n > 65535
			}
		}
		if bad {
			return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS contains an invalid origin")
		}
	}
	return xs, nil
}
func safePort(u *url.URL) (p string) {
	defer func() {
		if recover() != nil {
			p = "!invalid"
		}
	}()
	return u.Port()
}
func prefixes(l func(string) (string, bool)) ([]netip.Prefix, error) {
	xs, e := list(l, "TRUSTED_PROXY_CIDRS")
	if e != nil {
		return nil, e
	}
	out := make([]netip.Prefix, 0, len(xs))
	for _, x := range xs {
		p, e := netip.ParsePrefix(x)
		if e != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS contains an invalid prefix")
		}
		if p.Addr().Is4In6() {
			if p.Bits() < 96 {
				return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS contains an invalid mapped IPv6 prefix")
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
func level(l func(string) (string, bool)) (slog.Level, error) {
	v, ok := l("LOG_LEVEL")
	if !ok {
		v = "info"
	}
	switch v {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("LOG_LEVEL is invalid")
}
func validToken(s string) bool {
	if len(s) < 32 || len(s) > 256 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
func boolEnv(l func(string) (string, bool), k string, d bool) (bool, error) {
	v, ok := l(k)
	if !ok {
		return d, nil
	}
	switch v {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s is invalid", k)
	}
}
func siteContact(l func(string) (string, bool)) (string, error) {
	v, _ := l("SITE_CONTACT")
	if v == "" {
		return "", nil
	}
	if len(v) > 256 || !utf8.ValidString(v) {
		return "", fmt.Errorf("SITE_CONTACT is invalid")
	}
	return v, nil
}
func siteRepoURL(l func(string) (string, bool)) (string, error) {
	return optionalHTTPURL(l, "SITE_REPO_URL")
}
func apiBaseURL(l func(string) (string, bool)) (string, error) {
	v, err := optionalHTTPURL(l, "API_BASE_URL")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(v, "/"), nil
}
func apiMetricsURL(l func(string) (string, bool)) (string, error) {
	v, err := optionalHTTPURL(l, "API_METRICS_URL")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", nil
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", fmt.Errorf("API_METRICS_URL is invalid")
	}
	if u.Path == "" || u.Path == "/" {
		return strings.TrimRight(v, "/") + "/metrics", nil
	}
	if u.Path != "/metrics" {
		return "", fmt.Errorf("API_METRICS_URL is invalid")
	}
	return v, nil
}
func optionalHTTPURL(l func(string) (string, bool), k string) (string, error) {
	v, ok := l(k)
	if !ok || v == "" {
		return "", nil
	}
	if len(v) > 512 {
		return "", fmt.Errorf("%s is invalid", k)
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(v, "#") || strings.Contains(v, "?") || u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("%s is invalid", k)
	}
	return v, nil
}
