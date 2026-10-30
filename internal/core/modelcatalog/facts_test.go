package modelcatalog_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
)

func TestModelFacts_catalogExactMatch(t *testing.T) {
	t.Parallel()
	f := modelcatalog.ModelFacts{
		Tools:             modelcatalog.CapabilitySupported,
		StructuredOutputs: modelcatalog.CapabilityUnknown,
		ContextLimit:      modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 128000},
		Source:            modelcatalog.FactSourceCatalog,
		MatchKind:         modelcatalog.MatchExact,
	}
	if f.Source != modelcatalog.FactSourceCatalog {
		t.Fatalf("source: %v", f.Source)
	}
	if f.MatchKind != modelcatalog.MatchExact {
		t.Fatalf("match: %v", f.MatchKind)
	}
	if f.Tools != modelcatalog.CapabilitySupported {
		t.Fatalf("tools: %v", f.Tools)
	}
	if f.StructuredOutputs != modelcatalog.CapabilityUnknown {
		t.Fatalf("structured: want unknown, got %v", f.StructuredOutputs)
	}
	if f.ContextLimit.State != modelcatalog.LimitPresent || f.ContextLimit.Tokens != 128000 {
		t.Fatalf("limit: %+v", f.ContextLimit)
	}
}

func TestModelFacts_modelOverride(t *testing.T) {
	t.Parallel()
	f := modelcatalog.ModelFacts{
		Tools:     modelcatalog.CapabilityUnsupported,
		Source:    modelcatalog.FactSourceModelOverride,
		MatchKind: modelcatalog.MatchExact,
	}
	if f.Source != modelcatalog.FactSourceModelOverride {
		t.Fatal(f.Source)
	}
	if f.Tools != modelcatalog.CapabilityUnsupported {
		t.Fatal("unsupported must differ from unknown for explicit operator denial")
	}
}

func TestModelFacts_backendModelPairOverride(t *testing.T) {
	t.Parallel()
	f := modelcatalog.ModelFacts{
		Tools:     modelcatalog.CapabilitySupported,
		Source:    modelcatalog.FactSourcePairOverride,
		MatchKind: modelcatalog.MatchExact,
	}
	if f.Source != modelcatalog.FactSourcePairOverride {
		t.Fatal(f.Source)
	}
}

func TestModelFacts_backendDeclaration(t *testing.T) {
	t.Parallel()
	f := modelcatalog.ModelFacts{
		Tools:             modelcatalog.CapabilitySupported,
		StructuredOutputs: modelcatalog.CapabilityUnsupported,
		ContextLimit:      modelcatalog.LimitFact{State: modelcatalog.LimitUnknown},
		Source:            modelcatalog.FactSourceBackendDeclaration,
		MatchKind:         modelcatalog.MatchNone,
	}
	if f.Source != modelcatalog.FactSourceBackendDeclaration {
		t.Fatal(f.Source)
	}
	if f.ContextLimit.State != modelcatalog.LimitUnknown {
		t.Fatalf("limit unknown: %+v", f.ContextLimit)
	}
}

func TestModelFacts_noCatalogMatch(t *testing.T) {
	t.Parallel()
	f := modelcatalog.ModelFacts{
		Source:    modelcatalog.FactSourceBackendDeclaration,
		MatchKind: modelcatalog.MatchNoMatch,
	}
	if f.Source != modelcatalog.FactSourceBackendDeclaration || f.MatchKind != modelcatalog.MatchNoMatch {
		t.Fatalf("%+v", f)
	}
}

func TestModelFacts_ambiguousCatalogMatch(t *testing.T) {
	t.Parallel()
	f := modelcatalog.ModelFacts{
		Source:    modelcatalog.FactSourceBackendDeclaration,
		MatchKind: modelcatalog.MatchAmbiguous,
	}
	if f.MatchKind != modelcatalog.MatchAmbiguous {
		t.Fatal(f.MatchKind)
	}
}

func TestLimitFact_unknownVsUnsupported(t *testing.T) {
	t.Parallel()
	u := modelcatalog.LimitFact{State: modelcatalog.LimitUnknown}
	x := modelcatalog.LimitFact{State: modelcatalog.LimitUnsupported}
	if u.State == x.State {
		t.Fatal("unknown and unsupported limits must be distinct states")
	}
}
