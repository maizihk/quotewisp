package httpmw

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func ClientIP(trusted []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct := remoteIP(r.RemoteAddr)
		ip := direct
		if direct.IsValid() && contains(trusted, direct) {
			if p, ok := forwardedIP(r.Header.Values("X-Forwarded-For"), trusted); ok {
				ip = p
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip.String())))
	})
}

func ClientIPFrom(ctx context.Context) string {
	v, _ := ctx.Value(clientIPKey).(string)
	return v
}

func remoteIP(s string) netip.Addr {
	host, _, e := net.SplitHostPort(s)
	if e != nil {
		host = s
	}
	a, e := netip.ParseAddr(host)
	if e != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func forwardedIP(lines []string, trusted []netip.Prefix) (netip.Addr, bool) {
	var all []netip.Addr
	for _, line := range lines {
		for _, part := range strings.Split(line, ",") {
			part = strings.Trim(part, " \t")
			if part == "" || len(all) >= 32 {
				return netip.Addr{}, false
			}
			a, e := netip.ParseAddr(part)
			if e != nil || a.Zone() != "" {
				return netip.Addr{}, false
			}
			all = append(all, a.Unmap())
		}
	}
	if len(all) == 0 {
		return netip.Addr{}, false
	}
	for i := len(all) - 1; i >= 0; i-- {
		if !contains(trusted, all[i]) {
			return all[i], true
		}
	}
	return all[0], true
}
