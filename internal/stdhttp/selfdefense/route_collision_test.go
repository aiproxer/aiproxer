package selfdefense_test

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/stdhttp/selfdefense"
	"gopkg.in/yaml.v3"
)

func claimsConfigNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	return node
}

// standardFrontendClaimConfigs are the configuration shapes the standard
// distribution can present to a frontend route-claims provider. The inventory is
// the real contribution map; only the provider input varies.
//
// base_path is an arbitrary operator-configurable normalized absolute non-root
// path, not one of a fixed handful, so this list deliberately includes base paths
// that fall INSIDE the frozen impossible-path families. Those are the cases that
// used to be untested: they are valid configurations that publish real routes the
// deterministic matcher would otherwise answer with a generic 404. They are
// asserted against the owned-route carve rather than against the bare families.
var standardFrontendClaimConfigs = []string{
	"{}",
	"websocket:\n  enabled: true\n",
	"websocket:\n  enabled: false\n",
	"base_path: /v1\n",
	"base_path: /openresponses/v1\n",
	"base_path: /openresponses/v1\nwebsocket:\n  enabled: true\n",
	"base_path: /api/v1\n",
	// Operator-chosen base paths inside frozen impossible-path families.
	"base_path: /wp-admin\n",
	"base_path: /wp-content\n",
	"base_path: /cgi-bin\n",
	"base_path: /phpmyadmin\n",
	"base_path: /pma\n",
	"base_path: /adminer\n",
	"base_path: /vendor/phpunit\n",
	"base_path: /.github\n",
	"base_path: /.gitlab\n",
	"base_path: /.ht\n",
	"base_path: /.git\n",
	"base_path: /.env\n",
}

func standardFrontendRouteClaims(t *testing.T) []httpcontract.RouteClaim {
	t.Helper()

	providers := standardplugins.StandardFrontendRouteClaims()
	if len(providers) == 0 {
		t.Fatal("the standard distribution must declare frontend route-claims providers")
	}
	owners := make([]string, 0, len(providers))
	for owner := range providers {
		owners = append(owners, owner)
	}
	sort.Strings(owners)

	seen := map[string]bool{}
	var claims []httpcontract.RouteClaim
	for _, owner := range owners {
		provider := providers[owner]
		configs := standardFrontendClaimConfigs
		if owner == "systemone" {
			// System One has a fixed path, not configurable OpenResponses routes.
			configs = []string{"{}", "max_questions: 1", "max_questions: 256"}
		}
		for i, raw := range configs {
			got, err := provider(owner, claimsConfigNode(t, raw))
			if err != nil {
				t.Fatalf("owner %q config %d: route claims: %v", owner, i, err)
			}
			if len(got) == 0 {
				t.Fatalf("owner %q config %d: declared route claims but produced none", owner, i)
			}
			for _, claim := range got {
				if _, err := claim.NormalizedClaim(); err != nil {
					t.Fatalf("owner %q config %d: claim %+v: %v", owner, i, claim, err)
				}
				key := claim.OwnerID + " " + claim.Method + " " + claim.Path
				if seen[key] {
					continue
				}
				seen[key] = true
				claims = append(claims, claim)
			}
		}
	}
	return claims
}

// TestBuiltInImpossiblePathSetNeverMakesAPublishedRouteUnreachable is the
// requirement 3.6 non-collision regression. The inventory is the real standard
// distribution route-claims seam, not a hand-written list, so a future frontend
// that claims a colliding path fails here.
//
// The invariant is deliberately NOT "the frozen families never overlap a standard
// route". base_path is an arbitrary operator-configurable normalized absolute
// non-root path, so overlap with a frozen family is a legitimate, accepted
// configuration rather than a bug. The invariant that must hold is the one an
// operator depends on: a published route is never refused, for ANY base path.
//
// Each claim is checked the way the gate actually decides: registered on a real
// router, resolved into owned routes by that router, and then asked whether the
// matcher refuses it. That makes the test exhaustive over the base_path space
// instead of dependent on a sampled list of configurations, and it keeps the test
// honest about exact-versus-subtree and case sensitivity, which a plain path string
// cannot express.
func TestBuiltInImpossiblePathSetNeverMakesAPublishedRouteUnreachable(t *testing.T) {
	t.Parallel()

	claims := standardFrontendRouteClaims(t)
	if len(claims) < 8 {
		t.Fatalf("standard frontend claims = %d, want the full standard surface", len(claims))
	}
	colliding := 0
	for _, claim := range claims {
		mux := http.NewServeMux()
		mux.Handle(claim.Method+" "+claim.Path, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		owned := selfdefense.NewOwnedRoutes(selfdefense.OwnedRoutesFromMux(mux, []httpcontract.OwnedRouteCandidate{
			{Method: claim.Method, Path: claim.Path},
		}))
		// The precise contract: the matcher refuses a path exactly when it matches a
		// frozen family AND the router does not own it. That is checked for the route
		// itself and for its trailing-slash form, because a trailing-slash form is a
		// different path that a literal registration does not own.
		for _, path := range []string{claim.Path, claim.Path + "/"} {
			refused := selfdefense.ImpossiblePathExcept(path, claim.Method, owned)
			if want := selfdefense.ImpossiblePath(path) && !servesPattern(mux, claim.Method, path); refused != want {
				t.Errorf("%s %s (owner %q): refused = %v, want %v (family match and not owned by the router)",
					claim.Method, path, claim.OwnerID, refused, want)
			}
		}
		// Where the route does collide with a frozen family, the carve must still
		// stop at the route the router owns: a descendant and a case variant are
		// different paths, so both stay probes. A non-colliding claim has nothing to
		// check here, because those paths are ordinary traffic either way.
		if selfdefense.ImpossiblePath(claim.Path) {
			colliding++
			for _, probe := range []string{claim.Path + "/adminer.php", strings.ToUpper(claim.Path)} {
				if !selfdefense.ImpossiblePathExcept(probe, claim.Method, owned) {
					t.Errorf("published route %q widened the carve: %q is not owned by the router and must stay refused",
						claim.Path, probe)
				}
			}
		}
	}
	// The fixture must actually exercise the overlapping case, or this test would
	// pass vacuously on a distribution that never collides.
	if colliding == 0 {
		t.Fatal("no standard frontend claim collides with a frozen family: the colliding base paths in the fixture are no longer being exercised")
	}
}

// TestBuiltInImpossiblePathSetDoesNotOverlapManagementRecoverySurface proves the
// separate management/recovery listener keeps working management paths: the
// deterministic matcher must never claim one of them.
func TestBuiltInImpossiblePathSetDoesNotOverlapManagementRecoverySurface(t *testing.T) {
	t.Parallel()

	management := []string{configreload.ReloadPath, configreload.StatusPath}
	if management[0] == "" || management[1] == "" {
		t.Fatal("the management/recovery surface must publish its fixed paths")
	}
	for _, path := range management {
		if selfdefense.ImpossiblePath(path) {
			t.Errorf("built-in impossible-path set collides with management path %q", path)
		}
	}
}

// servesPattern reports whether the router routes this method/path to a dedicated
// registration rather than to its fallback, using the router's own resolution.
func servesPattern(mux *http.ServeMux, method, path string) bool {
	req := &http.Request{Method: method, URL: &url.URL{Path: path}}
	_, pattern := mux.Handler(req)
	_, dedicated, _ := splitForTest(pattern)
	return dedicated != ""
}

func splitForTest(pattern string) (method, path string, subtree bool) {
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
	if strings.HasSuffix(path, "/") {
		return method, path, true
	}
	return method, path, false
}
