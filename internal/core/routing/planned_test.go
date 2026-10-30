package routing

import (
	"testing"
)

// TestPlannedLeavesPreservesOccurrenceOrderAndMultiplicity locks the shared
// projection contract consumed by quote/admission: exact traversal order,
// duplicates retained per occurrence, model-only leaves with an empty backend,
// and no leaves for a nil selector.
func TestPlannedLeavesPreservesOccurrenceOrderAndMultiplicity(t *testing.T) {
	t.Parallel()
	sel, err := Parse("a:m1|a:m1!b:m2")
	if err != nil {
		t.Fatal(err)
	}
	got := PlannedLeaves(sel)
	want := []PlannedLeaf{
		{Key: "a:m1", Backend: "a", Model: "m1"},
		{Key: "a:m1", Backend: "a", Model: "m1"},
		{Key: "b:m2", Backend: "b", Model: "m2"},
	}
	if len(got) != len(want) {
		t.Fatalf("leaves = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Key != want[i].Key || got[i].Backend != want[i].Backend || got[i].Model != want[i].Model {
			t.Fatalf("leaf[%d] = %+v, want %+v (all=%+v)", i, got[i], want[i], got)
		}
	}
	if nilLeaves := PlannedLeaves(nil); nilLeaves != nil {
		t.Fatalf("nil selector leaves = %+v, want nil", nilLeaves)
	}
	modelOnly, err := Parse("m1")
	if err != nil {
		t.Fatal(err)
	}
	leaves := PlannedLeaves(modelOnly)
	if len(leaves) != 1 || leaves[0].Backend != "" || leaves[0].Model != "m1" || leaves[0].Key != "m1" {
		t.Fatalf("model-only leaves = %+v, want empty backend and model m1", leaves)
	}
}
