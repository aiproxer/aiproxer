package terminal_test

import (
	"errors"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Exhaustive Advance(from→to) legality including every legal ->failed path.

func TestOwner_Advance_ExhaustiveStateMatrix(t *testing.T) {
	t.Parallel()

	legal := map[sdk.State]map[sdk.State]bool{
		sdk.StateTerminalizing: {
			sdk.StateWorkPending: true,
			sdk.StateFailed:      true,
		},
		sdk.StateWorkPending: {
			sdk.StateSettled: true,
			sdk.StateFailed:  true,
		},
		sdk.StateSettled: {
			sdk.StateReleasePending: true,
			sdk.StateFailed:         true,
		},
		sdk.StateReleasePending: {
			sdk.StateReleased: true,
			sdk.StateFailed:   true,
		},
	}

	for _, from := range sdk.AllStates() {
		for _, to := range sdk.AllStates() {
			wantOK := legal[from][to]
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				t.Parallel()
				o := ownerAt(t, from)
				err := o.Advance(to)
				if wantOK {
					if err != nil {
						t.Fatalf("legal Advance(%s->%s)=%v", from, to, err)
					}
					if o.State() != to {
						t.Fatalf("state=%q want %q", o.State(), to)
					}
					return
				}
				if !errors.Is(err, sdk.ErrInvalidTransition) {
					t.Fatalf("illegal Advance(%s->%s)=%v want ErrInvalidTransition", from, to, err)
				}
				if o.State() != from {
					t.Fatalf("illegal advance mutated state %q -> %q", from, o.State())
				}
			})
		}
	}
}

func TestOwner_Advance_AllFailedPaths(t *testing.T) {
	t.Parallel()
	for _, from := range []sdk.State{
		sdk.StateTerminalizing,
		sdk.StateWorkPending,
		sdk.StateSettled,
		sdk.StateReleasePending,
	} {
		t.Run(string(from)+"->failed", func(t *testing.T) {
			t.Parallel()
			o := ownerAt(t, from)
			if err := o.Advance(sdk.StateFailed); err != nil {
				t.Fatalf("%v", err)
			}
			if o.State() != sdk.StateFailed {
				t.Fatalf("state=%q", o.State())
			}
			// failed is terminal: no further advances
			for _, to := range sdk.AllStates() {
				if err := o.Advance(to); !errors.Is(err, sdk.ErrInvalidTransition) {
					t.Fatalf("failed->%s err=%v", to, err)
				}
			}
		})
	}
}
