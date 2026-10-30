package config

import "testing"

// TestAuthLocalAttribution_toCore_isolatesMutableFields proves toCore deep-copies the
// slice/map attribution fields so core auth records never alias config-owned mutable state.
func TestAuthLocalAttribution_toCore_isolatesMutableFields(t *testing.T) {
	t.Parallel()
	a := AuthLocalAttribution{
		Roles:        []string{"r1"},
		SafeClaims:   map[string]string{"k": "v"},
		PolicyLabels: map[string]string{"p": "q"},
	}
	got := a.toCore()
	got.Roles[0] = "mutated"
	if a.Roles[0] == "mutated" {
		t.Fatal("toCore must clone Roles, not alias config-owned slice")
	}
	got.SafeClaims["k"] = "mutated"
	if a.SafeClaims["k"] == "mutated" {
		t.Fatal("toCore must clone SafeClaims, not alias config-owned map")
	}
	got.PolicyLabels["p"] = "mutated"
	if a.PolicyLabels["p"] == "mutated" {
		t.Fatal("toCore must clone PolicyLabels, not alias config-owned map")
	}
}
