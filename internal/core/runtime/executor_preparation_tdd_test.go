package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestIdentityBoundTurn_Invariants(t *testing.T) {
	// Test construction invariants
	_, err := newIdentityBoundTurn("", nil, execview.PrincipalView{}, scope.PrincipalScopeView{}, false, aipworkspace.WorkspaceView{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, execctx.SecureSessionTurn{}, false, session.SessionView{})
	if err == nil {
		t.Error("expected error for empty traceID and nil call")
	}

	call := &aipapi.Call{
		ID: "trace-123",
		Session: aipapi.SessionRef{
			ALegID:                 "aleg-123",
			AuthoritativeSessionID: "session-123",
		},
	}
	_, err = newIdentityBoundTurn("trace-123", call, execview.PrincipalView{}, scope.PrincipalScopeView{}, false, aipworkspace.WorkspaceView{}, b2bua.ALegRecord{}, routeAuthoritySnapshot{}, execctx.SecureSessionTurn{}, false, session.SessionView{})
	if err == nil {
		t.Error("expected error for empty A-Leg ID")
	}

	aLeg := b2bua.ALegRecord{ALegID: "aleg-123"}
	preSession := session.SessionView{
		ALegID:                 "aleg-123",
		AuthoritativeSessionID: "session-123",
		TurnID:                 "turn-1",
		WorkspaceID:            "ws-1",
	}
	ibt, err := newIdentityBoundTurn("trace-123", call, execview.PrincipalView{ID: "p-1"}, scope.PrincipalScopeView{Origin: "internal"}, true, aipworkspace.WorkspaceView{ID: "ws-1"}, aLeg, routeAuthoritySnapshot{}, execctx.SecureSessionTurn{SessionID: "session-123", TurnID: "turn-1"}, true, preSession)
	if err != nil {
		t.Fatalf("unexpected error constructing valid identityBoundTurn: %v", err)
	}

	if ibt.traceID != "trace-123" {
		t.Errorf("traceID: got %q, want %q", ibt.traceID, "trace-123")
	}
	if ibt.call == call {
		t.Errorf("expected call to be deep-cloned (frozen), but got matching pointer")
	}
	if ibt.call.ID != call.ID {
		t.Errorf("call content mismatch: got %q, want %q", ibt.call.ID, call.ID)
	}
}

func TestIdentityBoundTurn_ContextProjection(t *testing.T) {
	call := &aipapi.Call{
		ID: "trace-123",
		Session: aipapi.SessionRef{
			ALegID:                 "aleg-123",
			AuthoritativeSessionID: "session-123",
		},
	}
	aLeg := b2bua.ALegRecord{ALegID: "aleg-123"}
	p := execview.PrincipalView{ID: "p-1"}
	s := scope.PrincipalScopeView{Origin: "internal"}
	ws := aipworkspace.WorkspaceView{ID: "ws-1"}
	st := execctx.SecureSessionTurn{SessionID: "session-123", TurnID: "turn-1"}
	ps := session.SessionView{
		ALegID:                 "aleg-123",
		AuthoritativeSessionID: "session-123",
		TurnID:                 "turn-1",
		WorkspaceID:            "ws-1",
	}

	ibt, err := newIdentityBoundTurn("trace-123", call, p, s, true, ws, aLeg, routeAuthoritySnapshot{}, st, true, ps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()
	projectedCtx := ibt.projectContext(ctx)

	// Verify principal projection
	gotPrincipal, ok := execview.PrincipalFromContext(projectedCtx)
	if !ok || gotPrincipal.ID != "p-1" {
		t.Errorf("expected principal ID %q, got %q (ok=%t)", "p-1", gotPrincipal.ID, ok)
	}

	// Verify scope projection
	gotScope, ok := scope.ScopeFromContext(projectedCtx)
	if !ok || gotScope.Origin != "internal" {
		t.Errorf("expected scope origin %q, got %q (ok=%t)", "internal", gotScope.Origin, ok)
	}

	// Verify secure turn projection
	gotST, ok := execctx.SecureSessionTurnFromContext(projectedCtx)
	if !ok || gotST.TurnID != "turn-1" {
		t.Errorf("expected secure turn ID %q, got %q (ok=%t)", "turn-1", gotST.TurnID, ok)
	}
}

func TestPreStreamGuard_LifecycleAndHandoff(t *testing.T) {
	t.Run("cleanup on close without handoff", func(t *testing.T) {
		ex := &Executor{}
		coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		mockScope := coord.StartALeg("aleg-123")
		ctx := context.Background()

		guard := &preStreamGuard{
			executor:                 ex,
			ctx:                      ctx,
			aScope:                   mockScope,
			requestAuthorityAdmitted: false,
		}

		guard.Close()

		if mockScope.Err() == nil {
			t.Error("expected ALeg scope to be cancelled")
		}
		if !guard.closed {
			t.Error("expected guard to be marked closed")
		}
	})

	t.Run("no cleanup after handoff", func(t *testing.T) {
		ex := &Executor{}
		coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		mockScope := coord.StartALeg("aleg-123")
		ctx := context.Background()

		guard := &preStreamGuard{
			executor:                 ex,
			ctx:                      ctx,
			aScope:                   mockScope,
			requestAuthorityAdmitted: false,
		}

		guard.Handoff()
		guard.Close()

		if mockScope.Err() != nil {
			t.Error("expected no cleanup after handoff")
		}
	})
}
