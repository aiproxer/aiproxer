package powershellheredoc_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/expansion"
	"github.com/aiproxer/aiproxer/internal/plugins/features/powershellheredoc"
	"github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func TestFeatureBundle_ContributesOneFinalizerOrderedAfterRepair(t *testing.T) {
	t.Parallel()
	cfg, err := decodeConfig(t, "tools:\n  shell_command: { arg: command, shell: powershell }\n")
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	bundle, err := powershellheredoc.FeatureBundle(cfg)
	if err != nil {
		t.Fatalf("FeatureBundle: %v", err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatalf("bundle.Validate: %v", err)
	}
	contributed := aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneToolCallFinalizers)
	if len(contributed) != 1 {
		t.Fatalf("bundle contributes %d finalizers, want exactly 1", len(contributed))
	}
	fin := contributed[0]
	if fin.ID() != powershellheredoc.ID {
		t.Fatalf("finalizer ID=%q, want %q", fin.ID(), powershellheredoc.ID)
	}
	if fin.Order() != 46 {
		t.Fatalf("Order()=%d, want 46", fin.Order())
	}
	if fin.Order() <= toolcallrepair.DefaultFinalizerOrder || fin.Order() <= expansion.FinalizerOrder {
		t.Fatalf("order %d must sort after tool-call repair %d and path virtualization %d",
			fin.Order(), toolcallrepair.DefaultFinalizerOrder, expansion.FinalizerOrder)
	}
}

func TestFeatureBundle_RejectsEmptyConfig(t *testing.T) {
	t.Parallel()
	if _, err := powershellheredoc.FeatureBundle(powershellheredoc.Config{}); err == nil {
		t.Fatal("FeatureBundle accepted a zero config with no tools")
	}
}
