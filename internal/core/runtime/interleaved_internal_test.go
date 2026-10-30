package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// TestInterleavedPhaseForRole_MapsEveryRole covers interleavedPhaseForRole for every Role value
// so a new role added without updating the switch surfaces as an empty phase rather than a silent
// mislabeling.
func TestInterleavedPhaseForRole_MapsEveryRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		role interleavedstate.Role
		want string
	}{
		{interleavedstate.RoleThinker, "thinker"},
		{interleavedstate.RoleExecutor, "executor"},
		{interleavedstate.RoleNone, ""},
		{"unknown-role", ""},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			t.Parallel()
			if got := interleavedPhaseForRole(tc.role); got != tc.want {
				t.Fatalf("interleavedPhaseForRole(%q) = %q want %q", tc.role, got, tc.want)
			}
		})
	}
}

func TestInterleavedContinuationStream_UnknownPhaseRecvError(t *testing.T) {
	t.Parallel()
	s := &interleavedContinuationStream{}
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errUnknownInterleavedPhase) {
		t.Fatalf("got %v", err)
	}
}

// spyInterleavedStateStore records calls to FetchInterleavedState and SetInterleavedState.
type spyInterleavedStateStore struct {
	b2bua.Store
	fetchCalls int
	state      interleavedstate.State
}

// FetchInterleavedState increments the call count and returns the configured state.
func (s *spyInterleavedStateStore) FetchInterleavedState(ctx context.Context, aLegID string) (interleavedstate.State, error) {
	s.fetchCalls++
	return s.state, nil
}

// SetInterleavedState records the updated state.
func (s *spyInterleavedStateStore) SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	s.state = state
	return nil
}

// TestExecutor_LoadInterleavedState_DisabledSkipsStore verifies that when interleaved thinking is disabled,
// loadInterleavedState immediately returns an empty state without calling FetchInterleavedState.
func TestExecutor_LoadInterleavedState_DisabledSkipsStore(t *testing.T) {
	t.Parallel()
	spy := &spyInterleavedStateStore{
		state: interleavedstate.State{
			Cycle: interleavedstate.CycleState{
				Sequence: []interleavedstate.CycleEntry{{Key: "k1", Role: interleavedstate.RoleThinker}},
			},
		},
	}
	ex := TestExecutor()
	ex.Store = spy
	ex.InterleavedProcessor = nil // disabled

	got, err := ex.loadInterleavedState(context.Background(), "a-leg-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("expected empty state when interleaved disabled, got %+v", got)
	}
	if spy.fetchCalls != 0 {
		t.Fatalf("expected 0 calls to FetchInterleavedState when disabled, got %d", spy.fetchCalls)
	}
}

// TestExecutor_LoadInterleavedState_EnabledFetchesStore verifies that when interleaved thinking is enabled,
// loadInterleavedState calls FetchInterleavedState on the store and returns its state.
func TestExecutor_LoadInterleavedState_EnabledFetchesStore(t *testing.T) {
	t.Parallel()
	expectedState := interleavedstate.State{
		Cycle: interleavedstate.CycleState{
			Sequence: []interleavedstate.CycleEntry{{Key: "k1", Role: interleavedstate.RoleThinker}},
		},
	}
	spy := &spyInterleavedStateStore{
		state: expectedState,
	}
	ex := TestExecutor()
	ex.Store = spy
	ex.InterleavedProcessor = &testInterleavedProcessorAdapter{} // non-nil processor indicates enabled

	got, err := ex.loadInterleavedState(context.Background(), "a-leg-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Cycle.Sequence) != len(expectedState.Cycle.Sequence) || got.Cycle.Sequence[0] != expectedState.Cycle.Sequence[0] {
		t.Fatalf("expected %+v, got %+v", expectedState, got)
	}
	if spy.fetchCalls != 1 {
		t.Fatalf("expected 1 call to FetchInterleavedState when enabled, got %d", spy.fetchCalls)
	}
}
