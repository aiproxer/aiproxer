package selfdefense

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// TestConfiguredPercentEscapesCannotShadowOrCarveAnything is the P1 regression.
//
// http.ServeMux unescapes both the registered pattern and the request path, so a
// pattern written with escapes owns a request whose DECODED path is the unescaped
// form. A configured path that contains a percent escape therefore publishes a route
// whose decoded form is invisible to the carve: the real request has
// r.URL.Path == "/wp-admin/responses", which matches the frozen /wp-admin family and
// would be refused before the frontend.
//
// The fix is to forbid percent escapes in operator-configurable paths, so a
// registered pattern and its decoded request path can never disagree. This test
// pins the resulting invariant: no configured path can produce an escape, so the
// carved surface and the router's own answers always agree.
func TestConfiguredPercentEscapesCannotShadowOrCarveAnything(t *testing.T) {
	t.Parallel()

	for _, escaped := range []string{"/%77p-admin", "/phpmyadmin%2fadminer", "/cgi-bin/%2e%2e/etc"} {
		if _, err := httpcontract.NormalizePath(escaped); err == nil {
			t.Errorf("NormalizePath(%q) accepted a percent escape; a configured route must not be able to "+
				"publish a path whose decoded form the carve cannot see", escaped)
		}
	}
}

// TestEscapeFreeConfiguredRouteIsCarvedFromTheRealRouter is the positive companion:
// once escapes are impossible, a colliding route and the request that reaches it
// agree in the same space, and the carve resolves it from the real router.
func TestEscapeFreeConfiguredRouteIsCarvedFromTheRealRouter(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("POST /wp-admin/responses", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Method: http.MethodPost, Path: "/wp-admin/responses"},
	})
	if len(owned) == 0 {
		t.Fatal("an escape-free colliding route must resolve to an owned route")
	}
	// A real request carrying escapes in the target still decodes to the owned path,
	// so the gate must let it through exactly as the router would.
	req := httptest.NewRequest(http.MethodPost, "/wp%2dadmin/responses", nil)
	req.RemoteAddr = testClient
	in := testInput(t, testState(t), &gateObserver{})
	in.OwnedRoutes = owned
	rec := httptest.NewRecorder()
	Middleware(in, mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("escaped-target request to a real route: status = %d, want 200 (decoded path is owned)", rec.Code)
	}
}

// TestGetRegistrationAlsoOwnsHead pins Go's ServeMux rule that a GET pattern matches
// HEAD as well. An owned GET route that failed to cover HEAD would let the gate refuse
// a request the router routes, which is the same class of bug as the case sensitivity
// and the disabled-mount bugs: the carve must agree with the router.
func TestGetRegistrationAlsoOwnsHead(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET /wp-admin/responses", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	owned := OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Method: http.MethodGet, Path: "/wp-admin/responses"},
	})
	if len(owned) != 1 {
		t.Fatalf("owned routes = %+v, want one", owned)
	}
	// The router really does route HEAD there.
	_, pattern := mux.Handler(&http.Request{Method: http.MethodHead, URL: &url.URL{Path: "/wp-admin/responses"}})
	if pattern != "GET /wp-admin/responses" {
		t.Fatalf("fixture: HEAD is owned by %q, expected the GET registration", pattern)
	}
	// So the carve must cover it, and the gate must not intercept the request.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/wp-admin/responses", nil)
	req.RemoteAddr = testClient
	in := testInput(t, testState(t), &gateObserver{})
	in.OwnedRoutes = owned
	Middleware(in, mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD to a GET route: status = %d, want 200: a GET registration owns HEAD", rec.Code)
	}
	// A method the router does NOT route there is still a probe.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/wp-admin/responses", nil)
	req.RemoteAddr = testClient
	Middleware(in, mux).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE: status = %d, want 404: a GET registration owns no other method", rec.Code)
	}
}

// TestMethodlessRouteIsDiscoveredBehindAMethodSpecificOne is the P3 regression.
// ServeMux allows a methodless registration to coexist with method-specific ones for
// the same path. Probing such a candidate with GET returns the GET registration, so a
// resolver that probes only GET never learns the methodless registration exists -- and
// then a request the router routes to the methodless handler, such as DELETE, is not
// owned and gets intercepted.
func TestMethodlessRouteIsDiscoveredBehindAMethodSpecificOne(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("/wp-admin/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	mux.Handle("GET /wp-admin/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	mux.Handle("POST /wp-admin/responses", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	// The router really does route DELETE to the methodless registration.
	if _, pattern := mux.Handler(&http.Request{Method: http.MethodDelete, URL: &url.URL{Path: "/wp-admin/responses"}}); pattern != "/wp-admin/responses" {
		t.Fatalf("fixture: DELETE is owned by %q, expected the methodless registration", pattern)
	}

	// A candidate with no method must therefore resolve to BOTH registrations.
	owned := NewOwnedRoutes(OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
		{Path: "/wp-admin/responses"},
	}))
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete, http.MethodPut} {
		if !owned.Covers(method, "/wp-admin/responses") {
			t.Errorf("owned routes %+v do not cover %s, which the router routes there", owned.routes, method)
		}
	}
}

// TestMethodlessCandidateStillResolvesAPlainMethodlessRoute keeps the simple case
// working: a methodless registration with nothing shadowing it is still discovered.
func TestMethodlessCandidateStillResolvesAPlainMethodlessRoute(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("/phpmyadmin", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	owned := NewOwnedRoutes(OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{{Path: "/phpmyadmin"}}))
	if !owned.Covers(http.MethodGet, "/phpmyadmin") || !owned.Covers(http.MethodDelete, "/phpmyadmin") {
		t.Errorf("owned routes %+v must cover every method of a methodless registration", owned.routes)
	}
}
