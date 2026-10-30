package selfdefense

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// TestCarveMatchesTheRouterForWildcardPatterns is the differential invariant that
// closes this failure class structurally rather than one metacharacter at a time.
//
// The bundled openai-responses frontend really does claim
// /v1/responses/{id}/cancel, so a published route is not always a literal path. An
// exact string comparison would record that pattern and never match the concrete
// requests it owns, which is the same disagreement as the case-sensitivity and
// disabled-mount bugs: the mux says owned, the carve says not.
//
// Every pattern is checked against a real http.ServeMux, and the carve's answer must
// equal the router's answer for the same table of method/path pairs.
func TestCarveMatchesTheRouterForWildcardPatterns(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"POST /v1/responses/{id}/cancel",
		"POST /a/{rest...}",
		"POST /b/{$}",
		"POST /c/{one}/{two}",
		"POST /wp-admin/{tenant}/responses",
		"POST /wp-admin/responses",
		"/phpmyadmin",
		"/phpmyadmin/",
		"GET /.github/pprof/",
	}
	probes := []string{
		"/v1/responses/abc/cancel", "/v1/responses//cancel", "/v1/responses/a/b/cancel",
		"/a", "/a/", "/a/x", "/a/x/y",
		"/b", "/b/", "/b/x",
		"/c/1/2", "/c/1", "/c/1/2/3", "/c//2",
		"/wp-admin/acme/responses", "/wp-admin/{tenant}/responses", "/wp-admin/responses",
		"/wp-admin/responses/.env", "/wp-admin/tenant/responses/.env",
		"/wp-admin", "/phpmyadmin", "/phpmyadmin/", "/phpmyadmin/x",
		"/.github/pprof/heap", "/.github/workflows", "/.github/pprof",
	}

	mux := http.NewServeMux()
	candidates := make([]httpcontract.OwnedRouteCandidate, 0, len(patterns))
	for _, pattern := range patterns {
		mux.Handle(pattern, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		method, path := "", pattern
		if before, rest, found := strings.Cut(pattern, " "); found {
			method, path = before, rest
		}
		// Probe the exact path, a concrete descendant, and a concrete substitution
		// for a wildcard segment, because that is what a real client sends.
		candidates = append(candidates, httpcontract.OwnedRouteCandidate{Method: method, Path: path})
		if segs := strings.Split(strings.Trim(path, "/"), "/"); len(segs) > 0 {
			concrete := make([]string, 0, len(segs))
			for _, s := range segs {
				if strings.HasPrefix(s, "{") {
					concrete = append(concrete, "probe1")
					continue
				}
				concrete = append(concrete, s)
			}
			candidates = append(candidates, httpcontract.OwnedRouteCandidate{Method: method, Path: "/" + strings.Join(concrete, "/")})
		}
	}

	owned := NewOwnedRoutes(OwnedRoutesFromMux(mux, candidates))

	mismatches := 0
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, probe := range probes {
			routerOwns := routerServes(mux, method, probe)
			carveCovers := owned.Covers(method, probe)
			if routerOwns != carveCovers {
				mismatches++
				t.Errorf("%s %s: router owns = %v, carve covers = %v", method, probe, routerOwns, carveCovers)
			}
		}
	}
	if mismatches == 0 && !anyWildcardDiscovered(t, owned) {
		t.Fatal("no wildcard pattern was resolved, so this test would pass without exercising wildcards")
	}
}

// anyWildcardDiscovered keeps the table honest: a resolver that silently dropped every
// wildcard would make the differential trivially true.
func anyWildcardDiscovered(t *testing.T, owned OwnedRoutes) bool {
	t.Helper()
	for _, route := range owned.routes {
		if strings.Contains(route.Path, "{") {
			return true
		}
	}
	return false
}

// routerServes reports whether the router routes this method/path to a dedicated
// registration rather than to its fallback.
func routerServes(mux *http.ServeMux, method, path string) bool {
	_, pattern := mux.Handler(&http.Request{Method: method, URL: &url.URL{Path: path}})
	_, dedicated, _ := splitPatternForTest(pattern)
	return dedicated != ""
}

func splitPatternForTest(pattern string) (method, path string, subtree bool) {
	if pattern == "" {
		return "", "", false
	}
	method, path = "", pattern
	if before, rest, found := strings.Cut(pattern, " "); found {
		method, path = before, strings.TrimSpace(rest)
	}
	if path == "" || path == "/" {
		return "", "", false
	}
	return method, path, strings.HasSuffix(path, "/")
}

// TestWildcardRouteInAFrozenFamilyIsServedNotShadowed is the concrete P1 the reviewer
// reported, as a behavioural test: a route published under a colliding base through a
// wildcard segment is owned by the router, so the gate must serve it rather than
// returning its generic 404.
func TestWildcardRouteInAFrozenFamilyIsServedNotShadowed(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("POST /wp-admin/{tenant}/responses", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Method: http.MethodPost, Path: "/wp-admin/{tenant}/responses"},
		{Method: http.MethodPost, Path: "/wp-admin/probe1/responses"},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/wp-admin/acme/responses", nil)
	req.RemoteAddr = testClient
	in := testInput(t, testState(t), &gateObserver{})
	in.OwnedRoutes = owned
	Middleware(in, mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("wildcard route in a frozen family: status = %d, want 200; the gate shadowed a route the router serves", rec.Code)
	}
}

// TestOperatorSuppliedPathsAreLiteral is the second P1, in the form the review
// recommended: the operator-facing grammar is closed once, so no ServeMux pattern
// syntax and no query-like character can be configured as a literal mount path.
//
// The bundled frontends' OWN claims may legitimately contain wildcards, so the rule
// applies to operator input, not to NormalizePath in general.
func TestOperatorSuppliedPathsAreLiteral(t *testing.T) {
	t.Parallel()

	for _, p := range []string{
		"/wp-admin/{tenant}", "/wp-admin/{rest...}", "/wp-admin/{$}", "/wp-admin/{",
		"/wp-admin/?health", "/wp-admin/#frag", "/wp-admin/a*b", "/wp-admin/a\\b",
		"/wp-admin/%77p",
	} {
		if err := httpcontract.ValidateOperatorRoutePath("test", p); err == nil {
			t.Errorf("ValidateOperatorRoutePath(%q) accepted it; an operator path must be a literal route, not a ServeMux pattern", p)
		}
	}
	for _, p := range []string{"/v1", "/openresponses/v1", "/admin/attempts", "/wp-admin"} {
		if err := httpcontract.ValidateOperatorRoutePath("test", p); err != nil {
			t.Errorf("ValidateOperatorRoutePath(%q) = %v, want nil for an ordinary literal path", p, err)
		}
	}
}
