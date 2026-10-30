package selfdefense

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// realMuxServed builds the gate in front of a real mux whose handlers answer 200, so
// a delegated request is observable as 200 and a refused one as 404.
func realMuxServed(t *testing.T, owned []httpcontract.OwnedRoute, downstream http.Handler) http.Handler {
	t.Helper()
	in := testInput(t, testState(t), &gateObserver{})
	in.OwnedRoutes = owned
	return Middleware(in, downstream)
}

// clientAddr is the source address the carve cases use, so the served and the
// refused assertions compare on one identity.
var clientAddr = netip.MustParseAddr("203.0.113.10")

// realMuxCarve builds a real http.ServeMux with the registration forms the
// distribution actually uses, resolves the owned routes from THAT router, and
// returns a gate in front of it. Every assertion in this file is therefore about
// the routing model the application has, not a downstream stub that returns 200
// for anything.
func realMuxCarve(t *testing.T, owned []httpcontract.OwnedRoute) (http.Handler, *gateObserver, *ingressdefense.State) {
	t.Helper()
	state := testState(t)
	observer := &gateObserver{}
	in := testInput(t, state, observer)
	in.OwnedRoutes = owned
	return Middleware(in, neverDownstream(t)), observer, state
}

// TestCarveModelsTheRouterNotACaseInsensitiveSubtree is the regression for the
// carve-boundary defect. The carve must reproduce the real http.ServeMux, which is
// case-sensitive and distinguishes an exact registration from a subtree one. An
// earlier []string root model treated every published path as a case-insensitive
// subtree, so with a real route POST /wp-admin/responses it also let through
// /wp-admin/responses/.env (no such route) and /WP-ADMIN/responses (a different
// path). Both are impossible-path probes and must still be refused and score.
func TestCarveModelsTheRouterNotACaseInsensitiveSubtree(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("POST /wp-admin/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	mux.Handle("GET /wp-admin/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Method: http.MethodPost, Path: "/wp-admin/responses"},
		{Method: http.MethodGet, Path: "/wp-admin/responses"},
	})
	if len(owned) != 2 {
		t.Fatalf("owned routes = %+v, want one exact route per method", owned)
	}
	for _, route := range owned {
		if route.Subtree {
			t.Errorf("route %+v must be exact: ServeMux registered a literal path, not a subtree", route)
		}
	}

	handler, observer, state := realMuxCarve(t, owned)
	// Only paths the router does NOT own belong in this table; the served case is
	// TestOwnedRouteServesItsOwnRequest, which runs against a real downstream.
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"descendant of an exact route", http.MethodPost, "/wp-admin/responses/.env"},
		{"case variant", http.MethodPost, "/WP-ADMIN/responses"},
		{"mixed-case variant", http.MethodPost, "/Wp-Admin/Responses"},
		{"trailing segment", http.MethodPost, "/wp-admin/responsesX"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer.denials = nil
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newRequest(t, tc.method, tc.path, testClient))
			// neverDownstream fails the test if a request is delegated, so a non-404
			// status here has already failed. Assert the refusal and the scoring.
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if servesRealRoute(mux, tc.method, tc.path) {
				t.Fatalf("fixture: %s %s must NOT be a real route", tc.method, tc.path)
			}
			if len(observer.denials) == 0 {
				t.Errorf("%s %s is not a real route but recorded no impossible-path denial", tc.method, tc.path)
			}
			if !state.IsQuarantined(clientAddr, testNow) {
				t.Errorf("%s %s is not a real route but did not score a quarantine", tc.method, tc.path)
			}
		})
	}
	// And the owned route itself must be carved, checked through the matcher.
	if ImpossiblePathExcept("/wp-admin/responses", http.MethodPost, NewOwnedRoutes(owned)) {
		t.Error("the owned exact route must not be refused")
	}
}

// TestOwnedRouteServesItsOwnRequest proves the positive case against the real
// router: a request the mux actually owns must reach it, not be refused.
func TestOwnedRouteServesItsOwnRequest(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	served := make(chan string, 4)
	mux.Handle("POST /wp-admin/responses", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{{Method: http.MethodPost, Path: "/wp-admin/responses"}})

	state := testState(t)
	observer := &gateObserver{}
	in := testInput(t, state, observer)
	in.OwnedRoutes = owned
	handler := Middleware(in, mux)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin/responses", testClient))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a real route must be served", rec.Code)
	}
	if p := <-served; p != "/wp-admin/responses" {
		t.Fatalf("served path = %q", p)
	}
	if state.IsQuarantined(clientAddr, testNow) {
		t.Fatal("a served route must not score a probe offense")
	}
	if len(observer.denials) != 0 {
		t.Fatalf("denials = %v, want none for a served route", observer.denials)
	}
}

// TestSubtreeCarveOnlyWhenTheRealMountIsASubtree proves subtree semantics come
// from the router, not from a path-shaped guess. A trailing-slash registration
// really does own its subtree, and a bare path really does not.
func TestSubtreeCarveOnlyWhenTheRealMountIsASubtree(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	served := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/debug/pprof/", served)
	mux.Handle("/wp-admin", served)

	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Path: "/debug/pprof"},
		{Path: "/wp-admin"},
	})
	subtree := map[string]bool{}
	for _, route := range owned {
		subtree[route.Path] = route.Subtree
	}
	if !subtree["/debug/pprof/"] {
		t.Errorf("owned routes %+v: /debug/pprof/ is a trailing-slash registration and must be a subtree", owned)
	}
	if subtree["/wp-admin"] {
		t.Errorf("owned routes %+v: /wp-admin is a literal registration and must be exact", owned)
	}

	handler := realMuxServed(t, owned, mux)
	// Under the real subtree mount: carved, because the router owns the subtree.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodGet, "/debug/pprof/heap", testClient))
	if !servesRealRoute(mux, http.MethodGet, "/debug/pprof/heap") {
		t.Fatal("fixture: /debug/pprof/heap must be a real route")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("/debug/pprof/heap status = %d, want 200: a real subtree mount owns its whole subtree", rec.Code)
	}
	// Under the exact mount: NOT carved. The router owns no subtree here, so this is
	// an impossible-path probe and is refused.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodGet, "/wp-admin/anything", testClient))
	if servesRealRoute(mux, http.MethodGet, "/wp-admin/anything") {
		t.Fatal("fixture: /wp-admin/anything must NOT be a real route")
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("/wp-admin/anything status = %d, want 404: an exact mount owns no subtree", rec.Code)
	}
}

// TestDisabledMountCarvesNothing is the second form of the same defect. Reading
// the configured path is not the same as the router owning it: a diagnostics block
// with enabled: false mounts nothing, so its path must not carve anything even
// though the value is present in the configuration.
func TestDisabledMountCarvesNothing(t *testing.T) {
	t.Parallel()

	// A mux with nothing mounted at all: exactly the disabled case.
	mux := http.NewServeMux()
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{{Path: "/wp-admin"}})
	if len(owned) != 0 {
		t.Fatalf("owned routes = %+v, want none: nothing is mounted, so nothing is owned", owned)
	}

	handler, _, state := realMuxCarve(t, owned)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin", testClient))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !state.IsQuarantined(clientAddr, testNow) {
		t.Fatal("an unmounted configured path must not carve, so this must score a probe offense")
	}
}

// TestMethodScopedCarveKeepsOtherMethodsRefused keeps the carve faithful about
// methods the way the router is: a route registered for POST is owned for POST
// only, and a request the router would not route there is still a probe.
func TestMethodScopedCarveKeepsOtherMethodsRefused(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("POST /wp-admin/responses", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Method: http.MethodPost, Path: "/wp-admin/responses"},
	})

	state := testState(t)
	observer := &gateObserver{}
	in := testInput(t, state, observer)
	in.OwnedRoutes = owned
	handler := Middleware(in, mux)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin/responses", testClient))
	if rec.Code != http.StatusOK {
		t.Errorf("POST status = %d, want 200: the registered method is owned", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, newRequest(t, http.MethodDelete, "/wp-admin/responses", testClient))
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE status = %d, want 404: the carve is scoped to the registered method", rec.Code)
	}
	if !state.IsQuarantined(clientAddr, testNow) {
		t.Error("a method the router does not own must still score a probe offense")
	}
}

// TestCarveNeverMatchesTheCatchAllRoot guards the resolver against the router's
// fallback. Every path the mux does not own resolves to some fallback handler, so a
// resolver that treated a fallback as ownership would carve the entire data plane.
func TestCarveNeverMatchesTheCatchAllRoot(t *testing.T) {
	t.Parallel()

	// Both shapes: a "/" registration and an empty mux.
	for name, build := range map[string]func() *http.ServeMux{
		"root registration": func() *http.ServeMux {
			mux := http.NewServeMux()
			mux.Handle("/", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			return mux
		},
		"no registration": func() *http.ServeMux { return http.NewServeMux() },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mux := build()
			owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
				{Path: "/wp-admin"},
				{Method: http.MethodPost, Path: "/wp-admin/responses"},
				{Path: "/debug/pprof"},
			})
			for _, route := range owned {
				if route.Path == "/" || route.Path == "" {
					t.Errorf("owned routes %+v must not contain a fallback pattern", owned)
				}
			}
			// Whatever was (not) resolved, nothing may carve /wp-admin/**.
			handler, _, _ := realMuxCarve(t, owned)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin/responses/.env", testClient))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404: a fallback must never become a carve", rec.Code)
			}
		})
	}
}

// TestDegenerateOwnedRoutesCarveNothing keeps a malformed inventory from widening
// the carve.
func TestDegenerateOwnedRoutesCarveNothing(t *testing.T) {
	t.Parallel()

	for _, owned := range [][]httpcontract.OwnedRoute{
		nil,
		{},
		{{Path: ""}},
		{{Path: "/"}},
		{{Path: "  "}},
		{{Path: "relative/path"}},
		{{Path: "/wp-admin"}},
	} {
		handler, _, state := realMuxCarve(t, owned)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, newRequest(t, http.MethodPost, "/wp-admin/responses", testClient))
		if rec.Code != http.StatusNotFound {
			t.Errorf("owned %+v: status = %d, want 404", owned, rec.Code)
		}
		if !state.IsQuarantined(clientAddr, testNow) {
			t.Errorf("owned %+v: a degenerate inventory must not carve", owned)
		}
	}
}

// servesRealRoute reports whether the router actually owns this method/path, using
// the router's own resolution. It is the oracle the tests above compare against, so
// a carve can never be justified by a stub that answers 200 for everything.
func servesRealRoute(mux *http.ServeMux, method, path string) bool {
	req := &http.Request{Method: method, URL: &url.URL{Path: path}}
	_, pattern := mux.Handler(req)
	return isDedicatedPattern(pattern)
}
