package public

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"

	"sentence-api/internal/web/publicdata"
	"sentence-api/internal/web/ratelimit"
	"sentence-api/internal/web/render"
	"sentence-api/internal/web/store"
)

//go:embed templates/*.html assets/random.js LICENSE.txt
var embeddedFS embed.FS

// Metrics records submission outcomes. Nil is a no-op.
type Metrics interface {
	Submission(result string)
}

type noopMetrics struct{}

func (noopMetrics) Submission(string) {}

// Deps configures the public HTTP handler.
type Deps struct {
	Cache        *publicdata.Cache
	Store        *store.Store
	Renderer     *render.Renderer
	Tokens       *render.FormTokens
	Limiter      *ratelimit.Limiter
	Logger       *slog.Logger
	Metrics      Metrics
	PendingLimit int
}

type handler struct {
	cache        *publicdata.Cache
	store        *store.Store
	renderer     *render.Renderer
	tokens       *render.FormTokens
	limiter      *ratelimit.Limiter
	logger       *slog.Logger
	metrics      Metrics
	pendingLimit int
	indexPages   *template.Template
	docsPages    *template.Template
	submitPages  *template.Template
	donePages    *template.Template
	datasetPages *template.Template
	mux          *http.ServeMux
}

// New returns an HTTP handler for all public routes.
func New(d Deps) (http.Handler, error) {
	if d.Cache == nil || d.Store == nil || d.Renderer == nil || d.Tokens == nil || d.Limiter == nil {
		return nil, fmt.Errorf("public: Cache, Store, Renderer, Tokens, and Limiter are required")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Metrics == nil {
		d.Metrics = noopMetrics{}
	}
	if d.PendingLimit <= 0 {
		d.PendingLimit = 1000
	}
	indexPages, err := d.Renderer.Pages(embeddedFS, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("public: index template: %w", err)
	}
	docsPages, err := d.Renderer.Pages(embeddedFS, "templates/docs.html")
	if err != nil {
		return nil, fmt.Errorf("public: docs template: %w", err)
	}
	submitPages, err := d.Renderer.Pages(embeddedFS, "templates/submit.html")
	if err != nil {
		return nil, fmt.Errorf("public: submit template: %w", err)
	}
	donePages, err := d.Renderer.Pages(embeddedFS, "templates/submit_done.html")
	if err != nil {
		return nil, fmt.Errorf("public: submit_done template: %w", err)
	}
	datasetPages, err := d.Renderer.Pages(embeddedFS, "templates/dataset.html")
	if err != nil {
		return nil, fmt.Errorf("public: dataset template: %w", err)
	}
	h := &handler{
		cache:        d.Cache,
		store:        d.Store,
		renderer:     d.Renderer,
		tokens:       d.Tokens,
		limiter:      d.Limiter,
		logger:       d.Logger,
		metrics:      d.Metrics,
		pendingLimit: d.PendingLimit,
		indexPages:   indexPages,
		docsPages:    docsPages,
		submitPages:  submitPages,
		donePages:    donePages,
		datasetPages: datasetPages,
	}
	h.registerRoutes()
	return h.serve(), nil
}

var routeRules = []struct {
	re    *regexp.Regexp
	label string
}{
	{regexp.MustCompile(`^/$`), "/"},
	{regexp.MustCompile(`^/docs$`), "/docs"},
	{regexp.MustCompile(`^/submit/done$`), "/submit/done"},
	{regexp.MustCompile(`^/submit$`), "/submit"},
	{regexp.MustCompile(`^/dataset/sentences\.json$`), "/dataset/sentences.json"},
	{regexp.MustCompile(`^/dataset/LICENSE\.txt$`), "/dataset/LICENSE.txt"},
	{regexp.MustCompile(`^/dataset$`), "/dataset"},
	{regexp.MustCompile(`^/static/`), "/static/*"},
	{regexp.MustCompile(`^/favicon\.ico$`), "/favicon.ico"},
}

// RouteName returns a low-cardinality route label for metrics and access logs.
func RouteName(path string) string {
	if i := stringsIndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	for _, rule := range routeRules {
		if rule.re.MatchString(path) {
			return rule.label
		}
	}
	return "unmatched"
}

func stringsIndexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
