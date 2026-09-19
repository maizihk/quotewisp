package admin

import (
	"testing"
)

var knownAdminRoutes = map[string]struct{}{
	"/admin/": {}, "/admin/login": {}, "/admin/logout": {}, "/admin/password": {},
	"/admin/submissions": {}, "/admin/submissions/{id}": {}, "/admin/submissions/{id}/approve": {}, "/admin/submissions/{id}/reject": {},
	"/admin/sentences": {}, "/admin/sentences/new": {}, "/admin/sentences/{uuid}": {}, "/admin/sentences/{uuid}/disable": {}, "/admin/sentences/{uuid}/enable": {},
	"/admin/categories": {}, "/admin/categories/new": {}, "/admin/categories/{code}": {}, "/admin/categories/{code}/disable": {}, "/admin/categories/{code}/enable": {},
	"/admin/users": {}, "/admin/users/{id}/reset-password": {}, "/admin/users/{id}/disable": {}, "/admin/users/{id}/enable": {},
	"/admin/settings": {},
	"unmatched":       {},
}

func FuzzAdminUUIDPath(f *testing.F) {
	f.Add("550e8400-e29b-41d4-a716-446655440000")
	f.Add("not-a-uuid")
	f.Add("")
	f.Add("a")
	f.Fuzz(func(t *testing.T, segment string) {
		if len(segment) > 256 {
			t.Skip()
		}
		paths := []string{
			"/admin/sentences/" + segment,
			"/admin/sentences/" + segment + "/disable",
			"/admin/sentences/" + segment + "/enable",
		}
		for _, path := range paths {
			got := RouteName(path)
			if _, ok := knownAdminRoutes[got]; !ok {
				t.Fatalf("unknown route label %q for path %q", got, path)
			}
		}
	})
}
