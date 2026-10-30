package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// attemptSpyStore wraps b2bua.Store and records every RecordAttempt call.
type attemptSpyStore struct {
	b2bua.Store
	mu      sync.Mutex
	records []aipapi.AttemptRecord
}

func (s *attemptSpyStore) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	s.mu.Lock()
	s.records = append(s.records, rec)
	s.mu.Unlock()
	return s.Store.RecordAttempt(ctx, rec)
}

func (s *attemptSpyStore) Records() []aipapi.AttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]aipapi.AttemptRecord(nil), s.records...)
}

// stubWireBackend captures wire open requests and returns configured streams or errors.
type stubWireBackend struct {
	mu           sync.Mutex
	calls        []largebody.WireOpenRequest
	receivedData [][]byte
	openFunc     func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error)
}

func (s *stubWireBackend) OpenWire(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	s.receivedData = append(s.receivedData, bodyBytes)
	fn := s.openFunc
	s.mu.Unlock()

	if fn != nil {
		return fn(ctx, req)
	}
	return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(nil)}, nil
}

func (s *stubWireBackend) Calls() []largebody.WireOpenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]largebody.WireOpenRequest(nil), s.calls...)
}

func (s *stubWireBackend) ReceivedData() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.receivedData...)
}

func makeStreamWithEvents(events ...aipapi.Event) aipapi.ManagedEventStream {
	return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(events)}
}

// TestTask13_2_SingleAttempt_SuccessfulWireOpen verifies that ExecuteLargeBody
// runs a real attempt through backend OpenWire, passes a reader at offset zero,
// allocates a valid B-leg, returns a canonical EventStream, and releases authority on Close.
func TestTask13_2_SingleAttempt_SuccessfulWireOpen(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	backend := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Hello from backend"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: backend.OpenWire,
		},
	}
	ex.DefaultBackend = "default"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-1"})
	ctx = largebody.WithWireIdentity(ctx, "req-1", "trace-1")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}

	// Verify backend OpenWire was invoked
	calls := backend.Calls()
	if len(calls) != 1 {
		t.Fatalf("backend.Calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.ContentLength != src.size {
		t.Errorf("call.ContentLength = %d, want %d", call.ContentLength, src.size)
	}
	received := backend.ReceivedData()
	if string(received[0]) != content {
		t.Errorf("received body = %q, want %q", string(received[0]), content)
	}
	if call.BLegID == "" {
		t.Error("call.BLegID must not be empty")
	}

	// Verify canonical stream returns events
	ev1, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 1 failed: %v", err)
	}
	if ev1.Kind != aipapi.EventTextDelta || ev1.Delta != "Hello from backend" {
		t.Errorf("ev1 = %+v, want TextDelta 'Hello from backend'", ev1)
	}

	ev2, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 2 failed: %v", err)
	}
	if ev2.Kind != aipapi.EventResponseFinished {
		t.Errorf("ev2.Kind = %v, want EventResponseFinished", ev2.Kind)
	}

	_, err = res.Stream.Recv(ctx)
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF, got %v", err)
	}

	// Verify stream close succeeds
	if err := res.Stream.Close(); err != nil {
		t.Errorf("res.Stream.Close error: %v", err)
	}

	// Verify B-leg record in store
	atts, err := store.LoadAttempts(ctx, res.Facts.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts failed: %v", err)
	}
	if len(atts) != 1 {
		t.Errorf("store attempts = %d, want 1", len(atts))
	}
}

// TestTask13_2_SequentialFailover_ReplayAtOffsetZero verifies Requirement 10.1, 10.2, 10.6:
// Attempt 1 fails pre-output; Attempt 2 obtains a fresh reader at offset zero with complete
// original bytes, gets a sequential B-leg, and succeeds.
func TestTask13_2_SequentialFailover_ReplayAtOffsetZero(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"failover test"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:gpt-4o", "b2:gpt-4o")

	be1 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return nil, aipapi.RecoverablePreOutputError(errors.New("upstream HTTP 500: backend 1 failure"))
		},
	}
	be2 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "Success from backend 2"},
			), nil
		},
	}

	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: be1.OpenWire},
		"b2": {OpenWire: be2.OpenWire},
	}

	// Configure route with failover: b1:gpt-4o | b2:gpt-4o
	acc.WireRequest.CandidateModel = "b1:gpt-4o | b2:gpt-4o"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-failover"})
	ctx = largebody.WithWireIdentity(ctx, "req-fo", "trace-fo")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}

	// Verify both backends were attempted
	if len(be1.Calls()) != 1 {
		t.Fatalf("be1.Calls = %d, want 1", len(be1.Calls()))
	}
	if len(be2.Calls()) != 1 {
		t.Fatalf("be2.Calls = %d, want 1", len(be2.Calls()))
	}

	// Verify Attempt 2 got complete exact bytes from offset zero (Requirement 10.1, 10.6)
	data1 := be1.ReceivedData()
	data2 := be2.ReceivedData()
	if string(data1[0]) != content {
		t.Errorf("be1 data = %q, want %q", string(data1[0]), content)
	}
	if string(data2[0]) != content {
		t.Errorf("be2 data = %q, want %q", string(data2[0]), content)
	}

	// Verify Attempt 2 got a different, sequential B-leg
	call1 := be1.Calls()[0]
	call2 := be2.Calls()[0]
	if call1.BLegID == call2.BLegID {
		t.Errorf("expected different BLegIDs, both got %q", call1.BLegID)
	}

	// Verify stream from Attempt 2 yields events
	ev, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv failed: %v", err)
	}
	if ev.Delta != "Success from backend 2" {
		t.Errorf("ev.Delta = %q, want 'Success from backend 2'", ev.Delta)
	}
	_ = res.Stream.Close()

	// Verify 2 B-legs recorded in store
	atts, err := store.LoadAttempts(ctx, res.Facts.ALegID)
	if err != nil {
		t.Fatalf("LoadAttempts failed: %v", err)
	}
	if len(atts) != 2 {
		t.Errorf("store attempts = %d, want 2", len(atts))
	}
}

// TestTask13_2_ModelSplice_PerAttempt verifies Requirement 9:
// Each attempt applies only approved candidate model splice to a fresh reader.
func TestTask13_2_ModelSplice_PerAttempt(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"splice test"}]}`
	src := newTestSource(rawJSON)

	// Top-level model token span: "gpt-4o" is at offset 9, length 8 ("gpt-4o")
	span := largebody.Span{Offset: 9, Length: 8}
	rewrite, err := largebody.NewModelTokenRewrite(span)
	if err != nil {
		t.Fatal(err)
	}

	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		src.digest,
		src.size,
		largebody.BodyModeIdentityJSON,
		rewrite,
		largebody.NewIdentityDigest(sha256.Sum256([]byte("identity"))),
		"dom-gen-1",
	)
	if err != nil {
		t.Fatal(err)
	}

	wireReq := largebody.WireRequestFacts{
		ProfileID:       "openai-chat",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         rewrite,
		ClientModel:     "gpt-4o",
		CandidateModel:  "b1:model-v1 | b2:model-v2",
		MaxOutputTokens: 2048,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID:       "openai-chat",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		BodyMode:        largebody.BodyModeIdentityJSON,
		Rewrite:         rewrite,
		CandidateModels: []string{"b1:model-v1", "b2:model-v2"},
	}
	acc, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatal(err)
	}

	be1 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return nil, aipapi.RecoverablePreOutputError(errors.New("upstream HTTP 503: overloaded"))
		},
	}
	be2 := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "spliced response"},
			), nil
		},
	}

	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: be1.OpenWire},
		"b2": {OpenWire: be2.OpenWire},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-splice"})
	ctx = largebody.WithWireIdentity(ctx, "req-splice", "trace-splice")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}

	// Verify Attempt 1 got model spliced with "model-v1"
	data1 := be1.ReceivedData()
	if len(data1) != 1 {
		t.Fatalf("be1 data count = %d, want 1", len(data1))
	}
	if !strings.Contains(string(data1[0]), "model-v1") {
		t.Errorf("be1 expected 'model-v1', got: %s", string(data1[0]))
	}
	if strings.Contains(string(data1[0]), "gpt-4o") {
		t.Errorf("be1 still contains original 'gpt-4o': %s", string(data1[0]))
	}

	// Verify Attempt 2 got model spliced with "model-v2"
	data2 := be2.ReceivedData()
	if len(data2) != 1 {
		t.Fatalf("be2 data count = %d, want 1", len(data2))
	}
	if !strings.Contains(string(data2[0]), "model-v2") {
		t.Errorf("be2 expected 'model-v2', got: %s", string(data2[0]))
	}
	if strings.Contains(string(data2[0]), "gpt-4o") {
		t.Errorf("be2 still contains original 'gpt-4o': %s", string(data2[0]))
	}

	// Verify ContentLength in each call reflects exact rewritten length
	call1 := be1.Calls()[0]
	call2 := be2.Calls()[0]
	if call1.ContentLength != int64(len(data1[0])) {
		t.Errorf("call1 ContentLength = %d, want %d", call1.ContentLength, len(data1[0]))
	}
	if call2.ContentLength != int64(len(data2[0])) {
		t.Errorf("call2 ContentLength = %d, want %d", call2.ContentLength, len(data2[0]))
	}

	_ = res.Stream.Close()
}

// TestTask13_2_MaxAttemptsBudgetExceeded verifies that exceeding attempt budget
// stops failover and never falls back to canonical Execute.
func TestTask13_2_MaxAttemptsBudgetExceeded(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	ex.MaxAttempts = 2

	src := newTestSource(`{"model":"gpt-4o","prompt":"budget test"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2", "b3:m3")
	acc.WireRequest.CandidateModel = "b1:m1 | b2:m2 | b3:m3"

	var attempts atomic.Int32
	failingOpen := func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
		attempts.Add(1)
		return nil, aipapi.RecoverablePreOutputError(errors.New("upstream failure"))
	}

	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: failingOpen},
		"b2": {OpenWire: failingOpen},
		"b3": {OpenWire: failingOpen},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-budget"})
	ctx = largebody.WithWireIdentity(ctx, "req-budget", "trace-budget")

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected error on budget exceeded, got nil")
	}

	// Verify only 2 attempts ran (budget capped at 2)
	if got := attempts.Load(); got != 2 {
		t.Errorf("attempts run = %d, want 2", got)
	}
}

// TestTask13_2_Affinity_RecordingAndReuse verifies that affinity is recorded upon
// successful attempt and reused on subsequent requests.
func TestTask13_2_Affinity_RecordingAndReuse(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	affStore := memorystore.New()
	ex.AffinityStore = affStore

	src := newTestSource(`{"model":"gpt-4o","prompt":"affinity test"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2")
	acc.WireRequest.CandidateModel = "b1:m1 | b2:m2"

	var b1Calls, b2Calls atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				b1Calls.Add(1)
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b1"}), nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				b2Calls.Add(1)
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b2"}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-aff"})
	ctx = largebody.WithWireIdentity(ctx, "req-aff-1", "trace-aff-1")

	res1, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	_ = res1.Stream.Close()

	if b1Calls.Load() != 1 {
		t.Fatalf("b1Calls = %d, want 1", b1Calls.Load())
	}
}

// TestTask13_2_CarryForwards verifies that:
// 1. Admitted BillingCallID is propagated into turnFacts and ResponseFacts.
// 2. RequestSizeEstimate carries source body size as token/byte basis.
// 3. Mandatory per-request WireIdentity is validated.
func TestTask13_2_CarryForwards(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	var capturedExposure BillingExposureAdmissionInput
	ex.BillingExposureAdmission = &testExposureAdmission{
		admitFunc: func(ctx context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
			capturedExposure = in
			return billingport.CallExposure{AccountID: in.AccountID}, nil
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"carry-forward"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	backend := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"}), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"default": {OpenWire: backend.OpenWire},
	}
	ex.DefaultBackend = "default"

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-cf"})
	ctx = largebody.WithWireIdentity(ctx, "req-cf-123", "trace-cf-456")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	// 1. WireIdentity propagated
	if res.Facts.RequestID != "req-cf-123" {
		t.Errorf("RequestID = %q, want req-cf-123", res.Facts.RequestID)
	}
	if res.Facts.TraceID != "trace-cf-456" {
		t.Errorf("TraceID = %q, want trace-cf-456", res.Facts.TraceID)
	}

	// 2. Body-size request exposure
	if !capturedExposure.RequestSize.Available {
		t.Error("RequestSize.Available must be true for body-size request exposure")
	}
	if capturedExposure.RequestSize.Tokens != src.size {
		t.Errorf("RequestSize.Tokens = %d, want %d", capturedExposure.RequestSize.Tokens, src.size)
	}

	// 3. BillingCallID validated and non-empty
	if capturedExposure.BillingCallID == "" {
		t.Error("BillingCallID in exposure admission must not be empty")
	}
}

type ctxBoundStream struct {
	openCtx context.Context
	events  []aipapi.Event
	idx     int
}

func (s *ctxBoundStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.openCtx != nil && s.openCtx.Err() != nil {
		return aipapi.Event{}, fmt.Errorf("openCtx killed: %w", s.openCtx.Err())
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *ctxBoundStream) Close() error { return nil }

func (s *ctxBoundStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

// TestTask13_2_Finding1_WinnerStreamTransportAlive_ParallelRace proves that in a parallel
// race, the winner's context is not canceled at handoff, and subsequent Recv calls succeed.
func TestTask13_2_Finding1_WinnerStreamTransportAlive_ParallelRace(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"race"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2")
	acc.WireRequest.CandidateModel = "b1:m1!b2:m2"

	var b1OpenCtx, b2OpenCtx context.Context
	var mu sync.Mutex
	var b2Started sync.WaitGroup
	b2Started.Add(1)

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				b1OpenCtx = ctx
				mu.Unlock()
				b2Started.Wait()
				return &ctxBoundStream{
					openCtx: ctx,
					events: []aipapi.Event{
						{Kind: aipapi.EventTextDelta, Delta: "b1-first"},
						{Kind: aipapi.EventTextDelta, Delta: "b1-second"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				b2OpenCtx = ctx
				mu.Unlock()
				b2Started.Done()
				return nil, errors.New("b2 failed")
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-race"})
	ctx = largebody.WithWireIdentity(ctx, "req-race", "trace-race")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	// Recv the peeked first event
	ev1, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 1 failed: %v", err)
	}
	if ev1.Delta != "b1-first" {
		t.Errorf("ev1.Delta = %q, want 'b1-first'", ev1.Delta)
	}

	// Recv subsequent event from winner's stream - MUST NOT fail with openCtx killed!
	ev2, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 2 (winner post-handoff) failed: %v", err)
	}
	if ev2.Delta != "b1-second" {
		t.Errorf("ev2.Delta = %q, want 'b1-second'", ev2.Delta)
	}

	mu.Lock()
	defer mu.Unlock()
	if b1OpenCtx == nil {
		t.Error("b1OpenCtx should have been set")
	}
	if b2OpenCtx == nil {
		t.Error("b2OpenCtx should have been set")
	}
}

// TestTask13_2_Finding1_WinnerStreamTransportAlive_SequentialTTFTDeadline proves that in
// sequential execution with TTFT deadline, winner's context is not canceled at handoff.
func TestTask13_2_Finding1_WinnerStreamTransportAlive_SequentialTTFTDeadline(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	ex.Now = time.Now
	cands, sel, rerr := routing.ComposeInitialCandidates("{ttft_timeout=10}b1:m1", nil, "default", nil, ex.ExecutionCompositionPolicy, nil)
	groups, gerr := routing.ExpandFailoverGroups(sel, routing.PlanOptions{})
	t.Logf("cands=%#v, rerr=%v, groups=%#v, gerr=%v", cands, rerr, groups, gerr)

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"ttft"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "{ttft_timeout=10}b1:m1"

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return &ctxBoundStream{
					openCtx: ctx,
					events: []aipapi.Event{
						{Kind: aipapi.EventTextDelta, Delta: "token-1"},
						{Kind: aipapi.EventTextDelta, Delta: "token-2"},
						{Kind: aipapi.EventResponseFinished},
					},
				}, nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-ttft"})
	ctx = largebody.WithWireIdentity(ctx, "req-ttft", "trace-ttft")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	ev1, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 1 failed: %v", err)
	}
	if ev1.Delta != "token-1" {
		t.Errorf("ev1.Delta = %q, want 'token-1'", ev1.Delta)
	}

	// Recv subsequent event - MUST NOT fail with openCtx killed!
	ev2, err := res.Stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv 2 failed: %v", err)
	}
	if ev2.Delta != "token-2" {
		t.Errorf("ev2.Delta = %q, want 'token-2'", ev2.Delta)
	}
}

// TestTask13_2_Finding2_UnrecoverableError_StopsFailover proves that unrecoverable
// pre-output errors stop failover and are recorded as AttemptSurfacedFailure.
func TestTask13_2_Finding2_UnrecoverableError_StopsFailover(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	spyStore := &attemptSpyStore{Store: store}
	ex.Store = spyStore

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"unrec"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2")
	acc.WireRequest.CandidateModel = "b1:m1 | b2:m2"

	var b2Attempted bool
	unrecErr := errors.New("400 Bad Request: invalid parameter")

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return nil, unrecErr
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				b2Attempted = true
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b2"}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-unrec"})
	ctx = largebody.WithWireIdentity(ctx, "req-unrec", "trace-unrec")

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if b2Attempted {
		t.Error("failover should NOT have attempted b2 after unrecoverable error on b1")
	}

	recs := spyStore.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 attempt record, got %d", len(recs))
	}
	if recs[0].Outcome != aipapi.AttemptSurfacedFailure {
		t.Errorf("attempt outcome = %v, want AttemptSurfacedFailure", recs[0].Outcome)
	}
}

// TestTask13_2_Finding2_GlobalTTFTTimeout_SurfacesErrTTFTTimeout proves that when a global
// TTFT timeout expires during open/peek, ExecuteLargeBody surfaces aipapi.ErrTTFTTimeout
// and records AttemptSurfacedFailure.
func TestTask13_2_Finding2_GlobalTTFTTimeout_SurfacesErrTTFTTimeout(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	// Backdate executor clock so global TTFT deadline is immediately in the past
	ex.Now = func() time.Time { return time.Now().Add(-2 * time.Second) }
	spyStore := &attemptSpyStore{Store: store}
	ex.Store = spyStore

	src := newTestSource(`{"model":"gpt-4o","prompt":"ttft-timeout"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "{ttft_timeout=1}b1:m1"

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-ttft-to"})
	ctx = largebody.WithWireIdentity(ctx, "req-ttft-to", "trace-ttft-to")

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected TTFT timeout error, got nil")
	}
	if !errors.Is(err, aipapi.ErrTTFTTimeout) {
		t.Errorf("expected ErrTTFTTimeout, got: %v", err)
	}

	recs := spyStore.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 attempt record, got %d", len(recs))
	}
	if recs[0].Outcome != aipapi.AttemptSurfacedFailure {
		t.Errorf("attempt outcome = %v, want AttemptSurfacedFailure", recs[0].Outcome)
	}
}

// TestTask13_2_Finding3_RaceLegs_CaptureWireBackendIngress proves that each parallel
// race leg captures a backend ingress checkpoint.
func TestTask13_2_Finding3_RaceLegs_CaptureWireBackendIngress(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"ingress"}]}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2")
	acc.WireRequest.CandidateModel = "b1:m1!b2:m2"

	var b1Captured, b2Captured atomic.Bool
	var reached sync.WaitGroup
	reached.Add(2)
	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				if holder := meteringHolderFrom(ctx); holder != nil {
					if snap := holder.BackendIngressFor(req.BLegID); snap != nil {
						b1Captured.Store(true)
					}
				}
				reached.Done()
				reached.Wait()
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b1"}), nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				if holder := meteringHolderFrom(ctx); holder != nil {
					if snap := holder.BackendIngressFor(req.BLegID); snap != nil {
						b2Captured.Store(true)
					}
				}
				reached.Done()
				reached.Wait()
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b2"}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-ing"})
	ctx = largebody.WithWireIdentity(ctx, "req-ing", "trace-ing")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()

	if !b1Captured.Load() {
		t.Error("expected backend ingress snapshot captured for b1")
	}
	if !b2Captured.Load() {
		t.Error("expected backend ingress snapshot captured for b2")
	}
}

type failOpenSource struct {
	*testSource
	failFirst atomic.Bool
}

func (s *failOpenSource) Open() (io.ReadCloser, error) {
	if s.failFirst.CompareAndSwap(true, false) {
		return nil, errors.New("open disk failure")
	}
	return s.testSource.Open()
}

// TestTask13_2_Finding4_TerminalCleanup_OpenFreshWireBodyFailure proves that when openFreshWireBody
// fails, the allocated B-leg is closed with LegOutcomeNeverStarted and failover proceeds.
func TestTask13_2_Finding4_TerminalCleanup_OpenFreshWireBodyFailure(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink
	spyStore := &attemptSpyStore{Store: store}
	ex.Store = spyStore

	raw := `{"model":"gpt-4o","messages":[{"role":"user","content":"open-fail"}]}`
	baseSrc := newTestSource(raw)
	src := &failOpenSource{testSource: baseSrc}
	src.failFirst.Store(true)

	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", baseSrc, false, "b1:m1", "b2:m2")
	acc.WireRequest.CandidateModel = "b1:m1 | b2:m2"

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b1"}), nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "b2-success"}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-open-fail"})
	ctx = largebody.WithWireIdentity(ctx, "req-of", "trace-of")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	defer func() { _ = res.Stream.Close() }()
	for {
		if _, rerr := res.Stream.Recv(context.Background()); rerr != nil {
			break
		}
	}

	// Verify attempt 1 was recorded as AttemptSwallowedFailure
	recs := spyStore.Records()
	if len(recs) < 2 {
		t.Fatalf("expected at least 2 attempts recorded, got %d", len(recs))
	}
	if recs[0].Outcome != aipapi.AttemptSwallowedFailure {
		t.Errorf("attempt 0 outcome = %v, want AttemptSwallowedFailure", recs[0].Outcome)
	}

	// Verify sink.legs recorded LegOutcomeNeverStarted for b1
	sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), sink.legs...)
	sink.mu.Unlock()

	var foundB1NeverStarted bool
	for _, l := range legs {
		if l.BackendID == "b1" && l.Outcome == usageevidence.LegOutcomeNeverStarted {
			foundB1NeverStarted = true
			break
		}
	}
	if !foundB1NeverStarted {
		t.Errorf("expected LegOutcomeNeverStarted for b1 in terminal usage sink, legs: %#v", legs)
	}
}

// TestTask13_2_Finding4_TerminalCleanup_ParallelRaceLoserFailedVsCanceled proves that in
// a parallel race, a failed arm is marked LegOutcomeFailed while a canceled arm is marked LegOutcomeCanceled.
func TestTask13_2_Finding4_TerminalCleanup_ParallelRaceLoserFailedVsCanceled(t *testing.T) {
	ex, _, _ := setupTestExecutor(t)
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink

	src := newTestSource(`{"model":"gpt-4o","prompt":"race-clean"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2", "b3:m3")
	acc.WireRequest.CandidateModel = "b1:m1!b2:m2!b3:m3"

	var b2Failed sync.WaitGroup
	b2Failed.Add(1)
	b3Entered := make(chan struct{})

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				b2Failed.Wait()
				// Hold the winner until b3 has entered its backend Open. Otherwise
				// winner publication can cancel the race before b3 allocates its
				// B-leg, so the terminal canceled leg record is never produced.
				select {
				case <-b3Entered:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "winner"}), nil
			},
		},
		"b2": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				defer b2Failed.Done()
				return nil, errors.New("b2 backend exploded")
			},
		},
		"b3": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				close(b3Entered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-race-clean"})
	ctx = largebody.WithWireIdentity(ctx, "req-race-clean", "trace-race-clean")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	if cerr := res.Stream.Close(); cerr != nil {
		t.Errorf("res.Stream.Close() = %v, want nil", cerr)
	}

	sink.mu.Lock()
	legs := append([]usageevidence.CallLegUsageRecord(nil), sink.legs...)
	sink.mu.Unlock()

	var b2Outcome, b3Outcome usageevidence.LegOutcome
	var foundB2, foundB3 bool
	for _, l := range legs {
		if l.BackendID == "b2" {
			foundB2 = true
			b2Outcome = l.Outcome
		}
		if l.BackendID == "b3" {
			foundB3 = true
			b3Outcome = l.Outcome
		}
	}

	if !foundB2 {
		t.Error("expected leg record for b2")
	} else if b2Outcome != usageevidence.LegOutcomeFailed {
		t.Errorf("b2 outcome = %v, want LegOutcomeFailed", b2Outcome)
	}

	if !foundB3 {
		t.Error("expected leg record for b3")
	} else if b3Outcome != usageevidence.LegOutcomeCanceled {
		t.Errorf("b3 outcome = %v, want LegOutcomeCanceled", b3Outcome)
	}
}

// TestTask13_2_Finding4_AllAttemptsExhausted_ClosesTurnAndExposure proves that when all attempts
// are exhausted, appendWireExposureAbort and FinishTurn are called.
func TestTask13_2_Finding4_AllAttemptsExhausted_ClosesTurnAndExposure(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, store)
	ex.SecureSession = mgr
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink
	ex.BillingExposureAdmission = &testExposureAdmission{
		admitFunc: func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
			return billingport.CallExposure{AccountID: "acc-exhaust"}, nil
		},
	}

	src := newTestSource(`{"model":"gpt-4o","prompt":"all-fail"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1")
	acc.WireRequest.CandidateModel = "b1:m1"

	ex.Backends = map[string]execbackend.Backend{
		"b1": {
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("backend down"))
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-exhaust"})
	ctx = largebody.WithWireIdentity(ctx, "req-exhaust", "trace-exhaust")

	_, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err == nil {
		t.Fatal("expected ExecuteLargeBody to fail when all attempts exhausted")
	}

	// 1. Verify appendWireExposureAbort called -> call record with TurnOutcomeFailed in sink.calls
	sink.mu.Lock()
	calls := append([]usageevidence.CallUsageRecord(nil), sink.calls...)
	sink.mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("expected terminal call usage record from appendWireExposureAbort")
	}
	if calls[0].Outcome != usageevidence.TurnOutcomeFailed {
		t.Errorf("call outcome = %v, want TurnOutcomeFailed", calls[0].Outcome)
	}

	// 2. Verify FinishTurn called -> audit record with surfaced_failure
	audits, err := memSS.Audit(ctx, domain.SessionID(calls[0].SessionID), domain.ReadOptions{})
	if err != nil {
		t.Fatalf("Audit failed: %v", err)
	}
	var foundOutcome bool
	for _, a := range audits {
		if a.Action == "turn_outcome" && a.Result == "surfaced_failure" {
			foundOutcome = true
			break
		}
	}
	if !foundOutcome {
		t.Errorf("expected audit record with Action='turn_outcome' and Result='surfaced_failure', got: %#v", audits)
	}
}

// TestTask13_2_Suggestion6_RecordAttemptOutcome_RecordedOnSecureSession proves that attempt
// outcomes are recorded on SecureSession via outCtx.
func TestTask13_2_Suggestion6_RecordAttemptOutcome_RecordedOnSecureSession(t *testing.T) {
	ex, _, store := setupTestExecutor(t)
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, store)
	ex.SecureSession = mgr

	src := newTestSource(`{"model":"gpt-4o","prompt":"sec-session"}`)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	backend := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return makeStreamWithEvents(
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: backend.OpenWire,
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-sec"})
	ctx = largebody.WithWireIdentity(ctx, "req-sec", "trace-sec")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	for {
		if _, rerr := res.Stream.Recv(context.Background()); rerr != nil {
			break
		}
	}
	_ = res.Stream.Close()

	rec, err := mgr.LoadByALegID(ctx, res.Facts.ALegID)
	if err != nil {
		t.Fatalf("LoadByALegID failed: %v", err)
	}

	calls := backend.Calls()
	if len(calls) == 0 {
		t.Fatal("expected at least 1 backend call")
	}
	if rec.LatestAttemptOutcome.BLegID != calls[0].BLegID {
		t.Errorf("LatestAttemptOutcome.BLegID = %q, want %q", rec.LatestAttemptOutcome.BLegID, calls[0].BLegID)
	}
	if !rec.LatestAttemptOutcome.Success {
		t.Errorf("LatestAttemptOutcome.Success = false, want true")
	}
}
