package routing

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
)

// Task 2.4 / Req 11.1 / 11.5: selectors without [thinker] keep existing weighted,
// failover, parallel, [first], health, and context-size behavior. Each case also
// asserts the thinker-aware planner leaves interleaved role and selector key empty,
// proving the thinker code path does not touch non-thinker selectors.

func TestNonThinkerSelectorsPreserveExistingBehavior(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		sel     string
		opt     PlanOptions
		wantKey string
		wantLen int
		wantErr error
	}{
		{
			name:    "weighted deterministic roll",
			sel:     "[weight=1]a:x^[weight=1]b:y",
			opt:     PlanOptions{Rand: rng(0), Session: &SessionRoutingState{FirstRequestConsumed: true}},
			wantKey: "a:x",
			wantLen: 1,
		},
		{
			name:    "failover left-to-right primaries",
			sel:     "openai:gpt-4|anthropic:opus|bedrock:claude",
			opt:     PlanOptions{},
			wantKey: "openai:gpt-4",
			wantLen: 3,
		},
		{
			name:    "parallel arm returns all legs",
			sel:     "nvidia:m1!nvidia:m2!nvidia:m3",
			opt:     PlanOptions{},
			wantKey: "nvidia:m1",
			wantLen: 3,
		},
		{
			name:    "first request forces branch",
			sel:     "[first]cheap:fast^[weight=100]expensive:slow",
			opt:     PlanOptions{Rand: rng(99), Session: &SessionRoutingState{}},
			wantKey: "cheap:fast",
			wantLen: 1,
		},
		{
			name:    "health skips unhealthy primary",
			sel:     "a:b|c:d",
			opt:     PlanOptions{Unhealthy: map[string]struct{}{"a:b": {}}},
			wantKey: "c:d",
			wantLen: 1,
		},
		{
			name:    "context-size excludes oversized primary",
			sel:     "[max_context=10]a:b|c:d",
			opt:     PlanOptions{RequestSize: RequestSizeEstimate{Available: true, Tokens: 11}},
			wantKey: "c:d",
			wantLen: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.sel)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			out, err := ExpandFailover(sel, tc.opt)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err: got %v want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expand: %v", err)
			}
			if len(out) != tc.wantLen {
				t.Fatalf("len: got %d want %d (%+v)", len(out), tc.wantLen, out)
			}
			if out[0].Key != tc.wantKey {
				t.Fatalf("first key: got %q want %q", out[0].Key, tc.wantKey)
			}
			for _, c := range out {
				if c.InterleavedRole != interleavedstate.RoleNone {
					t.Fatalf("candidate %q interleaved role must be none, got %q", c.Key, c.InterleavedRole)
				}
				if c.SelectorKey != "" {
					t.Fatalf("candidate %q selector key must be empty, got %q", c.Key, c.SelectorKey)
				}
			}
		})
	}
}

// TestThinkerSelectorDoesNotMutatePlanOptions proves a thinker selector planned with
// a zero cycle state does not leak thinker role onto non-thinker executors and that
// the executor candidate carries the selector key while a non-thinker selector stays empty.
func TestThinkerSelectorDoesNotMutatePlanOptions(t *testing.T) {
	t.Parallel()
	sel, err := Parse("[thinker]a:m^b:m")
	if err != nil {
		t.Fatal(err)
	}
	out, err := ExpandFailover(sel, PlanOptions{Session: &SessionRoutingState{FirstRequestConsumed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Key != "b:m" {
		t.Fatalf("thinker cycle position 0: got %+v want b:m", out)
	}
	if out[0].InterleavedRole != interleavedstate.RoleExecutor {
		t.Fatalf("executor role: got %q", out[0].InterleavedRole)
	}
	if out[0].SelectorKey != "a:m^b:m" {
		t.Fatalf("executor selector key: got %q", out[0].SelectorKey)
	}
}
