package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingledger "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/ledger"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// authorityAdmissionCountFunc is an adapter that lets a plain function satisfy the
// tokenaccounting preflight CountCall interface used by authority admission denial tests.
type authorityAdmissionCountFunc func(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error)

func (f authorityAdmissionCountFunc) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return f(ctx, in)
}

// newAuthorityRuntimeTestExecutorWithStore builds an Executor wired to a b2bua
// MemoryStore, a single "backend-1" backend with the supplied open counter, and
// the supplied authority service. Returns the executor, store, backend counter,
// and the allocated A-leg ID.
func newAuthorityRuntimeTestExecutorWithStore(t *testing.T, authority UsageAuthorityService) (*Executor, *b2bua.MemoryStore, *authorityOpenCounter, string) {
	t.Helper()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "authority-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}
	backend := &authorityOpenCounter{}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			EnforcesMaxOutputTokens: true,
			Open:                    backend.open,
		},
	}
	ex.UsageAuthority = authority
	return ex, store, backend, leg.ALegID
}

// authorityOpenRequest builds the standard openNextRequest used by admission
// denial tests that exercise evaluateAndOpenCandidate directly.
// It populates typed requestFacts explicitly; no context fallback is used.
func authorityOpenRequest(t *testing.T, aLegID string, budget *attemptBudget) openNextRequest {
	t.Helper()
	failures := budget.getFailures()
	progress := &recoveryController{
		budget:   budget,
		excluded: map[string]struct{}{},
		failures: failures,
	}
	budget.failures = failures
	return openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID: "trace-1",
				aLegID:  aLegID,
				baseline: aipapi.Call{
					ID:    "request-1",
					Route: aipapi.RouteIntent{Selector: "backend-1:model-1"},
					Invocation: aipapi.Invocation{
						Operation:    aipapi.OperationOpenAIChatCompletions,
						DeliveryMode: aipapi.DeliveryModeStreaming,
					},
					Messages: testMinimalUserMessages(),
				},
				recvViews:        execctx.Views{},
				recvViewsOK:      false,
				billingCallID:    usageevidence.BillingCallID("bc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
				billingCallState: newBillingCallState(usageevidence.BillingCallID("bc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")),
			},
			bus: hooks.New(hooks.Config{}),
		},
		routeFacts: routeFacts{
			sel: &routing.Selector{},
			rng: routing.NewSeededRng(1),
		},
		progress:    progress,
		mode:        openModeInitial,
		interleaved: interleavedstate.State{},
	}
}

// newAuthorityRuntimeTestExecutor is the lightweight variant that drops the b2bua
// store from the return tuple. Used by tests that only need the executor and
// backend counter (admission, settlement, release paths that don't need the
// b2bua B-leg state machine).
func newAuthorityRuntimeTestExecutor(t *testing.T, authority UsageAuthorityService) (*Executor, *authorityOpenCounter, string) {
	t.Helper()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("new memory store: %v", err)
	}
	leg, err := store.CreateALeg(context.Background(), "authority-test")
	if err != nil {
		t.Fatalf("create a-leg: %v", err)
	}
	backend := &authorityOpenCounter{}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"backend-1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			EnforcesMaxOutputTokens: true,
			Open:                    backend.open,
		},
	}
	ex.UsageAuthority = authority
	return ex, backend, leg.ALegID
}

// openAuthorityCandidate runs a single authority-aware evaluateAndOpenCandidate call
// for the standard "backend-1:model-1" candidate.
func openAuthorityCandidate(t *testing.T, ex *Executor, aLegID string) (openedAttempt, error) {
	t.Helper()
	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}
	return ex.evaluateAndOpenCandidate(context.Background(), req, plan)
}

// authorityCandidate returns the standard "backend-1:model-1" attempt candidate
// used by most authority tests.
func authorityCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
		Key:     "backend-1:model-1",
	}
}

// testAuthorityLifecycle builds an authorityLifecycle owner over the supplied
// executor's authority service, the supplied reservation state, and candidate. It is
// the test-side analog of newAuthorityLifecycle used at retryRecvStream construction
// sites, so leak/settlement/release tests can pre-stage reservation state on a stream
// literal without repeating the owner wiring. The candidate only feeds debug log
// attributes, so passing the stream's cand keeps log correlation consistent.
func testAuthorityLifecycle(ex *Executor, state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	return ex.newAttemptAuthorityLifecycle(state, cand)
}

// authorityInputAmount builds the standard InputTokens amount used to reserve
// against the "backend-1:model-1" rule in most authority tests.
func authorityInputAmount(v int64) usagelimit.Amount {
	return usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: v}
}

// testAuthorityAdmissionInput builds a canonical admission input used by
// settlement / release / replacement tests that pre-stage the admit result
// and only exercise downstream authority behavior.
func testAuthorityAdmissionInput(amount int64) usagelimit.AdmissionInput {
	reqID := "request-1"
	legID := "a-leg-1"
	blegID := "bleg-1"
	key := usagelimit.ReservationKey{
		LogicalRequestID: reqID,
		ALegID:           legID,
		BLegID:           blegID,
		AttemptID:        blegID,
		RuleID:           "rule-1",
		Sequence:         1,
	}
	return usagelimit.AdmissionInput{
		Correlation: controlplane.Correlation{
			TraceID:    "trace-1",
			RequestID:  reqID,
			ALegID:     legID,
			BLegID:     blegID,
			AttemptSeq: 1,
			BackendID:  "backend-1",
			Model:      "model-1",
		},
		Scope:          scope.PrincipalScopeView{},
		Dimensions:     usagelimit.Dimensions{},
		Request:        authorityInputAmount(amount),
		Authority:      usagelimit.AuthorityLevelEstimated,
		ReservationKey: key,
		EstimateOnly:   false,
	}
}

// authorityOpenCounter counts backend Open invocations and optionally routes
// through a caller-supplied openFn for stream-content overrides.
type authorityOpenCounter struct {
	openCalls atomic.Int64
	openFn    func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)
}

func (c *authorityOpenCounter) open(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	c.openCalls.Add(1)
	if c.openFn != nil {
		return c.openFn(ctx, call, cand)
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

// recordingAuthorityService is a fully programmable UsageAuthorityService used
// to script admit/settle/release/apply-usage outcomes and to inspect the most
// recent inputs. It implements all the SDK's authority ports.
type recordingAuthorityService struct {
	admitMu           sync.Mutex
	admitInputsV      []usagelimit.AdmissionInput
	settleMu          sync.Mutex
	settleInputsV     []usagelimit.SettleInput
	releaseMu         sync.Mutex
	releaseInputsV    []usagelimit.ReleaseInput
	applyUsageMu      sync.Mutex
	applyUsageInputsV []usagelimit.ApplyUsageCommand
	admitCalls        atomic.Int64
	settleCalls       atomic.Int64
	releaseCalls      atomic.Int64
	applyUsageCalls   atomic.Int64

	admitResult   usagelimit.AdmissionResult
	admitErr      error
	settleErr     error
	releaseErr    error
	applyUsageErr error

	// settleNotApplied scripts a Settle that returns Applied=false without an
	// error. The zero value keeps the default outcome (Applied=true).
	settleNotApplied bool
	// applyUsageAppliedScript scripts the Applied flag of successive
	// ApplyUsage calls by call index; the last entry repeats. Empty keeps the
	// default: applied when the command carried rule IDs.
	applyUsageAppliedScript []bool

	lastAdmitInput      atomic.Value
	lastSettleInput     atomic.Value
	lastReleaseInput    atomic.Value
	lastApplyUsageInput atomic.Value

	status controlplane.AccountingAuthorityStatus
}

func (s *recordingAuthorityService) Admit(_ context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	s.admitCalls.Add(1)
	s.admitMu.Lock()
	s.admitInputsV = append(s.admitInputsV, in)
	s.admitMu.Unlock()
	s.lastAdmitInput.Store(in)
	return s.admitResult, s.admitErr
}

func (s *recordingAuthorityService) Settle(_ context.Context, in usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	s.settleCalls.Add(1)
	s.settleMu.Lock()
	s.settleInputsV = append(s.settleInputsV, in)
	s.settleMu.Unlock()
	s.lastSettleInput.Store(in)
	if s.settleErr != nil {
		return usagelimit.SettleResult{}, s.settleErr
	}
	return usagelimit.SettleResult{Applied: !s.settleNotApplied}, nil
}

func (s *recordingAuthorityService) Release(_ context.Context, in usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	s.releaseCalls.Add(1)
	s.releaseMu.Lock()
	s.releaseInputsV = append(s.releaseInputsV, in)
	s.releaseMu.Unlock()
	s.lastReleaseInput.Store(in)
	if s.releaseErr != nil {
		return usagelimit.ReleaseResult{}, s.releaseErr
	}
	return usagelimit.ReleaseResult{Applied: true}, nil
}

func (s *recordingAuthorityService) ApplyUsage(_ context.Context, cmd usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	s.applyUsageCalls.Add(1)
	s.applyUsageMu.Lock()
	s.applyUsageInputsV = append(s.applyUsageInputsV, cmd)
	call := len(s.applyUsageInputsV)
	script := s.applyUsageAppliedScript
	s.applyUsageMu.Unlock()
	s.lastApplyUsageInput.Store(cmd)
	if s.applyUsageErr != nil {
		return usagelimit.ApplyUsageResult{}, s.applyUsageErr
	}
	applied := len(cmd.RuleIDs) > 0
	if len(script) > 0 {
		index := call - 1
		if index >= len(script) {
			index = len(script) - 1
		}
		applied = script[index]
	}
	return usagelimit.ApplyUsageResult{Applied: applied, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}

func (s *recordingAuthorityService) Status(context.Context) (controlplane.AccountingAuthorityStatus, error) {
	if s.status.State == "" {
		return controlplane.AccountingAuthorityStatus{State: controlplane.AccountingAuthorityReady}, nil
	}
	return s.status, nil
}

func (s *recordingAuthorityService) LimitStatus(context.Context, controlplane.AccountingLimitStatusQuery) (controlplane.Page[controlplane.AccountingLimitStatusRow], error) {
	return controlplane.Page[controlplane.AccountingLimitStatusRow]{}, nil
}

func (s *recordingAuthorityService) Decisions(context.Context, controlplane.AccountingDecisionQuery) (controlplane.Page[controlplane.AccountingDecisionRow], error) {
	return controlplane.Page[controlplane.AccountingDecisionRow]{}, nil
}

func (s *recordingAuthorityService) lastAdmit() usagelimit.AdmissionInput {
	if v := s.lastAdmitInput.Load(); v != nil {
		if in, ok := v.(usagelimit.AdmissionInput); ok {
			return in
		}
	}
	return usagelimit.AdmissionInput{}
}

func (s *recordingAuthorityService) admitInputs() []usagelimit.AdmissionInput {
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	out := make([]usagelimit.AdmissionInput, len(s.admitInputsV))
	copy(out, s.admitInputsV)
	return out
}

func (s *recordingAuthorityService) lastSettle() usagelimit.SettleInput {
	if v := s.lastSettleInput.Load(); v != nil {
		if in, ok := v.(usagelimit.SettleInput); ok {
			return in
		}
	}
	return usagelimit.SettleInput{}
}

func (s *recordingAuthorityService) settleInputs() []usagelimit.SettleInput {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	out := make([]usagelimit.SettleInput, len(s.settleInputsV))
	copy(out, s.settleInputsV)
	return out
}

func (s *recordingAuthorityService) lastRelease() usagelimit.ReleaseInput {
	if v := s.lastReleaseInput.Load(); v != nil {
		if in, ok := v.(usagelimit.ReleaseInput); ok {
			return in
		}
	}
	return usagelimit.ReleaseInput{}
}

func (s *recordingAuthorityService) releaseInputs() []usagelimit.ReleaseInput {
	s.releaseMu.Lock()
	defer s.releaseMu.Unlock()
	out := make([]usagelimit.ReleaseInput, len(s.releaseInputsV))
	copy(out, s.releaseInputsV)
	return out
}

func (s *recordingAuthorityService) lastApplyUsage() usagelimit.ApplyUsageCommand {
	if v := s.lastApplyUsageInput.Load(); v != nil {
		if cmd, ok := v.(usagelimit.ApplyUsageCommand); ok {
			return cmd
		}
	}
	return usagelimit.ApplyUsageCommand{}
}

func (s *recordingAuthorityService) applyUsageInputs() []usagelimit.ApplyUsageCommand {
	s.applyUsageMu.Lock()
	defer s.applyUsageMu.Unlock()
	out := make([]usagelimit.ApplyUsageCommand, len(s.applyUsageInputsV))
	copy(out, s.applyUsageInputsV)
	return out
}

// stubStreamCounter is a deterministic stream-usage counter used by settlement
// tests that need to assert on reconstructed usage.
type stubStreamCounter struct {
	call   accountingapp.CountResult
	output accountingapp.CountResult
}

func (c *stubStreamCounter) CountCall(context.Context, accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	return c.call, nil
}

func (c *stubStreamCounter) CountOutput(context.Context, accountingapp.CountOutputInput) (accountingapp.CountResult, error) {
	return c.output, nil
}

// failingLedger is a ledger stub that always returns the supplied error,
// used by ledger-failure settlement tests.
type failingLedger struct{ err error }

func (l failingLedger) Record(context.Context, accountingledger.Record) error { return l.err }

// estimateThenFailAuthority is a UsageAuthorityService stub that succeeds for the
// estimate-only precheck admit and fails the first real (non-estimate) admit with
// the configured error. It models a strict authority store returning
// ErrReservationConflict when the live reservation window is full, so the precheck
// passes while the authoritative admit fails after the budget slot and B-leg seq
// are already allocated.
type estimateThenFailAuthority struct {
	estimateResult usagelimit.AdmissionResult
	realErr        error
	admitCalls     atomic.Int64
	realFailed     atomic.Bool
}

func (s *estimateThenFailAuthority) Admit(_ context.Context, in usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	s.admitCalls.Add(1)
	if in.EstimateOnly {
		return s.estimateResult, nil
	}
	if !s.realFailed.Swap(true) {
		return usagelimit.AdmissionResult{}, s.realErr
	}
	return s.estimateResult, nil
}

func (s *estimateThenFailAuthority) Settle(_ context.Context, _ usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{Applied: true}, nil
}

func (s *estimateThenFailAuthority) Release(_ context.Context, _ usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	return usagelimit.ReleaseResult{Applied: true}, nil
}

func (s *estimateThenFailAuthority) ApplyUsage(_ context.Context, cmd usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{Applied: len(cmd.RuleIDs) > 0, RuleIDs: append([]string(nil), cmd.RuleIDs...)}, nil
}
