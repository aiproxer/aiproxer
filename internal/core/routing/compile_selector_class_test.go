package routing_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestCompileSelector_plannerOwnsClassSemantics(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "aliasbe:m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name           string
		raw            string
		defaultBackend string
		aliases        *routing.AliasResolver
		check          func(t *testing.T, sel *routing.Selector)
	}{
		{
			name: "direct",
			raw:  "dirbe:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				p := mustPrimary(t, sel)
				if p.Backend != "dirbe" || p.Model != "m" {
					t.Fatalf("direct primary: %+v", p)
				}
			},
		},
		{
			name:           "modelOnly",
			raw:            "gpt-4",
			defaultBackend: "modelbe",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				p := mustPrimary(t, sel)
				if p.Backend != "modelbe" || p.Model != "gpt-4" {
					t.Fatalf("model-only defaulting: %+v", p)
				}
			},
		},
		{
			name:    "alias",
			raw:     "cheap",
			aliases: aliases,
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				p := mustPrimary(t, sel)
				if p.Backend != "aliasbe" || p.Model != "m" {
					t.Fatalf("alias expansion: %+v", p)
				}
			},
		},
		{
			name: "failover",
			raw:  "a:m|b:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				if len(sel.Alternatives) != 2 {
					t.Fatalf("failover arms: %d", len(sel.Alternatives))
				}
				if sel.Alternatives[0].Primary == nil || sel.Alternatives[0].Primary.Backend != "a" {
					t.Fatalf("failover[0]: %+v", sel.Alternatives[0].Primary)
				}
				if sel.Alternatives[1].Primary == nil || sel.Alternatives[1].Primary.Backend != "b" {
					t.Fatalf("failover[1]: %+v", sel.Alternatives[1].Primary)
				}
			},
		},
		{
			name: "weighted",
			raw:  "[weight=1]w1:m^[weight=1]w2:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				w := mustWeighted(t, sel)
				if len(w.Branches) != 2 || w.Branches[0].Target.Backend != "w1" || w.Branches[1].Target.Backend != "w2" {
					t.Fatalf("weighted branches: %+v", w.Branches)
				}
			},
		},
		{
			name: "race",
			raw:  "rleft:m!rright:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				p := mustParallel(t, sel)
				if len(p.Branches) != 2 || p.Branches[0].Target.Backend != "rleft" || p.Branches[1].Target.Backend != "rright" {
					t.Fatalf("race legs: %+v", p.Branches)
				}
			},
		},
		{
			name: "ttft",
			raw:  "{ttft_timeout=60}ttftbe:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				if sel.GlobalTTFTTimeout == nil || *sel.GlobalTTFTTimeout != 60*time.Second {
					t.Fatalf("global TTFT: %#v", sel.GlobalTTFTTimeout)
				}
				p := mustPrimary(t, sel)
				if p.Backend != "ttftbe" {
					t.Fatalf("ttft primary: %+v", p)
				}
			},
		},
		{
			name: "affinity",
			raw:  "{affinity=session}affbe:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				if sel.Affinity != routing.AffinitySession {
					t.Fatalf("affinity: got %q", sel.Affinity)
				}
			},
		},
		{
			name: "first",
			raw:  "[first]cheap:m^[weight=100]expensive:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				w := mustWeighted(t, sel)
				if !w.Branches[0].IsFirst || w.Branches[1].IsFirst {
					t.Fatalf("[first] flags: %+v", w.Branches)
				}
			},
		},
		{
			name: "thinker",
			raw:  "[thinker]think:m^exec:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				w := mustWeighted(t, sel)
				if !w.Branches[0].IsThinker || w.Branches[1].IsThinker {
					t.Fatalf("[thinker] flags: %+v", w.Branches)
				}
			},
		},
		{
			name: "handicap",
			raw:  "hleft:m![handicap=200]hright:m",
			check: func(t *testing.T, sel *routing.Selector) {
				t.Helper()
				p := mustParallel(t, sel)
				if p.Branches[0].Handicap != 0 {
					t.Fatalf("left handicap: %+v", p.Branches[0])
				}
				if p.Branches[1].Handicap != 200*time.Second {
					t.Fatalf("right handicap: %+v", p.Branches[1])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := routing.CompileSelector(tc.raw, tc.aliases, tc.defaultBackend)
			if err != nil {
				t.Fatalf("CompileSelector(%q): %v", tc.raw, err)
			}
			tc.check(t, sel)
		})
	}
}

func mustPrimary(t *testing.T, sel *routing.Selector) routing.Primary {
	t.Helper()
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Primary == nil {
		t.Fatalf("expected primary alternative, got %+v", sel)
	}
	return *sel.Alternatives[0].Primary
}

func mustWeighted(t *testing.T, sel *routing.Selector) *routing.Weighted {
	t.Helper()
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Weighted == nil {
		t.Fatalf("expected weighted alternative, got %+v", sel)
	}
	return sel.Alternatives[0].Weighted
}

func mustParallel(t *testing.T, sel *routing.Selector) *routing.Parallel {
	t.Helper()
	if sel == nil || len(sel.Alternatives) == 0 || sel.Alternatives[0].Parallel == nil {
		t.Fatalf("expected parallel alternative, got %+v", sel)
	}
	return sel.Alternatives[0].Parallel
}
