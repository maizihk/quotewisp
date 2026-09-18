package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Mode uint8

const (
	ModeService Mode = iota
	ModeImport
	ModeMigrate
)

type Config struct {
	HTTPAddr             string
	MYSQLDSN             string
	MySQLMaxOpenConns    int
	MySQLMaxIdleConns    int
	MySQLConnMaxLifetime time.Duration
	SnapshotPollInterval time.Duration
	SnapshotLoadTimeout  time.Duration
	ImportTimeout        time.Duration
	ReloadToken          string
	CORSAllowedOrigins   []string
	TrustedProxyCIDRs    []netip.Prefix
	LogLevel             slog.Level
	ShutdownTimeout      time.Duration
}

func Load(mode Mode) (Config, error) { return load(mode, os.LookupEnv) }

func load(mode Mode, lookup func(string) (string, bool)) (Config, error) {
	c := Config{HTTPAddr: ":8080", MySQLMaxOpenConns: 10, MySQLMaxIdleConns: 5, MySQLConnMaxLifetime: 30 * time.Minute, SnapshotPollInterval: time.Minute, SnapshotLoadTimeout: 30 * time.Second, ImportTimeout: 120 * time.Second, LogLevel: slog.LevelInfo, ShutdownTimeout: 10 * time.Second}
	var err error
	if c.MYSQLDSN, err = required(lookup, "MYSQL_DSN"); err != nil {
		return c, err
	}
	if mode == ModeService {
		if c.HTTPAddr, err = str(lookup, "HTTP_ADDR", c.HTTPAddr, false); err != nil {
			return c, err
		}
		if _, port, e := net.SplitHostPort(c.HTTPAddr); e != nil {
			return c, fmt.Errorf("HTTP_ADDR is invalid")
		} else if n, e := strconv.Atoi(port); e != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("HTTP_ADDR port is invalid")
		}
	}
	if mode == ModeService || mode == ModeImport {
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
	if mode == ModeService {
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
