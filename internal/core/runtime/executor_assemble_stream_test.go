package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestAssembleExecutorStream_WrapperSelection(t *testing.T) {
	t.Parallel()

	sel, err := routing.Parse("exec:m")
	if err != nil {
		t.Fatal(err)
	}
	newPlan := func() *routePlanState {
		return &routePlanState{
			routeFacts: routeFacts{sel: sel},
			progress: newRecoveryController(recoveryControllerInput{
				budget: &attemptBudget{max: 3},
				sel:    sel,
			}),
		}
	}
	newPrep := func() *preparedRequest {
		call := &aipapi.Call{
			Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions},
			Session: aipapi.SessionRef{
				ALegID: "a-1",
			},
		}
		preSession := session.SessionView{
			ALegID: "a-1",
		}
		ibt, err := newIdentityBoundTurn(
			"assemble-test",
			call,
			execview.PrincipalView{},
			scope.PrincipalScopeView{},
			false,
			aipworkspace.WorkspaceView{},
			b2bua.ALegRecord{ALegID: "a-1"},
			routeAuthoritySnapshot{},
			execctx.SecureSessionTurn{},
			false,
			preSession,
		)
		if err != nil {
			panic(err)
		}
		return &preparedRequest{
			identity: ibt,
			call:     ibt.call,
			guard:    &preStreamGuard{},
		}
	}
	stream := aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}})
	thinkerCand := routing.AttemptCandidate{
		Primary:         routing.Primary{Backend: "thinker", Model: "m"},
		InterleavedRole: interleavedstate.RoleThinker,
	}
	plainCand := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "exec", Model: "m"},
	}
	out := openedAttempt{
		ready: newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  plainCand,
		}, pendingSelectionEffects{}),
	}

	t.Run("plain", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), out)
		if err != nil {
			t.Fatal(err)
		}
		if !localPrep.guard.handedOver {
			t.Fatal("guard must be handed over")
		}
		if _, ok := got.(*retryRecvStream); !ok {
			t.Fatalf("want *retryRecvStream, got %T", got)
		}
	})

	t.Run("hidden interleaved", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{StreamToClient: "hidden"}, interleavedthinking.NewMemoStore(1024))
		hiddenOut := out
		hiddenOut.ready = newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  thinkerCand,
		}, pendingSelectionEffects{})
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), hiddenOut)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*hiddenInterleavedStream); !ok {
			t.Fatalf("want *hiddenInterleavedStream, got %T", got)
		}
	})

	t.Run("visible interleaved", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		ex := TestExecutor()
		ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{StreamToClient: "visible"}, interleavedthinking.NewMemoStore(1024))
		visibleOut := out
		visibleOut.ready = newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  thinkerCand,
		}, pendingSelectionEffects{})
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), visibleOut)
		if err != nil {
			t.Fatal(err)
		}
		s, ok := got.(*interleavedContinuationStream)
		if !ok {
			t.Fatalf("want *interleavedContinuationStream, got %T", got)
		}
		if !s.surfaceVisible {
			t.Fatal("visible wrapper must set surfaceVisible")
		}
	})

	t.Run("wire payload skips interleaved wrapper", func(t *testing.T) {
		t.Parallel()
		localPrep := newPrep()
		wp := &wireAttemptPayload{}
		localPrep.wirePayload = wp
		localPrep.wirePayload = wp
		ex := TestExecutor()
		ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{StreamToClient: "visible"}, interleavedthinking.NewMemoStore(1024))
		wireOut := out
		wireOut.ready = newReadyAttempt(&attemptSession{
			inner: stream,
			cand:  thinkerCand,
		}, pendingSelectionEffects{})
		got, err := ex.assembleExecutorStream(context.Background(), localPrep, newPlan(), wireOut)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.(*retryRecvStream); !ok {
			t.Fatalf("want *retryRecvStream on wire payload even with interleaved enabled, got %T", got)
		}
	})
}
