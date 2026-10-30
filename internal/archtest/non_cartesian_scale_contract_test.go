package archtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/archtest/tools/changesurface"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
	"github.com/aiproxer/aiproxer/internal/testkit/scale"
)

// TestNonCartesianScale_ThousandProfilesDoNotMultiplyCartesianPairs verifies that
// 1,000 provider profiles bound to existing families do not multiply into 5,000 Cartesian pairs.
func TestNonCartesianScale_ThousandProfilesDoNotMultiplyCartesianPairs(t *testing.T) {
	t.Parallel()

	frontends := conformance.BundledFrontendIDs()
	profiles := scale.ThousandProviderProfilesFixture()
	families := standardplugins.CompatibleBackendKinds()

	if len(frontends) != 5 {
		t.Fatalf("expected 5 bundled frontends in repo, got %d", len(frontends))
	}
	if len(profiles) != 1000 {
		t.Fatalf("expected 1000 profiles in fixture, got %d", len(profiles))
	}
	if len(families) != 5 {
		t.Fatalf("expected 5 compatible backend families in standardplugins, got %d", len(families))
	}

	// Generate and scan source directly from these actual fixture dimensions;
	// callers cannot substitute unrelated handwritten source as evidence.
	if err := scale.ValidateNonCartesianFixture(scale.FiveFrontendsFixture(), profiles); err != nil {
		t.Fatalf("non-Cartesian fixture proof failed: %v", err)
	}
	mappedFamilies := make(map[string]struct{})
	for _, profile := range profiles {
		mappedFamilies[profile.FamilyID] = struct{}{}
	}
	if len(mappedFamilies) != len(families) {
		t.Fatalf("expected all %d families represented, got %d", len(families), len(mappedFamilies))
	}

	// Release evidence is bounded sentinels, not the historical product.
	if got := len(conformance.BoundedSentinelCases()); got == 0 || got > 8 {
		t.Fatalf("sentinel count=%d outside bounded release policy", got)
	}
}

// TestNonCartesianScale_ZeroSharedBoundaryFootprint verifies that adding a provider-profile-only fixture
// requires zero shared boundary footprint by inspecting real repository architecture boundaries.
func TestNonCartesianScale_ZeroSharedBoundaryFootprint(t *testing.T) {
	t.Parallel()

	inspector := scale.RealSharedBoundaryInspector{}

	// Use a concrete temporary fixture repository and a real unified diff.
	fixture := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fixture, "internal", "providerprofiles", "catalog"), 0o755); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(fixture, "internal", "providerprofiles", "catalog", "custom_provider.json")
	if err := os.WriteFile(profilePath, []byte(`{"id":"provider-profile-clean"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := "+++ b/internal/providerprofiles/catalog/custom_provider.json\n"
	footprint, err := inspector.InspectDiff(fixture, diff)
	if err != nil {
		t.Fatalf("inspect clean fixture diff: %v", err)
	}
	if err := footprint.ValidateZeroSharedFootprint(); err != nil {
		t.Fatalf("zero footprint validation failed on clean profile files: %v", err)
	}

	// 2. Inspect against actual central backend lists in standardplugins
	centralFootprint := inspector.InspectProfileAgainstCentralLists("provider-profile-clean")
	if err := centralFootprint.ValidateZeroSharedFootprint(); err != nil {
		t.Fatalf("zero footprint validation failed on standardplugins lists: %v", err)
	}
}

// TestNonCartesianScale_DetectorRejectsSharedBoundaryViolation tests that the zero shared boundary detector
// correctly catches and rejects shared boundary violations on real file modifications and central list additions.
func TestNonCartesianScale_DetectorRejectsSharedBoundaryViolation(t *testing.T) {
	t.Parallel()

	inspector := scale.RealSharedBoundaryInspector{}

	// 1. Test violation on shared core file edit
	coreViolationFiles := []string{
		"internal/core/runtime/executor.go",
	}
	coreFootprint := inspector.InspectFileChanges("bad-profile-core", coreViolationFiles, 0)
	if err := coreFootprint.ValidateZeroSharedFootprint(); err == nil {
		t.Fatalf("expected detector to reject profile modifying internal/core")
	}

	// 2. Test violation on pkg/aipapi edit
	apiViolationFiles := []string{
		"pkg/aipapi/call.go",
	}
	apiFootprint := inspector.InspectFileChanges("bad-profile-api", apiViolationFiles, 0)
	if err := apiFootprint.ValidateZeroSharedFootprint(); err == nil {
		t.Fatalf("expected detector to reject profile modifying pkg/aipapi")
	}

	// 3. Test violation on proto edit
	protoViolationFiles := []string{
		"api/backendplugin/v1/backend.proto",
	}
	protoFootprint := inspector.InspectFileChanges("bad-profile-proto", protoViolationFiles, 0)
	if err := protoFootprint.ValidateZeroSharedFootprint(); err == nil {
		t.Fatalf("expected detector to reject profile modifying backend.proto")
	}

	// 4. Test violation on central table edit
	centralViolationFiles := []string{
		"internal/standardplugins/table.go",
	}
	centralFootprint := inspector.InspectFileChanges("bad-profile-central", centralViolationFiles, 0)
	if err := centralFootprint.ValidateZeroSharedFootprint(); err == nil {
		t.Fatalf("expected detector to reject profile modifying internal/standardplugins")
	}

	// 5. Test violation on central essential list addition
	essentialFootprint := inspector.InspectProfileAgainstCentralLists("openai-responses") // "openai-responses" is in EssentialBackendKinds!
	if err := essentialFootprint.ValidateZeroSharedFootprint(); err == nil {
		t.Fatalf("expected detector to reject profile added to EssentialBackendKinds")
	}
}

func TestNonCartesianScale_ChangeSurfaceProfileOnlyRatchet(t *testing.T) {
	t.Parallel()
	clean := []string{
		"provider-profiles/catalog/acme.yaml",
		"internal/providerprofiles/profile_test.go",
		"testdata/generated/acme.json",
		"internal/testkit/reference_fixture.go",
	}
	if err := changesurface.ValidateProfileOnlyPaths(clean); err != nil {
		t.Fatalf("profile-only fixture rejected: %v", err)
	}
	for _, forbidden := range []string{
		"pkg/aipapi/call.go",
		"internal/core/routing/router.go",
		"internal/plugins/frontends/contoso/mount.go",
		"api/backendplugin/v1/backend.proto",
		"internal/standardplugins/table.go",
		"internal/core/routing/router_test.go",
		"pkg/aipapi/call_test.go",
		"internal/plugins/frontends/contoso/mount_test.go",
	} {
		paths := append(append([]string(nil), clean...), forbidden)
		if err := changesurface.ValidateProfileOnlyPaths(paths); err == nil {
			t.Fatalf("profile-only fixture accepted forbidden shared path %q", forbidden)
		}
	}
}
