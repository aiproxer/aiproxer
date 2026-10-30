package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func terminalTestScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-terminal"),
		TenantID:    scope.Known("tenant-terminal"),
		Origin:      scope.OriginClient,
	}
}

func TestWithTerminalScopeFrozenAuthoritative(t *testing.T) {
	t.Parallel()
	frozen := terminalTestScope()
	live := terminalTestScope()
	live.PrincipalID = scope.Known("user-live-conflict")
	live.TenantID = scope.Known("tenant-live-conflict")
	out := withTerminalScope(scope.WithScope(context.Background(), live), frozen)
	got, ok := scope.ScopeFromContext(out)
	if !ok {
		t.Fatal("frozen scope must be present")
	}
	if !got.PrincipalID.Equal(frozen.PrincipalID) || !got.TenantID.Equal(frozen.TenantID) {
		t.Fatalf("conflicting live scope must not replace frozen: got %+v/%+v want %+v/%+v",
			got.PrincipalID, got.TenantID, frozen.PrincipalID, frozen.TenantID)
	}
}

func TestWithTerminalScopeSameLiveFrozenUnchanged(t *testing.T) {
	t.Parallel()
	frozen := terminalTestScope()
	out := withTerminalScope(scope.WithScope(context.Background(), frozen), frozen)
	got, ok := scope.ScopeFromContext(out)
	if !ok || !got.PrincipalID.Equal(frozen.PrincipalID) || !got.TenantID.Equal(frozen.TenantID) {
		t.Fatalf("same live/frozen must stay unchanged: %+v", got)
	}
}

func TestWithTerminalScopeFallsBackToFrozen(t *testing.T) {
	t.Parallel()
	frozen := terminalTestScope()
	out := withTerminalScope(context.Background(), frozen)
	got, ok := scope.ScopeFromContext(out)
	if !ok || !got.PrincipalID.Equal(frozen.PrincipalID) {
		t.Fatalf("frozen scope must attach to detached context: %+v", got)
	}
	// Cancellation and deadlines stay detached: only values travel.
	select {
	case <-out.Done():
		t.Fatal("frozen scope must not introduce cancellation")
	default:
	}
}

func TestWithTerminalScopeKeepsZeroWhenNothingTrusted(t *testing.T) {
	t.Parallel()
	out := withTerminalScope(context.Background(), scope.PrincipalScopeView{})
	if sc, ok := scope.ScopeFromContext(out); ok && sc.PrincipalID.IsKnown() {
		t.Fatalf("nothing trusted must stay scope-free: %+v", sc)
	}
}

func TestWithTerminalScopePreservesLiveWithoutFrozen(t *testing.T) {
	t.Parallel()
	live := terminalTestScope()
	out := withTerminalScope(scope.WithScope(context.Background(), live), scope.PrincipalScopeView{})
	got, ok := scope.ScopeFromContext(out)
	if !ok || !got.PrincipalID.Equal(live.PrincipalID) {
		t.Fatalf("no-frozen fallback must preserve live scope: %+v", got)
	}
}

func TestFrozenPrepScopeUsesIdentityFirst(t *testing.T) {
	t.Parallel()
	frozen := terminalTestScope()
	prep := &preparedRequest{identity: &identityBoundTurn{scope: frozen, hasPrincipal: true}}
	if got := frozenPrepScope(context.Background(), prep); !got.PrincipalID.Equal(frozen.PrincipalID) {
		t.Fatalf("identity scope must win: %+v", got)
	}
	other := terminalTestScope()
	other.PrincipalID = scope.Known("user-ctx")
	if got := frozenPrepScope(scope.WithScope(context.Background(), other), &preparedRequest{}); !got.PrincipalID.Equal(other.PrincipalID) {
		t.Fatalf("context scope must back up missing identity: %+v", got)
	}
	if got := frozenPrepScope(context.Background(), nil); got.PrincipalID.IsKnown() {
		t.Fatalf("nothing trusted must stay scope-free: %+v", got)
	}
}

func TestBillingCallStateFreezesScopeOnce(t *testing.T) {
	t.Parallel()
	state := newBillingCallState("call-scope")
	first := terminalTestScope()
	second := terminalTestScope()
	second.PrincipalID = scope.Known("user-second")
	state.freezeScope(first)
	state.freezeScope(second)
	if got := state.frozenScope(); !got.PrincipalID.Equal(first.PrincipalID) {
		t.Fatalf("first frozen scope must win: %+v", got)
	}
	var nilState *billingCallState
	nilState.freezeScope(first)
	if got := nilState.frozenScope(); got.PrincipalID.IsKnown() {
		t.Fatalf("nil state must stay scope-free: %+v", got)
	}
}

// TestHandoffBillingTurnPreservesFrozenScopeOnBackground proves the call
// closure carries the frozen facts scope even when terminalization runs on
// a detached background context (A-leg close/cancel paths).
func TestHandoffBillingTurnPreservesFrozenScopeOnBackground(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	frozen := terminalTestScope()
	var gotCtx context.Context
	term := &turnTerminal{
		appendBillingCall: func(ctx context.Context, _ usageevidence.CallUsageRecord) error {
			gotCtx = ctx
			return nil
		},
		billingWorkload: func(context.Context, string) usageevidence.WorkloadIdentity { return usageevidence.WorkloadIdentity{} },
	}
	facts := requestTerminalFacts{
		billingCallID:   callID,
		submissionID:    "sub-scope",
		aLegID:          "aleg-scope",
		sessionID:       "sess-scope",
		accountID:       "acct-scope",
		identityStamped: true,
		recvViews:       execctx.Views{Scope: frozen},
	}
	if err := term.handoffBillingTurn(context.Background(), facts, sdkterminal.CommandClose); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	got, ok := scope.ScopeFromContext(gotCtx)
	if !ok || !got.PrincipalID.Equal(frozen.PrincipalID) {
		t.Fatalf("sink ctx scope = %+v, want frozen facts scope", got)
	}
}

// TestSessionCloseTerminalizationPreservesFrozenScope proves the finding's
// headline case: terminalizeForClose runs on context.Background, yet the
// B-leg handoff still carries the session-frozen scope to the sink.
func TestSessionCloseTerminalizationPreservesFrozenScope(t *testing.T) {
	t.Parallel()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	frozen := terminalTestScope()
	var gotCtx context.Context
	var gotCall usageevidence.BillingCallID
	sess := newAttemptSession(attemptSessionInput{
		bleg:          b2bua.BLegRecord{ALegID: "aleg-close", BLegID: "bleg-close", Seq: 3},
		billingCallID: callID,
		boundaryScope: frozen,
	})
	sess.appendBillingLegStrict = func(ctx context.Context, id usageevidence.BillingCallID, _ usageevidence.CallLegUsageRecord) error {
		gotCtx = ctx
		gotCall = id
		return nil
	}
	sess.terminalizeForClose("", "", "")
	if gotCtx == nil {
		t.Fatal("close terminalization must reach the leg sink")
	}
	got, ok := scope.ScopeFromContext(gotCtx)
	if !ok || !got.PrincipalID.Equal(frozen.PrincipalID) {
		t.Fatalf("sink ctx scope = %+v, want frozen session scope", got)
	}
	if gotCall != callID {
		t.Fatalf("sink call = %q, want %q", gotCall, callID)
	}
}
