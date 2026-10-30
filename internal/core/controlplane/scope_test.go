package controlplane_test

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestScopeFlattenerRoundTripsUnknownKnownEmptyAndKnownValue(t *testing.T) {
	t.Parallel()
	f := controlplane.NewScopeFlattener()
	view := scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("principal-1"),
		CredentialID:   scope.Known(""), // known-empty
		TenantID:       scope.Unknown(), // unknown
		OrganizationID: scope.Known("org-1"),
		WorkspaceID:    scope.Unknown(),
		ProjectID:      scope.Known("proj-1"),
		DepartmentID:   scope.Known(""),
		CostCenterID:   scope.Known("cc-1"),
		Origin:         scope.OriginClient,
		ParentTraceID:  scope.Known("trace-parent"),
		Roles:          []string{"admin", "ops"},
		SafeClaims:     map[string]string{"team": "", "region": "eu"},
		PolicyLabels:   map[string]string{"tier": "gold"},
		DisplayName:    scope.Known("Alice"),
		AuthMethod:     scope.Known("api_key"),
	}

	snap := f.MustFlatten(view)

	if !snap.PrincipalID.Equal(scope.Known("principal-1")) {
		t.Fatalf("principal_id known value lost: %#v", snap.PrincipalID)
	}
	if !snap.CredentialID.IsKnownEmpty() {
		t.Fatalf("credential_id known-empty must round-trip as known-empty, got %#v", snap.CredentialID)
	}
	if !snap.TenantID.IsUnknown() {
		t.Fatalf("tenant_id unknown must round-trip as unknown, got %#v", snap.TenantID)
	}
	if !snap.DepartmentID.IsKnownEmpty() {
		t.Fatalf("department_id known-empty must round-trip as known-empty, got %#v", snap.DepartmentID)
	}

	got := f.Reconstruct(snap)
	if !reflect.DeepEqual(got, view) {
		t.Fatalf("reconstructed view mismatch:\n got  %#v\n want %#v", got, view)
	}
}

func TestScopeFlattenerClonesSlicesAndMaps(t *testing.T) {
	t.Parallel()
	f := controlplane.NewScopeFlattener()
	view := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("p1"),
		Roles:        []string{"a", "b"},
		SafeClaims:   map[string]string{"k": "v"},
		PolicyLabels: map[string]string{"p": "q"},
	}
	snap := f.MustFlatten(view)

	snap.Principal.Roles[0] = "mutated"
	snap.Principal.SafeClaims["k"] = "mutated"
	snap.Principal.PolicyLabels["p"] = "mutated"

	if view.Roles[0] == "mutated" || view.SafeClaims["k"] == "mutated" || view.PolicyLabels["p"] == "mutated" {
		t.Fatalf("flattener must clone roles/safe_claims/policy_labels so callers cannot mutate the source view")
	}
}

func TestScopeFlattenerReconstructClonesSlicesAndMaps(t *testing.T) {
	t.Parallel()
	f := controlplane.NewScopeFlattener()
	snap := cp.ScopeSnapshot{
		Principal: scope.PrincipalScopeView{
			PrincipalID:  scope.Known("p1"),
			Roles:        []string{"a", "b"},
			SafeClaims:   map[string]string{"k": "v"},
			PolicyLabels: map[string]string{"p": "q"},
		},
		PrincipalID: scope.Known("p1"),
	}
	got := f.Reconstruct(snap)
	got.Roles[0] = "mutated"
	got.SafeClaims["k"] = "mutated"
	got.PolicyLabels["p"] = "mutated"

	if snap.Principal.Roles[0] == "mutated" || snap.Principal.SafeClaims["k"] == "mutated" || snap.Principal.PolicyLabels["p"] == "mutated" {
		t.Fatalf("reconstruct must clone roles/safe_claims/policy_labels so callers cannot mutate the snapshot")
	}
}

func TestScopeFlattenerRejectsOversizedMaps(t *testing.T) {
	t.Parallel()
	f := controlplane.NewScopeFlattener()
	labels := make(map[string]string, controlplane.MaxScopeMapEntries+1)
	for i := range controlplane.MaxScopeMapEntries + 1 {
		labels[keyFor(i)] = "v"
	}
	view := scope.PrincipalScopeView{
		PrincipalID:  scope.Known("p1"),
		PolicyLabels: labels,
	}
	if _, err := f.FlattenE(view); err == nil {
		t.Fatalf("flattener must reject oversized policy_labels maps")
	}
}

func TestScopeFlattenerFlattenEAcceptsSafeView(t *testing.T) {
	t.Parallel()
	f := controlplane.NewScopeFlattener()
	view := scope.PrincipalScopeView{
		PrincipalID: scope.Known("p1"),
		Roles:       []string{"a"},
	}
	snap, err := f.FlattenE(view)
	if err != nil {
		t.Fatalf("safe view rejected: %v", err)
	}
	if !snap.PrincipalID.Equal(scope.Known("p1")) {
		t.Fatalf("principal id lost: %#v", snap.PrincipalID)
	}
}

func keyFor(i int) string {
	const base = "abcdefghijklmnop"
	return string([]byte{base[i%len(base)], base[(i/len(base))%len(base)]})
}
