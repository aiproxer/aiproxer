package interleavedstate_test

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

func TestInterleavedState_OnlyContainsCycle(t *testing.T) {
	t.Parallel()

	st := reflect.TypeFor[interleavedstate.State]()
	if st.NumField() != 1 {
		t.Fatalf("interleavedstate.State must have exactly 1 field (Cycle), but has %d fields", st.NumField())
	}
	f := st.Field(0)
	if f.Name != "Cycle" {
		t.Fatalf("interleavedstate.State field 0 must be 'Cycle', got %q", f.Name)
	}
	if f.Type != reflect.TypeFor[interleavedstate.CycleState]() {
		t.Fatalf("interleavedstate.State.Cycle type must be CycleState, got %v", f.Type)
	}
}
