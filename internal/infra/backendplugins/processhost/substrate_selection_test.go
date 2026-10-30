package processhost_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
)

func TestMandatoryRequirementMatrixIsComplete(t *testing.T) {
	t.Parallel()

	want := []processhost.RequirementID{
		processhost.ReqPublicABIOwnership,
		processhost.ReqProtocolNegotiation,
		processhost.ReqBidirectionalStreaming,
		processhost.ReqTransportRetriesDisabled,
		processhost.ReqExactByteLaunch,
		processhost.ReqExpectedProcessPeerIdentity,
		processhost.ReqProtectedBootstrap,
		processhost.ReqMinimalEnvHandleControl,
		processhost.ReqProcessTreeCleanup,
		processhost.ReqBoundedMessagesLogs,
		processhost.ReqDeclaredProcessModels,
		processhost.ReqReattachProhibition,
		processhost.ReqLicenseEvidence,
	}

	got := processhost.MandatoryRequirements()
	if len(got) != len(want) {
		t.Fatalf("MandatoryRequirements len=%d want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("requirement[%d]=%q want %q", i, got[i].ID, id)
		}
		if !got[i].Mandatory {
			t.Fatalf("%q must be mandatory", id)
		}
		if got[i].Summary == "" {
			t.Fatalf("%q summary must be non-empty", id)
		}
	}
}

func TestDefaultCatalog_SelectsProjectOwnedForReplacementCost(t *testing.T) {
	t.Parallel()

	result, err := processhost.SelectSubstrate(processhost.DefaultEvidenceCatalog())
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	if result.Selected != processhost.CandidateProjectOwnedHost {
		t.Fatalf("Selected=%q want %q", result.Selected, processhost.CandidateProjectOwnedHost)
	}

	owned, ok := result.Assessment(processhost.CandidateProjectOwnedHost)
	if !ok || !owned.Selectable {
		t.Fatal("project-owned must be selectable")
	}
	for _, dim := range owned.Dimensions {
		if dim.Level == processhost.EvidenceRuntimeVerified {
			t.Fatalf("project-owned %q must not claim runtime_verified in this spike", dim.Requirement)
		}
		if dim.Level != processhost.EvidenceSourceVerified {
			t.Fatalf("project-owned %q level=%q want source_verified", dim.Requirement, dim.Level)
		}
	}

	custom, ok := result.Assessment(processhost.CandidateCustomGoPluginV18x)
	if !ok {
		t.Fatal("missing custom assessment")
	}
	if custom.Selectable {
		t.Fatal("custom go-plugin must not be selectable when replacement cost exceeds retained value")
	}
	if !custom.ReplacementCost.ExceedsRetainedValue {
		t.Fatal("custom assessment must record ExceedsRetainedValue")
	}
	if custom.ReplacementCost.Rationale == "" {
		t.Fatal("replacement-cost rationale required")
	}

	stock, ok := result.Assessment(processhost.CandidateStockGoPluginV180)
	if !ok || stock.Selectable {
		t.Fatal("default stock go-plugin must not be selectable")
	}
}

func TestSelectSubstrate_NeutralWhenStockFullySourceVerified(t *testing.T) {
	t.Parallel()

	catalog := processhost.DefaultEvidenceCatalog()
	catalog[processhost.CandidateStockGoPluginV180] = allSourceVerifiedDims(processhost.CandidateStockGoPluginV180)
	catalog[processhost.CandidateProjectOwnedHost] = allSourceVerifiedDims(processhost.CandidateProjectOwnedHost)
	catalog[processhost.CandidateCustomGoPluginV18x] = allSourceVerifiedDims(processhost.CandidateCustomGoPluginV18x)

	result, err := processhost.SelectSubstrate(catalog)
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	if result.Selected != processhost.CandidateStockGoPluginV180 {
		t.Fatalf("Selected=%q want stock when stock is first fully feasible candidate (algorithm neutrality)", result.Selected)
	}
}

func TestSelectSubstrate_RejectsMissingEvidence(t *testing.T) {
	t.Parallel()

	catalog := processhost.DefaultEvidenceCatalog()
	owned := catalog[processhost.CandidateProjectOwnedHost]
	for i := range owned {
		if owned[i].Requirement == processhost.ReqProtectedBootstrap {
			owned[i].Level = processhost.EvidenceMissing
			owned[i].Notes = "intentionally cleared for missing-evidence test"
		}
	}
	catalog[processhost.CandidateProjectOwnedHost] = owned

	result, err := processhost.SelectSubstrate(catalog)
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	if result.Selected == processhost.CandidateProjectOwnedHost {
		t.Fatal("project-owned must not be selected with missing mandatory evidence")
	}
	assessment, ok := result.Assessment(processhost.CandidateProjectOwnedHost)
	if !ok || assessment.Selectable {
		t.Fatal("candidate with missing mandatory evidence must not be selectable")
	}
}

func TestSelectSubstrate_NoFalseRuntimePlatformVerification(t *testing.T) {
	t.Parallel()

	result, err := processhost.SelectSubstrate(processhost.DefaultEvidenceCatalog())
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	if len(result.Platforms) != 3 {
		t.Fatalf("Platforms len=%d want 3", len(result.Platforms))
	}
	for _, p := range result.Platforms {
		if p.Verification == processhost.PlatformRuntimeVerified {
			t.Fatalf("platform %q must not be runtime_verified without probes; got %q", p.Platform, p.Verification)
		}
		if p.Verification != processhost.PlatformDesignSourceEvidenced &&
			p.Verification != processhost.PlatformCompileUnverified {
			t.Fatalf("platform %q verification=%q", p.Platform, p.Verification)
		}
		if p.LaunchBinding == "" || p.LocalChannel == "" {
			t.Fatalf("platform %q missing approved profile fields", p.Platform)
		}
	}
}

func TestSelectSubstrate_StockSourceRefsRequired(t *testing.T) {
	t.Parallel()

	result, err := processhost.SelectSubstrate(processhost.DefaultEvidenceCatalog())
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	stock, ok := result.Assessment(processhost.CandidateStockGoPluginV180)
	if !ok {
		t.Fatal("missing stock assessment")
	}
	for _, dim := range stock.Dimensions {
		if dim.Source == nil {
			t.Fatalf("stock dimension %q missing SourceRef", dim.Requirement)
		}
		if dim.Source.Module != "github.com/hashicorp/go-plugin" {
			t.Fatalf("%q module=%q", dim.Requirement, dim.Source.Module)
		}
		if dim.Source.Version != "v1.8.0" {
			t.Fatalf("%q version=%q", dim.Requirement, dim.Source.Version)
		}
		if dim.Source.URL == "" && dim.Source.Path == "" {
			t.Fatalf("%q needs Path or URL", dim.Requirement)
		}
	}
	if result.OfficialSource.License != "MPL-2.0" {
		t.Fatalf("license=%q", result.OfficialSource.License)
	}
	if !strings.Contains(result.OfficialSource.LicenseURL, "LICENSE") {
		t.Fatalf("LicenseURL=%q", result.OfficialSource.LicenseURL)
	}
}

func TestValidateCatalog_RejectsDuplicatesUnknownEmptyNotesMissingSource(t *testing.T) {
	t.Parallel()

	base := processhost.DefaultEvidenceCatalog()

	t.Run("duplicate", func(t *testing.T) {
		t.Parallel()
		c := processhost.DefaultEvidenceCatalog()
		dims := append([]processhost.DimensionEvidence{}, c[processhost.CandidateProjectOwnedHost]...)
		dims = append(dims, dims[0])
		c[processhost.CandidateProjectOwnedHost] = dims
		if err := processhost.ValidateCatalog(c); err == nil {
			t.Fatal("expected duplicate requirement error")
		}
	})

	t.Run("unknown", func(t *testing.T) {
		t.Parallel()
		c := processhost.DefaultEvidenceCatalog()
		dims := append([]processhost.DimensionEvidence{}, c[processhost.CandidateProjectOwnedHost]...)
		dims = append(dims, processhost.DimensionEvidence{
			Requirement: "not_a_real_requirement",
			Level:       processhost.EvidenceSourceVerified,
			Notes:       "unknown",
		})
		c[processhost.CandidateProjectOwnedHost] = dims
		if err := processhost.ValidateCatalog(c); err == nil {
			t.Fatal("expected unknown requirement error")
		}
	})

	t.Run("empty_notes", func(t *testing.T) {
		t.Parallel()
		c := processhost.DefaultEvidenceCatalog()
		dims := append([]processhost.DimensionEvidence{}, c[processhost.CandidateProjectOwnedHost]...)
		dims[0].Notes = ""
		c[processhost.CandidateProjectOwnedHost] = dims
		if err := processhost.ValidateCatalog(c); err == nil {
			t.Fatal("expected empty notes error")
		}
	})

	t.Run("stock_missing_source", func(t *testing.T) {
		t.Parallel()
		c := processhost.DefaultEvidenceCatalog()
		dims := append([]processhost.DimensionEvidence{}, c[processhost.CandidateStockGoPluginV180]...)
		dims[0].Source = nil
		c[processhost.CandidateStockGoPluginV180] = dims
		if err := processhost.ValidateCatalog(c); err == nil {
			t.Fatal("expected missing source ref error for stock claim")
		}
	})

	t.Run("incomplete_candidate", func(t *testing.T) {
		t.Parallel()
		c := processhost.DefaultEvidenceCatalog()
		c[processhost.CandidateProjectOwnedHost] = c[processhost.CandidateProjectOwnedHost][:1]
		if err := processhost.ValidateCatalog(c); err == nil {
			t.Fatal("expected incomplete candidate error")
		}
	})

	_ = base
}

func TestDefaultEvidenceCatalog_ImmutableClone(t *testing.T) {
	t.Parallel()

	a := processhost.DefaultEvidenceCatalog()
	a[processhost.CandidateProjectOwnedHost][0].Level = processhost.EvidenceFailed
	a[processhost.CandidateProjectOwnedHost][0].Notes = "mutated"

	b := processhost.DefaultEvidenceCatalog()
	if b[processhost.CandidateProjectOwnedHost][0].Level == processhost.EvidenceFailed {
		t.Fatal("DefaultEvidenceCatalog must clone; mutation leaked")
	}
	if b[processhost.CandidateProjectOwnedHost][0].Notes == "mutated" {
		t.Fatal("DefaultEvidenceCatalog must clone notes")
	}
}

func TestAssessment_ReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	result, err := processhost.SelectSubstrate(processhost.DefaultEvidenceCatalog())
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	first, ok := result.Assessment(processhost.CandidateProjectOwnedHost)
	if !ok {
		t.Fatal("missing assessment")
	}
	first.Dimensions[0].Level = processhost.EvidenceFailed
	first.Selectable = false

	second, ok := result.Assessment(processhost.CandidateProjectOwnedHost)
	if !ok {
		t.Fatal("missing assessment on second read")
	}
	if second.Dimensions[0].Level == processhost.EvidenceFailed {
		t.Fatal("Assessment must return a defensive copy of dimensions")
	}
	if !second.Selectable {
		t.Fatal("Assessment must return a defensive copy of Selectable")
	}
}

func TestSelectSubstrate_Task12BlockersAndCustomNotRuntimeSatisfied(t *testing.T) {
	t.Parallel()

	result, err := processhost.SelectSubstrate(processhost.DefaultEvidenceCatalog())
	if err != nil {
		t.Fatalf("SelectSubstrate: %v", err)
	}
	if len(result.Task12Blockers) == 0 {
		t.Fatal("Task 1.2 blockers required")
	}
	custom, ok := result.Assessment(processhost.CandidateCustomGoPluginV18x)
	if !ok {
		t.Fatal("missing custom")
	}
	for _, dim := range custom.Dimensions {
		if dim.Level == processhost.EvidenceRuntimeVerified {
			t.Fatalf("custom %q must not claim runtime_verified", dim.Requirement)
		}
	}
}

func allSourceVerifiedDims(candidate processhost.CandidateID) []processhost.DimensionEvidence {
	out := make([]processhost.DimensionEvidence, 0, len(processhost.MandatoryRequirements()))
	for _, req := range processhost.MandatoryRequirements() {
		dim := processhost.DimensionEvidence{
			Requirement: req.ID,
			Level:       processhost.EvidenceSourceVerified,
			Notes:       "synthetic fully source-verified row for neutrality/feasibility tests",
		}
		if candidate == processhost.CandidateStockGoPluginV180 || candidate == processhost.CandidateCustomGoPluginV18x {
			if req.ID == processhost.ReqLicenseEvidence {
				dim.Source = &processhost.SourceRef{
					Module:     "github.com/hashicorp/go-plugin",
					Version:    "v1.8.0",
					Path:       "LICENSE",
					License:    "MPL-2.0",
					LicenseURL: "https://github.com/hashicorp/go-plugin/blob/v1.8.0/LICENSE",
					URL:        "https://github.com/hashicorp/go-plugin/blob/v1.8.0/LICENSE",
				}
			} else {
				dim.Source = &processhost.SourceRef{
					Module:  "github.com/hashicorp/go-plugin",
					Version: "v1.8.0",
					Path:    "plugin.go",
					URL:     "https://github.com/hashicorp/go-plugin/blob/v1.8.0/plugin.go",
				}
			}
		}
		out = append(out, dim)
	}
	return out
}
