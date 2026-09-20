package admin

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxMetricsBody = 256 << 10

type APIUsage struct {
	OK         bool
	Random     uint64
	UUID       uint64
	Categories uint64
}

func (u APIUsage) Total() uint64 {
	return u.Random + u.UUID + u.Categories
}

func fetchAPIUsage(ctx context.Context, metricsURL string) APIUsage {
	if metricsURL == "" {
		return APIUsage{}
	}
	client := &http.Client{
		Timeout: 600 * time.Millisecond,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return APIUsage{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return APIUsage{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return APIUsage{}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetricsBody+1))
	if err != nil || len(body) > maxMetricsBody {
		return APIUsage{}
	}
	return parseAPIRequestTotals(body)
}

func parseAPIRequestTotals(body []byte) APIUsage {
	var out APIUsage
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), 256<<10)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "# TYPE sentence_api_http_requests_total counter" {
			out.OK = true
			continue
		}
		if !strings.HasPrefix(line, "sentence_api_http_requests_total{") {
			continue
		}
		route := prometheusLabel(line, "route")
		n, ok := prometheusValue(line)
		if !ok {
			continue
		}
		switch route {
		case "/api/v1":
			out.Random += n
		case "/api/v1/sentences/{uuid}":
			out.UUID += n
		case "/api/v1/categories":
			out.Categories += n
		default:
			continue
		}
		out.OK = true
	}
	if scanner.Err() != nil {
		return APIUsage{}
	}
	return out
}

func prometheusLabel(line, key string) string {
	needle := key + `="`
	i := strings.Index(line, needle)
	if i < 0 {
		return ""
	}
	rest := line[i+len(needle):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	s, err := strconv.Unquote(`"` + rest[:j] + `"`)
	if err != nil {
		return rest[:j]
	}
	return s
}

func prometheusValue(line string) (uint64, bool) {
	i := strings.LastIndexByte(line, '}')
	if i < 0 || i+1 >= len(line) {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(line[i+1:]), 10, 64)
	return n, err == nil
}
