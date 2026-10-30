package stdhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit"
)

// selfDefenseRefusalBody is the exact body the ingress self-defense gate writes for
// a generic 404. Go's own router writes "404 page not found", and so does a handler
// that deliberately refuses, so the body is what separates "the deterministic gate
// refused this before the router" from "the router had no route for this". The gate
// is otherwise indistinguishable from a 404 by status alone, which is the point of
// it.
const selfDefenseRefusalBody = "Not Found\n"

// refusedBySelfDefense reports whether the composed stack refused this request with
// the gate's generic response rather than letting the router answer it.
func refusedBySelfDefense(t *testing.T, handler http.Handler, method, target string) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "203.0.113.10:443"
	handler.ServeHTTP(rec, req)
	return rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), selfDefenseRefusalBody)
}

func ownedRouteConfig(t *testing.T, mutate func(*config.Config)) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{MaxRequestBodyBytes: 1 << 20, Address: "127.0.0.1:0"},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return cfg
}

// composeOwnedRouteStack composes the REAL standard stack for cfg, so the owned-route
// inventory is resolved by the same code path production uses: every mount runs, and
// the resolver then asks the router that was just built.
func composeOwnedRouteStack(t *testing.T, cfg *config.Config) http.Handler {
	t.Helper()
	ex := runtime.TestExecutor()
	// The candidates come from the same config chokepoint the generation projection
	// uses, so this exercises the real seam: candidates in, router resolution, gate
	// behavior out.
	var candidates []httpcontract.OwnedRouteCandidate
	for _, path := range config.ConfiguredDataPlanePaths(cfg) {
		candidates = append(candidates, httpcontract.OwnedRouteCandidate{Path: path})
	}
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, pluginreg.NewRegistry()),
		Security: HTTPSecurityInput{
			SelfDefense: SelfDefenseSecurityInput{
				Policy:               &ingressdefense.Policy{Enabled: true, AuthFailures: 5, FailureWindow: time.Minute, InitialQuarantine: time.Minute, MaxQuarantine: time.Hour},
				State:                ownedRouteState(t),
				Resolver:             GeoIPResolverConfig{Source: "direct"},
				ImpossiblePaths:      true,
				OwnedRouteCandidates: candidates,
			},
		},
	}
	h, err := ComposeStandardHTTP(t.Context(), cfg, testkit.DiscardLogger(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}
	return h
}

func ownedRouteState(t *testing.T) *ingressdefense.State {
	t.Helper()
	state, err := ingressdefense.NewState(ingressdefense.StateLimits{MaxEntries: 1024, StateTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// TestComposedStackEnabledMountInsideAFrozenFamilyIsServed is the end-to-end
// statement of the fix, run against the real composed production stack and the real
// router: a legitimate mount inside a frozen impossible family must be served, not
// answered with the gate's generic 404, and its neighbours must still be refused.
//
// The status alone cannot tell the gate's refusal from the router's own 404, because
// they are deliberately identical. The body can: the gate writes "Not Found", the
// router writes "404 page not found". That is what makes this an assertion about the
// gate having intercepted a real route rather than about any 404 at all.
func TestComposedStackEnabledMountInsideAFrozenFamilyIsServed(t *testing.T) {
	t.Parallel()

	handler := composeOwnedRouteStack(t, ownedRouteConfig(t, func(cfg *config.Config) {
		cfg.Diagnostics.Enabled = true
		cfg.Diagnostics.HealthPath = "/wp-admin"
	}))

	// The mounted route exists in the router, so the gate must not have intercepted it.
	if refusedBySelfDefense(t, handler, http.MethodGet, "/wp-admin") {
		t.Error("an enabled mount inside a frozen family was refused by the self-defense gate")
	}
	// Neighbours in the same family that are not routes stay refused and keep scoring.
	for _, probe := range []string{"/wp-admin/adminer.php", "/wp-admin/.env", "/wp-admin/responses/.env"} {
		if !refusedBySelfDefense(t, handler, http.MethodPost, probe) {
			t.Errorf("probe %q beside a published route must still be refused by the gate", probe)
		}
	}
	// The router is case-sensitive, so a case variant is a different path and a probe.
	if !refusedBySelfDefense(t, handler, http.MethodGet, "/WP-ADMIN") {
		t.Error("a case variant of a published route must still be refused by the gate")
	}
}

// TestComposedStackDisabledMountCarvesNothing is the second form of the same defect,
// run end to end. A diagnostics block with enabled: false mounts nothing, so its
// configured path must not become a carve even though the value is present in the
// configuration and sits inside a frozen family. Reading the configuration is not the
// same as the router owning the path, and the pair of tests together is what proves
// ownership is the router's answer and not the configuration's.
func TestComposedStackDisabledMountCarvesNothing(t *testing.T) {
	t.Parallel()

	handler := composeOwnedRouteStack(t, ownedRouteConfig(t, func(cfg *config.Config) {
		cfg.Diagnostics.Enabled = false
		cfg.Diagnostics.HealthPath = "/wp-admin"
	}))

	for _, probe := range []string{"/wp-admin", "/wp-admin/adminer.php"} {
		if !refusedBySelfDefense(t, handler, http.MethodGet, probe) {
			t.Errorf("%q: a configured path whose feature is disabled must not carve, so the gate must still refuse it", probe)
		}
	}
}

// TestComposedStackSubtreeMountCarvesItsSubtreeOnly keeps the subtree semantics
// honest end to end. pprof is registered with a trailing separator, so it really owns
// its whole subtree and the gate must let that subtree through to it. A neighbouring
// family, and a probe beside the separately-mounted exact health path, are not owned
// and must still be refused.
func TestComposedStackSubtreeMountCarvesItsSubtreeOnly(t *testing.T) {
	t.Parallel()

	handler := composeOwnedRouteStack(t, ownedRouteConfig(t, func(cfg *config.Config) {
		cfg.Diagnostics.Enabled = true
		cfg.Diagnostics.HealthPath = "/phpmyadmin"
		cfg.Diagnostics.PprofPath = "/.github"
	}))

	// Inside the trailing-slash mount: owned by the router, so not gate-refused. The
	// subtree really is served by pprof, which answers unknown profiles itself; what
	// matters here is that the deterministic gate did not intercept the request.
	for _, owned := range []string{"/.github/", "/.github/heap", "/.github/workflows"} {
		if refusedBySelfDefense(t, handler, http.MethodGet, owned) {
			t.Errorf("%q is inside a trailing-slash mount and must not be refused by the gate", owned)
		}
	}
	// A different family is untouched.
	if !refusedBySelfDefense(t, handler, http.MethodGet, "/.gitlab/workflows") {
		t.Error("a dotfile family with no owned mount must still be refused")
	}
	// The health mount at /phpmyadmin is a LITERAL registration, so it owns no subtree
	// and its neighbour is still a probe. This is the exact-versus-subtree distinction.
	if !refusedBySelfDefense(t, handler, http.MethodGet, "/phpmyadmin/index.php") {
		t.Error("an exact mount must not carve its subtree")
	}
}
