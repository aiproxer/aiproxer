package proxymemory

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/stretchr/testify/require"
)

// Worker test identity. The project key is an opaque 64-hex scope identifier,
// the same shape the capture plane derives and the service validates.
const (
	workerTestTenant    = "tenant-worker"
	workerTestPrincipal = "alice-worker"
	workerTestProject   = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	workerTestLease     = "lease-owner-worker"
	workerTestALeg1     = "aleg-worker-1"
	workerTestALeg2     = "aleg-worker-2"
	workerTestALeg3     = "aleg-worker-3"
	workerTestLabel     = "/w/worker"
	workerTestExtractRM = "route-worker-extract"
	workerTestConsolRM  = "route-worker-consolidate"
)

// Canned auxiliary model replies. Both carry a secret-like value that only the
// parsers may redact, and the phase 1 slug only the parser may normalize.
const (
	workerPhase1Output  = `{"raw_memory":"learned to run tests sk-ABCDEFGHIJKLMNOPQRSTUVWX","rollout_summary":"summary Bearer abcdefghijklmnopqrstuvwxyz","rollout_slug":"Fix Tests"}`
	workerPhase2Output  = `{"memory_md":"# Memory\nlearned token=supersecretvalue123","memory_summary":"short summary"}`
	workerInvalidOutput = "this is not a phase reply at all"
)

// workerOwner is the owner every worker test claim belongs to.
func workerOwner() Owner {
	return Owner{TenantID: workerTestTenant, PrincipalID: workerTestPrincipal, ProjectKey: workerTestProject}
}

// workerTestConfig enables the worker routes for one principal.
func workerTestConfig() Config {
	cfg := DefaultConfig()
	cfg.Principals = []string{workerTestPrincipal}
	cfg.ExtractRoute = workerTestExtractRM
	cfg.ConsolidateRoute = workerTestConsolRM
	return cfg
}

// workerClaimedSession is one idle claimed session of the worker tests.
func workerClaimedSession(aLegID string, base time.Time) ClaimedSession {
	return ClaimedSession{
		Owner:          workerOwner(),
		ProjectLabel:   workerTestLabel,
		ALegID:         aLegID,
		Transcript:     `[{"type":"message","role":"user","text":"hello sk-ABCDEFGHIJKLMNOPQRSTUVWX"}]`,
		Revision:       3,
		LastActivityMS: base.Add(-2 * time.Hour).UnixMilli(),
		LeaseOwner:     workerTestLease,
	}
}

// workerClaimedBank is one dirty claimed bank of the worker tests.
func workerClaimedBank() ClaimedBank {
	return ClaimedBank{
		Owner:             workerOwner(),
		ProjectLabel:      workerTestLabel,
		Version:           2,
		DirtySeq:          7,
		MemoryMD:          "# Current memory",
		MemorySummary:     "current summary",
		ConsolidatedNames: []string{"old-name"},
		LeaseOwner:        workerTestLease,
	}
}

// workerClaimedBankWithoutNames is a claimed bank whose previous publication
// consolidated nothing, so no consolidated reference can be removed.
func workerClaimedBankWithoutNames() ClaimedBank {
	bank := workerClaimedBank()
	bank.ConsolidatedNames = nil
	return bank
}

// workerExtraction is one consolidation-window extraction of the worker tests.
func workerExtraction(base time.Time) Extraction {
	return Extraction{
		Owner:           workerOwner(),
		ALegID:          workerTestALeg1,
		SessionRef:      SessionRef(workerTestALeg1),
		RawMemory:       "raw memory",
		RolloutSummary:  "rollout summary",
		RolloutSlug:     "fix-tests",
		SourceUpdatedMS: base.Add(-2 * time.Hour).UnixMilli(),
		GeneratedAtMS:   base.Add(-time.Hour).UnixMilli(),
	}
}

// workerCollectedWithToolCall is a collection that violated the no-tools
// request.
func workerCollectedWithToolCall() aipapi.Collected {
	var collected aipapi.Collected
	collected.Text.WriteString(workerPhase1Output)
	collected.ToolCallOrder = []string{"call-1"}
	collected.ToolNames = map[string]string{"call-1": "memory_read"}
	return collected
}

// workerCollectedWithTerminalError is a failed auxiliary collection.
func workerCollectedWithTerminalError() aipapi.Collected {
	var collected aipapi.Collected
	collected.Text.WriteString(workerPhase1Output)
	collected.TerminalError = &aipapi.Event{}
	return collected
}

// workerPurgeCall records one store.Purge invocation.
type workerPurgeCall struct {
	nowMS               int64
	sessionsBeforeMS    int64
	extractionsBeforeMS int64
	limit               int
}

// workerCompleteCall records one store.CompleteSession invocation.
type workerCompleteCall struct {
	session    ClaimedSession
	extraction Extraction
}

// workerSessionFailCall records one store.FailSession invocation.
type workerSessionFailCall struct {
	session   ClaimedSession
	retryAtMS int64
}

// workerBankFailCall records one store.FailBank invocation.
type workerBankFailCall struct {
	bank      ClaimedBank
	retryAtMS int64
}

// workerPublishCall records one store.PublishBank invocation.
type workerPublishCall struct {
	bank        ClaimedBank
	publication Publication
}

// workerInputCall records one store.ConsolidationInput invocation.
type workerInputCall struct {
	bank    ClaimedBank
	sinceMS int64
	limit   int
}

// workerStoreStub is the in-memory Store of the worker tests: it records every
// call the worker makes, answers from canned data, and reports injected
// failures. The embedded Store stays nil, so any call outside the worker's own
// surface panics loudly instead of silently passing. panicMethod makes one
// method panic like a broken dependency would; note releases the recorder
// mutex before panicking so the test can still inspect the recorder.
type workerStoreStub struct {
	Store

	mu      sync.Mutex
	order   []string
	callsCh chan struct{}

	purges        []workerPurgeCall
	sessionClaims []ClaimQuery
	bankClaims    []ClaimQuery
	completes     []workerCompleteCall
	sessionFails  []workerSessionFailCall
	bankFails     []workerBankFailCall
	publishes     []workerPublishCall
	inputs        []workerInputCall

	// Canned claim data: ClaimSessions and ClaimBanks pop one entry per call.
	sessions    []ClaimedSession
	banks       []ClaimedBank
	extractions []Extraction
	windowNames []string
	notes       []Note

	// Injected outcomes.
	purgeDeleted    int
	purgeErr        error
	claimSessionErr error
	claimBankErr    error
	completeErr     error
	failSessionErr  error
	inputErr        error
	publishErr      error
	failBankErr     error
	failureOutcome  FailureOutcome
	failureErr      error
	failures        []workerFailureCall
	panicMethod     string
}

// workerFailureCall records one RecordPhaseFailure invocation and the bounded
// settlement context the worker supplied.
type workerFailureCall struct {
	failure  FailureEvidence
	deadline time.Time
	ctxErr   error
}

func newWorkerStoreStub() *workerStoreStub {
	return &workerStoreStub{callsCh: make(chan struct{}, 64)}
}

func (s *workerStoreStub) ReadSettings(context.Context, Principal) (StoredOwnerSettings, error) {
	return StoredOwnerSettings{}, nil
}

// note records one call, wakes a waiter and injects the armed panic.
func (s *workerStoreStub) note(method string) {
	s.mu.Lock()
	s.order = append(s.order, method)
	panicMethod := s.panicMethod
	s.mu.Unlock()
	select {
	case s.callsCh <- struct{}{}:
	default:
	}
	if panicMethod == method {
		panic("worker fake store: injected panic in " + method)
	}
}

func (s *workerStoreStub) Purge(_ context.Context, nowMS, sessionsBeforeMS, extractionsBeforeMS int64, limit int) (int, error) {
	s.note("purge")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purges = append(s.purges, workerPurgeCall{
		nowMS:               nowMS,
		sessionsBeforeMS:    sessionsBeforeMS,
		extractionsBeforeMS: extractionsBeforeMS,
		limit:               limit,
	})
	return s.purgeDeleted, s.purgeErr
}

func (s *workerStoreStub) ClaimSessions(_ context.Context, q ClaimQuery) ([]ClaimedSession, error) {
	s.note("claim-sessions")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionClaims = append(s.sessionClaims, q)
	if s.claimSessionErr != nil {
		return nil, s.claimSessionErr
	}
	if len(s.sessions) == 0 {
		return nil, nil
	}
	claimed := s.sessions[0]
	s.sessions = s.sessions[1:]
	return []ClaimedSession{claimed}, nil
}

func (s *workerStoreStub) CompleteSession(_ context.Context, session ClaimedSession, e Extraction) error {
	s.note("complete-session:" + session.ALegID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completes = append(s.completes, workerCompleteCall{session: session, extraction: e})
	return s.completeErr
}

func (s *workerStoreStub) RecordPhaseFailure(ctx context.Context, failure FailureEvidence) (FailureOutcome, error) {
	s.note("record-phase-failure")
	deadline, _ := ctx.Deadline()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, workerFailureCall{failure: failure, deadline: deadline, ctxErr: ctx.Err()})
	if s.failureErr != nil {
		return "", s.failureErr
	}
	return s.failureOutcome, nil
}

func (s *workerStoreStub) failureCalls() []workerFailureCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.failures)
}

func (s *workerStoreStub) FailSession(_ context.Context, session ClaimedSession, retryAtMS int64) error {
	s.note("fail-session:" + session.ALegID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionFails = append(s.sessionFails, workerSessionFailCall{session: session, retryAtMS: retryAtMS})
	return s.failSessionErr
}

func (s *workerStoreStub) ClaimBanks(_ context.Context, q ClaimQuery) ([]ClaimedBank, error) {
	s.note("claim-banks")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bankClaims = append(s.bankClaims, q)
	if s.claimBankErr != nil {
		return nil, s.claimBankErr
	}
	if len(s.banks) == 0 {
		return nil, nil
	}
	claimed := s.banks[0]
	s.banks = s.banks[1:]
	return []ClaimedBank{claimed}, nil
}

func (s *workerStoreStub) ConsolidationInput(_ context.Context, bank ClaimedBank, sinceMS int64, limit int) (ConsolidationBatch, error) {
	s.note("consolidation-input")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inputs = append(s.inputs, workerInputCall{bank: bank, sinceMS: sinceMS, limit: limit})
	if s.inputErr != nil {
		return ConsolidationBatch{}, s.inputErr
	}
	// Model the store's obligation: an extraction the bank already recorded as
	// consolidated is no longer outstanding. The window keeps every live name.
	consolidated := make(map[string]struct{}, len(bank.ConsolidatedNames))
	for _, name := range bank.ConsolidatedNames {
		consolidated[name] = struct{}{}
	}
	window := slices.Clone(s.windowNames)
	if window == nil {
		for _, e := range s.extractions {
			window = append(window, SummaryName(e))
		}
	}
	var outstanding []Extraction
	for _, e := range s.extractions {
		if _, done := consolidated[SummaryName(e)]; done {
			continue
		}
		outstanding = append(outstanding, e)
	}
	return ConsolidationBatch{
		Extractions: slices.Clone(outstanding),
		WindowNames: window,
		Notes:       slices.Clone(s.notes),
	}, nil
}

func (s *workerStoreStub) PublishBank(_ context.Context, bank ClaimedBank, p Publication) error {
	s.note("publish-bank")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishes = append(s.publishes, workerPublishCall{bank: bank, publication: p})
	return s.publishErr
}

func (s *workerStoreStub) FailBank(_ context.Context, bank ClaimedBank, retryAtMS int64) error {
	s.note("fail-bank")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bankFails = append(s.bankFails, workerBankFailCall{bank: bank, retryAtMS: retryAtMS})
	return s.failBankErr
}

func (s *workerStoreStub) callOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

func (s *workerStoreStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.order)
}

func (s *workerStoreStub) purgeCalls() []workerPurgeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.purges)
}

func (s *workerStoreStub) sessionClaimCalls() []ClaimQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sessionClaims)
}

func (s *workerStoreStub) bankClaimCalls() []ClaimQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bankClaims)
}

func (s *workerStoreStub) completeCalls() []workerCompleteCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.completes)
}

func (s *workerStoreStub) sessionFailCalls() []workerSessionFailCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sessionFails)
}

func (s *workerStoreStub) bankFailCalls() []workerBankFailCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.bankFails)
}

func (s *workerStoreStub) publishCalls() []workerPublishCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.publishes)
}

func (s *workerStoreStub) inputCalls() []workerInputCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inputs)
}

func (s *workerStoreStub) pendingSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// waitFor waits until one recorded call of method exists. It is event-driven:
// every recorded call wakes it, and only an actually missing call times out.
func (s *workerStoreStub) waitFor(t *testing.T, method string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		found := slices.Contains(s.order, method)
		s.mu.Unlock()
		if found {
			return
		}
		select {
		case <-s.callsCh:
		case <-timeout:
			t.Fatalf("store saw no %q call", method)
		}
	}
}

// workerSubmitCall records one auxiliary submission with its scope-carrying
// context.
type workerSubmitCall struct {
	ctx  context.Context
	req  auxiliary.Request
	opts auxiliary.SubmitOptions
}

// workerAuxStub is the auxiliary.BackgroundClient of the worker tests. It
// records submissions (including the context, so the test can read the owner
// scope the worker attached) and answers with canned text, a collected hook or
// an injected error.
type workerAuxStub struct {
	mu        sync.Mutex
	calls     []workerSubmitCall
	forgets   []auxiliary.JobID
	nextID    int
	text      string
	submitErr error
	awaitErr  error
	collect   func(id auxiliary.JobID) (aipapi.Collected, error)
	receipt   CollectionReceipt
}

func (a *workerAuxStub) SubmitCollect(ctx context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.submitErr != nil {
		return "", a.submitErr
	}
	a.nextID++
	a.calls = append(a.calls, workerSubmitCall{ctx: ctx, req: req, opts: opts})
	return auxiliary.JobID("worker-job-" + strconv.Itoa(a.nextID)), nil
}

func (a *workerAuxStub) Await(_ context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	a.mu.Lock()
	collect := a.collect
	text := a.text
	awaitErr := a.awaitErr
	a.mu.Unlock()
	if collect != nil {
		return collect(id)
	}
	if awaitErr != nil {
		return aipapi.Collected{}, awaitErr
	}
	var collected aipapi.Collected
	collected.Text.WriteString(text)
	return collected, nil
}

func (a *workerAuxStub) AwaitCollection(_ context.Context, id auxiliary.JobID) (aipapi.Collected, CollectionReceipt, error) {
	collected, err := a.Await(context.Background(), id)
	a.mu.Lock()
	receipt := a.receipt
	a.mu.Unlock()
	return collected, receipt, err
}

func (a *workerAuxStub) Forget(id auxiliary.JobID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forgets = append(a.forgets, id)
}

func (a *workerAuxStub) submitCalls() []workerSubmitCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

func (a *workerAuxStub) forgetCalls() []auxiliary.JobID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.forgets)
}

// workerFakeTicker is the deterministic tick source of the worker tests: only
// the test can emit a tick, so no worker test ever sleeps.
type workerFakeTicker struct {
	ch chan time.Time
}

func (f *workerFakeTicker) ticks() <-chan time.Time { return f.ch }

func (f *workerFakeTicker) stop() {}

// fire emits one tick and fails when the worker loop never reads it.
func (f *workerFakeTicker) fire(t *testing.T) {
	t.Helper()
	select {
	case f.ch <- time.Time{}:
	case <-time.After(2 * time.Second):
		t.Fatal("worker loop did not receive a tick")
	}
}

// workerTestHarness wires one worker over recording stubs, the shared fake
// clock and a capturing log handler. The harness arms a deterministic ticker
// factory, so tests that call Tick directly are not affected by it. The lease
// owner is preset for direct-Tick tests; Start replaces it the way production
// does.
type workerTestHarness struct {
	worker  *Worker
	store   *workerStoreStub
	aux     *workerAuxStub
	clock   *captureFakeClock
	metrics *Metrics
	logs    *captureLogCapture
	ticker  *workerFakeTicker
	base    time.Time

	// tickerDurations records the factory inputs; tests read it only after
	// Stop joined the loop, which makes the goroutine's write visible.
	tickerDurations []time.Duration
}

func newWorkerTestHarness(t *testing.T, cfg Config) *workerTestHarness {
	t.Helper()
	h := &workerTestHarness{
		store:   newWorkerStoreStub(),
		aux:     &workerAuxStub{text: workerPhase1Output},
		clock:   newCaptureFakeClock(),
		metrics: NewPrometheusCollector(),
		logs:    &captureLogCapture{},
		ticker:  &workerFakeTicker{ch: make(chan time.Time, 8)},
	}
	h.base = h.clock.Now()
	h.worker = NewWorker(cfg, h.store, h.aux, h.metrics, h.clock.Now, slog.New(h.logs))
	h.worker.leaseOwner = workerTestLease
	h.worker.newTicker = func(d time.Duration) workerTicker {
		h.tickerDurations = append(h.tickerDurations, d)
		return h.ticker
	}
	t.Cleanup(func() { _ = h.worker.Stop(context.Background()) })
	return h
}

// TestWorkerPhase1SuccessStoresRedactedExtraction pins design §10's phase 1
// success path: the parsed phase 1 reply reaches CompleteSession with the
// claim's ownership and activity, the run time is the worker clock, the
// submission carries the owner's internal principal scope, and the request
// routes through the configured extraction route.
func TestWorkerPhase1SuccessStoresRedactedExtraction(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	session := workerClaimedSession(workerTestALeg1, h.base)
	h.store.sessions = []ClaimedSession{session}

	h.worker.Tick(context.Background())

	submits := h.aux.submitCalls()
	if len(submits) != 1 {
		t.Fatalf("auxiliary submissions = %d, want 1", len(submits))
	}
	submit := submits[0]
	if got, want := submit.req.Call.Route.Selector, workerTestExtractRM; got != want {
		t.Fatalf("submit route = %q, want %q", got, want)
	}
	if got, want := submit.req.Role, phase1Role; got != want {
		t.Fatalf("submit role = %q, want %q", got, want)
	}
	if got, want := submit.opts.CoalesceKey, "proxy-memory/p1/"+workerTestLease+"/"+workerTestALeg1+"/3"; got != want {
		t.Fatalf("coalesce key = %q, want %q", got, want)
	}
	if got, want := submit.opts.Timeout, h.worker.cfg.AuxTimeout; got != want {
		t.Fatalf("submit timeout = %v, want %v", got, want)
	}
	if got, want := submit.opts.MaxOutputBytes, h.worker.cfg.MaxOutputBytes; got != want {
		t.Fatalf("submit max output bytes = %d, want %d", got, want)
	}
	sc, ok := scope.ScopeFromContext(submit.ctx)
	if !ok {
		t.Fatal("submit context carries no principal scope")
	}
	if sc.SubjectKind != scope.SubjectService {
		t.Fatalf("submit scope subject kind = %q, want %q", sc.SubjectKind, scope.SubjectService)
	}
	if !sc.PrincipalID.Equal(scope.Known(workerTestPrincipal)) {
		t.Fatalf("submit scope principal = %#v, want %#v", sc.PrincipalID, scope.Known(workerTestPrincipal))
	}
	if !sc.TenantID.Equal(scope.Known(workerTestTenant)) {
		t.Fatalf("submit scope tenant = %#v, want %#v", sc.TenantID, scope.Known(workerTestTenant))
	}
	if sc.Origin != scope.OriginInternal {
		t.Fatalf("submit scope origin = %q, want %q", sc.Origin, scope.OriginInternal)
	}

	completes := h.store.completeCalls()
	if len(completes) != 1 {
		t.Fatalf("CompleteSession calls = %d, want 1", len(completes))
	}
	wantExtraction := Extraction{
		Owner:           workerOwner(),
		ALegID:          workerTestALeg1,
		SessionRef:      SessionRef(workerTestALeg1),
		RawMemory:       "learned to run tests [REDACTED_SECRET]",
		RolloutSummary:  "summary Bearer [REDACTED_SECRET]",
		RolloutSlug:     "fix-tests",
		SourceUpdatedMS: session.LastActivityMS,
		GeneratedAtMS:   h.base.UnixMilli(),
	}
	if got := completes[0].extraction; !reflect.DeepEqual(got, wantExtraction) {
		t.Fatalf("extraction mismatch\n got: %#v\nwant: %#v", got, wantExtraction)
	}
	if !reflect.DeepEqual(completes[0].session, session) {
		t.Fatalf("completed session mismatch\n got: %#v\nwant: %#v", completes[0].session, session)
	}
	if got := h.store.sessionFailCalls(); len(got) != 0 {
		t.Fatalf("FailSession calls = %d, want 0", len(got))
	}
	if forgets := h.aux.forgetCalls(); len(forgets) != 1 || forgets[0] != "worker-job-1" {
		t.Fatalf("Forget calls = %#v, want [worker-job-1]", forgets)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Succeeded); got != 1 {
		t.Fatalf("phase1_succeeded = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 0 {
		t.Fatalf("phase1_failed = %v, want 0", got)
	}
}

// TestWorkerPhase1InvalidJSONFailsAndRetries pins design §10's failure path:
// an unparsable reply schedules the next attempt at now+RetryBackoff, stores
// nothing, and logs without content.
func TestWorkerPhase1InvalidJSONFailsAndRetries(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	session := workerClaimedSession(workerTestALeg1, h.base)
	h.store.sessions = []ClaimedSession{session}
	h.aux.text = workerInvalidOutput

	h.worker.Tick(context.Background())

	fails := h.store.sessionFailCalls()
	if len(fails) != 1 {
		t.Fatalf("FailSession calls = %d, want 1", len(fails))
	}
	if got, want := fails[0].retryAtMS, h.base.Add(cfg.RetryBackoff).UnixMilli(); got != want {
		t.Fatalf("retryAtMS = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(fails[0].session, session) {
		t.Fatalf("failed session mismatch\n got: %#v\nwant: %#v", fails[0].session, session)
	}
	if got := h.store.completeCalls(); len(got) != 0 {
		t.Fatalf("CompleteSession calls = %d, want 0", len(got))
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Succeeded); got != 0 {
		t.Fatalf("phase1_succeeded = %v, want 0", got)
	}
	records := h.logs.snapshot()
	if len(records) == 0 {
		t.Fatal("a failed extraction must be logged")
	}
	for _, record := range records {
		text := captureRecordText(record)
		for _, forbidden := range []string{workerInvalidOutput, workerTestProject, workerTestLabel, workerTestALeg1, workerTestPrincipal} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("log record leaks %q: %q", forbidden, text)
			}
		}
	}
}

// TestWorkerAuxFailuresArePhaseFailures pins design §10 step 2.4: an auxiliary
// submission error, an await error, a collection with tool calls and a
// collection with a terminal error are all extraction failures.
func TestWorkerAuxFailuresArePhaseFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*workerAuxStub)
	}{
		{"submit_error", func(a *workerAuxStub) { a.submitErr = errors.New("submit unavailable") }},
		{"await_error", func(a *workerAuxStub) { a.awaitErr = errors.New("await unavailable") }},
		{"tool_calls", func(a *workerAuxStub) {
			a.collect = func(auxiliary.JobID) (aipapi.Collected, error) { return workerCollectedWithToolCall(), nil }
		}},
		{"terminal_error", func(a *workerAuxStub) {
			a.collect = func(auxiliary.JobID) (aipapi.Collected, error) { return workerCollectedWithTerminalError(), nil }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkerTestHarness(t, workerTestConfig())
			h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
			tc.setup(h.aux)

			h.worker.Tick(context.Background())

			if got := h.store.completeCalls(); len(got) != 0 {
				t.Fatalf("CompleteSession calls = %d, want 0", len(got))
			}
			if got := h.store.sessionFailCalls(); len(got) != 1 {
				t.Fatalf("FailSession calls = %d, want 1", len(got))
			}
			if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
				t.Fatalf("phase1_failed = %v, want 1", got)
			}
			if got := captureCounter(t, h.metrics, EventPhase1Succeeded); got != 0 {
				t.Fatalf("phase1_succeeded = %v, want 0", got)
			}
		})
	}
}

// TestWorkerCompletionFailureSchedulesRetry pins design §10 step 2.6 and
// Requirement 4.5 on a failed completion: a non-lease CompleteSession failure
// after a valid model result clears the claim through FailSession with the
// configured RetryBackoff instead of waiting out the lease, and the attempt is
// counted phase1_failed exactly once.
func TestWorkerCompletionFailureSchedulesRetry(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	session := workerClaimedSession(workerTestALeg1, h.base)
	h.store.sessions = []ClaimedSession{session}
	h.store.completeErr = errors.New("transient commit failure")

	h.worker.Tick(context.Background())

	fails := h.store.sessionFailCalls()
	if len(fails) != 1 {
		t.Fatalf("FailSession calls = %d, want 1 after a failed completion", len(fails))
	}
	if got, want := fails[0].retryAtMS, h.base.Add(cfg.RetryBackoff).UnixMilli(); got != want {
		t.Fatalf("retryAtMS = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(fails[0].session, session) {
		t.Fatalf("failed session mismatch\n got: %#v\nwant: %#v", fails[0].session, session)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Succeeded); got != 0 {
		t.Fatalf("phase1_succeeded = %v, want 0", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1LeaseLost); got != 0 {
		t.Fatalf("phase1_lease_lost = %v, want 0", got)
	}
}

// TestWorkerCompletionFailureCountsOnceWhenRecordingFails pins the
// exactly-once failure contract of the completion compensation when the
// FailSession write itself fails: the attempt is still counted phase1_failed
// exactly once (no recursion, no double count), and a lease lost while
// recording the failure keeps the established phase1_lease_lost classification
// instead of a failure count.
func TestWorkerCompletionFailureCountsOnceWhenRecordingFails(t *testing.T) {
	t.Run("recording_error", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
		h.store.completeErr = errors.New("transient commit failure")
		h.store.failSessionErr = errors.New("failure write unavailable")

		h.worker.Tick(context.Background())

		if got := len(h.store.sessionFailCalls()); got != 1 {
			t.Fatalf("FailSession calls = %d, want exactly 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
			t.Fatalf("phase1_failed = %v, want exactly 1 when the failure write also fails", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase1LeaseLost); got != 0 {
			t.Fatalf("phase1_lease_lost = %v, want 0", got)
		}
	})
	t.Run("recording_lease_lost", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
		h.store.completeErr = errors.New("transient commit failure")
		h.store.failSessionErr = ErrLeaseLost

		h.worker.Tick(context.Background())

		if got := captureCounter(t, h.metrics, EventPhase1LeaseLost); got != 1 {
			t.Fatalf("phase1_lease_lost = %v, want 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 0 {
			t.Fatalf("phase1_failed = %v, want 0 when recording the failure lost the lease", got)
		}
	})
}

// TestWorkerPhase2PublishesParsedDocumentsAndConsumedInputs pins design §10's
// phase 2 success path: the parsed documents are published with exactly the
// summary names and note IDs the request builder included, under the claimed
// bank's lease, and the consolidation input window matches the §10 cutoffs.
func TestWorkerPhase2PublishesParsedDocumentsAndConsumedInputs(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = 1
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	bank := workerClaimedBank()
	extraction := workerExtraction(h.base)
	notes := []Note{
		{NoteID: "note-1", Text: "remember alpha", CreatedAtMS: h.base.Add(-2 * time.Hour).UnixMilli()},
		{NoteID: "note-2", Text: "remember beta", CreatedAtMS: h.base.Add(-time.Hour).UnixMilli()},
	}
	h.store.banks = []ClaimedBank{bank}
	h.store.extractions = []Extraction{extraction}
	h.store.notes = notes

	h.worker.Tick(context.Background())

	claims := h.store.bankClaimCalls()
	if len(claims) != 1 {
		t.Fatalf("ClaimBanks calls = %d, want 1", len(claims))
	}
	if got, want := claims[0].Limit, 1; got != want {
		t.Fatalf("claim limit = %d, want %d", got, want)
	}
	if claims[0].LeaseOwner != workerTestLease {
		t.Fatalf("claim lease owner = %q, want %q", claims[0].LeaseOwner, workerTestLease)
	}
	if got, want := claims[0].NowMS, h.base.UnixMilli(); got != want {
		t.Fatalf("claim NowMS = %d, want %d", got, want)
	}
	if got, want := claims[0].LeaseUntilMS, h.base.Add(cfg.Lease).UnixMilli(); got != want {
		t.Fatalf("claim LeaseUntilMS = %d, want %d", got, want)
	}

	inputs := h.store.inputCalls()
	if len(inputs) != 1 {
		t.Fatalf("ConsolidationInput calls = %d, want 1", len(inputs))
	}
	if got, want := inputs[0].sinceMS, h.base.Add(-cfg.MaxUnusedAge).UnixMilli(); got != want {
		t.Fatalf("ConsolidationInput sinceMS = %d, want %d", got, want)
	}
	if got, want := inputs[0].limit, cfg.MaxRawMemories; got != want {
		t.Fatalf("ConsolidationInput limit = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(inputs[0].bank, bank) {
		t.Fatalf("ConsolidationInput bank mismatch\n got: %#v\nwant: %#v", inputs[0].bank, bank)
	}

	submits := h.aux.submitCalls()
	if len(submits) != 1 {
		t.Fatalf("auxiliary submissions = %d, want 1", len(submits))
	}
	if got, want := submits[0].req.Call.Route.Selector, workerTestConsolRM; got != want {
		t.Fatalf("submit route = %q, want %q", got, want)
	}
	if got, want := submits[0].opts.CoalesceKey, "proxy-memory/p2/"+workerTestLease+"/"+workerTestProject+"/7"; got != want {
		t.Fatalf("coalesce key = %q, want %q", got, want)
	}

	publishes := h.store.publishCalls()
	if len(publishes) != 1 {
		t.Fatalf("PublishBank calls = %d, want 1", len(publishes))
	}
	wantPublication := Publication{
		MemoryMD:          "# Memory\nlearned token=[REDACTED_SECRET]",
		MemorySummary:     "short summary",
		ConsolidatedNames: []string{SummaryName(extraction)},
		ConsumedNoteIDs:   []string{"note-1", "note-2"},
		Acknowledged:      []ExtractionRef{{ALegID: extraction.ALegID, GeneratedAtMS: extraction.GeneratedAtMS}},
		WindowSinceMS:     h.base.Add(-cfg.MaxUnusedAge).UnixMilli(),
		PublishedAtMS:     h.base.UnixMilli(),
	}
	if got := publishes[0].publication; !reflect.DeepEqual(got, wantPublication) {
		t.Fatalf("publication mismatch\n got: %#v\nwant: %#v", got, wantPublication)
	}
	if !reflect.DeepEqual(publishes[0].bank, bank) {
		t.Fatalf("published bank mismatch\n got: %#v\nwant: %#v", publishes[0].bank, bank)
	}
	if got := h.store.bankFailCalls(); len(got) != 0 {
		t.Fatalf("FailBank calls = %d, want 0", len(got))
	}
	if got := captureCounter(t, h.metrics, EventPhase2Published); got != 1 {
		t.Fatalf("phase2_published = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase2Truncated); got != 0 {
		t.Fatalf("phase2_truncated = %v, want 0", got)
	}
}

// workerOversizedExtraction is one consolidation-window extraction whose raw
// memory does not fit the phase 2 input budget.
func workerOversizedExtraction(base time.Time) Extraction {
	extraction := workerExtraction(base)
	extraction.RawMemory = strings.Repeat("r", 512*1024)
	return extraction
}

// workerExtractionAt is one consolidation-window extraction of a distinct
// session, generated age before the harness clock. Distinct generation times
// give distinct summary names, which is what orders the consolidation window.
func workerExtractionAt(base time.Time, aLegID string, age time.Duration) Extraction {
	extraction := workerExtraction(base)
	extraction.ALegID = aLegID
	extraction.SessionRef = SessionRef(aLegID)
	extraction.GeneratedAtMS = base.Add(-age).UnixMilli()
	return extraction
}

// TestWorkerUnprocessableNoteNeverSubmitsAuxiliary pins review F7: a note the
// input budget cannot fit must not buy another inference when the attempt
// would consolidate nothing else. The notes and the prior publication survive,
// and the backoff and the failure classification expose the budget problem.
func TestWorkerUnprocessableNoteNeverSubmitsAuxiliary(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2InputTokens, cfg.Phase2Batch = 2_000, 1
	h := newWorkerTestHarness(t, cfg)
	h.store.notes = []Note{{NoteID: "oversized-for-input", Text: strings.Repeat("n", 8_192)}}
	for range 2 {
		// No previous publication and no extraction: nothing but the note,
		// which cannot fit, so the attempt has no progress to make.
		h.store.banks = []ClaimedBank{workerClaimedBankWithoutNames()}
		h.worker.Tick(context.Background())
	}
	require.Len(t, h.aux.submitCalls(), 0, "an unprocessable note must not buy an inference")
	require.Len(t, h.store.publishCalls(), 0, "the notes and the published memory must survive")
	failures := h.store.bankFailCalls()
	require.Len(t, failures, 2)
	require.Equal(t, h.base.Add(cfg.RetryBackoff).UnixMilli(), failures[0].retryAtMS, "both attempts must be deferred")
	require.Equal(t, float64(2), captureCounter(t, h.metrics, EventPhase2Failed), "the refusal must stay observable")
	var logs strings.Builder
	for _, record := range h.logs.snapshot() {
		logs.WriteString(captureRecordText(record))
	}
	require.Equal(t, 2, strings.Count(logs.String(), "note_input_budget"), "the refusal must be classified as a budget failure")
}

// TestWorkerExcludedExtractionIsNotAcknowledgedAsConsolidated pins the review
// finding on the symmetric half of the zero-progress guard: a bank whose only
// new extraction does not fit the input budget must not be published as
// consolidated. The publication would advance the bank past the extraction, so
// the extraction would never be consolidated again; the attempt is refused
// instead, the extraction stays pending work, and no inference is bought.
func TestWorkerExcludedExtractionIsNotAcknowledgedAsConsolidated(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2InputTokens, cfg.Phase2Batch = 100_000, 1
	h := newWorkerTestHarness(t, cfg)
	h.store.extractions = []Extraction{workerOversizedExtraction(h.base)}
	// No previously consolidated name is missing: this is truly zero progress.
	h.store.banks = []ClaimedBank{workerClaimedBankWithoutNames()}

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 0, "an excluded extraction must not buy an inference")
	require.Len(t, h.store.publishCalls(), 0, "the excluded extraction must not be acknowledged as consolidated")
	failures := h.store.bankFailCalls()
	require.Len(t, failures, 1, "the refused attempt must schedule a retry")
	require.Equal(t, h.base.Add(cfg.RetryBackoff).UnixMilli(), failures[0].retryAtMS, "the attempt must be deferred")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Failed), "the refusal must stay observable")
	var logs strings.Builder
	for _, record := range h.logs.snapshot() {
		logs.WriteString(captureRecordText(record))
	}
	require.Equal(t, 1, strings.Count(logs.String(), "extraction_input_budget"), "the refusal must be classified as a budget failure")
}

// TestWorkerPhase2ExtractionProgressSurvivesUnprocessableNote pins the review
// finding on progress: a pending note the input budget cannot fit must not
// block the consolidation of new extraction content. The extraction is
// consolidated and published, the excluded note stays pending because it is
// not consumed, and the attempt advances the bank exactly once.
func TestWorkerPhase2ExtractionProgressSurvivesUnprocessableNote(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2InputTokens, cfg.Phase2Batch = 2_000, 1
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	extraction := workerExtraction(h.base)
	h.store.notes = []Note{{NoteID: "oversized-for-input", Text: strings.Repeat("n", 8_192)}}
	h.store.extractions = []Extraction{extraction}
	h.store.banks = []ClaimedBank{workerClaimedBank()}

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 1, "the fitting extraction must buy one inference")
	publishes := h.store.publishCalls()
	require.Len(t, publishes, 1, "the extraction progress must be published")
	require.Equal(t, []string{SummaryName(extraction)}, publishes[0].publication.ConsolidatedNames)
	require.Empty(t, publishes[0].publication.ConsumedNoteIDs, "the excluded note must stay pending")
	require.Len(t, h.store.bankFailCalls(), 0, "the attempt must not be a failure")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Published), "the publication must stay observable")
}

// TestWorkerPhase2RemovalOnlyPublicationStaysLegitimate pins the review
// requirement that a genuine removal-only publication stays legitimate: the
// window holds only an already consolidated extraction, and a summary the bank
// consolidated before has disappeared from the window, so the published memory
// can remove the obsolete content, not merely its reference metadata.
func TestWorkerPhase2RemovalOnlyPublicationStaysLegitimate(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = 1
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	extraction := workerExtraction(h.base)
	bank := workerClaimedBank()
	// "vanished-summary" was consolidated by an earlier publication and is no
	// longer in the consolidation window: a legitimate removal.
	bank.ConsolidatedNames = []string{SummaryName(extraction), "vanished-summary"}
	h.store.extractions = []Extraction{extraction}
	h.store.banks = []ClaimedBank{bank}

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 1, "removing obsolete content needs one consolidation")
	publishes := h.store.publishCalls()
	require.Len(t, publishes, 1, "the removal-only publication must be published")
	require.Equal(t, []string{SummaryName(extraction)}, publishes[0].publication.ConsolidatedNames)
	require.Empty(t, publishes[0].publication.Acknowledged, "no extraction content was carried")
	require.Len(t, h.store.bankFailCalls(), 0, "the attempt must not be a failure")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Published))

	// The removal is self-limiting: the publication consumed the vanished
	// name, so a settled bank has nothing left to publish. The store contract
	// proves the settle prevents the next claim; this harness would re-claim a
	// settled bank by construction, so only the published identity is checked.
	require.NotContains(t, publishes[0].publication.ConsolidatedNames, "vanished-summary")
}

// TestWorkerPartialExtractionExclusionPublishesCarriedPrefix verifies that a
// whole oldest extraction makes durable progress even when the input budget
// excludes a newer extraction. Only the exact carried revision is acknowledged;
// the omitted extraction is neither in the prompt nor the publication.
func TestWorkerPartialExtractionExclusionPublishesCarriedPrefix(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2InputTokens, cfg.Phase2Batch = 100_000, 1
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	carried := workerExtractionAt(h.base, workerTestALeg2, 2*time.Hour)
	dropped := workerExtractionAt(h.base, workerTestALeg3, time.Hour)
	dropped.RawMemory = strings.Repeat("r", 512*1024)
	h.store.extractions = []Extraction{carried, dropped}
	bank := workerClaimedBankWithoutNames()
	h.store.banks = []ClaimedBank{bank}

	// The budget must fit exactly the older whole pair, not the newer one.
	_, included, err := BuildPhase2Request(cfg, Phase2Input{
		CurrentSummary:  bank.MemorySummary,
		CurrentMemoryMD: bank.MemoryMD,
		ProjectLabel:    bank.ProjectLabel,
		Extractions:     []Extraction{carried, dropped},
		WindowNames:     []string{SummaryName(carried), SummaryName(dropped)},
		PreviousNames:   bank.ConsolidatedNames,
	})
	require.NoError(t, err)
	require.True(t, included.Truncated)
	require.Equal(t, []string{SummaryName(carried)}, included.SummaryNames)

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 1, "the carried oldest prefix must reach inference")
	publishes := h.store.publishCalls()
	require.Len(t, publishes, 1, "a useful partial consolidation must publish")
	require.Equal(t, []ExtractionRef{{
		ALegID: carried.ALegID, Revision: carried.Revision, GeneratedAtMS: carried.GeneratedAtMS,
	}}, publishes[0].publication.Acknowledged, "only the carried revision may be acknowledged")
	require.Equal(t, []string{SummaryName(carried)}, publishes[0].publication.ConsolidatedNames)
	require.NotContains(t, publishes[0].publication.ConsolidatedNames, SummaryName(dropped))
	require.Empty(t, publishes[0].publication.ConsumedNoteIDs)
	require.Empty(t, h.store.bankFailCalls(), "a partial publication is progress, not a failure")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Published))
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Truncated))
}

// TestPhase2ProgressFailureWithExcludedExtraction covers the other useful
// work that can justify a publication while a new extraction does not fit.
// Neither a carried note nor a legitimate removal acknowledges that extraction.
func TestPhase2ProgressFailureWithExcludedExtraction(t *testing.T) {
	batch := ConsolidationBatch{
		Extractions: []Extraction{{ALegID: "unfit"}},
		WindowNames: []string{"still-live"},
		Notes:       []Note{{NoteID: "note-1"}},
	}
	t.Run("carried_note", func(t *testing.T) {
		require.NoError(t, phase2ProgressFailure(ClaimedBank{}, batch, Phase2Included{
			NoteIDs: []string{"note-1"},
		}))
	})
	t.Run("legitimate_removal", func(t *testing.T) {
		require.NoError(t, phase2ProgressFailure(ClaimedBank{
			ConsolidatedNames: []string{"vanished"},
		}, batch, Phase2Included{}))
	})
	t.Run("no_useful_work", func(t *testing.T) {
		batch.Notes = nil
		require.ErrorIs(t, phase2ProgressFailure(ClaimedBank{}, batch, Phase2Included{}),
			errPhase2ExtractionBudget)
	})
}

// TestWorkerAlreadyConsolidatedReplayIsNotProgress pins the review finding on
// repeated billing: replaying an already consolidated extraction is not work,
// so an unprocessable pending note must not drive paid consolidations. Each
// tick re-claims the bank, which models the downstream residual-note progress
// that keeps it claimable; no tick may buy an inference or publish.
func TestWorkerAlreadyConsolidatedReplayIsNotProgress(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2InputTokens, cfg.Phase2Batch = 2_000, 1
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	extraction := workerExtraction(h.base)
	bank := workerClaimedBank()
	bank.ConsolidatedNames = []string{SummaryName(extraction)}
	h.store.extractions = []Extraction{extraction}
	h.store.notes = []Note{{NoteID: "oversized-for-input", Text: strings.Repeat("n", 8_192)}}
	for range 3 {
		h.store.banks = []ClaimedBank{bank}
		h.worker.Tick(context.Background())
	}

	require.Len(t, h.aux.submitCalls(), 0, "replaying an unchanged extraction must not buy an inference")
	require.Len(t, h.store.publishCalls(), 0, "a replay must not acknowledge progress")
	failures := h.store.bankFailCalls()
	require.Len(t, failures, 3, "every tick must be deferred")
	var logs strings.Builder
	for _, record := range h.logs.snapshot() {
		logs.WriteString(captureRecordText(record))
	}
	require.Equal(t, 3, strings.Count(logs.String(), "note_input_budget"), "the refusal must be classified as a note budget failure")
	require.Equal(t, float64(3), captureCounter(t, h.metrics, EventPhase2Failed), "the refusal must stay observable")
}

// TestWorkerBudgetOmittedExtractionKeepsConsolidationIdentity pins the review
// finding on the progress baseline: excluding an already consolidated
// extraction from one prompt because it no longer fits must not delete the fact
// that it was consolidated. Two successive notes must still be processed, and
// the omitted extraction must not be reclassified as new.
func TestWorkerBudgetOmittedExtractionKeepsConsolidationIdentity(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2InputTokens, cfg.Phase2Batch = 4_000, 1
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	extraction := workerExtraction(h.base)
	extraction.RawMemory = strings.Repeat("r", 100*1024)
	bank := workerClaimedBank()
	bank.ConsolidatedNames = []string{SummaryName(extraction)}
	h.store.extractions = []Extraction{extraction}

	// First note: the extraction no longer fits, the note does.
	bank.MemoryMD = strings.Repeat("m", 64*1024)
	h.store.notes = []Note{{NoteID: "note-1", Text: "first", CreatedAtMS: h.base.Add(-2 * time.Hour).UnixMilli()}}
	h.store.banks = []ClaimedBank{bank}
	h.worker.Tick(context.Background())

	publishes := h.store.publishCalls()
	require.Len(t, publishes, 1, "the fitting note must be consolidated")
	require.Equal(t, []string{"note-1"}, publishes[0].publication.ConsumedNoteIDs)
	require.Equal(t, []string{SummaryName(extraction)}, publishes[0].publication.ConsolidatedNames,
		"the omitted already consolidated extraction must keep its identity")

	// Second note: the bank carries the first publication's baseline, and the
	// extraction is still too large. It must not be treated as new work.
	next := bank
	next.ConsolidatedNames = publishes[0].publication.ConsolidatedNames
	next.MemoryMD = publishes[0].publication.MemoryMD
	h.store.notes = []Note{{NoteID: "note-2", Text: "second", CreatedAtMS: h.base.Add(-time.Hour).UnixMilli()}}
	h.store.banks = []ClaimedBank{next}
	h.worker.Tick(context.Background())

	publishes = h.store.publishCalls()
	require.Len(t, publishes, 2, "the second fitting note must be consolidated too")
	require.Equal(t, []string{"note-2"}, publishes[1].publication.ConsumedNoteIDs)
	require.Equal(t, []string{SummaryName(extraction)}, publishes[1].publication.ConsolidatedNames)
	require.Len(t, h.aux.submitCalls(), 2, "each note buys exactly one inference")
	require.Len(t, h.store.bankFailCalls(), 0, "the budget omission must not be a failure")
}

func TestWorkerPhase2EmptyInputPublishesEmptyWithoutAux(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	bank := workerClaimedBank()
	h.store.banks = []ClaimedBank{bank}

	h.worker.Tick(context.Background())

	if got := h.aux.submitCalls(); len(got) != 0 {
		t.Fatalf("auxiliary submissions = %d, want 0", len(got))
	}
	publishes := h.store.publishCalls()
	if len(publishes) != 1 {
		t.Fatalf("PublishBank calls = %d, want 1", len(publishes))
	}
	wantPublication := Publication{
		ConsolidatedNames: []string{},
		WindowSinceMS:     h.base.Add(-workerTestConfig().MaxUnusedAge).UnixMilli(),
		PublishedAtMS:     h.base.UnixMilli(),
	}
	if got := publishes[0].publication; !reflect.DeepEqual(got, wantPublication) {
		t.Fatalf("publication mismatch\n got: %#v\nwant: %#v", got, wantPublication)
	}
	if !reflect.DeepEqual(publishes[0].bank, bank) {
		t.Fatalf("published bank mismatch\n got: %#v\nwant: %#v", publishes[0].bank, bank)
	}
	if got := captureCounter(t, h.metrics, EventPhase2PublishedEmpty); got != 1 {
		t.Fatalf("phase2_published_empty = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase2Published); got != 0 {
		t.Fatalf("phase2_published = %v, want 0", got)
	}
}

// TestWorkerLeaseLostCountsWithoutFailure pins design §10's lease-lost rule on
// every store call that can lose a lease: the loss is counted as the phase's
// lease_lost event and never as a failure, and no compensating call is made.
func TestWorkerLeaseLostCountsWithoutFailure(t *testing.T) {
	t.Run("phase1_complete", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
		h.store.completeErr = ErrLeaseLost

		h.worker.Tick(context.Background())

		if got := captureCounter(t, h.metrics, EventPhase1LeaseLost); got != 1 {
			t.Fatalf("phase1_lease_lost = %v, want 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 0 {
			t.Fatalf("phase1_failed = %v, want 0", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase1Succeeded); got != 0 {
			t.Fatalf("phase1_succeeded = %v, want 0", got)
		}
		if got := h.store.sessionFailCalls(); len(got) != 0 {
			t.Fatalf("FailSession calls = %d, want 0", len(got))
		}
	})
	t.Run("phase1_fail", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
		h.store.failSessionErr = ErrLeaseLost
		h.aux.text = workerInvalidOutput

		h.worker.Tick(context.Background())

		if got := captureCounter(t, h.metrics, EventPhase1LeaseLost); got != 1 {
			t.Fatalf("phase1_lease_lost = %v, want 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 0 {
			t.Fatalf("phase1_failed = %v, want 0", got)
		}
	})
	t.Run("phase2_publish", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.aux.text = workerPhase2Output
		h.store.banks = []ClaimedBank{workerClaimedBank()}
		h.store.extractions = []Extraction{workerExtraction(h.base)}
		h.store.publishErr = ErrLeaseLost

		h.worker.Tick(context.Background())

		if got := captureCounter(t, h.metrics, EventPhase2LeaseLost); got != 1 {
			t.Fatalf("phase2_lease_lost = %v, want 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 0 {
			t.Fatalf("phase2_failed = %v, want 0", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2Published); got != 0 {
			t.Fatalf("phase2_published = %v, want 0", got)
		}
		if got := h.store.bankFailCalls(); len(got) != 0 {
			t.Fatalf("FailBank calls = %d, want 0", len(got))
		}
	})
	t.Run("phase2_fail", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.aux.text = workerInvalidOutput
		h.store.banks = []ClaimedBank{workerClaimedBank()}
		h.store.extractions = []Extraction{workerExtraction(h.base)}
		h.store.failBankErr = ErrLeaseLost

		h.worker.Tick(context.Background())

		if got := captureCounter(t, h.metrics, EventPhase2LeaseLost); got != 1 {
			t.Fatalf("phase2_lease_lost = %v, want 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 0 {
			t.Fatalf("phase2_failed = %v, want 0", got)
		}
	})
}

// TestWorkerPurgesOncePerTickWithCutoffs pins design §10 step 1: every Tick
// runs exactly one purge with the maximum-session-age and maximum-unused-age
// cutoffs and the fixed row limit, and counts the deleted rows as
// phase1_purged.
func TestWorkerPurgesOncePerTickWithCutoffs(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	h.store.purgeDeleted = 2

	h.worker.Tick(context.Background())

	purges := h.store.purgeCalls()
	if len(purges) != 1 {
		t.Fatalf("Purge calls = %d, want 1", len(purges))
	}
	want := workerPurgeCall{
		nowMS:               h.base.UnixMilli(),
		sessionsBeforeMS:    h.base.Add(-cfg.MaxSessionAge).UnixMilli(),
		extractionsBeforeMS: h.base.Add(-cfg.MaxUnusedAge).UnixMilli(),
		limit:               500,
	}
	if got := purges[0]; got != want {
		t.Fatalf("purge call = %#v, want %#v", got, want)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Purged); got != 2 {
		t.Fatalf("phase1_purged = %v, want 2", got)
	}
}

// TestWorkerTickRecoversStorePanic pins design §10's panic boundary: a panic
// from any store step is recovered inside Tick, counted as worker_panic and
// logged, and the process sees no panic.
func TestWorkerTickRecoversStorePanic(t *testing.T) {
	cases := []struct {
		name  string
		panic string
		setup func(*workerTestHarness)
	}{
		{
			name:  "purge",
			panic: "purge",
		},
		{
			name:  "complete_session",
			panic: "complete-session:" + workerTestALeg1,
			setup: func(h *workerTestHarness) {
				h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkerTestHarness(t, workerTestConfig())
			if tc.setup != nil {
				tc.setup(h)
			}
			h.store.panicMethod = tc.panic

			h.worker.Tick(context.Background())

			if got := captureCounter(t, h.metrics, EventWorkerPanic); got != 1 {
				t.Fatalf("worker_panic = %v, want 1", got)
			}
			var warned bool
			for _, record := range h.logs.snapshot() {
				if record.Level >= slog.LevelWarn {
					warned = true
				}
			}
			if !warned {
				t.Fatal("a recovered panic must be logged")
			}
		})
	}
}

// TestWorkerStartStopClosesLoop pins the design §10 lifecycle: Start generates
// a 16-byte-hex lease owner, starts one loop over the injected ticker source
// for cfg.PollInterval, is idempotent, and Stop cancels the loop and waits
// until it exited (the done channel is closed).
func TestWorkerStartStopClosesLoop(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)

	if err := h.worker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	leaseOwner := h.worker.leaseOwner
	if len(leaseOwner) != 32 || strings.Trim(leaseOwner, "0123456789abcdef") != "" {
		t.Fatalf("lease owner = %q, want 32 lowercase hex characters", leaseOwner)
	}
	if err := h.worker.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if h.worker.leaseOwner != leaseOwner {
		t.Fatalf("second Start replaced the lease owner: %q -> %q", leaseOwner, h.worker.leaseOwner)
	}
	if err := h.worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-h.worker.done:
	default:
		t.Fatal("worker loop did not exit before Stop returned")
	}
	if !slices.Equal(h.tickerDurations, []time.Duration{cfg.PollInterval}) {
		t.Fatalf("ticker durations = %v, want [%v]", h.tickerDurations, cfg.PollInterval)
	}
}

// TestWorkerStartRejectsNonPositivePollInterval pins the Start guard that
// keeps a misconfigured interval from panicking time.NewTicker in the loop
// goroutine: Start reports the error and starts nothing.
func TestWorkerStartRejectsNonPositivePollInterval(t *testing.T) {
	cfg := workerTestConfig()
	cfg.PollInterval = 0
	h := newWorkerTestHarness(t, cfg)

	if err := h.worker.Start(context.Background()); err == nil {
		t.Fatal("Start with a zero poll interval = nil, want an error")
	}
	if h.worker.lifecycle != workerLifecycleIdle {
		t.Fatal("a rejected Start must not start the loop")
	}
}

// TestWorkerStopBeforeStartLeavesLaterStopEffective pins the design §10
// Start/Stop lifecycle semantics: Stop is not a latch but cancels whatever
// loop the lifecycle state publishes, so a Stop before Start finds no loop and
// the Stop after Start observes the newly published cancel and joins the loop.
// The wait is bounded: a permanently consumed cancellation would time out
// here, never hang the package.
func TestWorkerStopBeforeStartLeavesLaterStopEffective(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())

	if err := h.worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
	if err := h.worker.Start(context.Background()); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.worker.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after Stop/Start = %v, want the later Stop to observe the published cancel", err)
	}
	select {
	case <-h.worker.done:
	default:
		t.Fatal("worker loop did not exit before Stop returned")
	}
}

// TestWorkerConcurrentStopAndStartDoNotDeadlock pins the concurrent Stop/Start
// lifecycle window: when Stop and Start of one worker run concurrently, the
// single lifecycle mutex lets both finish whichever order they take, and a
// later Stop still cancels and joins the loop. Every wait is bounded, so a
// lost cancel or a deadlock between the two operations fails here instead of
// hanging the package.
func TestWorkerConcurrentStopAndStartDoNotDeadlock(t *testing.T) {
	for range 8 {
		h := newWorkerTestHarness(t, workerTestConfig())
		begin := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-begin
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := h.worker.Stop(stopCtx); err != nil {
				t.Errorf("concurrent Stop = %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			<-begin
			if err := h.worker.Start(context.Background()); err != nil {
				t.Errorf("concurrent Start = %v", err)
			}
		}()
		close(begin)
		wg.Wait()
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := h.worker.Stop(stopCtx)
		cancel()
		if err != nil {
			t.Fatalf("final Stop = %v; concurrent Stop/Start left the loop unjoinable", err)
		}
	}
}

// TestWorkerFirstTickRunsAfterPollIntervalNotAtStart pins design §10's loop
// rule: Start alone performs no store work, and the first tick after one
// injected PollInterval runs a full Tick under the generated lease owner.
func TestWorkerFirstTickRunsAfterPollIntervalNotAtStart(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)

	if err := h.worker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := h.store.callCount(); got != 0 {
		t.Fatalf("store calls right after Start = %d, want 0", got)
	}

	h.ticker.fire(t)
	h.store.waitFor(t, "claim-banks")

	purges := h.store.purgeCalls()
	if len(purges) != 1 {
		t.Fatalf("Purge calls after one tick = %d, want 1", len(purges))
	}
	claims := h.store.sessionClaimCalls()
	if len(claims) != 1 {
		t.Fatalf("ClaimSessions calls after one tick = %d, want 1", len(claims))
	}
	if claims[0].LeaseOwner != h.worker.leaseOwner {
		t.Fatalf("claim lease owner = %q, want the generated %q", claims[0].LeaseOwner, h.worker.leaseOwner)
	}
	if claims[0].Limit != 1 {
		t.Fatalf("claim limit = %d, want 1", claims[0].Limit)
	}
	if err := h.worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestWorkerClaimsOneSessionAtATime pins design §10's claim cadence: with a
// Phase1Batch of two and three idle sessions, the worker claims exactly one
// session at a time with Limit 1, processes it before the next claim, and
// stops at the batch size with the third session still unclaimed.
func TestWorkerClaimsOneSessionAtATime(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase1Batch = 2
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{
		workerClaimedSession(workerTestALeg1, h.base),
		workerClaimedSession(workerTestALeg2, h.base),
		workerClaimedSession(workerTestALeg3, h.base),
	}

	h.worker.Tick(context.Background())

	wantOrder := []string{
		"purge",
		"claim-sessions",
		"complete-session:" + workerTestALeg1,
		"claim-sessions",
		"complete-session:" + workerTestALeg2,
		"claim-banks",
	}
	if got := h.store.callOrder(); !slices.Equal(got, wantOrder) {
		t.Fatalf("tick order = %v, want %v", got, wantOrder)
	}
	claims := h.store.sessionClaimCalls()
	if len(claims) != 2 {
		t.Fatalf("ClaimSessions calls = %d, want 2", len(claims))
	}
	for i, claim := range claims {
		if claim.Limit != 1 {
			t.Fatalf("claim %d limit = %d, want 1", i, claim.Limit)
		}
	}
	completes := h.store.completeCalls()
	if len(completes) != 2 {
		t.Fatalf("CompleteSession calls = %d, want 2", len(completes))
	}
	if completes[0].session.ALegID != workerTestALeg1 || completes[1].session.ALegID != workerTestALeg2 {
		t.Fatalf("completed sessions = [%s %s], want [%s %s]",
			completes[0].session.ALegID, completes[1].session.ALegID, workerTestALeg1, workerTestALeg2)
	}
	if got := h.store.pendingSessions(); got != 1 {
		t.Fatalf("unclaimed sessions = %d, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Succeeded); got != 2 {
		t.Fatalf("phase1_succeeded = %v, want 2", got)
	}
}

// TestWorkerPhase2ConsumesOnlyIncludedNotesAndCountsTruncation pins design
// §10 step 3.3/3.4 and Requirement 5.7: a note the input budget left out stays
// pending, and a truncated build is counted as phase2_truncated.
func TestWorkerPhase2ConsumesOnlyIncludedNotesAndCountsTruncation(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2NoteBytes = 8
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	extraction := workerExtraction(h.base)
	h.store.banks = []ClaimedBank{workerClaimedBank()}
	h.store.extractions = []Extraction{extraction}
	h.store.notes = []Note{
		{NoteID: "note-1", Text: "alpha", CreatedAtMS: h.base.Add(-2 * time.Hour).UnixMilli()},
		{NoteID: "note-2", Text: "a much longer note text that does not fit", CreatedAtMS: h.base.Add(-time.Hour).UnixMilli()},
	}

	h.worker.Tick(context.Background())

	publishes := h.store.publishCalls()
	if len(publishes) != 1 {
		t.Fatalf("PublishBank calls = %d, want 1", len(publishes))
	}
	if got, want := publishes[0].publication.ConsumedNoteIDs, []string{"note-1"}; !slices.Equal(got, want) {
		t.Fatalf("ConsumedNoteIDs = %v, want %v", got, want)
	}
	if got, want := publishes[0].publication.ConsolidatedNames, []string{SummaryName(extraction)}; !slices.Equal(got, want) {
		t.Fatalf("ConsolidatedNames = %v, want %v", got, want)
	}
	if got := captureCounter(t, h.metrics, EventPhase2Truncated); got != 1 {
		t.Fatalf("phase2_truncated = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase2Published); got != 1 {
		t.Fatalf("phase2_published = %v, want 1", got)
	}
}

// TestWorkerPublishFailureSchedulesRetry pins design §10 steps 3.2/3.5 and
// Requirements 5.5/5.6 on failed publications: a non-lease PublishBank failure
// — the consolidated publication and the empty-bank publication — routes the
// bank through FailBank at now+RetryBackoff, and the attempt is counted
// phase2_failed exactly once.
func TestWorkerPublishFailureSchedulesRetry(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*workerTestHarness)
	}{
		{"consolidated", func(h *workerTestHarness) {
			h.aux.text = workerPhase2Output
			h.store.extractions = []Extraction{workerExtraction(h.base)}
		}},
		{"empty_bank", func(*workerTestHarness) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := workerTestConfig()
			h := newWorkerTestHarness(t, cfg)
			bank := workerClaimedBank()
			h.store.banks = []ClaimedBank{bank}
			tc.setup(h)
			h.store.publishErr = errors.New("transient commit failure")

			h.worker.Tick(context.Background())

			if got := len(h.store.publishCalls()); got != 1 {
				t.Fatalf("PublishBank calls = %d, want 1", got)
			}
			fails := h.store.bankFailCalls()
			if len(fails) != 1 {
				t.Fatalf("FailBank calls = %d, want 1 after a failed publication", len(fails))
			}
			if got, want := fails[0].retryAtMS, h.base.Add(cfg.RetryBackoff).UnixMilli(); got != want {
				t.Fatalf("retryAtMS = %d, want %d", got, want)
			}
			if !reflect.DeepEqual(fails[0].bank, bank) {
				t.Fatalf("failed bank mismatch\n got: %#v\nwant: %#v", fails[0].bank, bank)
			}
			if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 1 {
				t.Fatalf("phase2_failed = %v, want 1", got)
			}
			if got := captureCounter(t, h.metrics, EventPhase2Published); got != 0 {
				t.Fatalf("phase2_published = %v, want 0", got)
			}
			if got := captureCounter(t, h.metrics, EventPhase2PublishedEmpty); got != 0 {
				t.Fatalf("phase2_published_empty = %v, want 0", got)
			}
			if got := captureCounter(t, h.metrics, EventPhase2LeaseLost); got != 0 {
				t.Fatalf("phase2_lease_lost = %v, want 0", got)
			}
		})
	}
}

// TestWorkerPublishFailureCountsOnceWhenRecordingFails pins the exactly-once
// failure contract of the publication compensation when the FailBank write
// itself fails: the attempt is still counted phase2_failed exactly once (no
// second compensation, no recursion) on both the failed-publication and the
// invalid-reply fallback paths, and a lease lost while recording keeps the
// phase2_lease_lost classification.
func TestWorkerPublishFailureCountsOnceWhenRecordingFails(t *testing.T) {
	t.Run("recording_error", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.aux.text = workerPhase2Output
		h.store.banks = []ClaimedBank{workerClaimedBank()}
		h.store.extractions = []Extraction{workerExtraction(h.base)}
		h.store.publishErr = errors.New("transient commit failure")
		h.store.failBankErr = errors.New("failure write unavailable")

		h.worker.Tick(context.Background())

		if got := len(h.store.bankFailCalls()); got != 1 {
			t.Fatalf("FailBank calls = %d, want exactly 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 1 {
			t.Fatalf("phase2_failed = %v, want exactly 1 when the failure write also fails", got)
		}
	})
	t.Run("invalid_parse_recording_error", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.aux.text = workerInvalidOutput
		h.store.banks = []ClaimedBank{workerClaimedBank()}
		h.store.extractions = []Extraction{workerExtraction(h.base)}
		h.store.failBankErr = errors.New("failure write unavailable")

		h.worker.Tick(context.Background())

		if got := len(h.store.bankFailCalls()); got != 1 {
			t.Fatalf("FailBank calls = %d, want exactly 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 1 {
			t.Fatalf("phase2_failed = %v, want exactly 1", got)
		}
	})
	t.Run("recording_lease_lost", func(t *testing.T) {
		h := newWorkerTestHarness(t, workerTestConfig())
		h.store.banks = []ClaimedBank{workerClaimedBank()}
		h.store.publishErr = errors.New("transient commit failure")
		h.store.failBankErr = ErrLeaseLost

		h.worker.Tick(context.Background())

		if got := len(h.store.bankFailCalls()); got != 1 {
			t.Fatalf("FailBank calls = %d, want exactly 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2LeaseLost); got != 1 {
			t.Fatalf("phase2_lease_lost = %v, want 1", got)
		}
		if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 0 {
			t.Fatalf("phase2_failed = %v, want 0 when recording the failure lost the lease", got)
		}
	})
}

// TestWorkerPhase2TruncationSurvivesFailedAttempts pins Requirement 5.9 on
// failed consolidate attempts: the phase2_truncated count and log happen as
// soon as BuildPhase2Request fixes the input, ahead of the collection and the
// parse, so a failed collection or an invalid reply still proves the submitted
// input was truncated; the omitted notes stay unconsumed because nothing is
// published, and the record stays content-free.
func TestWorkerPhase2TruncationSurvivesFailedAttempts(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*workerAuxStub)
	}{
		{"await_error", func(a *workerAuxStub) { a.awaitErr = errors.New("await unavailable") }},
		{"invalid_output", func(a *workerAuxStub) { a.text = workerInvalidOutput }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := workerTestConfig()
			cfg.Phase2NoteBytes = 8
			h := newWorkerTestHarness(t, cfg)
			h.store.banks = []ClaimedBank{workerClaimedBank()}
			h.store.extractions = []Extraction{workerExtraction(h.base)}
			h.store.notes = []Note{
				{NoteID: "note-1", Text: "alpha", CreatedAtMS: h.base.Add(-2 * time.Hour).UnixMilli()},
				{NoteID: "note-2", Text: "a much longer note text that does not fit", CreatedAtMS: h.base.Add(-time.Hour).UnixMilli()},
			}
			tc.setup(h.aux)

			h.worker.Tick(context.Background())

			if got := len(h.aux.submitCalls()); got != 1 {
				t.Fatalf("auxiliary submissions = %d, want 1", got)
			}
			if got := len(h.store.bankFailCalls()); got != 1 {
				t.Fatalf("FailBank calls = %d, want 1", got)
			}
			if got := len(h.store.publishCalls()); got != 0 {
				t.Fatalf("PublishBank calls = %d, want 0 so omitted notes stay unconsumed", got)
			}
			if got := captureCounter(t, h.metrics, EventPhase2Truncated); got != 1 {
				t.Fatalf("phase2_truncated = %v, want 1 even when the attempt fails", got)
			}
			if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 1 {
				t.Fatalf("phase2_failed = %v, want 1", got)
			}
			for _, record := range h.logs.snapshot() {
				text := captureRecordText(record)
				for _, forbidden := range []string{workerTestProject, workerTestLabel, "note-2", "a much longer note"} {
					if strings.Contains(text, forbidden) {
						t.Fatalf("truncation log leaks %q: %q", forbidden, text)
					}
				}
			}
		})
	}
}

// TestWorkerLifecycleFlags pins design §10's lifecycle classification: the
// worker opts into quiesce-phase stop semantics and overlapping generations
// are safe because every claim is leased.
func TestWorkerLifecycleFlags(t *testing.T) {
	h := newWorkerTestHarness(t, workerTestConfig())
	if !h.worker.QuiescePhase() {
		t.Fatal("QuiescePhase() = false, want true")
	}
	if !h.worker.SafeUnderCandidateOverlap() {
		t.Fatal("SafeUnderCandidateOverlap() = false, want true")
	}
}

// TestWorkerOwnerScope pins design §10's ownerScope helper: a service
// principal carrying the owner identity and an internal origin, with a present
// tenant when the owner has one and an unknown tenant when it does not.
func TestWorkerAuxiliaryDeadlineIncludesAdmission(t *testing.T) {
	for _, phase := range []string{"phase1", "phase2"} {
		t.Run(phase, func(t *testing.T) {
			cfg := workerTestConfig()
			h := newWorkerTestHarness(t, cfg)
			if phase == "phase1" {
				h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
			} else {
				h.store.banks = []ClaimedBank{workerClaimedBank()}
				h.store.notes = []Note{{NoteID: "fits", Text: "remember this"}}
				h.aux.text = workerPhase2Output
			}
			before := time.Now()
			h.worker.Tick(context.Background())
			calls := h.aux.submitCalls()
			if len(calls) != 1 {
				t.Fatalf("submissions = %d, want 1", len(calls))
			}
			deadline := calls[0].opts.Deadline
			if deadline.IsZero() || deadline.Before(before) || deadline.After(time.Now().Add(cfg.AuxTimeout)) {
				t.Fatalf("submission deadline = %v, want admission-inclusive auxiliary bound", deadline)
			}
			contextDeadline, ok := calls[0].ctx.Deadline()
			if !ok || !contextDeadline.Equal(deadline) {
				t.Fatalf("Await/submit context deadline %v differs from job deadline %v", contextDeadline, deadline)
			}
		})
	}
}

func TestWorkerExpiredLeaseNeverSubmits(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	h.clock.Advance(cfg.Lease)
	_, err := h.worker.collect(context.Background(), workerOwner(), auxiliary.Request{}, "expired-claim", h.worker.claimDeadline(h.base))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired claim result = %v, want deadline exceeded", err)
	}
	if got := len(h.aux.submitCalls()); got != 0 {
		t.Fatalf("auxiliary submissions = %d, want 0 after claim expiry", got)
	}
}

func TestWorkerOwnerScope(t *testing.T) {
	known := ownerScope(workerOwner())
	if known.SubjectKind != scope.SubjectService {
		t.Fatalf("subject kind = %q, want %q", known.SubjectKind, scope.SubjectService)
	}
	if !known.PrincipalID.Equal(scope.Known(workerTestPrincipal)) {
		t.Fatalf("principal = %#v, want %#v", known.PrincipalID, scope.Known(workerTestPrincipal))
	}
	if !known.TenantID.Equal(scope.Known(workerTestTenant)) {
		t.Fatalf("tenant = %#v, want %#v", known.TenantID, scope.Known(workerTestTenant))
	}
	if known.Origin != scope.OriginInternal {
		t.Fatalf("origin = %q, want %q", known.Origin, scope.OriginInternal)
	}
	unknown := ownerScope(Owner{TenantID: "", PrincipalID: workerTestPrincipal})
	if !unknown.TenantID.IsUnknown() {
		t.Fatalf("tenant without an owner tenant = %#v, want unknown", unknown.TenantID)
	}
}
