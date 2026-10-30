//go:build architecture_red

package archtest

import (
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
	"github.com/aiproxer/aiproxer/internal/testkit/scale"
)

// TestRED_ContinuationAuthorities_DuplicateStructs asserts that duplicate MemoryStore
// and StreamRecorder concrete struct authorities across pkg/aipsdk/continuation
// and internal/core/continuation are detected and fail under the RED build target.
func TestRED_ContinuationAuthorities_DuplicateStructs(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	duplicates, err := DetectDuplicateContinuationStructs(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect continuation structs: %v", err)
	}

	if len(duplicates) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered duplicate continuation struct authorities across pkg/aipsdk/continuation and internal/core/continuation: %v", duplicates)
	}
}

// TestRED_ContributionRegistries_ParallelAuthoritativeViews asserts that parallel authoritative
// contribution registries across internal/pluginreg and internal/standardplugins fail under RED target.
func TestRED_ContributionRegistries_ParallelAuthoritativeViews(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	registries, err := DetectDuplicateAuthoritativeRegistries(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect contribution registries: %v", err)
	}

	if len(registries) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered parallel authoritative contribution registries across internal packages: %+v", registries)
	}
}

// TestRED_RouteKinds_CentralDeclarations asserts that central route operation
// declarations fail under RED; concrete IDs belong to contributions.
func TestRED_RouteKinds_CentralOperationDeclarations(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	routeKinds, err := DetectCentralProtocolRouteKinds(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect central route kinds: %v", err)
	}

	if len(routeKinds) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered central route operation declarations in internal/stdhttp/contract: %+v", routeKinds)
	}
}

// TestRED_Diagnostics_CentralProtocolSpecificDebt asserts that central protocol-specific
// diagnostic DTO rows, switches, and projectors fail under RED target until generalized in Phase 4.
func TestRED_Diagnostics_CentralOwnedRowsOrSwitches(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	diagDebt, err := DetectCentralProtocolDiagnosticsDebt(repoRoot)
	if err != nil {
		t.Fatalf("failed to detect central diagnostics debt: %v", err)
	}

	if len(diagDebt) > 0 {
		t.Fatalf("RED ARCHITECTURE DEBT DETECTED: Discovered central protocol-specific diagnostic DTO rows and switches: %+v", diagDebt)
	}
}

// TestRED_NonCartesianScale_CartesianCellDebt is retained as the opt-in ratchet
// entry point. Release correctness now checks the explicit bounded sentinel,
// while the legacy matrix remains characterization-only evidence.
func TestRED_NonCartesianScale_CartesianCellDebt(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("..", "..")
	if _, err := scale.ScanCurrentCartesianDebt(repoRoot); err != nil {
		t.Fatalf("failed to scan legacy characterization debt: %v", err)
	}
	if got := len(conformance.BoundedSentinelCases()); got == 0 || got > 8 {
		t.Fatalf("bounded sentinel count=%d, want 1..8", got)
	}
}
