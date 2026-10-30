package modelcatalog_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestNormalizeStripOneProviderPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "strip_amazon_prefix", in: "amazon/claude-sonnet-4", want: "claude-sonnet-4"},
		{name: "already_short_id", in: "claude-sonnet-4", want: "claude-sonnet-4"},
		{name: "strip_anthropic_prefix", in: "anthropic/claude-3-5-sonnet", want: "claude-3-5-sonnet"},
		{name: "no_slash_unchanged", in: "single", want: "single"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := modelcatalog.NormalizeStripOneProviderPrefix(tt.in); got != tt.want {
				t.Errorf("NormalizeStripOneProviderPrefix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatcher_Match_exact(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"anthropic/claude-3-5-sonnet": {Source: modelcatalog.FactSourceCatalog},
	})
	m := modelcatalog.DefaultMatcher{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "anthropic/claude-3-5-sonnet"}}
	got := m.Match(cand, idx)
	if got.Kind != modelcatalog.MatchExact {
		t.Fatalf("Kind: %v", got.Kind)
	}
	if got.MatchedID != "anthropic/claude-3-5-sonnet" {
		t.Fatalf("MatchedID: %q", got.MatchedID)
	}
	if got.InputModel != "anthropic/claude-3-5-sonnet" {
		t.Fatalf("InputModel: %q", got.InputModel)
	}
}

func TestMatcher_Match_nonExactPrefixedRoute(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"anthropic/claude-sonnet-4": {Source: modelcatalog.FactSourceCatalog},
	})
	m := modelcatalog.DefaultMatcher{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "amazon/claude-sonnet-4"}}
	got := m.Match(cand, idx)
	if got.Kind != modelcatalog.MatchNonExact {
		t.Fatalf("Kind: %v want MatchNonExact", got.Kind)
	}
	if got.MatchedID != "anthropic/claude-sonnet-4" {
		t.Fatalf("MatchedID: %q", got.MatchedID)
	}
	if got.InputModel != "amazon/claude-sonnet-4" {
		t.Fatalf("InputModel: %q", got.InputModel)
	}
}

func TestMatcher_Match_ambiguous(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"anthropic/claude-sonnet-4": {Source: modelcatalog.FactSourceCatalog},
		"amazon/claude-sonnet-4":    {Source: modelcatalog.FactSourceCatalog},
	})
	m := modelcatalog.DefaultMatcher{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "claude-sonnet-4"}}
	got := m.Match(cand, idx)
	if got.Kind != modelcatalog.MatchAmbiguous {
		t.Fatalf("Kind: %v want MatchAmbiguous", got.Kind)
	}
	if got.MatchedID != "" {
		t.Fatalf("MatchedID should be empty, got %q", got.MatchedID)
	}
	want := []string{"amazon/claude-sonnet-4", "anthropic/claude-sonnet-4"}
	if len(got.Candidates) != len(want) {
		t.Fatalf("Candidates: %v", got.Candidates)
	}
	for i := range want {
		if got.Candidates[i] != want[i] {
			t.Fatalf("Candidates[%d] = %q want %q", i, got.Candidates[i], want[i])
		}
	}
}

func TestMatcher_Match_noMatch(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/gpt-4o": {Source: modelcatalog.FactSourceCatalog},
	})
	m := modelcatalog.DefaultMatcher{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "unknown/model-id"}}
	got := m.Match(cand, idx)
	if got.Kind != modelcatalog.MatchNoMatch {
		t.Fatalf("Kind: %v", got.Kind)
	}
	if got.MatchedID != "" {
		t.Fatalf("MatchedID: %q", got.MatchedID)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("Candidates: %v", got.Candidates)
	}
}

func TestMatcher_Match_nilIndex(t *testing.T) {
	t.Parallel()
	m := modelcatalog.DefaultMatcher{}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Model: "x"}}
	got := m.Match(cand, nil)
	if got.Kind != modelcatalog.MatchNoMatch {
		t.Fatal(got.Kind)
	}
}

func TestDefaultMatcher_emptyInputNoMatch(t *testing.T) {
	t.Parallel()
	idx := modelcatalog.NewSnapshotIndex(map[string]modelcatalog.ModelFacts{
		"openai/": {Tools: modelcatalog.CapabilitySupported},
	})
	got := modelcatalog.DefaultMatcher{}.Match(routing.AttemptCandidate{Primary: routing.Primary{Model: "  "}}, idx)
	if got.Kind != modelcatalog.MatchNoMatch {
		t.Fatalf("kind = %v", got.Kind)
	}
}
