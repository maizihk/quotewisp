package observability

import (
	"io"
	"log/slog"
	"strings"
	"time"
)

func NewJSONLogger(w io.Writer, level string) (*slog.Logger, error) {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info", "":
		l = slog.LevelInfo
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return nil, &invalidLevel{level}
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Value = slog.TimeValue(a.Value.Time().UTC().Truncate(time.Microsecond))
		}
		return a
	}})), nil
}

type invalidLevel struct{ value string }

func (e *invalidLevel) Error() string { return "invalid log level: " + e.value }
