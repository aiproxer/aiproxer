package config

import (
	"slices"
	"testing"
)

// TestAuthLocalRecord_ToCore_preservesAttributionAndIsolatesMutableFields
// proves the shared config-to-core conversion keeps every safe attribution
// field and deep-copies the mutable slice/map fields, so the runtime record
// never aliases config-owned state.
func TestAuthLocalRecord_ToCore_preservesAttributionAndIsolatesMutableFields(t *testing.T) {
	t.Parallel()
	rec := AuthLocalAPIKeyRecord{
		KeyID:       "key-a",
		PrincipalID: "alice",
		Key:         "test-local-api-key-16",
		Attribution: AuthLocalAttribution{
			DisplayName:    "Alice Operator",
			AuthMethod:     "operator_key",
			TenantID:       "tenant-a",
			OrganizationID: "org-1",
			WorkspaceID:    "ws-1",
			ProjectID:      "proj-1",
			DepartmentID:   "dept-1",
			CostCenterID:   "cc-1",
			Roles:          []string{"operator", "auditor"},
			SafeClaims:     map[string]string{"tier": "gold"},
			PolicyLabels:   map[string]string{"region": "eu"},
		},
	}
	got := rec.ToCore()
	if got.KeyID != rec.KeyID || got.PrincipalID != rec.PrincipalID || got.Key != rec.Key {
		t.Fatalf("record fields: %+v", got)
	}
	a := got.Attribution
	if a.DisplayName != "Alice Operator" || a.AuthMethod != "operator_key" ||
		a.TenantID != "tenant-a" || a.OrganizationID != "org-1" ||
		a.WorkspaceID != "ws-1" || a.ProjectID != "proj-1" ||
		a.DepartmentID != "dept-1" || a.CostCenterID != "cc-1" {
		t.Fatalf("attribution fields: %+v", a)
	}
	if !slices.Equal(a.Roles, []string{"operator", "auditor"}) {
		t.Fatalf("roles: %#v", a.Roles)
	}
	if a.SafeClaims["tier"] != "gold" || len(a.SafeClaims) != 1 {
		t.Fatalf("safe_claims: %#v", a.SafeClaims)
	}
	if a.PolicyLabels["region"] != "eu" || len(a.PolicyLabels) != 1 {
		t.Fatalf("policy_labels: %#v", a.PolicyLabels)
	}

	a.Roles[0] = "mutated"
	if rec.Attribution.Roles[0] == "mutated" {
		t.Fatal("ToCore must clone Roles, not alias config-owned slice")
	}
	a.SafeClaims["tier"] = "mutated"
	if rec.Attribution.SafeClaims["tier"] == "mutated" {
		t.Fatal("ToCore must clone SafeClaims, not alias config-owned map")
	}
	a.PolicyLabels["region"] = "mutated"
	if rec.Attribution.PolicyLabels["region"] == "mutated" {
		t.Fatal("ToCore must clone PolicyLabels, not alias config-owned map")
	}
}
