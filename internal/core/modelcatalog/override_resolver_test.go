package modelcatalog_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

func TestOverrideResolver_pairWinsOverModel(t *testing.T) {
	t.Parallel()
	set := modelcatalog.OverrideSet{
		Pair: map[string]modelcatalog.ModelFacts{
			"openai-responses:gpt-4o": {
				Tools:     modelcatalog.CapabilityUnsupported,
				Source:    modelcatalog.FactSourcePairOverride,
				MatchKind: modelcatalog.MatchExact,
			},
		},
		Model: map[string]modelcatalog.ModelFacts{
			"gpt-4o": {
				Tools:     modelcatalog.CapabilitySupported,
				Source:    modelcatalog.FactSourceModelOverride,
				MatchKind: modelcatalog.MatchExact,
			},
		},
	}
	r := modelcatalog.NewOverrideResolver(set)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: "gpt-4o"}}
	f, ok := r.Resolve(cand)
	if !ok {
		t.Fatal("expected override hit")
	}
	if f.Source != modelcatalog.FactSourcePairOverride {
		t.Fatalf("source: %v", f.Source)
	}
	if f.Tools != modelcatalog.CapabilityUnsupported {
		t.Fatalf("tools: %v", f.Tools)
	}
}

func TestOverrideResolver_modelFallback(t *testing.T) {
	t.Parallel()
	set := modelcatalog.OverrideSet{
		Model: map[string]modelcatalog.ModelFacts{
			"gpt-4o-mini": {
				Tools:     modelcatalog.CapabilitySupported,
				Source:    modelcatalog.FactSourceModelOverride,
				MatchKind: modelcatalog.MatchExact,
			},
		},
	}
	r := modelcatalog.NewOverrideResolver(set)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "openai-responses", Model: "gpt-4o-mini"}}
	f, ok := r.Resolve(cand)
	if !ok {
		t.Fatal("expected model override")
	}
	if f.Source != modelcatalog.FactSourceModelOverride {
		t.Fatalf("source: %v", f.Source)
	}
}

func TestOverrideResolver_unknownModelAccepted(t *testing.T) {
	t.Parallel()
	set := modelcatalog.OverrideSet{
		Model: map[string]modelcatalog.ModelFacts{
			"custom/operator-model": {
				Tools:             modelcatalog.CapabilityUnknown,
				StructuredOutputs: modelcatalog.CapabilityUnsupported,
				Source:            modelcatalog.FactSourceModelOverride,
				MatchKind:         modelcatalog.MatchExact,
			},
		},
	}
	r := modelcatalog.NewOverrideResolver(set)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "bedrock", Model: "custom/operator-model"}}
	f, ok := r.Resolve(cand)
	if !ok {
		t.Fatal("expected override for unknown catalog id")
	}
	if f.Source != modelcatalog.FactSourceModelOverride {
		t.Fatal(f.Source)
	}
	if f.StructuredOutputs != modelcatalog.CapabilityUnsupported {
		t.Fatal(f.StructuredOutputs)
	}
}

func TestOverrideResolver_noHit(t *testing.T) {
	t.Parallel()
	set := modelcatalog.OverrideSet{}
	r := modelcatalog.NewOverrideResolver(set)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "x", Model: "y"}}
	_, ok := r.Resolve(cand)
	if ok {
		t.Fatal("expected no override")
	}
}
