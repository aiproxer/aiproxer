package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

// dummyUsageSink acts as a no-op billing collector for testing.
type dummyUsageSink struct{}

func (dummyUsageSink) AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error {
	return nil
}

func (dummyUsageSink) AppendCall(context.Context, usageevidence.CallUsageRecord) error {
	return nil
}

// wireDummyBilling configures mock billing fields on the executor to ensure
// billing-related policies and checks do not fail during tests.
func wireDummyBilling(ex *Executor) {
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{
			AccountID: "acct", CallID: in.CallID, Status: billingport.ExposureOpen,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
		}, nil
	})
	ex.TerminalUsageSink = dummyUsageSink{}
}

// phase1FaultStore wraps b2bua.Store to inject allocation errors.
type phase1FaultStore struct {
	b2bua.Store
	nextBLegErr error
}

func (s *phase1FaultStore) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	if s.nextBLegErr != nil {
		return b2bua.BLegRecord{}, s.nextBLegErr
	}
	return s.Store.NextBLeg(ctx, aLegID)
}

// TestPhase1_1_CharacterizeAcquisitionFailurePoints implements Task 1.1:
// Characterize acquisition, readiness and publication failure points by injecting deterministic faults.
func TestPhase1_1_CharacterizeAcquisitionFailurePoints(t *testing.T) {
	t.Parallel()

	t.Run("budget_acquisition_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:  true,
				Reserved: true,
			},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

		// Set budget to already fully used so tryAcquire fails
		budget := &attemptBudget{max: 1, used: 1}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on budget acquisition failure, got nil")
		}
		if !errors.Is(err, aipapi.ErrMaxRouteAttempts) {
			t.Errorf("expected ErrMaxRouteAttempts, got: %v", err)
		}

		// Verify no real (non-estimate) admit was called, but estimate check was run
		realAdmits := 0
		auth.admitMu.Lock()
		for _, in := range auth.admitInputsV {
			if !in.EstimateOnly {
				realAdmits++
			}
		}
		auth.admitMu.Unlock()
		if realAdmits != 0 {
			t.Errorf("real admit called: %d times", realAdmits)
		}
		if auth.admitCalls.Load() != 1 {
			t.Errorf("expected exactly 1 admit call (precheck estimate), got: %d", auth.admitCalls.Load())
		}
	})

	t.Run("bleg_allocation_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:  true,
				Reserved: true,
			},
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

		// Wrap store with fault injector
		ex.Store = &phase1FaultStore{
			Store:       ex.Store,
			nextBLegErr: errors.New("injected next b-leg allocation failure"),
		}

		budget := &attemptBudget{max: 10}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on b-leg allocation failure, got nil")
		}
		if !strings.Contains(err.Error(), "injected next b-leg allocation failure") {
			t.Errorf("expected b-leg allocation error message, got: %v", err)
		}

		// Verify no real (non-estimate) admit was called, but estimate check was run
		realAdmits := 0
		auth.admitMu.Lock()
		for _, in := range auth.admitInputsV {
			if !in.EstimateOnly {
				realAdmits++
			}
		}
		auth.admitMu.Unlock()
		if realAdmits != 0 {
			t.Errorf("real admit called: %d times", realAdmits)
		}
		if auth.admitCalls.Load() != 1 {
			t.Errorf("expected exactly 1 admit call (precheck estimate), got: %d", auth.admitCalls.Load())
		}
	})

	t.Run("authority_admission_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitErr: errors.New("injected authority admission failure"),
		}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

		budget := &attemptBudget{max: 10}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on authority admission failure, got nil")
		}
		if !strings.Contains(err.Error(), "usage authority unavailable") {
			t.Errorf("expected authority admission error message, got: %v", err)
		}
	})

	t.Run("bleg_registration_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-reg-fail",
				ReservedAmount: authorityInputAmount(7),
			},
		}
		ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
		ex.ALegLifecycle = coord
		aScope := coord.StartALeg(aLegID)

		// Cancel A-leg inside Open to trigger RegisterBLeg failure immediately after Open succeeds
		backend.openFn = func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
		}

		budget := &attemptBudget{max: 10}
		req := authorityOpenRequest(t, aLegID, budget)
		req.reqFacts.aScope = aScope
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on b-leg registration failure, got nil")
		}
		if !errors.Is(err, leglifecycle.ErrALegCanceled) {
			t.Errorf("expected ErrALegCanceled, got: %v", err)
		}

		// Verify authority release was not called because backend dial was incurred, so Settle was called instead.
		if auth.releaseCalls.Load() != 0 {
			t.Errorf("expected 0 release calls, got %d", auth.releaseCalls.Load())
		}
		if auth.settleCalls.Load() != 1 {
			t.Errorf("expected exactly 1 settle call, got %d", auth.settleCalls.Load())
		}
	})

	t.Run("backend_open_failure", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "res-open-fail",
				ReservedAmount: authorityInputAmount(7),
			},
		}
		ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

		// Inject backend open error
		backend.openFn = func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, errors.New("injected backend open failure")
		}

		budget := &attemptBudget{max: 10}
		req := authorityOpenRequest(t, aLegID, budget)
		plan := candidatePlan{cand: authorityCandidate()}

		_, err := ex.evaluateAndOpenCandidate(context.Background(), req, plan)
		if err == nil {
			t.Fatal("expected error on backend open failure, got nil")
		}
		if !strings.Contains(err.Error(), "injected backend open failure") {
			t.Errorf("expected backend open failure message, got: %v", err)
		}

		// Verify authority release was not called because backend dial was incurred, so Settle was called instead.
		if auth.releaseCalls.Load() != 0 {
			t.Errorf("expected 0 release calls, got %d", auth.releaseCalls.Load())
		}
		if auth.settleCalls.Load() != 1 {
			t.Errorf("expected exactly 1 settle call, got %d", auth.settleCalls.Load())
		}
	})
}

// TestPhase1_1_ObserverStartupFailure_Initial tests that final observer startup failure
// is handled during initial stream assembly.
func TestPhase1_1_ObserverStartupFailure_Initial(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireDummyBilling(ex)

	// Configure a fail-closed stream observer factory
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{failClosedStreamObserverFactory{}},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-obs-initial-abort", ContinuityKey: "sess-obs-initial-abort"},
		Route:    aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}

	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected Execute failure due to observer startup failure, got nil")
	}

	var pde *aipapi.PolicyDecisionError
	if !errors.As(err, &pde) {
		t.Fatalf("expected PolicyDecisionError, got: %T (%v)", err, err)
	}
	if pde.Cause == nil || !strings.Contains(pde.Cause.Error(), "assemble observer open boom") {
		t.Errorf("expected cause to contain 'assemble observer open boom', got: %v", pde.Cause)
	}
}

// TestPhase1_1_ObserverStartupFailure_Replacement characterizes the defect where
// observer startup failure on a replacement attempt leaks the published attempt in the slot.
func TestPhase1_1_ObserverStartupFailure_Replacement(t *testing.T) {
	t.Parallel()

	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "replacement-reservation",
			ReservedAmount: authorityInputAmount(8),
			PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
		},
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			StreamObserverFactories: []response.StreamObserverFactory{failClosedStreamObserverFactory{}},
		}),
	})

	budget := &attemptBudget{max: 3}
	baseline := aipapi.Call{
		ID:         "request-1",
		Route:      aipapi.RouteIntent{Selector: "backend-1:model-1"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming},
		Messages:   testMinimalUserMessages(),
	}
	sel, _ := routing.Parse("backend-1:model-1")

	oldStream := &closeReplacementRaceStream{}
	replacementStream := &closeReplacementRaceStream{}
	backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return replacementStream, nil
	}

	oldAdmission := auth.admitResult
	oldAdmission.ReservationID = "old-reservation"
	oldAuthority := attemptAuthorityState{
		admissionInput:  testAuthorityAdmissionInput(8),
		admissionResult: oldAdmission,
	}
	terminal := newTurnTerminal()
	rs := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: baseline,
			aLegID:   aLegID,
			traceID:  "trace-1",
		}),
		terminal: terminal,
		recovery: newRecoveryController(recoveryControllerInput{
			e:             ex,
			affinityStore: ex.AffinityStore,
			log:           ex.Log,
			opener:        newReplacementOpener(ex, hooks.New(hooks.Config{}), terminal.aLegScope()),
			budget:        budget,
			sel:           sel,
			session:       &routing.SessionRoutingState{},
			excluded:      map[string]struct{}{},
			rng:           routing.NewSeededRng(1),
		}),
		attempt: testAttemptSlot(
			b2bua.BLegRecord{ALegID: aLegID, BLegID: "old-bleg", Seq: 1},
			authorityCandidate(),
			testAuthorityLifecycle(ex, oldAuthority, authorityCandidate()),
		),
		responsePipeline: newResponsePipelineForExecutor(ex),
	}

	testStoreInner(rs, oldStream)
	oldAttempt := rs.attempt.snapshot()

	testRetirePriorAttempt(rs)

	// Trigger replacement iteration
	plan, err := rs.recovery.tryReplacementIteration(context.Background(), rs.facts.terminalFacts(), rs.attempt.require(), rs.terminal.committed())
	if err != nil || !plan.opened {
		t.Fatalf("failed to open replacement candidate: %v", err)
	}

	err = rs.terminal.registerReplacement(context.Background(), plan.open, plan.next)
	if err != nil {
		t.Fatalf("failed to register replacement: %v", err)
	}

	// In Phase 2, we run readiness on the unpublished replacement attempt BEFORE swapping:
	ready := plan.next
	obsErr := ready.Prepare(context.Background(), rs.facts, rs.responsePipeline, rs.terminal.committed())
	if obsErr == nil {
		t.Fatal("expected observer startup error, got nil")
	}
	if !ready.IsConsumed() {
		t.Fatal("expected ready capability to be disposed on observer startup failure")
	}

	// Verify that the slot still holds the old attempt (not the failed replacement)
	if rs.attempt.snapshot() != oldAttempt {
		t.Error("failed replacement attempt was published/swapped into the slot despite observer startup failure")
	}
}

// TestPhase1_2_FreezeReplacementCloseAndRaces implements Task 1.2:
// Freeze replacement, Close and terminal race semantics.
func TestPhase1_2_FreezeReplacementCloseAndRaces(t *testing.T) {
	t.Parallel()

	t.Run("replacement_vs_close_linearization", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{
			status: controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady},
			admitResult: usagelimit.AdmissionResult{
				Allowed:        true,
				Reserved:       true,
				ReservationID:  "replacement-reservation",
				ReservedAmount: authorityInputAmount(8),
				PolicyRecord:   policydecision.Record{ReasonCode: "reserved"},
			},
		}
		ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		wireDummyBilling(ex)

		budget := &attemptBudget{max: 3}
		baseline := aipapi.Call{
			ID:         "request-1",
			Route:      aipapi.RouteIntent{Selector: "backend-1:model-1"},
			Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming},
			Messages:   testMinimalUserMessages(),
		}
		sel, _ := routing.Parse("backend-1:model-1")

		oldStream := &closeReplacementRaceStream{}
		replacementStream := &closeReplacementRaceStream{}
		replacementOpenEntered := make(chan struct{})
		releaseReplacementOpen := make(chan struct{})
		backend.openFn = func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			close(replacementOpenEntered)
			<-releaseReplacementOpen
			return replacementStream, nil
		}

		oldAdmission := auth.admitResult
		oldAdmission.ReservationID = "old-reservation"
		oldAuthority := attemptAuthorityState{
			admissionInput:  testAuthorityAdmissionInput(8),
			admissionResult: oldAdmission,
		}
		terminal := newTurnTerminal()
		rs := &retryRecvStream{
			facts: testRecvTurnFacts(recvTurnFacts{
				baseline: baseline,
				aLegID:   aLegID,
				traceID:  "trace-1",
			}),
			terminal: terminal,
			recovery: newRecoveryController(recoveryControllerInput{
				e:             ex,
				affinityStore: ex.AffinityStore,
				log:           ex.Log,
				opener:        newReplacementOpener(ex, hooks.New(hooks.Config{}), terminal.aLegScope()),
				budget:        budget,
				sel:           sel,
				session:       &routing.SessionRoutingState{},
				excluded:      map[string]struct{}{},
				rng:           routing.NewSeededRng(1),
			}),
			attempt: testAttemptSlot(
				b2bua.BLegRecord{ALegID: aLegID, BLegID: "old-bleg", Seq: 1},
				authorityCandidate(),
				testAuthorityLifecycle(ex, oldAuthority, authorityCandidate()),
			),
			responsePipeline: newResponsePipeline(),
		}

		testStoreInner(rs, oldStream)

		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(releaseReplacementOpen) })

		type replResult struct {
			opened bool
			err    error
		}
		resultCh := make(chan replResult, 1)
		go func() {
			testRetirePriorAttempt(rs)

			plan, err := rs.recovery.tryReplacementIteration(context.Background(), rs.facts.terminalFacts(), rs.attempt.require(), rs.terminal.committed())
			if err == nil && plan.opened {
				if regErr := rs.terminal.registerReplacement(context.Background(), plan.open, plan.next); err == nil {
					err = regErr
				}
				if err == nil {
					ready := plan.next
					if _, published := rs.attempt.swapIfOpen(ready); !published {
						rs.terminal.cleanupUnpublishedReplacement(context.Background(), plan.next)
					}
				}
			}
			resultCh <- replResult{opened: plan.opened, err: err}
		}()

		select {
		case <-replacementOpenEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("replacement open did not enter barrier")
		}

		// Call Close concurrently to race replacement publication
		if err := rs.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		releaseOnce.Do(func() { close(releaseReplacementOpen) })
		res := <-resultCh

		if res.opened && rs.attempt.snapshot().bleg.BLegID == "replacement-reservation" {
			t.Error("replacement attempt was published after Close won the race")
		}
	})

	t.Run("no_replacement_occurs_after_output_commitment", func(t *testing.T) {
		t.Parallel()
		auth := &recordingAuthorityService{}
		ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
		budget := &attemptBudget{max: 3}
		terminal := newTurnTerminal()

		rs := &retryRecvStream{
			facts:    testRecvTurnFacts(recvTurnFacts{aLegID: aLegID}),
			terminal: terminal,
			recovery: newRecoveryController(recoveryControllerInput{
				e:      ex,
				budget: budget,
			}),
			attempt: testAttemptSlot(
				b2bua.BLegRecord{ALegID: aLegID, BLegID: "b-leg", Seq: 1},
				authorityCandidate(),
				authorityLifecycle{},
			),
			responsePipeline: newResponsePipeline(),
		}

		// Mark request committed
		rs.terminal.markCommitted(rs.attempt.require())

		// Attempt to start replacement. Since the request is committed, no replacement iteration should occur.
		plan, err := rs.recovery.tryReplacementIteration(context.Background(), rs.facts.terminalFacts(), rs.attempt.require(), rs.terminal.committed())
		if err == nil && plan.opened {
			t.Fatal("expected no replacement iteration to succeed after output commitment")
		}
	})

	t.Run("race_compating_terminal_callers", func(t *testing.T) {
		t.Parallel()
		var callCount atomic.Int32
		ex := &Executor{
			CoreRuntime: CoreRuntime{Backends: map[string]execbackend.Backend{
				"backend": {
					FinalizeBilling: func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error) {
						callCount.Add(1)
						return aipapi.Event{Kind: aipapi.EventUsageDelta}, nil
					},
				},
			}},
		}
		wireDummyBilling(ex)

		rs := &retryRecvStream{
			responsePipeline: newResponsePipelineForExecutor(ex),
			terminal:         newTurnTerminal(),
			facts:            testRecvTurnFacts(recvTurnFacts{aLegID: "a-race", billingCallState: &billingCallState{}}),
			attempt: testAttemptSlot(
				b2bua.BLegRecord{ALegID: "a-race", BLegID: "b-race", Seq: 1},
				routing.AttemptCandidate{Primary: routing.Primary{Backend: "backend", Model: "model"}},
				authorityLifecycle{},
			),
		}
		bindTestRuntimeOwners(rs, ex)

		finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				_, _, _ = rs.terminal.finalizeResponseFinishedAuthority(context.Background(), finish, rs.facts.terminalFacts(), rs.attempt.snapshot(), rs.responsePipeline)
			})
		}
		wg.Wait()

		if callCount.Load() != 1 {
			t.Errorf("expected exactly 1 terminal execution, got %d", callCount.Load())
		}
	})

	t.Run("request_vs_attempt_terminal_lifetime_separation", func(t *testing.T) {
		t.Parallel()
		ex := TestExecutor()
		terminal := newTurnTerminal()
		bindTurnTerminalRuntime(terminal, ex)

		// Terminate attempt session
		att := testAttemptSlot(
			b2bua.BLegRecord{ALegID: "a-sep", BLegID: "b-sep", Seq: 1},
			authorityCandidate(),
			authorityLifecycle{},
		)
		sess := att.snapshot()

		// Abort the attempt session
		_ = sess.AbortBeforeReturn(context.Background(), errors.New("attempt abort"))

		// Prove that the attempt session's stream is nil/closed, but the turn terminal (request scope) is not finished
		if sess.loadInner() != nil {
			t.Error("attempt session stream was not cleaned up/nil after abort")
		}
		if terminal.finished() {
			t.Error("request turn terminal is finished; attempt termination must remain separate from request terminal lifetime")
		}
	})
}
