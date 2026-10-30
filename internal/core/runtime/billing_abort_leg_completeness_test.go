package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

type abortJoinCapture struct {
	mu    sync.Mutex
	calls []usageevidence.CallUsageRecord
	legs  []usageevidence.CallLegUsageRecord
}

func (c *abortJoinCapture) appendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, record)
	return nil
}

func (c *abortJoinCapture) AppendCall(ctx context.Context, record usageevidence.CallUsageRecord) error {
	return c.appendCall(ctx, record)
}

func (c *abortJoinCapture) AppendLeg(ctx context.Context, record usageevidence.CallLegUsageRecord) error {
	return c.appendLeg(ctx, record)
}

func (c *abortJoinCapture) appendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	sealed, err := record.Seal()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.legs = append(c.legs, sealed)
	return nil
}

func (c *abortJoinCapture) snapshot() ([]usageevidence.CallUsageRecord, []usageevidence.CallLegUsageRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	calls := append([]usageevidence.CallUsageRecord(nil), c.calls...)
	legs := append([]usageevidence.CallLegUsageRecord(nil), c.legs...)
	return calls, legs
}

type failClosedStreamObserverFactory struct{}

func (failClosedStreamObserverFactory) ID() string                        { return "fail-closed-assemble" }
func (failClosedStreamObserverFactory) Order() int                        { return 0 }
func (failClosedStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (failClosedStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, errors.New("assemble observer open boom")
}

func wireAbortBilling(ex *Executor, capture *abortJoinCapture) {
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{
			AccountID: "acct", CallID: in.CallID, Status: billingport.ExposureOpen,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
		}, nil
	})
	ex.TerminalUsageSink = capture
}

func assertJoinableAbort(t *testing.T, calls []usageevidence.CallUsageRecord, legs []usageevidence.CallLegUsageRecord) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 1", len(calls))
	}
	if len(calls[0].ExpectedBLegIDs) == 0 {
		t.Fatal("expected at least one frozen B-leg after Open")
	}
	if len(legs) == 0 {
		t.Fatal("expected independent terminal leg rows")
	}
	seen := map[string]bool{}
	for _, leg := range legs {
		seen[strings.TrimSpace(leg.BLegID)] = true
		if leg.Outcome != usageevidence.LegOutcomeFailed && leg.Outcome != usageevidence.LegOutcomeNeverStarted {
			t.Fatalf("leg %q outcome = %s, want Failed or NeverStarted", leg.BLegID, leg.Outcome)
		}
	}
	for _, id := range calls[0].ExpectedBLegIDs {
		if !seen[id] {
			t.Fatalf("expected B-leg %q missing from terminal leg rows %#v", id, legs)
		}
	}
	if _, err := usageevidence.JoinCompleteCall(calls[0], legs); err != nil {
		t.Fatalf("abort path must remain joinable: %v", err)
	}
}

func TestAssembleAbortAfterOpenAppendsTerminalLegForJoin(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
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
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-assemble-abort", ContinuityKey: "sess-assemble-abort"},
		Route:    aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected assemble failure from fail-closed stream observer")
	}
	calls, legs := capture.snapshot()
	assertJoinableAbort(t, calls, legs)
	for _, leg := range legs {
		if leg.Outcome != usageevidence.LegOutcomeFailed {
			t.Fatalf("assemble-after-open leg outcome = %s, want Failed", leg.Outcome)
		}
	}
}

func TestOpenInitialRegisterBLegFailureAppendsTerminalLegForJoin(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	ex.ALegLifecycle = coord
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				aLegID := strings.TrimSpace(call.Session.ALegID)
				if aLegID == "" {
					return nil, errors.New("missing a-leg id on open")
				}
				_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-register-abort", ContinuityKey: "sess-register-abort"},
		Route:    aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected RegisterBLeg failure after Open")
	}
	calls, legs := capture.snapshot()
	assertJoinableAbort(t, calls, legs)
}

func TestParallelRegisterBLegFailureAppendsTerminalLegForJoin(t *testing.T) {
	capture := &abortJoinCapture{}
	auth := reservedAuthorityRecorder("res-billing-l2")
	ex, _, backend, aLegID := newAuthorityRuntimeTestExecutorWithStore(t, auth)
	wireAbortBilling(ex, capture)
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg(aLegID)
	backend.openFn = func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		_ = coord.CancelALeg(ctx, aLegID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
		return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	req.reqFacts.aScope = aScope
	req.reqFacts.billingCallID = callID
	req.reqFacts.billingCallState = newBillingCallState(callID)

	raceErr := runRaceInGoroutine(t, 5*time.Second, func() error {
		_, err := ex.tryOpenParallelGroup(context.Background(), req, []routing.AttemptCandidate{authorityCandidate()}, nil, "", false)
		return err
	})
	if raceErr == nil {
		t.Fatal("expected parallel race error from RegisterBLeg failure")
	}

	_, legs := capture.snapshot()
	if len(legs) == 0 {
		t.Fatal("RegisterBLeg failure after Open must append independent terminal leg")
	}
	if legs[0].Outcome != usageevidence.LegOutcomeFailed {
		t.Fatalf("parallel RegisterBLeg failure outcome = %s, want Failed", legs[0].Outcome)
	}
	if legs[0].CallID != callID {
		t.Fatalf("leg CallID = %s, want %s", legs[0].CallID, callID)
	}

	frozen := req.reqFacts.billingCallState.freezeAllocatedBLegs()
	if len(frozen) == 0 {
		t.Fatal("expected allocated B-leg to remain frozen after RegisterBLeg failure")
	}
	record := usageevidence.CallUsageRecord{
		SchemaVersion:      usageevidence.CurrentRecordSchemaVersion,
		CallID:             callID,
		AccountID:          "acct",
		ALegID:             aLegID,
		Outcome:            usageevidence.TurnOutcomeFailed,
		CustomerPricingRef: usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
		ChargePolicyRef:    usageevidence.VersionRef{ID: "policy:test", Version: "1"},
		ExpectedBLegIDs:    frozen,
		StartedAt:          time.Unix(1, 0).UTC(),
		FinishedAt:         time.Unix(2, 0).UTC(),
	}
	sealed, err := record.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := usageevidence.JoinCompleteCall(sealed, legs); err != nil {
		t.Fatalf("parallel RegisterBLeg failure must remain joinable: %v", err)
	}
}

type boomRequestPartHook struct{}

func (boomRequestPartHook) ID() string { return "boom-request-part" }

func (boomRequestPartHook) Order() int { return 0 }

func (boomRequestPartHook) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (boomRequestPartHook) HandleRequestParts(context.Context, *aipapi.Call, sdkhooks.PartMeta) error {
	return errors.New("request part hook boom after NextBLeg")
}

type toolsExcludeRequestPartHook struct{}

func (toolsExcludeRequestPartHook) ID() string { return "tools-exclude-part" }

func (toolsExcludeRequestPartHook) Order() int { return 0 }

func (toolsExcludeRequestPartHook) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailClosed
}

func (toolsExcludeRequestPartHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	call.Tools = append(call.Tools, aipapi.ToolDef{Name: "need-tools", Description: "x"})
	return nil
}

// assertClosureJoinableOrEmpty requires that any emitted call-closure ExpectedBLegIDs
// set is fully backed by durable terminal leg rows (no ghost freeze membership).
func assertClosureJoinableOrEmpty(t *testing.T, calls []usageevidence.CallUsageRecord, legs []usageevidence.CallLegUsageRecord) {
	t.Helper()
	if len(calls) == 0 {
		return
	}
	if len(calls) != 1 {
		t.Fatalf("call-closure appends = %d, want 0 or 1", len(calls))
	}
	seen := map[string]bool{}
	for _, leg := range legs {
		seen[strings.TrimSpace(leg.BLegID)] = true
	}
	for _, id := range calls[0].ExpectedBLegIDs {
		if !seen[id] {
			t.Fatalf("ghost ExpectedBLegID %q has no terminal leg row; legs=%#v expected=%#v", id, legs, calls[0].ExpectedBLegIDs)
		}
	}
	if _, err := usageevidence.JoinCompleteCall(calls[0], legs); err != nil {
		t.Fatalf("emitted call-closure must remain joinable: %v", err)
	}
}

func TestRequestPartHookFailureAfterNextBLegDoesNotFreezeGhostBLeg(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{boomRequestPartHook{}}})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("Open must not run after request-part hook failure")
				return nil, nil
			},
		},
	}
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-hook-abort", ContinuityKey: "sess-hook-abort"},
		Route:    aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected request-part hook failure")
	}
	calls, legs := capture.snapshot()
	assertClosureJoinableOrEmpty(t, calls, legs)
}

func TestPostHookExcludeAfterNextBLegDoesNotFreezeExcludedCandidate(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.MaxAttempts = 4
	ex.Rand = routing.NewSeededRng(3)
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{toolsExcludeRequestPartHook{}}})
	wireAbortBilling(ex, capture)
	var openedA int
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				openedA++
				t.Fatal("tools-incapable candidate must be excluded before Open")
				return nil, nil
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		ID:       "exclude-ghost",
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-exclude-ghost", ContinuityKey: "sess-exclude-ghost"},
		Route:    aipapi.RouteIntent{Selector: "[first]a:m^b:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if openedA != 0 {
		t.Fatalf("excluded candidate opened %d times", openedA)
	}
	calls, legs := capture.snapshot()
	assertClosureJoinableOrEmpty(t, calls, legs)
}

func TestAuthorityAdmitFailureAfterNextBLegDoesNotFreezeGhostBLeg(t *testing.T) {
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &abortJoinCapture{}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	wireAbortBilling(ex, capture)
	auth := &estimateThenFailAuthority{
		estimateResult: usagelimit.AdmissionResult{Allowed: true, Reserved: true, ReservationID: "res-est"},
		realErr:        usagelimit.ErrReservationConflict,
	}
	ex.UsageAuthority = auth
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("Open must not run after authoritative admit failure")
				return nil, nil
			},
		},
	}
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "sess-admit-abort", ContinuityKey: "sess-admit-abort"},
		Route:    aipapi.RouteIntent{Selector: "ok:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected authoritative admit failure")
	}
	calls, legs := capture.snapshot()
	assertClosureJoinableOrEmpty(t, calls, legs)
}
