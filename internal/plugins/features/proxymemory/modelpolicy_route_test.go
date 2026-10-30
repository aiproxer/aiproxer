package proxymemory_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	pm "github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
)

// The derivation replaces the target and nothing else: the operator's route
// metadata survives it. The canonical parser is the authority here, so the
// assertions compare the parsed selector and not the derived string.
func TestRouteDerivationPreservesOperatorMetadata(t *testing.T) {
	t.Parallel()

	for _, route := range []string{
		"{affinity=session}openai:gpt-5-mini",
		"{ttft_timeout=60}openai:gpt-5-mini",
		"{affinity=session}[ttft_timeout=5]openai:gpt-5-mini?reasoning_effort=medium",
		"{affinity=session}gpt-5-mini",
	} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			before, err := routing.Parse(route)
			if err != nil {
				t.Fatalf("routing.Parse(%q): %v", route, err)
			}
			got, err := pm.DeriveRouteSelector(pm.PhaseExtraction, route, "anthropic", "")
			if err != nil {
				t.Fatalf("DeriveRouteSelector(%q): %v", route, err)
			}
			after, err := routing.Parse(got)
			if err != nil {
				t.Fatalf("valid route %q derives invalid %q: %v", route, got, err)
			}
			if before.Affinity != after.Affinity {
				t.Errorf("%q derives %q: affinity %q becomes %q", route, got, before.Affinity, after.Affinity)
			}
			if before.GlobalTTFTTimeout != nil && (after.GlobalTTFTTimeout == nil || *after.GlobalTTFTTimeout != *before.GlobalTTFTTimeout) {
				t.Errorf("%q derives %q: the global timeout is lost", route, got)
			}
			if before.Alternatives[0].Primary.TTFTTimeout != nil {
				if after.Alternatives[0].Primary.TTFTTimeout == nil || *after.Alternatives[0].Primary.TTFTTimeout != *before.Alternatives[0].Primary.TTFTTimeout {
					t.Errorf("%q derives %q: the leaf timeout is lost", route, got)
				}
			}
			if want := before.Alternatives[0].Primary.Params.Get("reasoning_effort"); after.Alternatives[0].Primary.Params.Get("reasoning_effort") != want {
				t.Errorf("%q derives %q: the leaf query is lost", route, got)
			}
		})
	}
}

// A route that names more than one target has no single operator default to
// keep, so a new target is refused. The canonical grammar reads the
// separators at the top level of the whole selector, so a separator behind a
// leaf query still names a second target.
func TestRouteDerivationRefusesMultiTargetRoutes(t *testing.T) {
	t.Parallel()

	for _, route := range []string{
		"openai:gpt-5-mini|anthropic:claude",
		"openai:gpt-5-mini^anthropic:claude",
		"openai:gpt-5-mini!anthropic:claude",
		"openai:gpt-5-mini?temperature=0.1|anthropic:claude",
		"openai:gpt-5-mini?temperature=0.1^anthropic:claude",
		"openai:gpt-5-mini?temperature=0.1!anthropic:claude",
	} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			if _, err := routing.Parse(route); err != nil {
				t.Fatalf("routing.Parse(%q): %v", route, err)
			}
			if got, err := pm.DeriveRouteSelector(pm.PhaseExtraction, route, "", "gpt-5"); err == nil {
				t.Errorf("DeriveRouteSelector(%q) = %q, want an explicit refusal", route, got)
			}
		})
	}
}

// A route that names no new target is the operator's own and stays verbatim,
// including the multi-target and the global-block forms.
func TestRouteDerivationKeepsTheOperatorRouteWithoutANewTarget(t *testing.T) {
	t.Parallel()

	for _, route := range []string{
		"openai:a|anthropic:b",
		"openai:a?temperature=0.1|anthropic:b",
		"{affinity=session}openai:a",
		"{affinity=session}[ttft_timeout=5]openai:a?reasoning_effort=medium",
	} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			got, err := pm.DeriveRouteSelector(pm.PhaseExtraction, route, "", "")
			if err != nil {
				t.Fatalf("DeriveRouteSelector(%q): %v", route, err)
			}
			if got != route {
				t.Errorf("DeriveRouteSelector(%q) = %q, want the operator route verbatim", route, got)
			}
		})
	}
}
