package product

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGradedEffortLevelsExactLookup(t *testing.T) {
	t.Parallel()
	spec := claudeTestSpec(t, nil)
	if _, ok := spec.GradedEffortLevels("future-family-17-3"); ok {
		t.Fatalf("undiscovered snapshot must yield no facts")
	}
	rows := []DiscoveredModel{
		{NativeID: "future-family-17-3", DisplayName: "Future", Description: "F", Capabilities: map[string]json.RawMessage{"supportedEffortLevels": json.RawMessage(`["low","high"]`)}},
		{NativeID: "other-family-8-2", DisplayName: "Other", Description: "O", Capabilities: map[string]json.RawMessage{"supportsEffort": json.RawMessage(`true`)}},
		{NativeID: "negative-family-3-4", DisplayName: "Negative", Description: "N", Capabilities: map[string]json.RawMessage{"supportsEffort": json.RawMessage(`false`), "supportedEffortLevels": json.RawMessage(`["low","high"]`)}},
	}
	if err := spec.PublishModels(ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"future-family-17-3", "anthropic/future-family-17.3"} {
		levels, ok := spec.GradedEffortLevels(query)
		if !ok || len(levels) != 2 || levels[0] != "low" || levels[1] != "high" {
			t.Fatalf("query %q: levels=%v ok=%v", query, levels, ok)
		}
	}
	if _, ok := spec.GradedEffortLevels("other-family-8-2"); ok {
		t.Fatalf("binary supportsEffort alone must not yield graded facts")
	}
	// An explicit non-positive or unknown binary declaration contradicts the
	// graded list: it can never be represented as verified support.
	if _, ok := spec.GradedEffortLevels("negative-family-3-4"); ok {
		t.Fatalf("supportsEffort=false with a graded list must not yield facts")
	}
	if _, ok := spec.GradedEffortLevels("missing-family-9-1"); ok {
		t.Fatalf("unproven model must yield no facts")
	}
	if _, ok := spec.GradedEffortLevels("auto"); ok {
		t.Fatalf("alias query must yield no facts")
	}
	if _, ok := spec.GradedEffortLevels(""); ok {
		t.Fatalf("empty query must yield no facts")
	}
}

func TestGradedEffortLevelsInvalidationAndRecovery(t *testing.T) {
	t.Parallel()
	spec := claudeTestSpec(t, nil)
	rows := []DiscoveredModel{
		{NativeID: "future-family-17-3", DisplayName: "Future", Description: "F", Capabilities: map[string]json.RawMessage{"supportsEffort": json.RawMessage(`true`), "supportedEffortLevels": json.RawMessage(`["low","high"]`)}},
	}
	if err := spec.PublishModels(ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if levels, ok := spec.GradedEffortLevels("future-family-17-3"); !ok || len(levels) != 2 {
		t.Fatalf("initial publication: levels=%v ok=%v", levels, ok)
	}
	// A failed refresh invalidates the graded evidence without touching the
	// accepted inventory.
	spec.InvalidateGradedEffortEvidence()
	if _, ok := spec.GradedEffortLevels("future-family-17-3"); ok {
		t.Fatalf("stale evidence must answer unknown")
	}
	if got := spec.ResolveModel("future-family-17-3"); got != "future-family-17-3" {
		t.Fatalf("inventory must stay usable after invalidation, got %q", got)
	}
	// A successful publication restores the facts.
	if err := spec.PublishModels(ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if levels, ok := spec.GradedEffortLevels("future-family-17-3"); !ok || len(levels) != 2 {
		t.Fatalf("recovered publication: levels=%v ok=%v", levels, ok)
	}
}
