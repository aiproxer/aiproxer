package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestExecutor_SharedProgressParity(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	aLeg, err := st.CreateALeg(context.Background(), "parity-test")
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLeg.ALegID)

	var openCalls []string
	failingBe := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
		}),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openCalls = append(openCalls, cand.Key)
			return nil, aipapi.RecoverablePreOutputError(errors.New("open failed"))
		},
	}
	successBe := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
		}),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			openCalls = append(openCalls, cand.Key)
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
		},
	}

	ex := TestExecutor()
	ex.Store = st
	ex.Backends = map[string]execbackend.Backend{
		"failing-be": failingBe,
		"success-be": successBe,
	}

	sel, err := routing.Parse("failing-be:m|success-be:m")
	if err != nil {
		t.Fatal(sel)
	}

	rng := routing.NewSeededRng(1)
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
			rng: rng,
		},
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "failing-be:m|success-be:m"},
		Session: aipapi.SessionRef{
			ALegID: aLeg.ALegID,
		},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	preSession := session.SessionView{
		ALegID: aLeg.ALegID,
	}
	ibt, err := newIdentityBoundTurn(
		"parity-test",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		aLeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		t.Fatal(err)
	}
	prep := &preparedRequest{
		identity: ibt,
		call:     ibt.call,
		aScope:   aScope,
	}

	// 1. Run initial open
	out, err := ex.openInitialAttempt(context.Background(), prep, plan)
	if err != nil {
		t.Fatalf("openInitialAttempt failed: %v", err)
	}

	t.Logf("openCalls: %v", openCalls)
	t.Logf("excluded: %v", plan.progress.excluded)

	if out.ready == nil || out.ready.session == nil {
		t.Fatal("expected opened attempt session")
	}
	if out.ready.Candidate().Primary.Backend != "success-be" {
		t.Fatalf("expected success-be, got %s", out.ready.Candidate().Primary.Backend)
	}

	// The progress controller must be shared, and we should check that failing-be was excluded in it
	progress := plan.progress
	if progress == nil {
		t.Fatal("expected plan.progress to be initialized")
	}

	if _, ok := progress.excluded["failing-be:m"]; !ok {
		t.Errorf("expected failing-be to be marked excluded in progress, got: %v", progress.excluded)
	}

	// 2. Simulate replacement / retry using the replacement opener
	opener := progress.opener
	if opener == nil {
		t.Fatal("expected progress.opener to be populated")
	}

	// Run replacement opener to simulate a retry on the same progress
	priorOutcome := priorAttemptOutcome{
		attempt: out.ready.session,
		retired: true,
	}
	req := replacementOpenRequest{
		facts: requestTerminalFacts{
			call:          *prep.call,
			traceID:       "parity-test",
			aLegID:        aLeg.ALegID,
			billingCallID: prep.billingCallID,
			billingState:  prep.billingCallState,
		},
		pinnedFacts: prep.recvTurnFacts,
		recovery:    progress.openSnapshot(),
		prior:       priorOutcome,
		isRetryPath: true,
	}

	res, err := opener(context.Background(), req)
	if err != nil {
		t.Fatalf("replacement opener failed: %v", err)
	}

	if !res.opened {
		t.Fatal("expected replacement opener to open successfully")
	}
	if res.cand.Primary.Backend != "success-be" {
		t.Fatalf("expected success-be for replacement open, got %s", res.cand.Primary.Backend)
	}

	// Check that we reused the same progress pointer
	if req.recovery.progress != progress {
		t.Errorf("expected recovery progress pointer to be identical, got %p, want %p", req.recovery.progress, progress)
	}
}
