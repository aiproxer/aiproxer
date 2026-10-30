package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement 11.5-11.9 shrinkage evidence and machine-checkable gate.
// ADR 0008 connector-architecture overlay is measured and ratcheted separately
// from the legacy convergence delta (main-spec historical ≥800-line target).
// Path-marker feature overlays are driven by a single table.

func TestShrinkage_BaselineInventoryLocked(t *testing.T) {
	t.Parallel()
	// Recorded numbers below are ceilings, not exact pins: constants and tables
	// may shrink freely, but nothing may grow above the recorded value. Content
	// hashes are not pinned here at all.
	if RuntimeConvergenceMinNetLineReduction > 791 {
		t.Fatalf("min reduction %d exceeds recorded ceiling 791", RuntimeConvergenceMinNetLineReduction)
	}
	if ConnectorArchitectureOverlayMax > 3550 {
		t.Fatalf("connector overlay cap %d exceeds recorded ceiling 3550", ConnectorArchitectureOverlayMax)
	}
	if BackendResourcePoolOverlayMax > 381 {
		t.Fatalf("backend resource pool overlay cap %d exceeds recorded ceiling 381", BackendResourcePoolOverlayMax)
	}
	if GenericCompatibleBackendOverlayMax > 946 {
		t.Fatalf("generic compatible overlay cap %d exceeds recorded ceiling 946", GenericCompatibleBackendOverlayMax)
	}
	if BillingHostCompositionOverlayMax > 428 {
		t.Fatalf("billing host composition overlay cap %d exceeds recorded ceiling 428", BillingHostCompositionOverlayMax)
	}
	if AtomicOwnedResourceLifecycleOverlayMax > 92 {
		t.Fatalf("atomic owned resource lifecycle overlay cap %d exceeds recorded ceiling 92", AtomicOwnedResourceLifecycleOverlayMax)
	}
	if len(pathMarkerOverlaySpecs) > 11 {
		t.Fatalf("path-marker overlay table: got %d specs, want at most 11", len(pathMarkerOverlaySpecs))
	}
	if UsageEconomicsOverlayMax > 1588 {
		t.Fatalf("usage economics overlay cap %d exceeds recorded ceiling 1588", UsageEconomicsOverlayMax)
	}
	wantGrowth := []usageEconomicsGrowthFile{
		{path: "internal/infra/runtimebundle/accounting_recovery.go", baseline: 0},
		{path: "internal/infra/runtimebundle/external_billing.go", baseline: 0},
		{path: "internal/infra/runtimebundle/observation_economic_bridge.go", baseline: 0},
		{path: "internal/infra/runtimebundle/operator_reports.go", baseline: 0},
		{path: "internal/infra/runtimebundle/shadow_v2_compose.go", baseline: 0},
		{path: "internal/stdhttp/admin/billing/operator.go", baseline: 0},
		{path: "pkg/aipruntime/billing.go", baseline: 0},
		{path: "internal/infra/runtimebundle/production_options.go", baseline: 80},
		{path: "internal/infra/runtimebundle/process_billing.go", baseline: 75},
	}
	if len(usageEconomicsGrowthFiles) > len(wantGrowth) {
		t.Fatalf("usage economics growth table: got %d files, want at most %d", len(usageEconomicsGrowthFiles), len(wantGrowth))
	}
	for i, got := range usageEconomicsGrowthFiles {
		if got.path != wantGrowth[i].path {
			t.Fatalf("usage economics growth file[%d] = %+v, want path %+v", i, got, wantGrowth[i])
		}
		if got.baseline > wantGrowth[i].baseline {
			t.Fatalf("usage economics growth file[%d] baseline %d exceeds recorded ceiling %d", i, got.baseline, wantGrowth[i].baseline)
		}
	}
	if LargePayloadHostCompositionOverlayMax > 291 {
		t.Fatalf("large payload host composition overlay cap %d exceeds recorded ceiling 291", LargePayloadHostCompositionOverlayMax)
	}
	if GeoIPIngressOverlayMax > 700 {
		t.Fatalf("GeoIP ingress overlay cap %d exceeds recorded ceiling 700", GeoIPIngressOverlayMax)
	}
	if ReasoningSemanticCompressionOverlayMax > 366 {
		t.Fatalf("reasoning semantic compression overlay cap %d exceeds recorded ceiling 366", ReasoningSemanticCompressionOverlayMax)
	}
	if TerminalDecisionFeatureExtensionOverlayMax > 622 {
		t.Fatalf("terminal decision feature extension overlay cap %d exceeds recorded ceiling 622", TerminalDecisionFeatureExtensionOverlayMax)
	}
	if IngressSelfDefenseOverlayMax > 1610 {
		t.Fatalf("ingress self-defense overlay cap %d exceeds recorded ceiling 1610", IngressSelfDefenseOverlayMax)
	}
	if APIKeyCredentialCompositionOverlayMax > 75 {
		t.Fatalf("api-key credential composition overlay cap %d exceeds recorded ceiling 75", APIKeyCredentialCompositionOverlayMax)
	}
	want := []AffectedSurfaceBaseline{
		{Tree: "internal/infra/runtimebundle", BaselineLines: 10133}, // +1 client-reasoning-unrepresentable-policy wiring, +7 frontend logger wiring, +18 connector output-budget terminal cause wiring, +209 discovered-inventory default route slice
		{Tree: "internal/infra/runtimehost", BaselineLines: 3056},
		{Tree: "internal/stdhttp", BaselineLines: 4670}, // +4 frontend logger wiring
		{Tree: "cmd/aipstd", BaselineLines: 998},        // +13 frontend logger wiring
		{Tree: "pkg/aipruntime", BaselineLines: 1037},
	}
	if len(RuntimeConvergenceAffectedSurfaces) > len(want) {
		t.Fatalf("surface count: got %d want at most %d", len(RuntimeConvergenceAffectedSurfaces), len(want))
	}
	sum := 0
	for i, got := range RuntimeConvergenceAffectedSurfaces {
		if got.Tree != want[i].Tree {
			t.Fatalf("surface[%d]: got %+v want tree %+v", i, got, want[i])
		}
		if got.BaselineLines > want[i].BaselineLines {
			t.Fatalf("surface[%d] baseline %d exceeds recorded ceiling %d", i, got.BaselineLines, want[i].BaselineLines)
		}
		sum += got.BaselineLines
	}
	if sum > 19894 {
		t.Fatalf("baseline total: got %d want at most 19894", sum)
	}
}

func TestShrinkage_GenericOverlayIgnoresSiblingWorktrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	production := filepath.Join(root, "internal", "core", "compatible_ownership.go")
	sibling := filepath.Join(root, ".worktrees", "other", "internal", "core", "compatible_ownership.go")
	tools := filepath.Join(root, "tools", "compatible_admission.go")
	vendor := filepath.Join(root, "vendor", "compatible_admission.go")
	for _, path := range []string{production, sibling, tools, vendor} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package core\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Generic compatible")
	want := []string{"internal/core/compatible_ownership.go", "tools/compatible_admission.go", "vendor/compatible_admission.go"}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] || overlay.Files[1] != want[1] || overlay.Files[2] != want[2] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 3 {
		t.Fatalf("overlay lines = %d, want 3", overlay.Lines)
	}
}

func TestShrinkage_AtomicOverlaySelectsPrimitives(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	production := filepath.Join(root, "internal", "infra", "runtimebundle", "process_owner.go")
	unrelated := filepath.Join(root, "internal", "infra", "runtimebundle", "build_persistence.go")
	for _, path := range []string{production, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package runtimebundle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Atomic owned resource lifecycle")
	want := []string{"internal/infra/runtimebundle/process_owner.go"}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 1 {
		t.Fatalf("overlay lines = %d, want 1", overlay.Lines)
	}
}

func TestShrinkage_BackendResourcePoolOverlaySelectsOnlyPoolFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	selected := filepath.Join(root, "internal", "infra", "runtimebundle", "backend_resource_pool.go")
	testFile := filepath.Join(root, "internal", "infra", "runtimebundle", "backend_resource_pool_test.go")
	unrelated := filepath.Join(root, "internal", "infra", "runtimebundle", "backend_resource_pool_helper.go")
	for _, path := range []string{selected, testFile, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package runtimebundle\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Backend resource pool")
	if overlay.Max > 381 {
		t.Fatalf("overlay max = %d, want at most 381", overlay.Max)
	}
	want := []string{"internal/infra/runtimebundle/backend_resource_pool.go"}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 1 {
		t.Fatalf("overlay lines = %d, want 1", overlay.Lines)
	}
}

func TestShrinkage_LargePayloadHostCompositionOverlaySelectsAssessor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	selected := filepath.Join(root, "internal", "infra", "runtimebundle", "build_large_body_assessor.go")
	selected2 := filepath.Join(root, "internal", "stdhttp", "contract", "large_payload_input.go")
	testFile := filepath.Join(root, "internal", "infra", "runtimebundle", "build_large_body_assessor_test.go")
	unrelated := filepath.Join(root, "internal", "infra", "runtimebundle", "build_persistence.go")
	for _, path := range []string{selected, selected2, testFile, unrelated} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package p\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	overlay := pathOverlayByName(t, root, "Large payload host composition")
	if overlay.Max > 291 {
		t.Fatalf("overlay max = %d, want at most 291", overlay.Max)
	}
	want := []string{
		"internal/infra/runtimebundle/build_large_body_assessor.go",
		"internal/stdhttp/contract/large_payload_input.go",
	}
	if len(overlay.Files) != len(want) || overlay.Files[0] != want[0] || overlay.Files[1] != want[1] {
		t.Fatalf("overlay files = %v, want %v", overlay.Files, want)
	}
	if overlay.Lines != 2 {
		t.Fatalf("overlay lines = %d, want 2", overlay.Lines)
	}
}

func pathOverlayByName(t *testing.T, root, name string) OverlayMeasurement {
	t.Helper()
	overlays, err := measurePathMarkerOverlays(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range overlays {
		if o.Name == name {
			return o
		}
	}
	names := make([]string, 0, len(overlays))
	for _, o := range overlays {
		names = append(names, o.Name)
	}
	t.Fatalf("path overlay %q not found (have %v)", name, names)
	return OverlayMeasurement{}
}

func TestShrinkage_ConnectorOverlayExactMeasured(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	overlay, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	if overlay.Lines > ConnectorArchitectureOverlayMax {
		t.Fatalf("connector overlay: measured %d exceeds cap %d (files=%v)", overlay.Lines, ConnectorArchitectureOverlayMax, overlay.Files)
	}
	if !overlay.Pass {
		t.Fatal("overlay Pass must be true when lines <= Max")
	}
	if len(overlay.Files) == 0 {
		t.Fatal("overlay must select at least one production file")
	}
	for _, f := range overlay.Files {
		if !strings.HasPrefix(f, "internal/infra/runtimebundle/") {
			t.Fatalf("overlay file outside expected host/discovery surface: %s", f)
		}
		if strings.HasSuffix(f, "_test.go") {
			t.Fatalf("overlay must not include tests: %s", f)
		}
	}
}

func TestShrinkage_MeasureDeterministicTotals(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.BaselineTotal > 19894 {
		t.Fatalf("baseline total: got %d want at most 19894", m.BaselineTotal)
	}
	if m.RequiredMax > -791 {
		t.Fatalf("required max delta: got %d want at most -791", m.RequiredMax)
	}
	if len(m.Surfaces) > 5 {
		t.Fatalf("surfaces: got %d want at most 5", len(m.Surfaces))
	}
	recomputed := 0
	for _, s := range m.Surfaces {
		n, err := CountNonTestGoLines(filepath.Join(root, filepath.FromSlash(s.Tree)))
		if err != nil {
			t.Fatal(err)
		}
		if s.CurrentLines != n {
			t.Fatalf("%s: measurement %d != recount %d", s.Tree, s.CurrentLines, n)
		}
		if s.Delta != s.CurrentLines-s.BaselineLines {
			t.Fatalf("%s: delta inconsistency %d", s.Tree, s.Delta)
		}
		recomputed += s.CurrentLines
	}
	if m.CurrentTotal != recomputed {
		t.Fatalf("current total: got %d want %d", m.CurrentTotal, recomputed)
	}
	if m.Delta != m.CurrentTotal-m.BaselineTotal {
		t.Fatalf("aggregate delta inconsistency: %d", m.Delta)
	}
	overlayLines := m.Connector.Lines
	for _, o := range m.PathOverlays {
		overlayLines += o.Lines
	}
	overlayLines += m.Growth.Lines + m.SourceOwnership.Lines + m.APIKeyCredential.Lines + m.OpenCoreSeam.Lines + m.OpenCoreAdmin.Lines + m.OpenCoreServe.Lines
	if m.ConvergenceDelta != m.Delta-overlayLines {
		t.Fatalf("convergence delta inconsistency: got %d want %d-%d", m.ConvergenceDelta, m.Delta, overlayLines)
	}
	wantPass := m.ConvergenceDelta <= m.RequiredMax && m.Connector.Pass
	for _, o := range m.PathOverlays {
		wantPass = wantPass && o.Pass
	}
	wantPass = wantPass && m.Growth.Pass && m.SourceOwnership.Pass && m.APIKeyCredential.Pass && m.OpenCoreSeam.Pass && m.OpenCoreAdmin.Pass && m.OpenCoreServe.Pass
	if m.Pass != wantPass {
		t.Fatalf("pass flag inconsistency: pass=%v convergence=%+d", m.Pass, m.ConvergenceDelta)
	}
}

func TestShrinkage_ReportSectionIncludesVerdict(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	section, m, err := FormatRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"## Runtime-convergence net shrinkage (Req 11.5)",
		"Baseline SHA: `" + RuntimeConvergenceShrinkageBaselineSHA + "`",
		"| **TOTAL** |",
		"ADR 0008 connector-architecture overlay",
		"Billing host composition overlay lines:",
		"Atomic owned resource lifecycle overlay lines:",
		"Keep-warm orchestration overlay lines:",
		"Backend resource pool overlay lines:",
		"GeoIP ingress overlay lines:",
		"Terminal decision feature extension overlay lines:",
		"Large payload host composition overlay lines:",
		"Usage economics overlay lines:",
		"Config-source ownership overlay lines:",
		"API-key credential composition overlay lines:",
		"Open-core seam growth overlay lines:",
		"Open-core admin seam growth overlay lines:",
		"Open-core serve growth overlay lines:",
		"locked per-file baselines at `" + SourceOwnershipGrowthBaselineSHA + "`",
		"locked pre-task-4.1 per-file baselines at `" + OpenCoreSeamGrowthBaselineSHA + "`",
		"locked pre-task-4.2 per-file baselines at `" + OpenCoreAdminGrowthBaselineSHA + "`",
		"locked pre-C4 per-file baselines at `" + OpenCoreServeGrowthBaselineSHA + "`",
		"Convergence delta (raw − overlays):",
		"Required: convergence delta ≤ -791",
	} {
		if !strings.Contains(section, needle) {
			t.Fatalf("report missing %q\n%s", needle, section)
		}
	}
	if m.Pass {
		if !strings.Contains(section, "Verdict: **PASS**") {
			t.Fatalf("expected PASS verdict in report:\n%s", section)
		}
	} else if !strings.Contains(section, "Verdict: **FAIL**") {
		t.Fatalf("expected FAIL verdict in report:\n%s", section)
	}
}

func TestShrinkage_NetReductionMeetsRequirement115(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	m, err := MeasureRuntimeConvergenceShrinkage(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Pass {
		var b strings.Builder
		for _, s := range m.Surfaces {
			fmt.Fprintf(&b, "  %s: %d -> %d (%+d)\n", s.Tree, s.BaselineLines, s.CurrentLines, s.Delta)
		}
		t.Fatalf("Req 11.5 FAIL: raw delta %+d; connector overlay %d/%d; convergence delta %+d (need ≤ %+d)\n%sbaseline_total=%d current_total=%d connector_files=%v",
			m.Delta, m.Connector.Lines, m.Connector.Max, m.ConvergenceDelta, m.RequiredMax,
			b.String(), m.BaselineTotal, m.CurrentTotal, m.Connector.Files)
	}
}

// TestShrinkage_OverlaysArePairwiseDisjoint proves no production file is counted
// by two overlays: connector files seed the exclusion set, each path overlay
// claims only unclaimed files, and the growth allowance skips claimed files, so
// the convergence subtraction never double counts.
func TestShrinkage_OverlaysArePairwiseDisjoint(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	connector, err := MeasureConnectorArchitectureOverlay(root)
	if err != nil {
		t.Fatal(err)
	}
	overlays, err := measurePathMarkerOverlays(root, connector.Files)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]string, len(connector.Files))
	for _, f := range connector.Files {
		seen[f] = "Connector"
	}
	for _, o := range overlays {
		for _, f := range o.Files {
			if owner, dup := seen[f]; dup {
				t.Fatalf("file %s counted by both %s and %s", f, owner, o.Name)
			}
			seen[f] = o.Name
		}
	}
	// Independent pre-exclusion guard: every growth allowlist path is compared
	// directly against the connector and path-marker selections before the growth
	// measurement's defensive filtering could hide an overlap.
	if msg := growthAllowlistOverlap(usageEconomicsGrowthFiles, seen); msg != "" {
		t.Fatal(msg)
	}
	for _, f := range apiKeyCredentialGrowthFiles {
		if owner, dup := seen[f.path]; dup {
			t.Fatalf("file %s counted by both %s and API-key credential composition", f.path, owner)
		}
		seen[f.path] = "API-key credential composition"
	}
	for _, f := range openCoreSeamGrowthFiles {
		if owner, dup := seen[f.path]; dup {
			t.Fatalf("file %s counted by both %s and Open-core seam growth", f.path, owner)
		}
		seen[f.path] = "Open-core seam growth"
	}
	for _, f := range openCoreAdminGrowthFiles {
		if owner, dup := seen[f.path]; dup {
			t.Fatalf("file %s counted by both %s and Open-core admin seam growth", f.path, owner)
		}
		seen[f.path] = "Open-core admin seam growth"
	}
	for _, f := range openCoreServeGrowthFiles {
		if owner, dup := seen[f.path]; dup {
			t.Fatalf("file %s counted by both %s and Open-core serve growth", f.path, owner)
		}
		seen[f.path] = "Open-core serve growth"
	}
}

// TestShrinkage_UsageEconomicsOverlayStaysInScannedSurfaces ties the economics
// allowance to the historical scan: every allowlisted file must live under a Req
// 11.5 affected-surface tree, so unscanned areas can never inflate the subtraction.
func TestShrinkage_UsageEconomicsOverlayStaysInScannedSurfaces(t *testing.T) {
	t.Parallel()
	for _, f := range usageEconomicsGrowthFiles {
		ok := false
		for _, s := range RuntimeConvergenceAffectedSurfaces {
			if f.path == s.Tree || strings.HasPrefix(f.path, s.Tree+"/") {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("usage economics allowlist file outside scanned surfaces: %s", f.path)
		}
	}
}
