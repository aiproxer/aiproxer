package openresponses_test

import (
	"testing"

	testkit "github.com/aiproxer/aiproxer/internal/testkit/openresponses"
)

func TestScenarioRegistry_DuplicateAndInvalidIDs(t *testing.T) {
	t.Parallel()

	reg := testkit.NewScenarioRegistry()

	s1 := testkit.ScenarioDescriptor{
		ID:          "openresponses-json-basic-v1",
		Kind:        testkit.ScenarioKindJSONText,
		Description: "Basic non-streaming JSON request scenario",
	}

	if err := reg.Register(s1); err != nil {
		t.Fatalf("unexpected error registering valid scenario: %v", err)
	}

	// Duplicate registration must fail
	if err := reg.Register(s1); err == nil {
		t.Fatal("expected error registering duplicate scenario ID, got nil")
	}

	// Invalid ID format (uppercase, spaces, special chars)
	invalidScenarios := []testkit.ScenarioDescriptor{
		{ID: "INVALID_UPPERCASE", Kind: testkit.ScenarioKindJSONText, Description: "desc"},
		{ID: "invalid id spaces", Kind: testkit.ScenarioKindJSONText, Description: "desc"},
		{ID: "invalid!", Kind: testkit.ScenarioKindJSONText, Description: "desc"},
		{ID: "", Kind: testkit.ScenarioKindJSONText, Description: "desc"},
		{ID: "valid-id", Kind: "unknown_kind", Description: "desc"},
		{ID: "valid-id-2", Kind: testkit.ScenarioKindJSONText, Description: ""},
	}

	for _, inv := range invalidScenarios {
		if err := reg.Register(inv); err == nil {
			t.Errorf("expected error registering invalid scenario %+v, got nil", inv)
		}
	}

	// Retrieve scenario
	got, found := reg.Get("openresponses-json-basic-v1")
	if !found {
		t.Fatal("registered scenario not found")
	}
	if got.Description != s1.Description {
		t.Fatalf("got description %q, want %q", got.Description, s1.Description)
	}

	// List scenarios
	all := reg.List()
	if len(all) != 1 {
		t.Fatalf("expected 1 scenario in list, got %d", len(all))
	}
}
