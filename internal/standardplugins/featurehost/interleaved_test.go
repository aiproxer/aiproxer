package featurehost

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// interleavedAdapterTestKey is a test-only context key type; bare string keys
// are rejected by SA1029 even in tests.
type interleavedAdapterTestKey string

type fakeFeatureProcessor struct {
	lastCtx       context.Context
	lastIn        interleavedthinking.TurnInput
	turnToRet     interleavedthinking.Turn
	errToRet      error
	visibleALegID string
	visibleResult bool
}

func (f *fakeFeatureProcessor) BeginTurn(ctx context.Context, in interleavedthinking.TurnInput) (interleavedthinking.Turn, error) {
	f.lastCtx = ctx
	f.lastIn = in
	return f.turnToRet, f.errToRet
}

func (f *fakeFeatureProcessor) IsMemoVisibleToClient(ctx context.Context, aLegID string) bool {
	f.lastCtx = ctx
	f.visibleALegID = aLegID
	return f.visibleResult
}

type fakeFeatureTurn struct {
	shapeThinkerInCall   aipapi.Call
	shapeThinkerOutCall  aipapi.Call
	shapeThinkerErr      error
	observeEventIn       aipapi.Event
	observeEventOut      []aipapi.Event
	observeEventErr      error
	finalizeCtx          context.Context
	finalizeOut          interleavedthinking.MemoResult
	finalizeErr          error
	finalizeStatusCtx    context.Context
	finalizeStatusInter  bool
	finalizeStatusCommit bool
	finalizeStatusOut    interleavedthinking.MemoResult
	finalizeStatusErr    error
	shapeExecutorInCall  aipapi.Call
	shapeExecutorInMemo  interleavedthinking.MemoResult
	shapeExecutorOutCall aipapi.Call
	shapeExecutorErr     error
	visibleRet           bool
	canContinueRet       bool
	diagHeader           string
	diagTurns            int
	commitCtx            context.Context
	commitRemaining      int
	commitErr            error
	flushVisibleRet      []aipapi.Event
}

func (f *fakeFeatureTurn) ShapeThinker(call aipapi.Call) (aipapi.Call, error) {
	f.shapeThinkerInCall = call
	return f.shapeThinkerOutCall, f.shapeThinkerErr
}

func (f *fakeFeatureTurn) ObserveThinkerEvent(ev aipapi.Event) ([]aipapi.Event, error) {
	f.observeEventIn = ev
	return f.observeEventOut, f.observeEventErr
}

func (f *fakeFeatureTurn) FinalizeThinker(ctx context.Context) (interleavedthinking.MemoResult, error) {
	f.finalizeCtx = ctx
	return f.finalizeOut, f.finalizeErr
}

func (f *fakeFeatureTurn) FinalizeThinkerStatus(ctx context.Context, interrupted bool, visibleCommitted bool) (interleavedthinking.MemoResult, error) {
	f.finalizeStatusCtx = ctx
	f.finalizeStatusInter = interrupted
	f.finalizeStatusCommit = visibleCommitted
	return f.finalizeStatusOut, f.finalizeStatusErr
}

func (f *fakeFeatureTurn) ShapeExecutor(ctx context.Context, call aipapi.Call, memo interleavedthinking.MemoResult) (aipapi.Call, error) {
	f.shapeExecutorInCall = call
	f.shapeExecutorInMemo = memo
	return f.shapeExecutorOutCall, f.shapeExecutorErr
}

func (f *fakeFeatureTurn) Visible() bool {
	return f.visibleRet
}

func (f *fakeFeatureTurn) CanContinue() bool {
	return f.canContinueRet
}

func (f *fakeFeatureTurn) ShapeDiagnostics() (string, int) {
	return f.diagHeader, f.diagTurns
}

func (f *fakeFeatureTurn) CommitExecutor(ctx context.Context) (int, error) {
	f.commitCtx = ctx
	return f.commitRemaining, f.commitErr
}

func (f *fakeFeatureTurn) FlushVisible() []aipapi.Event {
	return f.flushVisibleRet
}

func TestInterleavedProcessorAdapter_NilDisabled(t *testing.T) {
	t.Parallel()
	adapter := NewInterleavedProcessorAdapter(nil)
	if adapter != nil {
		t.Fatalf("expected nil adapter for nil processor, got %v", adapter)
	}
}

func TestInterleavedProcessorAdapter_AllFiveOperations(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), interleavedAdapterTestKey("test-key"), "test-val")
	fakeTurn := &fakeFeatureTurn{
		shapeThinkerOutCall:  aipapi.Call{ID: "shaped-thinker-call"},
		observeEventOut:      []aipapi.Event{{Kind: aipapi.EventReasoningDelta, Delta: "reasoning"}},
		finalizeOut:          interleavedthinking.MemoResult{Text: "memo text", Reference: "ref-1", Version: 2},
		shapeExecutorOutCall: aipapi.Call{ID: "shaped-exec-call"},
	}
	fakeProc := &fakeFeatureProcessor{turnToRet: fakeTurn}
	adapter := NewInterleavedProcessorAdapter(fakeProc)
	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}

	// 1. BeginTurn: verify all fields mapped explicitly
	in := runtime.InterleavedTurnInput{
		ALegID:              "aleg-123",
		Selector:            "openai:gpt-4o",
		Backend:             "openai",
		Model:               "gpt-4o",
		RequestID:           "req-456",
		StreamToClient:      "visible",
		SuppressVisibleMemo: true,
	}
	turnAdapter, err := adapter.BeginTurn(ctx, in)
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if fakeProc.lastCtx != ctx {
		t.Fatal("expected context passed unchanged to BeginTurn")
	}
	expectedIn := interleavedthinking.TurnInput{
		ALegID:              in.ALegID,
		Selector:            in.Selector,
		Backend:             in.Backend,
		Model:               in.Model,
		RequestID:           in.RequestID,
		StreamToClient:      in.StreamToClient,
		SuppressVisibleMemo: in.SuppressVisibleMemo,
	}
	if !reflect.DeepEqual(fakeProc.lastIn, expectedIn) {
		t.Fatalf("field mapping mismatch in BeginTurn: got %+v, want %+v", fakeProc.lastIn, expectedIn)
	}

	// 2. ShapeThinker
	thinkerCall := aipapi.Call{ID: "raw-thinker-call"}
	shapedThinker, err := turnAdapter.ShapeThinker(thinkerCall)
	if err != nil {
		t.Fatalf("ShapeThinker: %v", err)
	}
	if fakeTurn.shapeThinkerInCall.ID != "raw-thinker-call" {
		t.Fatalf("ShapeThinker did not pass call to inner turn")
	}
	if shapedThinker.ID != "shaped-thinker-call" {
		t.Fatalf("ShapeThinker did not return shaped call")
	}

	// 3. ObserveThinkerEvent + slice isolation
	inputEv := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "some text"}
	emitted, err := turnAdapter.ObserveThinkerEvent(inputEv)
	if err != nil {
		t.Fatalf("ObserveThinkerEvent: %v", err)
	}
	if fakeTurn.observeEventIn.Delta != "some text" {
		t.Fatalf("ObserveThinkerEvent did not pass event to inner turn")
	}
	if len(emitted) != 1 || emitted[0].Delta != "reasoning" {
		t.Fatalf("ObserveThinkerEvent returned wrong events: %+v", emitted)
	}
	// Verify defensive copy (slice isolation)
	emitted[0].Delta = "mutated"
	if fakeTurn.observeEventOut[0].Delta != "reasoning" {
		t.Fatal("mutating returned slice mutated the inner slice; slice isolation violated")
	}

	// 4. FinalizeThinker: verify MemoResult mapping
	memoRes, err := turnAdapter.FinalizeThinker(ctx)
	if err != nil {
		t.Fatalf("FinalizeThinker: %v", err)
	}
	if fakeTurn.finalizeCtx != ctx {
		t.Fatal("expected context passed unchanged to FinalizeThinker")
	}
	if memoRes.Text != "memo text" || memoRes.Reference != "ref-1" || memoRes.Version != 2 {
		t.Fatalf("memo mapping mismatch: got %+v", memoRes)
	}

	// 5. ShapeExecutor: verify InterleavedMemo mapped back to MemoResult
	execCall := aipapi.Call{ID: "raw-exec-call"}
	memoInput := runtime.InterleavedMemo{Text: "memo text", Reference: "ref-1", Version: 2}
	shapedExec, err := turnAdapter.ShapeExecutor(ctx, execCall, memoInput)
	if err != nil {
		t.Fatalf("ShapeExecutor: %v", err)
	}
	if fakeTurn.shapeExecutorInCall.ID != "raw-exec-call" {
		t.Fatal("ShapeExecutor did not pass call to inner turn")
	}
	expectedMemoResult := interleavedthinking.MemoResult{Text: "memo text", Reference: "ref-1", Version: 2}
	if !reflect.DeepEqual(fakeTurn.shapeExecutorInMemo, expectedMemoResult) {
		t.Fatalf("ShapeExecutor memo mapping mismatch: got %+v, want %+v", fakeTurn.shapeExecutorInMemo, expectedMemoResult)
	}
	if shapedExec.ID != "shaped-exec-call" {
		t.Fatalf("ShapeExecutor did not return shaped call")
	}
}

func TestInterleavedProcessorAdapter_ErrorPropagation(t *testing.T) {
	t.Parallel()
	customErr := errors.New("custom test error")
	fakeTurn := &fakeFeatureTurn{
		shapeThinkerErr:  customErr,
		observeEventErr:  customErr,
		finalizeErr:      customErr,
		shapeExecutorErr: customErr,
	}
	fakeProc := &fakeFeatureProcessor{
		turnToRet: fakeTurn,
	}
	adapter := NewInterleavedProcessorAdapter(fakeProc)

	// 1. BeginTurn error propagation
	fakeProc.errToRet = customErr
	_, err := adapter.BeginTurn(context.Background(), runtime.InterleavedTurnInput{})
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from BeginTurn, got %v", err)
	}
	fakeProc.errToRet = nil

	turnAdapter, err := adapter.BeginTurn(context.Background(), runtime.InterleavedTurnInput{})
	if err != nil {
		t.Fatalf("unexpected BeginTurn error: %v", err)
	}

	// 2. ShapeThinker error
	_, err = turnAdapter.ShapeThinker(aipapi.Call{})
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from ShapeThinker, got %v", err)
	}

	// 3. ObserveThinkerEvent error
	_, err = turnAdapter.ObserveThinkerEvent(aipapi.Event{})
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from ObserveThinkerEvent, got %v", err)
	}

	// 4. FinalizeThinker error
	_, err = turnAdapter.FinalizeThinker(context.Background())
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from FinalizeThinker, got %v", err)
	}

	// 5. ShapeExecutor error
	_, err = turnAdapter.ShapeExecutor(context.Background(), aipapi.Call{}, runtime.InterleavedMemo{})
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from ShapeExecutor, got %v", err)
	}

	// 6. FinalizeThinkerStatus error
	fakeTurn.finalizeStatusErr = customErr
	_, err = turnAdapter.FinalizeThinkerStatus(context.Background(), true, false)
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from FinalizeThinkerStatus, got %v", err)
	}

	// 7. CommitExecutor error
	fakeTurn.commitErr = customErr
	_, err = turnAdapter.CommitExecutor(context.Background())
	if !errors.Is(err, customErr) {
		t.Fatalf("expected customErr from CommitExecutor, got %v", err)
	}
}

func TestInterleavedProcessorAdapter_AdditionalOperations(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), interleavedAdapterTestKey("additional-op-key"), "additional-op-val")
	fakeTurn := &fakeFeatureTurn{
		visibleRet:      true,
		canContinueRet:  true,
		diagHeader:      "X-AIP-Interleaved",
		diagTurns:       4,
		commitRemaining: 3,
		flushVisibleRet: []aipapi.Event{
			{Kind: aipapi.EventReasoningDelta, Delta: "visible-chunk-1"},
			{Kind: aipapi.EventReasoningDelta, Delta: "visible-chunk-2"},
		},
		finalizeStatusOut: interleavedthinking.MemoResult{
			Text:       "status-memo",
			Reference:  "ref-status",
			Version:    5,
			HadContent: true,
		},
	}
	fakeProc := &fakeFeatureProcessor{
		turnToRet:     fakeTurn,
		visibleResult: true,
	}
	adapter := NewInterleavedProcessorAdapter(fakeProc)

	// 1. IsMemoVisibleToClient
	if !adapter.IsMemoVisibleToClient(ctx, "aleg-test-123") {
		t.Fatal("expected IsMemoVisibleToClient to return true")
	}
	if fakeProc.visibleALegID != "aleg-test-123" {
		t.Fatalf("expected alegID passed to IsMemoVisibleToClient, got %q", fakeProc.visibleALegID)
	}
	if fakeProc.lastCtx != ctx {
		t.Fatal("expected context passed to IsMemoVisibleToClient")
	}

	turnAdapter, err := adapter.BeginTurn(ctx, runtime.InterleavedTurnInput{ALegID: "aleg-test-123"})
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	// 2. FinalizeThinkerStatus
	statusMemo, err := turnAdapter.FinalizeThinkerStatus(ctx, true, false)
	if err != nil {
		t.Fatalf("FinalizeThinkerStatus: %v", err)
	}
	if fakeTurn.finalizeStatusCtx != ctx {
		t.Fatal("expected context passed to FinalizeThinkerStatus")
	}
	if !fakeTurn.finalizeStatusInter || fakeTurn.finalizeStatusCommit {
		t.Fatalf("flags mismatch in FinalizeThinkerStatus: inter=%v commit=%v", fakeTurn.finalizeStatusInter, fakeTurn.finalizeStatusCommit)
	}
	if statusMemo.Text != "status-memo" || statusMemo.Reference != "ref-status" || statusMemo.Version != 5 || !statusMemo.HadContent {
		t.Fatalf("status memo mismatch: %+v", statusMemo)
	}

	// 3. Visible & CanContinue
	if !turnAdapter.Visible() {
		t.Fatal("expected Visible() == true")
	}
	if !turnAdapter.CanContinue() {
		t.Fatal("expected CanContinue() == true")
	}

	// 4. ShapeDiagnostics
	header, turns := turnAdapter.ShapeDiagnostics()
	if header != "X-AIP-Interleaved" || turns != 4 {
		t.Fatalf("ShapeDiagnostics: got (%q, %d), want (%q, %d)", header, turns, "X-AIP-Interleaved", 4)
	}

	// 5. CommitExecutor
	rem, err := turnAdapter.CommitExecutor(ctx)
	if err != nil {
		t.Fatalf("CommitExecutor: %v", err)
	}
	if rem != 3 {
		t.Fatalf("CommitExecutor remaining turns: got %d, want 3", rem)
	}
	if fakeTurn.commitCtx != ctx {
		t.Fatal("expected context passed to CommitExecutor")
	}

	// 6. FlushVisible & slice isolation
	flushed := turnAdapter.FlushVisible()
	if len(flushed) != 2 || flushed[0].Delta != "visible-chunk-1" {
		t.Fatalf("FlushVisible returned unexpected events: %+v", flushed)
	}
	flushed[0].Delta = "mutated-chunk"
	if fakeTurn.flushVisibleRet[0].Delta != "visible-chunk-1" {
		t.Fatal("FlushVisible slice isolation violated")
	}
}

func TestInterleavedProcessorAdapter_ShapeExecutor_MemoPresentVsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fakeTurn := &fakeFeatureTurn{}
	fakeProc := &fakeFeatureProcessor{turnToRet: fakeTurn}
	adapter := NewInterleavedProcessorAdapter(fakeProc)

	turnAdapter, err := adapter.BeginTurn(ctx, runtime.InterleavedTurnInput{})
	if err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}

	// Case 1: Memo Present
	presentMemo := runtime.InterleavedMemo{
		Text:       "non-empty planning memo",
		Reference:  "memo-ref-100",
		Version:    3,
		HadContent: true,
	}
	call := aipapi.Call{ID: "exec-call-present"}
	if _, err := turnAdapter.ShapeExecutor(ctx, call, presentMemo); err != nil {
		t.Fatalf("ShapeExecutor memo present: %v", err)
	}
	wantMemoPresent := interleavedthinking.MemoResult{
		Text:       "non-empty planning memo",
		Reference:  "memo-ref-100",
		Version:    3,
		HadContent: true,
	}
	if !reflect.DeepEqual(fakeTurn.shapeExecutorInMemo, wantMemoPresent) {
		t.Fatalf("expected memo present mapped to %+v, got %+v", wantMemoPresent, fakeTurn.shapeExecutorInMemo)
	}

	// Case 2: Empty Memo
	emptyMemo := runtime.InterleavedMemo{}
	callEmpty := aipapi.Call{ID: "exec-call-empty"}
	if _, err := turnAdapter.ShapeExecutor(ctx, callEmpty, emptyMemo); err != nil {
		t.Fatalf("ShapeExecutor empty memo: %v", err)
	}
	wantMemoEmpty := interleavedthinking.MemoResult{}
	if !reflect.DeepEqual(fakeTurn.shapeExecutorInMemo, wantMemoEmpty) {
		t.Fatalf("expected empty memo mapped to %+v, got %+v", wantMemoEmpty, fakeTurn.shapeExecutorInMemo)
	}
}

func TestInterleavedProcessorAdapter_NilSafe(t *testing.T) {
	t.Parallel()
	// Processor adapter: NewInterleavedProcessorAdapter(nil) returns nil
	nilAdapter := NewInterleavedProcessorAdapter(nil)
	if nilAdapter != nil {
		t.Fatal("expected nil processor adapter for nil inner")
	}

	// Test nil processor adapter variations
	procAdapters := []runtime.InterleavedProcessor{
		(*interleavedProcessorAdapter)(nil),
		&interleavedProcessorAdapter{inner: nil},
	}
	for i, pa := range procAdapters {
		if pa.IsMemoVisibleToClient(context.Background(), "aleg-1") {
			t.Fatalf("[%d] expected IsMemoVisibleToClient == false", i)
		}
		turn, err := pa.BeginTurn(context.Background(), runtime.InterleavedTurnInput{})
		if err != nil || turn != nil {
			t.Fatalf("[%d] expected (nil, nil) from BeginTurn, got (%v, %v)", i, turn, err)
		}
	}

	// Test nil turn adapter variations (typed nil pointer, and non-nil struct with nil inner)
	turnAdapters := []runtime.InterleavedTurn{
		(*interleavedTurnAdapter)(nil),
		&interleavedTurnAdapter{inner: nil},
	}
	call := aipapi.Call{ID: "orig-call"}
	for i, ta := range turnAdapters {
		shaped, err := ta.ShapeThinker(call)
		if err != nil || shaped.ID != "orig-call" {
			t.Fatalf("[%d] nil turn ShapeThinker error or mismatch: err=%v shaped=%+v", i, err, shaped)
		}
		events, err := ta.ObserveThinkerEvent(aipapi.Event{Kind: aipapi.EventTextDelta})
		if err != nil || len(events) != 1 {
			t.Fatalf("[%d] nil turn ObserveThinkerEvent error or mismatch: err=%v events=%+v", i, err, events)
		}
		memo, err := ta.FinalizeThinker(context.Background())
		if err != nil || memo != (runtime.InterleavedMemo{}) {
			t.Fatalf("[%d] nil turn FinalizeThinker error: %v", i, err)
		}
		statusMemo, err := ta.FinalizeThinkerStatus(context.Background(), true, false)
		if err != nil || statusMemo != (runtime.InterleavedMemo{}) {
			t.Fatalf("[%d] nil turn FinalizeThinkerStatus error: %v", i, err)
		}
		shapedExec, err := ta.ShapeExecutor(context.Background(), call, runtime.InterleavedMemo{})
		if err != nil || shapedExec.ID != "orig-call" {
			t.Fatalf("[%d] nil turn ShapeExecutor error or mismatch: %v", i, err)
		}
		if ta.Visible() {
			t.Fatalf("[%d] nil turn Visible must be false", i)
		}
		if ta.CanContinue() {
			t.Fatalf("[%d] nil turn CanContinue must be false", i)
		}
		header, count := ta.ShapeDiagnostics()
		if header != "" || count != 0 {
			t.Fatalf("[%d] nil turn ShapeDiagnostics: got (%q, %d)", i, header, count)
		}
		rem, err := ta.CommitExecutor(context.Background())
		if err != nil || rem != -1 {
			t.Fatalf("[%d] nil turn CommitExecutor: got (%d, %v), want (-1, nil)", i, rem, err)
		}
		if ta.FlushVisible() != nil {
			t.Fatalf("[%d] nil turn FlushVisible must return nil", i)
		}
	}
}

func TestInterleavedProcessorAdapter_AlreadyCanceledContext(t *testing.T) {
	t.Parallel()

	fakeTurn := &fakeFeatureTurn{}
	fakeProc := &fakeFeatureProcessor{turnToRet: fakeTurn}
	adapter := NewInterleavedProcessorAdapter(fakeProc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := runtime.InterleavedTurnInput{
		ALegID: "aleg-cancel-test",
	}
	_, err := adapter.BeginTurn(ctx, in)
	if err == nil {
		t.Fatal("expected prompt ctx.Canceled-respecting error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestInterleavedProcessorAdapter_MemoSteeringPolicy(t *testing.T) {
	t.Parallel()

	cfg := interleavedthinking.Config{Enabled: true}
	store := interleavedthinking.NewMemoStore(cfg.EffectiveMaxMemoBytes())
	proc, err := interleavedthinking.NewProcessor(cfg, store)
	if err != nil {
		t.Fatalf("NewProcessor: %v", err)
	}
	adapter := NewInterleavedProcessorAdapter(proc)
	var _ runtime.InterleavedProcessor = adapter //nolint:staticcheck // QF1011: intentional compile-time interface assertion

	req := adapter.MemoSteeringPutRequest("  memo body ")
	if req.OverlayID != adapter.MemoSteeringOverlayID() {
		t.Fatalf("PutRequest.OverlayID = %q, want adapter identity %q", req.OverlayID, adapter.MemoSteeringOverlayID())
	}
	if req.OverlayID != steering.OverlayID(interleavedthinking.MemoOverlayID) {
		t.Fatalf("OverlayID = %q, want feature identity %q", req.OverlayID, interleavedthinking.MemoOverlayID)
	}
	if req.Message.Role != aipapi.RoleUser {
		t.Fatalf("Role = %q, want user", req.Message.Role)
	}
	if !strings.Contains(req.Message.Text, interleavedthinking.SessionSteeringGuidanceHeader) {
		t.Fatalf("Text missing feature header: %q", req.Message.Text)
	}
	if req.Placement != steering.AfterIngressTail {
		t.Fatalf("Placement = %q, want after_ingress_tail", req.Placement)
	}
	if req.AnchorMissingPolicy != steering.StablePrefixFallback {
		t.Fatalf("AnchorMissingPolicy = %q, want stable_prefix_fallback", req.AnchorMissingPolicy)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("PutRequest.Validate: %v", err)
	}
	if !adapter.IsMemoSteeringOverlay(interleavedthinking.MemoOverlayID) {
		t.Fatal("IsMemoSteeringOverlay(feature ID) = false, want true")
	}
	if adapter.IsMemoSteeringOverlay("other-overlay") {
		t.Fatal("IsMemoSteeringOverlay(other) = true, want false")
	}
}
