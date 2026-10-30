package terminal_test

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.1 RED/GREEN contracts: single terminal owner state machine
// (requirements 7.1–7.8, 13.1, 13.4; design D8, D13, D17).

func TestOwner_Claim_CommandTransitionMatrix(t *testing.T) {
	t.Parallel()

	type want struct {
		won   bool
		state sdk.State
		errIs error
	}

	cases := []struct {
		name  string
		setup func(*coreterm.Owner)
		cmd   sdk.Command
		snap  coreterm.AccumulatorSnapshot
		want  want
		check func(t *testing.T, o *coreterm.Owner, r coreterm.Result)
	}{
		{
			name: "open+normal_finish wins to terminalizing",
			cmd:  sdk.CommandNormalFinish,
			snap: coreterm.NewAccumulatorSnapshot([]byte("acc"), false),
			want: want{won: true, state: sdk.StateTerminalizing},
			check: func(t *testing.T, o *coreterm.Owner, r coreterm.Result) {
				t.Helper()
				if r.Outcome.Code != sdk.OutcomeCodeSuccess {
					t.Fatalf("code=%q", r.Outcome.Code)
				}
				if !r.Outcome.SettleCustomer || !r.Outcome.ReleaseConcurrency {
					t.Fatal("request owner must settle customer and release concurrency")
				}
			},
		},
		{
			name: "open+unknown command illegal",
			cmd:  sdk.Command("nope"),
			snap: coreterm.NewAccumulatorSnapshot(nil, false),
			want: want{won: false, state: sdk.StateOpen, errIs: sdk.ErrInvalid},
		},
		{
			name: "open+gate_replacement with output committed rejected",
			cmd:  sdk.CommandGateReplacement,
			snap: coreterm.NewAccumulatorSnapshot([]byte("out"), true),
			want: want{won: false, state: sdk.StateOpen, errIs: sdk.ErrOutputCommitted},
		},
		{
			name: "open+gate_replacement without output committed wins",
			cmd:  sdk.CommandGateReplacement,
			snap: coreterm.NewAccumulatorSnapshot([]byte("pre"), false),
			want: want{won: true, state: sdk.StateTerminalizing},
		},
		{
			name: "attempt scope does not settle customer",
			setup: func(o *coreterm.Owner) {
				// rebuilt below with attempt scope
			},
			cmd:  sdk.CommandParallelLoser,
			snap: coreterm.NewAccumulatorSnapshot([]byte("a"), false),
			want: want{won: true, state: sdk.StateTerminalizing},
			check: func(t *testing.T, o *coreterm.Owner, r coreterm.Result) {
				t.Helper()
				if o.Scope() != sdk.ScopeAttempt {
					t.Fatalf("scope=%q", o.Scope())
				}
				if r.Outcome.SettleCustomer || r.Outcome.ReleaseConcurrency {
					t.Fatal("attempt owner must not settle customer / release concurrency")
				}
				if !r.Outcome.SettleOperator || !r.Outcome.ReleaseAttempt {
					t.Fatal("attempt owner must settle operator and release attempt")
				}
				if r.Outcome.Code != sdk.OutcomeCodeParallelLoser {
					t.Fatalf("code=%q", r.Outcome.Code)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := sdk.ScopeRequest
			if tc.name == "attempt scope does not settle customer" {
				scope = sdk.ScopeAttempt
			}
			o := coreterm.NewOwner(scope)
			if tc.setup != nil {
				tc.setup(o)
			}
			r := o.Claim(tc.cmd, tc.snap)
			if r.Won != tc.want.won {
				t.Fatalf("Won=%v want %v (err=%v)", r.Won, tc.want.won, r.Err)
			}
			if r.State != tc.want.state {
				t.Fatalf("State=%q want %q", r.State, tc.want.state)
			}
			if tc.want.errIs != nil {
				if !errors.Is(r.Err, tc.want.errIs) {
					t.Fatalf("Err=%v want %v", r.Err, tc.want.errIs)
				}
			} else if r.Err != nil {
				t.Fatalf("unexpected Err=%v", r.Err)
			}
			if tc.check != nil {
				tc.check(t, o, r)
			}
		})
	}

	// Exhaustive: every request-legal command wins from open (scope matrix covers mismatches).
	for _, cmd := range sdk.AllCommands() {
		if !cmd.AllowsScope(sdk.ScopeRequest) {
			continue
		}
		t.Run("open+"+string(cmd), func(t *testing.T) {
			t.Parallel()
			o := coreterm.NewOwner(sdk.ScopeRequest)
			r := o.Claim(cmd, coreterm.NewAccumulatorSnapshot([]byte(cmd), false))
			if !r.Won || r.Err != nil || r.State != sdk.StateTerminalizing {
				t.Fatalf("cmd=%s Won=%v State=%q Err=%v", cmd, r.Won, r.State, r.Err)
			}
			if r.Outcome.Command != cmd {
				t.Fatalf("outcome cmd=%q", r.Outcome.Command)
			}
			if r.Outcome.Code != sdk.OutcomeCodeFor(cmd) {
				t.Fatalf("outcome code=%q want %q", r.Outcome.Code, sdk.OutcomeCodeFor(cmd))
			}
			if r.Outcome.SettleOperator || r.Outcome.ReleaseAttempt {
				t.Fatal("request claim must not set attempt-plane effects")
			}
		})
	}
}

func TestOwner_Advance_LegalMatrix(t *testing.T) {
	t.Parallel()

	type step struct {
		to    sdk.State
		errIs error
	}
	path := []step{
		{to: sdk.StateWorkPending},
		{to: sdk.StateSettled},
		{to: sdk.StateReleasePending},
		{to: sdk.StateReleased},
	}
	o := coreterm.NewOwner(sdk.ScopeRequest)
	r := o.Claim(sdk.CommandNormalFinish, coreterm.NewAccumulatorSnapshot([]byte("x"), false))
	if !r.Won {
		t.Fatalf("claim: %+v", r)
	}
	for _, s := range path {
		if err := o.Advance(s.to); !errors.Is(err, s.errIs) && (s.errIs != nil || err != nil) {
			t.Fatalf("Advance(%s)=%v want %v", s.to, err, s.errIs)
		}
		if o.State() != s.to && s.errIs == nil {
			t.Fatalf("state=%q want %q", o.State(), s.to)
		}
	}

	illegal := []struct {
		from sdk.State
		to   sdk.State
	}{
		{sdk.StateOpen, sdk.StateSettled},
		{sdk.StateReleased, sdk.StateOpen},
		{sdk.StateReleased, sdk.StateWorkPending},
		{sdk.StateFailed, sdk.StateSettled},
		{sdk.StateTerminalizing, sdk.StateReleased}, // skip work_pending/settled
	}
	for _, tc := range illegal {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			t.Parallel()
			own := ownerAt(t, tc.from)
			err := own.Advance(tc.to)
			if !errors.Is(err, sdk.ErrInvalidTransition) {
				t.Fatalf("Advance(%s->%s)=%v want ErrInvalidTransition", tc.from, tc.to, err)
			}
		})
	}

	// failed from terminalizing is legal
	own := coreterm.NewOwner(sdk.ScopeRequest)
	if r := own.Claim(sdk.CommandPanic, coreterm.NewAccumulatorSnapshot(nil, false)); !r.Won {
		t.Fatal(r)
	}
	if err := own.Advance(sdk.StateFailed); err != nil {
		t.Fatalf("terminalizing->failed: %v", err)
	}
	if own.State() != sdk.StateFailed {
		t.Fatalf("state=%q", own.State())
	}
}

func ownerAt(t *testing.T, state sdk.State) *coreterm.Owner {
	t.Helper()
	o := coreterm.NewOwner(sdk.ScopeRequest)
	if state == sdk.StateOpen {
		return o
	}
	if r := o.Claim(sdk.CommandClose, coreterm.NewAccumulatorSnapshot([]byte("s"), false)); !r.Won {
		t.Fatalf("claim: %+v", r)
	}
	order := []sdk.State{
		sdk.StateTerminalizing,
		sdk.StateWorkPending,
		sdk.StateSettled,
		sdk.StateReleasePending,
		sdk.StateReleased,
	}
	for _, s := range order {
		if s == sdk.StateTerminalizing {
			if state == s {
				return o
			}
			continue
		}
		if err := o.Advance(s); err != nil {
			t.Fatalf("advance to %s: %v", s, err)
		}
		if s == state {
			return o
		}
	}
	if state == sdk.StateFailed {
		o2 := coreterm.NewOwner(sdk.ScopeRequest)
		_ = o2.Claim(sdk.CommandPanic, coreterm.NewAccumulatorSnapshot(nil, false))
		_ = o2.Advance(sdk.StateFailed)
		return o2
	}
	t.Fatalf("unsupported from state %s", state)
	return nil
}

func TestOwner_IdempotentReclaimSameCommand(t *testing.T) {
	t.Parallel()
	o := coreterm.NewOwner(sdk.ScopeRequest)
	snap := coreterm.NewAccumulatorSnapshot([]byte("once"), true)
	first := o.Claim(sdk.CommandEOF, snap)
	if !first.Won || first.Err != nil {
		t.Fatalf("first: %+v", first)
	}
	second := o.Claim(sdk.CommandEOF, coreterm.NewAccumulatorSnapshot([]byte("ignored"), false))
	if second.Won {
		t.Fatal("re-claim must not re-win effects")
	}
	if second.Err != nil {
		t.Fatalf("idempotent re-claim err=%v", second.Err)
	}
	if !second.Outcome.Snapshot.Equal(first.Outcome.Snapshot) {
		t.Fatal("re-claim must observe winner snapshot, not new input")
	}
	if second.Outcome.Command != sdk.CommandEOF {
		t.Fatalf("cmd=%q", second.Outcome.Command)
	}
	if o.State() != sdk.StateTerminalizing {
		t.Fatalf("state=%q", o.State())
	}
}

func TestOwner_ConflictingClaimObservesWinner(t *testing.T) {
	t.Parallel()
	o := coreterm.NewOwner(sdk.ScopeRequest)
	win := o.Claim(sdk.CommandClose, coreterm.NewAccumulatorSnapshot([]byte("close"), false))
	if !win.Won {
		t.Fatalf("winner: %+v", win)
	}
	lose := o.Claim(sdk.CommandCancel, coreterm.NewAccumulatorSnapshot([]byte("cancel"), true))
	if lose.Won {
		t.Fatal("loser must not win")
	}
	if !errors.Is(lose.Err, sdk.ErrConflict) {
		t.Fatalf("loser err=%v want ErrConflict", lose.Err)
	}
	if !lose.Outcome.Snapshot.Equal(win.Outcome.Snapshot) {
		t.Fatal("loser must observe winner snapshot")
	}
	if lose.Outcome.Command != sdk.CommandClose {
		t.Fatalf("loser cmd=%q want close", lose.Outcome.Command)
	}
}

func TestOwner_UnknownSecondCommandObservesWinner(t *testing.T) {
	t.Parallel()
	o := coreterm.NewOwner(sdk.ScopeAttempt)
	win := o.Claim(sdk.CommandParallelLoser, coreterm.NewAccumulatorSnapshot([]byte("a"), false))
	if !win.Won {
		t.Fatalf("winner: %+v", win)
	}
	lose := o.Claim(sdk.Command("0"), coreterm.NewAccumulatorSnapshot([]byte("b"), true))
	if lose.Won {
		t.Fatal("unknown second command must not win")
	}
	if !lose.Outcome.Snapshot.Equal(win.Outcome.Snapshot) {
		t.Fatal("unknown second command must still observe winner snapshot")
	}
	if lose.Outcome.Command != sdk.CommandParallelLoser {
		t.Fatalf("loser cmd=%q want parallel_loser", lose.Outcome.Command)
	}
}

func TestOwner_ConcurrentClaim_RecvCloseRace(t *testing.T) {
	t.Parallel()
	const n = 32
	for i := range n {
		o := coreterm.NewOwner(sdk.ScopeRequest)
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make([]coreterm.Result, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results[0] = o.Claim(sdk.CommandClose, coreterm.NewAccumulatorSnapshot([]byte("close"), false))
		}()
		go func() {
			defer wg.Done()
			<-start
			results[1] = o.Claim(sdk.CommandEOF, coreterm.NewAccumulatorSnapshot([]byte("recv"), true))
		}()
		close(start)
		wg.Wait()

		winners := 0
		var winner coreterm.Result
		for _, r := range results {
			if r.Won {
				winners++
				winner = r
			}
		}
		if winners != 1 {
			t.Fatalf("iter %d: winners=%d results=%+v", i, winners, results)
		}
		for _, r := range results {
			if !r.Outcome.Snapshot.Equal(winner.Outcome.Snapshot) {
				t.Fatalf("iter %d: divergent snapshots winner=%q other=%q",
					i, winner.Outcome.Snapshot.Bytes(), r.Outcome.Snapshot.Bytes())
			}
			if r.Outcome.Command != winner.Outcome.Command {
				t.Fatalf("iter %d: divergent commands", i)
			}
			if r.Won {
				continue
			}
			if !errors.Is(r.Err, sdk.ErrConflict) {
				t.Fatalf("iter %d: loser err=%v", i, r.Err)
			}
		}
		out, ok := o.Outcome()
		if !ok || out.Command != winner.Outcome.Command {
			t.Fatalf("iter %d: published outcome mismatch", i)
		}
	}
}

func TestOwner_OutputCommittedBlocksRetryReplacement(t *testing.T) {
	t.Parallel()
	o := coreterm.NewOwner(sdk.ScopeRequest)
	r := o.Claim(sdk.CommandGateReplacement, coreterm.NewAccumulatorSnapshot([]byte("x"), true))
	if r.Won || !errors.Is(r.Err, sdk.ErrOutputCommitted) {
		t.Fatalf("got %+v", r)
	}
	if o.State() != sdk.StateOpen {
		t.Fatalf("state must remain open, got %q", o.State())
	}

	// After a successful claim with committed output, replacement is also rejected.
	o2 := coreterm.NewOwner(sdk.ScopeRequest)
	if first := o2.Claim(sdk.CommandEOF, coreterm.NewAccumulatorSnapshot([]byte("out"), true)); !first.Won {
		t.Fatalf("first: %+v", first)
	}
	rep := o2.Claim(sdk.CommandGateReplacement, coreterm.NewAccumulatorSnapshot([]byte("retry"), false))
	if rep.Won {
		t.Fatal("replacement after output committed must not win")
	}
	if !errors.Is(rep.Err, sdk.ErrOutputCommitted) {
		t.Fatalf("err=%v want ErrOutputCommitted", rep.Err)
	}
}

func TestOwner_SnapshotImmutabilityAfterClaim(t *testing.T) {
	t.Parallel()
	raw := []byte("mutable-acc")
	snap := coreterm.NewAccumulatorSnapshot(raw, true)
	o := coreterm.NewOwner(sdk.ScopeRequest)
	r := o.Claim(sdk.CommandClose, snap)
	if !r.Won {
		t.Fatalf("claim: %+v", r)
	}
	raw[0] = 'X'
	mutated := snap.Bytes()
	mutated[0] = 'Y'
	_ = mutated
	got, ok := o.Outcome()
	if !ok {
		t.Fatal("missing outcome")
	}
	if !bytes.Equal(got.Snapshot.Bytes(), []byte("mutable-acc")) {
		t.Fatalf("snapshot mutated: %q", got.Snapshot.Bytes())
	}
	if !bytes.Equal(r.Outcome.Snapshot.Bytes(), []byte("mutable-acc")) {
		t.Fatalf("result snapshot mutated: %q", r.Outcome.Snapshot.Bytes())
	}
}

func TestOwner_AttemptMayFinishBeforeRequest(t *testing.T) {
	t.Parallel()
	req := coreterm.NewOwner(sdk.ScopeRequest)
	att := coreterm.NewOwner(sdk.ScopeAttempt)
	if r := att.Claim(sdk.CommandParallelLoser, coreterm.NewAccumulatorSnapshot([]byte("lose"), false)); !r.Won {
		t.Fatal(r)
	}
	_ = att.Advance(sdk.StateWorkPending)
	_ = att.Advance(sdk.StateSettled)
	_ = att.Advance(sdk.StateReleasePending)
	_ = att.Advance(sdk.StateReleased)
	if att.State() != sdk.StateReleased {
		t.Fatalf("attempt state=%q", att.State())
	}
	if req.State() != sdk.StateOpen {
		t.Fatalf("request must still be open, got %q", req.State())
	}
}

func FuzzOwner_CommandSequences(f *testing.F) {
	for _, c := range sdk.AllCommands() {
		f.Add(string(c), string(sdk.CommandClose), false, true)
	}
	f.Fuzz(func(t *testing.T, c1, c2 string, out1, out2 bool) {
		cmd1 := sdk.Command(c1)
		cmd2 := sdk.Command(c2)
		scope := sdk.ScopeRequest
		if cmd1.IsKnown() && !cmd1.AllowsScope(sdk.ScopeRequest) && cmd1.AllowsScope(sdk.ScopeAttempt) {
			scope = sdk.ScopeAttempt
		}
		o := coreterm.NewOwner(scope)
		r1 := o.Claim(cmd1, coreterm.NewAccumulatorSnapshot([]byte("a"), out1))
		if !cmd1.IsKnown() {
			if r1.Won || o.State() != sdk.StateOpen {
				t.Fatalf("unknown first command must not claim: %+v state=%s", r1, o.State())
			}
			return
		}
		if !cmd1.AllowsScope(scope) {
			if r1.Won || !errors.Is(r1.Err, sdk.ErrScopeMismatch) {
				t.Fatalf("scope-illegal first command: %+v", r1)
			}
			return
		}
		if cmd1.IsRetryOrReplacement() && out1 {
			if r1.Won || !errors.Is(r1.Err, sdk.ErrOutputCommitted) {
				t.Fatalf("committed replacement must fail: %+v", r1)
			}
			return
		}
		if !r1.Won {
			t.Fatalf("known in-scope command should win: %+v", r1)
		}
		// Second claim only after a published winner exists.
		r2 := o.Claim(cmd2, coreterm.NewAccumulatorSnapshot([]byte("b"), out2))
		out, ok := o.Outcome()
		if !ok || !out.Snapshot.Equal(r1.Outcome.Snapshot) {
			t.Fatal("published snapshot mismatch")
		}
		if r2.Won {
			t.Fatal("second claim must not win")
		}
		if !r2.Outcome.Snapshot.Equal(r1.Outcome.Snapshot) {
			t.Fatal("second must observe first snapshot")
		}
	})
}
