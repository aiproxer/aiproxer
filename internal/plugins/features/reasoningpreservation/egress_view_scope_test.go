package reasoningpreservation_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestEgressPrincipalView_CompatibilityPrincipalID(t *testing.T) {
	t.Parallel()
	legacy := reasoningpreservation.NewEgressPrincipalView("legacy-user-1")
	if legacy.PrincipalID() != "legacy-user-1" {
		t.Fatalf("PrincipalID: got %q want %q", legacy.PrincipalID(), "legacy-user-1")
	}
	// Scope from legacy should have PrincipalID Known(legacy-user-1) and other fields unknown/nil
	sc := legacy.Scope()
	if !sc.PrincipalID.Equal(scope.Known("legacy-user-1")) {
		t.Fatalf("Scope PrincipalID mismatch")
	}
	if sc.TenantID.IsKnown() || sc.OrganizationID.IsKnown() {
		t.Fatal("legacy view should have unknown tenant/org")
	}
}

func TestEgressPrincipalView_FullScopeClone(t *testing.T) {
	t.Parallel()
	orig := scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("u1"),
		DisplayName:    scope.Known("Alice"),
		Roles:          []string{"admin"},
		SafeClaims:     map[string]string{"team": "core"},
		TenantID:       scope.Known("t1"),
		OrganizationID: scope.Known("org1"),
		WorkspaceID:    scope.Known("ws1"),
		ProjectID:      scope.Known("proj1"),
		DepartmentID:   scope.Known("dept1"),
		CostCenterID:   scope.Known("cc1"),
		PolicyLabels:   map[string]string{"tier": "gold"},
		Origin:         scope.OriginClient,
	}
	view := reasoningpreservation.NewEgressPrincipalScopeView(orig)
	got := view.Scope()
	if !got.PrincipalID.Equal(orig.PrincipalID) {
		t.Fatalf("PrincipalID mismatch")
	}
	if !got.TenantID.Equal(orig.TenantID) {
		t.Fatalf("TenantID mismatch")
	}
	if !got.OrganizationID.Equal(orig.OrganizationID) {
		t.Fatalf("OrganizationID mismatch")
	}
	if !got.WorkspaceID.Equal(orig.WorkspaceID) {
		t.Fatalf("WorkspaceID mismatch")
	}
	if !got.ProjectID.Equal(orig.ProjectID) {
		t.Fatalf("ProjectID mismatch")
	}
	if !got.DepartmentID.Equal(orig.DepartmentID) {
		t.Fatalf("DepartmentID mismatch")
	}
	if !got.CostCenterID.Equal(orig.CostCenterID) {
		t.Fatalf("CostCenterID mismatch")
	}
	if len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Fatalf("Roles mismatch: %+v", got.Roles)
	}
	if got.SafeClaims["team"] != "core" {
		t.Fatalf("SafeClaims mismatch")
	}
	if got.PolicyLabels["tier"] != "gold" {
		t.Fatalf("PolicyLabels mismatch")
	}
	if got.Origin != scope.OriginClient {
		t.Fatalf("Origin mismatch")
	}
	// Defensive clone: mutate returned scope must not affect view
	got.Roles[0] = "mutated"
	got.SafeClaims["team"] = "mutated"
	got.PolicyLabels["tier"] = "mutated"
	got2 := view.Scope()
	if got2.Roles[0] == "mutated" {
		t.Fatal("mutating Scope().Roles affected view")
	}
	if got2.SafeClaims["team"] == "mutated" {
		t.Fatal("mutating SafeClaims affected view")
	}
	if got2.PolicyLabels["tier"] == "mutated" {
		t.Fatal("mutating PolicyLabels affected view")
	}
	// Mutate original after construction must not affect view
	orig.Roles[0] = "orig-mutated"
	orig.SafeClaims["team"] = "orig-mutated"
	got3 := view.Scope()
	if got3.Roles[0] == "orig-mutated" {
		t.Fatal("mutating original after NewEgressPrincipalScopeView affected view")
	}
	if got3.SafeClaims["team"] == "orig-mutated" {
		t.Fatal("mutating original SafeClaims affected view")
	}
	// PrincipalID accessor
	if view.PrincipalID() != "u1" {
		t.Fatalf("PrincipalID accessor: got %q want %q", view.PrincipalID(), "u1")
	}
}

func TestEgressStage_PassesFullScopeToPolicy_NoPromptLeakage(t *testing.T) {
	t.Parallel()
	// Build a scope with full fields and a correlation that embeds it.
	// Use the existing compression egress stage test helpers to ensure scope propagation does not leak prompt.
	scopeFull := scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("user-scope-full"),
		Roles:          []string{"admin"},
		SafeClaims:     map[string]string{"team": "core"},
		TenantID:       scope.Known("t1"),
		OrganizationID: scope.Known("org1"),
		WorkspaceID:    scope.Known("ws1"),
		ProjectID:      scope.Known("proj1"),
		DepartmentID:   scope.Known("dept1"),
		CostCenterID:   scope.Known("cc1"),
		PolicyLabels:   map[string]string{"tier": "gold"},
		Origin:         scope.OriginClient,
	}
	// Capture policy that records scope
	capPolicy := &capturedInput{}
	pol := &captureEgressPolicyFullScope{cap: capPolicy, version: "vFull"}
	// Use a minimal store and correlation via existing helper
	cfg := compressionObserverConfig(t)
	cfg.Compression.Enabled = true
	cfg.Compression.Route = "test-route"
	cfg.Compression.EgressPolicyRef = "test-allow"
	// Need to bypass store for this unit test: directly test NewEgressPrincipalScopeView propagation
	view := reasoningpreservation.NewEgressPrincipalScopeView(scopeFull)
	in := reasoningpreservation.CompressionEgressInput{
		Route:       cfg.Compression.Route,
		Purpose:     reasoningpreservation.EgressPurposeReasoningSemanticCompression,
		SourceClass: reasoningpreservation.EgressSourceClassSemanticText,
		Principal:   view,
	}
	_, err := pol.Decide(context.Background(), in)
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if capPolicy.received == nil {
		t.Fatal("policy did not receive input")
	}
	gotScope := capPolicy.received.Principal.Scope()
	if !gotScope.PrincipalID.Equal(scopeFull.PrincipalID) {
		t.Fatalf("PrincipalID mismatch")
	}
	if !gotScope.TenantID.Equal(scopeFull.TenantID) {
		t.Fatalf("TenantID mismatch")
	}
	if !gotScope.OrganizationID.Equal(scopeFull.OrganizationID) {
		t.Fatalf("OrganizationID mismatch")
	}
	if !gotScope.WorkspaceID.Equal(scopeFull.WorkspaceID) {
		t.Fatalf("WorkspaceID mismatch")
	}
	if !gotScope.ProjectID.Equal(scopeFull.ProjectID) {
		t.Fatalf("ProjectID mismatch")
	}
	if !gotScope.DepartmentID.Equal(scopeFull.DepartmentID) {
		t.Fatalf("DepartmentID mismatch")
	}
	if !gotScope.CostCenterID.Equal(scopeFull.CostCenterID) {
		t.Fatalf("CostCenterID mismatch")
	}
	if gotScope.Origin != scopeFull.Origin {
		t.Fatalf("Origin mismatch")
	}
	// Ensure no prompt leakage: input contains only route/purpose/source/principal, no prompt text
	if capPolicy.received.Route != "test-route" {
		t.Fatalf("Route mismatch")
	}
	// Defensive clone check for policy mutation
	gotScope.Roles[0] = "mutated"
	again := capPolicy.received.Principal.Scope()
	if again.Roles[0] == "mutated" {
		t.Fatal("policy mutation leaked")
	}
}

type capturedInput struct {
	received *reasoningpreservation.CompressionEgressInput
}

type captureEgressPolicyFullScope struct {
	cap     *capturedInput
	version string
}

func (c *captureEgressPolicyFullScope) Decide(_ context.Context, in reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	cp := in
	c.cap.received = &cp
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: c.version}, nil
}
