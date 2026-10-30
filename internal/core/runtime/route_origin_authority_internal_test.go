package runtime

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// TestFinalRouteOrigin_authorityDecidesNotText proves the final route origin
// follows the authority that selected the route, not the selector text.
//
// Two different authorities can select exactly the same expression, and only
// one of them is the proxy's own default route. An override therefore replaces
// the origin even when it selected the same selector, while a route no later
// authority touched keeps the frontend's origin.
func TestFinalRouteOrigin_authorityDecidesNotText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		frontendOrigin   routing.RouteOrigin
		frontendSelector string
		effective        string
		overridden       bool
		want             routing.RouteOrigin
	}{
		{
			name:             "proxy default route nobody replaced",
			frontendOrigin:   routing.RouteOriginDefault,
			frontendSelector: "backend:vendor/a",
			effective:        "backend:vendor/a",
			want:             routing.RouteOriginDefault,
		},
		{
			name:             "override selecting the same expression",
			frontendOrigin:   routing.RouteOriginDefault,
			frontendSelector: "backend:vendor/a",
			effective:        "backend:vendor/a",
			overridden:       true,
			want:             routing.RouteOriginOverride,
		},
		{
			name:             "transform rewriting the selector",
			frontendOrigin:   routing.RouteOriginDefault,
			frontendSelector: "backend:vendor/a",
			effective:        "backend:alias/a",
			want:             routing.RouteOriginOverride,
		},
		{
			name:             "explicit route nobody replaced",
			frontendOrigin:   routing.RouteOriginExplicit,
			frontendSelector: "backend:vendor/a",
			effective:        "backend:vendor/a",
			want:             routing.RouteOriginExplicit,
		},
		{
			name:             "lineage route rewritten by a transform",
			frontendOrigin:   routing.RouteOriginLineage,
			frontendSelector: "backend:vendor/a",
			effective:        "backend:vendor/b",
			want:             routing.RouteOriginOverride,
		},
		{
			name:             "surrounding whitespace is not a rewrite",
			frontendOrigin:   routing.RouteOriginDefault,
			frontendSelector: " backend:vendor/a ",
			effective:        "backend:vendor/a",
			want:             routing.RouteOriginDefault,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := finalRouteOrigin(tc.frontendOrigin, tc.frontendSelector, tc.effective, tc.overridden)
			if got != tc.want {
				t.Fatalf("origin = %q want %q", got, tc.want)
			}
		})
	}
}

// TestFinalRouteOrigin_overrideIsNotTheDefaultRoute proves an overridden route
// never keeps the generation's frozen model binding.
func TestFinalRouteOrigin_overrideIsNotTheDefaultRoute(t *testing.T) {
	t.Parallel()
	origin := finalRouteOrigin(routing.RouteOriginDefault, "backend:vendor/a", "backend:vendor/a", true)
	if origin.IsDefaultRoute() {
		t.Fatal("an overridden route must not report itself as the default route")
	}
}
