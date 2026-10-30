package largebody_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

// TestWireEligibility_CatalogCompatibilityWithDeclarationBaseline is an
// independent characterization fixture: it hardcodes the 28 production plane
// IDs in bit order plus their V1 access classes (mirroring the SDK manifest
// declaration order at the consolidation baseline). It pins prefix-stability
// while the serving catalog moves from handwritten literals to generated
// output: the original 28 IDs/positions/classes stay stable and validated
// appends (up to the 32-bit blocker capacity) are permitted (Requirements
// 1.5, 4.5).
var catalogBaselineOrder = [...]string{
	"submit_hooks",
	"request_part_hooks",
	"response_part_hooks",
	"tool_reactors",
	"session_openers",
	"workspace_resolvers",
	"tool_catalog_filters",
	"tool_call_policies",
	"tool_call_finalizers",
	"tool_call_finalization_max_args_bytes",
	"request_transforms",
	"pre_request_handlers",
	"route_hint_providers",
	"completion_gates",
	"attempt_transforms",
	"stream_observer_factories",
	"traffic_observers",
	"usage_observers",
	"raw_capture_sinks",
	"traffic_redactors",
	"compaction_observers",
	"compaction_preservers",
	"secret_guards",
	"secret_guard_execution",
	"local_turn_handlers",
	"terminal_decision_provider",
	"session_classifier",
	"control_tool_provider",
}

var catalogBaselineAccess = map[string]largebody.PlaneAccess{
	"submit_hooks":                          largebody.PlaneAccessCanonicalRequired,
	"request_part_hooks":                    largebody.PlaneAccessCanonicalRequired,
	"response_part_hooks":                   largebody.PlaneAccessResponseOnly,
	"tool_reactors":                         largebody.PlaneAccessCanonicalRequired,
	"session_openers":                       largebody.PlaneAccessMetadataOnly,
	"workspace_resolvers":                   largebody.PlaneAccessMetadataOnly,
	"tool_catalog_filters":                  largebody.PlaneAccessCanonicalRequired,
	"tool_call_policies":                    largebody.PlaneAccessCanonicalRequired,
	"tool_call_finalizers":                  largebody.PlaneAccessCanonicalRequired,
	"tool_call_finalization_max_args_bytes": largebody.PlaneAccessMetadataOnly,
	"request_transforms":                    largebody.PlaneAccessCanonicalRequired,
	"pre_request_handlers":                  largebody.PlaneAccessCanonicalRequired,
	"route_hint_providers":                  largebody.PlaneAccessCanonicalRequired,
	"completion_gates":                      largebody.PlaneAccessResponseOnly,
	"attempt_transforms":                    largebody.PlaneAccessCanonicalRequired,
	"stream_observer_factories":             largebody.PlaneAccessResponseOnly,
	"traffic_observers":                     largebody.PlaneAccessCanonicalRequired,
	"usage_observers":                       largebody.PlaneAccessResponseOnly,
	"raw_capture_sinks":                     largebody.PlaneAccessCanonicalRequired,
	"traffic_redactors":                     largebody.PlaneAccessCanonicalRequired,
	"compaction_observers":                  largebody.PlaneAccessCanonicalRequired,
	"compaction_preservers":                 largebody.PlaneAccessCanonicalRequired,
	"secret_guards":                         largebody.PlaneAccessCanonicalRequired,
	"secret_guard_execution":                largebody.PlaneAccessCanonicalRequired,
	"local_turn_handlers":                   largebody.PlaneAccessCanonicalRequired,
	"terminal_decision_provider":            largebody.PlaneAccessCanonicalRequired,
	"session_classifier":                    largebody.PlaneAccessMetadataOnly,
	"control_tool_provider":                 largebody.PlaneAccessCanonicalRequired,
}

func TestWireEligibility_CatalogCompatibilityWithDeclarationBaseline(t *testing.T) {
	t.Parallel()

	if largebody.WireEligibilityPlaneCount < len(catalogBaselineOrder) || largebody.WireEligibilityPlaneCount > 32 {
		t.Fatalf("serving catalog holds %d planes, baseline declares %d (append-only within 32-bit blocker capacity)",
			largebody.WireEligibilityPlaneCount, len(catalogBaselineOrder))
	}
	seen := make(map[string]struct{}, len(catalogBaselineOrder))
	for wantIdx, wantID := range catalogBaselineOrder {
		if _, dup := seen[wantID]; dup {
			t.Fatalf("baseline fixture declares duplicate plane %q", wantID)
		}
		seen[wantID] = struct{}{}
		gotID, ok := largebody.WireEligibilityPlaneID(wantIdx)
		if !ok || gotID != wantID {
			t.Fatalf("diagnostic position %d: catalog names %q, baseline declares %q", wantIdx, gotID, wantID)
		}
		gotIdx, ok := largebody.WireEligibilityPlaneIndex(wantID)
		if !ok || gotIdx != wantIdx {
			t.Fatalf("plane %q: catalog index %d, baseline position %d", wantID, gotIdx, wantIdx)
		}
		if _, classified := catalogBaselineAccess[wantID]; !classified {
			t.Fatalf("baseline fixture leaves plane %q without an access class", wantID)
		}
	}
}

func TestWireEligibility_StandardCensusSeedsCatalogBaseline(t *testing.T) {
	t.Parallel()

	census := largebody.NewStandardDependencyCensus("gen-baseline")
	if len(census.Planes) != largebody.WireEligibilityPlaneCount {
		t.Fatalf("standard census covers %d planes, serving catalog holds %d",
			len(census.Planes), largebody.WireEligibilityPlaneCount)
	}
	if len(census.Planes) < len(catalogBaselineOrder) {
		t.Fatalf("standard census covers %d planes, baseline declares %d (append-only)",
			len(census.Planes), len(catalogBaselineOrder))
	}
	for i, wantID := range catalogBaselineOrder {
		got := census.Planes[i]
		if got.ID != wantID {
			t.Fatalf("census position %d: got %q, baseline declares %q", i, got.ID, wantID)
		}
		if want := catalogBaselineAccess[wantID]; got.Access != want {
			t.Fatalf("census plane %q: access %v, baseline declares %v", wantID, got.Access, want)
		}
		if got.Occupied {
			t.Fatalf("fresh standard census must leave plane %q unoccupied", wantID)
		}
	}
}
