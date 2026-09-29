package admin

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"sentence-api/internal/web/auth"
	"sentence-api/internal/web/render"
	"sentence-api/internal/web/store"
)

//go:embed templates/*.html
var templateFS embed.FS

const pageSize = 50

// Metrics records admin-side counters.
type Metrics interface {
	LoginAttempt(result string)
	Review(action, result string)
	SentenceChange(action, result string)
	CategoryChange(action, result string)
}

// Deps configures the admin HTTP handler.
type Deps struct {
	Store          *store.Store
	Renderer       *render.Renderer
	Logger         *slog.Logger
	Metrics        Metrics
	Logins         *auth.LoginLimiter
	Tokens         *render.FormTokens
	CookieSecure   bool
	OnChange       func()
	APIMetricsURL  string
	APIUsage       func(context.Context) APIUsage
	Imports        ImportService
	ImportMaxBytes int64
}

type handler struct {
	store          *store.Store
	renderer       *render.Renderer
	logger         *slog.Logger
	metrics        Metrics
	logins         *auth.LoginLimiter
	tokens         *render.FormTokens
	cookieSecure   bool
	onChange       func()
	apiMetricsURL  string
	apiUsage       func(context.Context) APIUsage
	imports        ImportService
	importMaxBytes int64
	dummyHash      string
	mux            *http.ServeMux
	settingsMu     sync.Mutex
}

type noopMetrics struct{}

func (noopMetrics) LoginAttempt(string)           {}
func (noopMetrics) Review(string, string)         {}
func (noopMetrics) SentenceChange(string, string) {}
func (noopMetrics) CategoryChange(string, string) {}

// New returns an HTTP handler for all /admin routes.
func New(d Deps) (http.Handler, error) {
	if d.Store == nil || d.Renderer == nil {
		return nil, fmt.Errorf("admin: Store and Renderer are required")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Metrics == nil {
		d.Metrics = noopMetrics{}
	}
	if d.Logins == nil {
		d.Logins = auth.NewLoginLimiter(10, 5, 15*time.Minute, 100000)
	}
	if d.Tokens == nil {
		return nil, fmt.Errorf("admin: Tokens is required")
	}
	if d.OnChange == nil {
		d.OnChange = func() {}
	}
	dummyHash, err := auth.HashPassword("__dummy_timing_password__")
	if err != nil {
		return nil, fmt.Errorf("admin: dummy hash: %w", err)
	}
	h := &handler{
		store:          d.Store,
		renderer:       d.Renderer,
		logger:         d.Logger,
		metrics:        d.Metrics,
		logins:         d.Logins,
		tokens:         d.Tokens,
		cookieSecure:   d.CookieSecure,
		onChange:       d.OnChange,
		apiMetricsURL:  d.APIMetricsURL,
		apiUsage:       d.APIUsage,
		imports:        d.Imports,
		importMaxBytes: d.ImportMaxBytes,
		dummyHash:      dummyHash,
	}
	h.registerRoutes()
	return h.serve(), nil
}

var routeRules = []struct {
	re    *regexp.Regexp
	label string
}{
	{regexp.MustCompile(`^/admin/login$`), "/admin/login"},
	{regexp.MustCompile(`^/admin/logout$`), "/admin/logout"},
	{regexp.MustCompile(`^/admin/password$`), "/admin/password"},
	{regexp.MustCompile(`^/admin/submissions/\d+/approve$`), "/admin/submissions/{id}/approve"},
	{regexp.MustCompile(`^/admin/submissions/\d+/reject$`), "/admin/submissions/{id}/reject"},
	{regexp.MustCompile(`^/admin/submissions/\d+$`), "/admin/submissions/{id}"},
	{regexp.MustCompile(`^/admin/submissions$`), "/admin/submissions"},
	{regexp.MustCompile(`^/admin/sentences/new$`), "/admin/sentences/new"},
	{regexp.MustCompile(`^/admin/sentences/[0-9a-f-]{36}/disable$`), "/admin/sentences/{uuid}/disable"},
	{regexp.MustCompile(`^/admin/sentences/[0-9a-f-]{36}/enable$`), "/admin/sentences/{uuid}/enable"},
	{regexp.MustCompile(`^/admin/sentences/[0-9a-f-]{36}$`), "/admin/sentences/{uuid}"},
	{regexp.MustCompile(`^/admin/sentences$`), "/admin/sentences"},
	{regexp.MustCompile(`^/admin/categories/new$`), "/admin/categories/new"},
	{regexp.MustCompile(`^/admin/categories/[a-z0-9][a-z0-9_-]*/disable$`), "/admin/categories/{code}/disable"},
	{regexp.MustCompile(`^/admin/categories/[a-z0-9][a-z0-9_-]*/enable$`), "/admin/categories/{code}/enable"},
	{regexp.MustCompile(`^/admin/categories/[a-z0-9][a-z0-9_-]*$`), "/admin/categories/{code}"},
	{regexp.MustCompile(`^/admin/categories$`), "/admin/categories"},
	{regexp.MustCompile(`^/admin/users/\d+/reset-password$`), "/admin/users/{id}/reset-password"},
	{regexp.MustCompile(`^/admin/users/\d+/disable$`), "/admin/users/{id}/disable"},
	{regexp.MustCompile(`^/admin/users/\d+/enable$`), "/admin/users/{id}/enable"},
	{regexp.MustCompile(`^/admin/users$`), "/admin/users"},
	{regexp.MustCompile(`^/admin/settings$`), "/admin/settings"},
	{regexp.MustCompile(`^/admin/imports/[0-9A-Za-z_-]+/confirm$`), "/admin/imports/{id}/confirm"},
	{regexp.MustCompile(`^/admin/imports/[0-9A-Za-z_-]+/cancel$`), "/admin/imports/{id}/cancel"},
	{regexp.MustCompile(`^/admin/imports/[0-9A-Za-z_-]+/refresh$`), "/admin/imports/{id}/refresh"},
	{regexp.MustCompile(`^/admin/imports/[0-9A-Za-z_-]+$`), "/admin/imports/{id}"},
	{regexp.MustCompile(`^/admin/imports$`), "/admin/imports"},
	{regexp.MustCompile(`^/admin/?$`), "/admin/"},
}

// RouteName returns a low-cardinality route label for metrics and access logs.
func RouteName(path string) string {
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	if path == "/admin" {
		return "/admin/"
	}
	for _, rule := range routeRules {
		if rule.re.MatchString(path) {
			return rule.label
		}
	}
	if len(path) >= 6 && path[:6] == "/admin" {
		return "unmatched"
	}
	return "unmatched"
}
