package runtime

// Spec: tool-name-correction Task 2.4 (requirements 2.2, 2.3, 2.4, 2.5, 3.7):
// the withholding attempt's backend finish runs the hidden correction
// continuation. On publication the client receives only B2's output on one
// response and the correction message is gone once the response ends; when the
// follow-up cannot be published the withheld output and the finish are
// replayed in their original order, each recorded exactly once. With Agent
// Loop Guard enabled, B2's finish is evaluated with B1's lineage, so the guard
// never sees an invalid progress state.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/causepolicy"
	algprogress "github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/progress"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

const (
	tccALegID      = "a-characterization"
	tccBLegID      = "b-characterization"
	tccB2BLegID    = "b2-tool-name-correction"
	tccInstruction = "call one of the available tools, the previous name does not exist"
	// tccB1LaterText is output B1 produces after the invalid call: it must be
	// held with the call and never reach the client when the correction
	// publishes.
	tccB1LaterText = "and then it kept talking"
)

// tccIngress is a realistic tool-bearing client request: the correction rails
// need its history, and the projection needs a forwardable terminal user
// message as the correction message's anchor.
func tccIngress() aipapi.Call {
	return aipapi.Call{
		ID: "request-tcc",
		Items: []aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "fix the failing test"}},
		}},
	}
}

// tccSteerFin always asks for a correction. The unknown name and the offered
// catalog are the feature plugin's business; core only honors the outcome.
func tccSteerFin() toolcall.Finalizer {
	return &steerFin{id: "tcc-steer", order: 10, result: toolcall.Result{
		Action:     toolcall.ActionSteer,
		Steer:      &toolcall.SteerRequest{Instruction: tccInstruction, TurnBudget: 1, AllowAfterReasoning: true},
		ReasonCode: toolcall.ReasonUnknownToolName,
	}}
}

// tccB1Script is the invalid attempt: reasoning, the unknown call, later
// output, and the backend finish. The later output must be held with the call
// and must never reach the client when the correction publishes.
func tccB1Script() []aipapi.Event {
	b1 := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "the requested tool does not exist"},
	}
	b1 = append(b1, twCallEvents("c1", correctionName, `{"q":"x"}`, 1)...)
	b1 = append(b1, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: tccB1LaterText})
	return append(b1, aipapi.Event{Kind: aipapi.EventResponseFinished})
}

// tccB2Script is the corrected attempt. tccB2TextScript additionally releases
// text, which is what makes the terminal-decision verifier eligibility gate
// reachable for the Agent Loop Guard lineage cells.
func tccB2Script() []aipapi.Event { return tccB2ScriptWith(nil) }

func tccB2TextScript() []aipapi.Event {
	return tccB2ScriptWith([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "the failing test now passes"}})
}

func tccB2ScriptWith(prefix []aipapi.Event) []aipapi.Event {
	b2 := []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}}
	b2 = append(b2, prefix...)
	b2 = append(b2, twCallEvents("c2", steerToolName, `{"q":"y"}`, 1)...)
	return append(b2, aipapi.Event{Kind: aipapi.EventResponseFinished})
}

// tccWriter observes the SDK-level steering mutations the continuation
// transaction performs over a real conversation-view store.
type tccWriter struct {
	inner       steering.Writer
	puts        []steering.PutRequest
	deactivated []steering.OverlayID
}

func (w *tccWriter) Put(ctx context.Context, req steering.PutRequest) (steering.State, error) {
	w.puts = append(w.puts, req)
	return w.inner.Put(ctx, req)
}

func (w *tccWriter) Deactivate(ctx context.Context, id steering.OverlayID) (steering.State, error) {
	w.deactivated = append(w.deactivated, id)
	return w.inner.Deactivate(ctx, id)
}

func (w *tccWriter) deactivatedOverlay(id steering.OverlayID) bool {
	return slices.Contains(w.deactivated, id)
}

// tccProvider records what the terminal-decision provider is asked at B2's
// finish while delegating every decision to the real provider it wraps.
type tccProvider struct {
	inner     terminaldecision.Provider
	inputs    []terminaldecision.Input
	decisions []terminaldecision.Decision
}

func (p *tccProvider) ID() string { return p.inner.ID() }

func (p *tccProvider) Decide(ctx context.Context, in terminaldecision.Input) (terminaldecision.Decision, error) {
	decision, err := p.inner.Decide(ctx, in)
	p.inputs = append(p.inputs, in)
	p.decisions = append(p.decisions, decision)
	return decision, err
}

func (p *tccProvider) lastInput() terminaldecision.Input { return p.inputs[len(p.inputs)-1] }
func (p *tccProvider) lastDecision() terminaldecision.Decision {
	return p.decisions[len(p.decisions)-1]
}

// tccHarness layers a real scripted two-attempt backend over the correction
// withhold harness: B1 runs the withheld script, the opener publishes B2 with
// its own script, and both attempts are assembled by the real receive loop.
type tccHarness struct {
	*twHarness
	store        *conversationview.ReferenceStore
	writer       *tccWriter
	traffic      *tccTrafficObserver
	b2Events     []aipapi.Event
	b2Stream     *continuationCountingManagedStream
	disposeProbe bool
	openErr      error
	opens        atomic.Int32
	openReq      replacementOpenRequest
}

func newTCCHarness(t *testing.T, b1 []aipapi.Event) *tccHarness {
	t.Helper()
	return newTCCHarnessWithInner(t, aipapi.NewFixedEventStream(b1))
}

// newTCCHarnessWithInner takes the invalid attempt's stream directly, so a cell
// can drive the receive loop up to a chosen point.
func newTCCHarnessWithInner(t *testing.T, inner aipapi.ManagedEventStream) *tccHarness {
	t.Helper()
	return newTCCHarnessWithFinalizers(t, inner, tccSteerFin())
}

// newTCCHarnessWithFinalizers takes the invalid attempt's stream and finalizer
// chain directly, so a cell can shape the assembly boundary the finish meets.
func newTCCHarnessWithFinalizers(t *testing.T, inner aipapi.ManagedEventStream, finalizers ...toolcall.Finalizer) *tccHarness {
	t.Helper()
	h := newTWHarness(t, inner, finalizers...)
	store := conversationview.NewReferenceStore()
	if err := store.CreateALeg(context.Background(), tccALegID); err != nil {
		t.Fatalf("create conversation A-leg: %v", err)
	}
	writer := &tccWriter{}
	h.stream.terminal.steeringWriterFactory = func(ctx context.Context, aLegID string, resolver SteeringWriterResolver) (steering.Writer, error) {
		inner, err := sdkadapter.NewWriterWithObserver(store, aLegID, sdkadapter.TrajectoryResolver(resolver), nil)
		if err != nil {
			return nil, err
		}
		writer.inner = inner
		return writer, nil
	}
	h.stream.terminal.conversationReader = store
	// Production assembly binds the request-lifetime commitment truth into the
	// pipeline; the ALG eligibility gate reads it, so the harness binds it too.
	h.stream.responsePipeline.bindTerminalSnapshot(func() (bool, bool) {
		return h.stream.terminal.committed(), h.stream.terminal.accountingFinalized()
	})
	h.stream.facts.baseline = tccIngress()
	h.stream.facts.ingressCall = tccIngress()
	traffic := &tccTrafficObserver{}
	h.stream.responsePipeline.runtimeSnapshot = tccTrafficSnapshot(traffic)
	// Production builds the decision-evidence emitter from the executor and
	// returns nil for a no-observer snapshot, where Emit is a no-op; the
	// harness supplies the same nil builder.
	h.stream.responsePipeline.policyEvidenceEmitter = func(*extensions.RequestRuntimeSnapshot) *extensions.EvidenceEmitter { return nil }
	harness := &tccHarness{twHarness: h, store: store, writer: writer, traffic: traffic}
	h.stream.recovery.opener = func(_ context.Context, req replacementOpenRequest) (replacementOpenResult, error) {
		harness.opens.Add(1)
		harness.openReq = req
		return harness.openB2(req), harness.openErr
	}
	return harness
}

// openB2 builds the prepared continuation attempt the transaction may publish.
// The B2 assembler carries a pass finalizer, so the corrected attempt runs the
// normal finalizer chain.
func (h *tccHarness) openB2(req replacementOpenRequest) replacementOpenResult {
	b1 := h.stream.attempt.require()
	bleg := b2bua.BLegRecord{ALegID: req.pinnedFacts.aLegID, BLegID: tccB2BLegID, Seq: b1.bleg.Seq + 1}
	session := testAttemptSlot(bleg, b1.cand, authorityLifecycle{}).current
	session.toolFinal = newToolCallAssembler(
		[]toolcall.Finalizer{&steerFin{id: "tcc-b2-pass", order: 10, result: steerPassResult()}}, 64*1024, steerCatalog())
	scripted := aipapi.NewFixedEventStream(h.b2Events)
	if h.disposeProbe {
		h.b2Stream = &continuationCountingManagedStream{}
		session.storeInner(h.b2Stream)
	} else {
		session.storeInner(scripted)
	}
	ready := newReadyAttempt(session, pendingSelectionEffects{})
	ready.state = readyStatePrepared
	return replacementOpenResult{opened: true, ready: ready, bleg: bleg, cand: session.cand}
}

// installProvider wires a recording terminal-decision provider for the Agent
// Loop Guard lineage cells.
func (h *tccHarness) installProvider(provider terminaldecision.Provider) *tccProvider {
	recorder := &tccProvider{inner: provider}
	h.stream.terminal.terminalDecisionProvider = recorder
	h.stream.terminal.terminalDecisionProviderID = recorder.ID()
	h.stream.terminal.terminalDecisionProviderHasID = true
	return recorder
}

// tccTrafficObserver records the backend-side traffic observations the
// pipeline emits, so a cell can prove a withheld attempt's finish was observed
// by preparation exactly once - and not observed again by the drain replay,
// which is client-side only.
type tccTrafficObserver struct {
	mu           sync.Mutex
	observations []sdktraffic.Observation
}

func (o *tccTrafficObserver) OnObservation(_ context.Context, ev sdktraffic.Observation) error {
	o.mu.Lock()
	o.observations = append(o.observations, ev)
	o.mu.Unlock()
	return nil
}

// countFinish reports how many backend-way observations of one attempt's
// response_finished the BTP leg received.
func (o *tccTrafficObserver) countFinish(bLegID string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, obs := range o.observations {
		if obs.Leg == sdktraffic.LegBTP && obs.BLegID == bLegID && bytes.Contains(obs.Body, []byte(`"response_finished"`)) {
			count++
		}
	}
	return count
}

// tccTrafficSnapshot builds the request snapshot that carries the counting
// observer, exactly as production chains a feature-plane traffic observer into
// the snapshot's traffic port bundle.
func tccTrafficSnapshot(observer sdktraffic.Observer) *extensions.RequestRuntimeSnapshot {
	return extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		TrafficObserver: sdktraffic.ChainObservers(observer),
	})
}

// tccCountingStream counts the canonical lifecycle frames a collector drains,
// so the collected cell proves one response start and one finish exactly as the
// streaming cell does.
type tccCountingStream struct {
	inner    aipapi.EventStream
	started  atomic.Int32
	finished atomic.Int32
}

func (s *tccCountingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.inner.Recv(ctx)
	switch ev.Kind {
	case aipapi.EventResponseStarted:
		s.started.Add(1)
	case aipapi.EventResponseFinished:
		s.finished.Add(1)
	}
	return ev, err
}

func (s *tccCountingStream) Close() error { return s.inner.Close() }

// tccDrain reads like a client until the response ends.
func tccDrain(t *testing.T, s *retryRecvStream) ([]aipapi.Event, error) {
	t.Helper()
	var got []aipapi.Event
	for range 64 {
		ev, err := s.Recv(context.Background())
		if err != nil {
			return got, err
		}
		got = append(got, ev)
	}
	t.Fatal("receive loop did not terminate")
	return nil, nil
}

func tccCountKind(events []aipapi.Event, kind aipapi.EventKind) int {
	count := 0
	for _, ev := range events {
		if ev.Kind == kind {
			count++
		}
	}
	return count
}

func tccCountRecorded(kinds []aipapi.EventKind, kind aipapi.EventKind) int {
	count := 0
	for _, got := range kinds {
		if got == kind {
			count++
		}
	}
	return count
}

// tccDeliveredToolNames reports the tool call names that reached the client.
func tccDeliveredToolNames(events []aipapi.Event) []string {
	var names []string
	for _, ev := range events {
		if ev.Kind == aipapi.EventToolCallStarted {
			names = append(names, ev.ToolName)
		}
	}
	return names
}

// tccCallNames reports the tool call names the follow-up request carries.
func tccCallNames(call aipapi.Call) []string {
	var names []string
	for _, item := range aipapi.NormalizedItems(call) {
		if item.Kind == aipapi.ItemKindToolCall && item.ToolCall != nil {
			names = append(names, item.ToolCall.Name)
		}
	}
	return names
}

// tccCarriesText reports whether any message item of the call carries text.
func tccCarriesText(call aipapi.Call, text string) bool {
	for _, item := range aipapi.NormalizedItems(call) {
		if item.Kind != aipapi.ItemKindMessage {
			continue
		}
		for _, part := range item.Content {
			if part.Kind == aipapi.ContentPartText && part.Text == text {
				return true
			}
		}
	}
	return false
}

// tccEventPayload is the client-visible part of one delivered event.
func tccEventPayload(events []aipapi.Event) string {
	var b strings.Builder
	for _, ev := range events {
		b.WriteString(ev.ToolName)
		b.WriteString(ev.Delta)
	}
	return b.String()
}

// TestToolNameCorrectionContinuation_PublishesCorrectedAttempt is the core
// integration cell: the invalid attempt's call is withheld, B1's backend finish
// publishes B2, and the client receives one response whose only tool call is
// B2's. The follow-up request carries the correction message and not the
// invalid call, and the correction overlay is gone once the response ends.
func TestToolNameCorrectionContinuation_PublishesCorrectedAttempt(t *testing.T) {
	t.Parallel()

	t.Run("streaming", func(t *testing.T) {
		t.Parallel()
		h := newTCCHarness(t, tccB1Script())
		h.b2Events = tccB2Script()

		events, err := tccDrain(t, h.stream)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("terminal recv error = %v, want io.EOF", err)
		}
		if got := tccCountKind(events, aipapi.EventResponseStarted); got != 1 {
			t.Fatalf("delivered response starts = %d, want one", got)
		}
		if got := tccCountKind(events, aipapi.EventResponseFinished); got != 1 {
			t.Fatalf("delivered finishes = %d, want one", got)
		}
		if names := tccDeliveredToolNames(events); !slices.Equal(names, []string{steerToolName}) {
			t.Fatalf("delivered tool calls = %v, want only the corrected call", names)
		}
		if payload := tccEventPayload(events); strings.Contains(payload, correctionName) || strings.Contains(payload, tccB1LaterText) {
			t.Fatalf("delivered output carries the withheld attempt's output: %q", payload)
		}
		if recorded := h.recorder.recordedKinds(); tccCountRecorded(recorded, aipapi.EventToolCallStarted) != 1 ||
			tccCountRecorded(recorded, aipapi.EventResponseFinished) != 1 {
			t.Fatalf("recorded kinds = %v, want exactly one corrected call and one finish", recorded)
		}
		if h.opens.Load() != 1 {
			t.Fatalf("follow-up opens = %d, want exactly one", h.opens.Load())
		}
		if got := h.traffic.countFinish(tccBLegID); got != 1 {
			t.Fatalf("B1 finish BTP observations = %d, want exactly one: preparation observes the withheld finish before the intercept", got)
		}
		if got := h.traffic.countFinish(tccB2BLegID); got != 1 {
			t.Fatalf("B2 finish BTP observations = %d, want exactly one", got)
		}
		if puts := h.writer.puts; len(puts) != 1 || puts[0].OverlayID != toolNameCorrectionOverlayID ||
			puts[0].Message.Text != tccInstruction || puts[0].Placement != steering.AfterIngressTail {
			t.Fatalf("correction overlay put = %+v, want the dedicated overlay carrying the accepted instruction", puts)
		}
		if names := tccCallNames(h.openReq.pinnedFacts.baseline); len(names) != 0 {
			t.Fatalf("follow-up request carries tool calls %v, want none: the invalid call must never be replayed", names)
		}
		if !tccCarriesText(h.openReq.pinnedFacts.baseline, tccInstruction) {
			t.Fatal("follow-up request does not carry the correction message")
		}
		if h.attempt.isWithholding() {
			t.Fatal("B1 is still withholding after publication")
		}
		assertTCCOverlayGone(t, h)
	})

	t.Run("collected", func(t *testing.T) {
		t.Parallel()
		h := newTCCHarness(t, tccB1Script())
		h.b2Events = tccB2Script()
		counting := &tccCountingStream{inner: h.stream}

		collected, err := aipapi.CollectUnbounded(context.Background(), counting)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if counting.started.Load() != 1 || counting.finished.Load() != 1 {
			t.Fatalf("collected lifecycle = %d starts/%d finishes, want one each", counting.started.Load(), counting.finished.Load())
		}
		if !collected.FinishReceived {
			t.Fatal("collected response never received its finish")
		}
		if len(collected.ToolCallOrder) != 1 || collected.ToolNames[collected.ToolCallOrder[0]] != steerToolName {
			t.Fatalf("collected tool calls = %v/%v, want only the corrected call", collected.ToolCallOrder, collected.ToolNames)
		}
		if payload := collected.Text.String() + collected.Reasoning.String(); strings.Contains(payload, correctionName) || strings.Contains(payload, tccB1LaterText) {
			t.Fatalf("collected output carries the withheld attempt's output: %q", payload)
		}
		assertTCCOverlayGone(t, h)
	})
}

func assertTCCOverlayGone(t *testing.T, h *tccHarness) {
	t.Helper()
	if !h.writer.deactivatedOverlay(toolNameCorrectionOverlayID) {
		t.Fatal("the correction overlay was never deactivated")
	}
	snap, err := h.store.Snapshot(context.Background(), tccALegID)
	if err != nil {
		t.Fatalf("conversation snapshot: %v", err)
	}
	for _, overlay := range snap.Steering {
		if overlay.Active {
			t.Fatalf("active overlay after the turn: %+v, want the correction message removed", overlay)
		}
	}
}

// TestToolNameCorrectionContinuation_AdmissionFailureReplaysOriginalOutput
// forces the follow-up admission to fail and proves the fail-open replay: the
// client receives B1's call and later output followed by B1's finish, in order,
// each recorded once, with the admission resources disposed.
func TestToolNameCorrectionContinuation_AdmissionFailureReplaysOriginalOutput(t *testing.T) {
	t.Parallel()
	h := newTCCHarness(t, tccB1Script())
	h.disposeProbe = true
	h.openErr = errors.New("backend admission denied")

	events, err := tccDrain(t, h.stream)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("terminal recv error = %v, want io.EOF", err)
	}
	want := tccB1Script()
	if !slices.EqualFunc(events, want, func(a, b aipapi.Event) bool { return a.Kind == b.Kind && a.Delta == b.Delta }) {
		t.Fatalf("delivered %+v, want the original attempt's output in order", events)
	}
	recorded := h.recorder.recordedKinds()
	if len(recorded) != len(want) {
		t.Fatalf("recorded %d events, want %d: every replayed event is recorded exactly once", len(recorded), len(want))
	}
	if got := tccCountRecorded(recorded, aipapi.EventResponseFinished); got != 1 {
		t.Fatalf("recorded finishes = %d, want one", got)
	}
	if got := h.traffic.countFinish(tccBLegID); got != 1 {
		t.Fatalf("B1 finish BTP observations = %d, want exactly one even on the fail-open path: the drain replay is client-side only", got)
	}
	if h.b2Stream == nil || h.b2Stream.cancelCalls.Load() != 1 || h.b2Stream.closeCalls.Load() != 1 {
		t.Fatalf("admission resources = %+v, want one Cancel and one Close", h.b2Stream)
	}
	record := h.logs.String()
	if !strings.Contains(record, "outcome=delivered") || !strings.Contains(record, "reason=followup_failed") || strings.Contains(record, correctionName) {
		t.Fatalf("decision record = %q, want one bounded followup_failed delivery", record)
	}
	if !h.writer.deactivatedOverlay(toolNameCorrectionOverlayID) {
		t.Fatal("the failed follow-up left its correction overlay active")
	}
}

// TestToolNameCorrectionContinuation_CanceledFinishKeepsClaimPath proves the
// cancellation rule the correction failure policy must preserve: when the
// request is already canceled at the finish intercept, the transaction keeps
// its existing claim path, the intercept returns that error as today, and the
// withheld output is discarded.
func TestToolNameCorrectionContinuation_CanceledFinishKeepsClaimPath(t *testing.T) {
	t.Parallel()
	b1 := tccB1Script()
	inner := newTWScriptedStream(b1[:len(b1)-1]...)
	h := newTCCHarnessWithInner(t, inner)
	h.script = inner
	for range 3 {
		if _, err := h.stream.Recv(context.Background()); err != nil {
			t.Fatalf("recv preamble: %v", err)
		}
	}
	recvCtx, cancelRecv := context.WithCancel(context.Background())
	defer cancelRecv()
	blocked := make(chan twRecvResult, 1)
	go func() {
		ev, err := h.stream.Recv(recvCtx)
		blocked <- twRecvResult{ev: ev, err: err}
	}()
	h.awaitBackendReads(t, 1)
	if !h.attempt.isWithholding() {
		t.Fatal("the steered call was not withheld")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	finish := aipapi.Event{Kind: aipapi.EventResponseFinished}
	if err := runToolCorrection(canceled, h.stream.terminal, h.stream, h.attempt, finish); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled finish intercept = %v, want context.Canceled", err)
	}
	if h.attempt.isWithholding() || len(h.attempt.withheld.events) != 0 {
		t.Fatal("a canceled correction must discard the withheld output")
	}
	if state := h.stream.terminal.requestTerminal().Owner().State(); state == sdkterminal.StateOpen {
		t.Fatal("a canceled correction left the request terminal open")
	}

	cancelRecv()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("the blocked recv did not return after cancellation")
	}
}

// TestToolNameCorrectionContinuation_CompletionFailureDeliversCallFirst proves
// the assembly boundary's own completion failure ends the attempt exactly as it
// would without the feature: the client still receives the withheld call and the
// output held with it before that failure surfaces.
func TestToolNameCorrectionContinuation_CompletionFailureDeliversCallFirst(t *testing.T) {
	t.Parallel()
	script := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "the requested tool does not exist"},
	}
	script = append(script, twCallEvents("c1", correctionName, `{"q":"x"}`, 1)...)
	// A second call starts and never finishes, so the assembly boundary carries
	// unresolved mandatory work when the stream ends.
	script = append(script, twCallEvents("c2", steerToolName, `{"q":"z"}`, 2)[0])
	script = append(script, aipapi.Event{Kind: aipapi.EventResponseFinished})
	h := newTCCHarnessWithFinalizers(t, aipapi.NewFixedEventStream(script),
		tccSteerFin(), &steerDeclarerFin{order: 20, result: steerPassResult()})

	events, err := tccDrain(t, h.stream)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("terminal recv error = %v, want the assembly boundary's completion failure", err)
	}
	want := script[:len(script)-2]
	if !slices.EqualFunc(events, want, func(a, b aipapi.Event) bool { return a.Kind == b.Kind && a.Delta == b.Delta }) {
		t.Fatalf("delivered %+v, want the withheld output before the failure", events)
	}
	if h.attempt.isWithholding() {
		t.Fatal("the attempt is still withholding after its completion failure")
	}
}

// TestToolNameCorrectionContinuation_GuardLineage proves requirement 2.4's
// lineage rule through the real Agent Loop Guard provider: B2's finish is
// evaluated with B1's lineage, so the guard's progress-state decode sees the
// first-attempt classification instead of an invalid progress state, both for a
// first continuation and after an earlier guard continuation.
func TestToolNameCorrectionContinuation_GuardLineage(t *testing.T) {
	t.Parallel()

	t.Run("first continuation", func(t *testing.T) {
		t.Parallel()
		h := newTCCHarness(t, tccB1Script())
		h.b2Events = tccB2TextScript()
		recorder := h.installProvider(agentloopguard.NewProvider())

		if _, err := tccDrain(t, h.stream); !errors.Is(err, io.EOF) {
			t.Fatalf("terminal recv error = %v, want io.EOF", err)
		}
		assertGuardEvaluatedCorrectedAttempt(t, recorder)
		in := recorder.lastInput()
		wantAttempt := uint8(h.attempt.bleg.Seq)
		if in.Evidence.Lineage.ProgressRef != tccBLegID || in.Evidence.Lineage.Attempt != wantAttempt || in.Continuation.Attempt != wantAttempt {
			t.Fatalf("B2 finish lineage = %+v, want B1's own leg and attempt %d", in.Evidence.Lineage, wantAttempt)
		}
		if reason := recorder.lastDecision().ReasonCode; reason == "invalid_progress_state" {
			t.Fatalf("B2's finish was refused as an invalid progress state: lineage = %+v", in.Evidence.Lineage)
		}
	})

	t.Run("after an earlier guard continuation", func(t *testing.T) {
		t.Parallel()
		h := newTCCHarness(t, tccB1Script())
		h.b2Events = tccB2TextScript()
		token, err := algprogress.EncodeState(algprogress.State{LastFingerprint: "fingerprint-1"})
		if err != nil {
			t.Fatalf("encode progress state: %v", err)
		}
		h.stream.facts.continuationIntent = continuationIntentFacts{
			trajectoryRef: "trajectory-1",
			controlRef:    token,
			attempt:       2,
			set:           true,
		}
		recorder := h.installProvider(agentloopguard.NewProvider())

		if _, err := tccDrain(t, h.stream); !errors.Is(err, io.EOF) {
			t.Fatalf("terminal recv error = %v, want io.EOF", err)
		}
		assertGuardEvaluatedCorrectedAttempt(t, recorder)
		in := recorder.lastInput()
		if in.Evidence.Lineage.ProgressRef != token || in.Evidence.Lineage.Attempt != 2 || in.Continuation.Attempt != 2 {
			t.Fatalf("B2 finish lineage = %+v, want the earlier continuation's state unchanged", in.Evidence.Lineage)
		}
		if reason := recorder.lastDecision().ReasonCode; reason == "invalid_progress_state" {
			t.Fatalf("B2's finish was refused as an invalid progress state: lineage = %+v", in.Evidence.Lineage)
		}
	})
}

// assertGuardEvaluatedCorrectedAttempt proves the guard was asked about the
// corrected attempt's finish, and that the input it received is fully eligible
// for the verifier: the progress-state decode that produces
// invalid_progress_state is reached only after that eligibility gate, so the
// lineage assertions that follow really are what the decode consumed.
func assertGuardEvaluatedCorrectedAttempt(t *testing.T, recorder *tccProvider) {
	t.Helper()
	if len(recorder.inputs) == 0 {
		t.Fatal("B2's finish never reached the terminal-decision provider")
	}
	for i, in := range recorder.inputs {
		if in.Request.BLegID != tccB2BLegID {
			t.Fatalf("terminal decision %d evaluated B-leg %q, want only the corrected attempt", i, in.Request.BLegID)
		}
		if result := causepolicy.Evaluate(in); result.Eligibility != causepolicy.EligibilityVerifier {
			t.Fatalf("terminal decision %d is not verifier-eligible, so the progress decode is unreachable: %+v", i, result)
		}
	}
}
