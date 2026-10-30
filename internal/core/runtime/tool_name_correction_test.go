package runtime

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// Spec: tool-name-correction Task 2.2 (requirements 3.1-3.6, 5.1, 5.2). Rails
// decide in fixed order; acceptance alone consumes turn budget and marks the
// attempt; every decision emits one bounded record without the raw tool name.
// The gate and per-event wiring under test are production code.

const (
	correctionName    = "invented_tool"
	correctionTraceID = "trace-1"
	correctionBLegID  = "b-leg-1"

	// correctionHash is the first 8 bytes, hex, of SHA-256(correctionName).
	correctionHash = "5f9c5dc867258c3d"
)

func correctionFacts() recvTurnFacts {
	return recvTurnFacts{traceID: correctionTraceID, ingressCall: aipapi.Call{ID: "call-1"}}
}

func correctionReq() toolcall.SteerRequest {
	return toolcall.SteerRequest{Instruction: "call one of the available tools", TurnBudget: 1}
}

func correctionIngress() aipapi.Call {
	return aipapi.Call{Items: []aipapi.Item{{Kind: aipapi.ItemKindToolCall, ToolCall: &aipapi.ToolCallItem{Name: correctionName}}}}
}

func steerableTerminal() *turnTerminal {
	t := newTurnTerminal()
	t.supportsContinuation = true
	t.steeringWriterFactory = func(context.Context, string, SteeringWriterResolver) (steering.Writer, error) { return nil, nil }
	t.conversationReader = &staticReader{}
	return t
}

func correctionGatesSnapshot() *extensions.RequestRuntimeSnapshot {
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneCompletionGates, "t", []completion.Gate{gatedLeakPassGate{}}); err != nil {
		panic(err)
	}
	return extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{FeaturePlanes: cs.Freeze()})
}

type correctionHarness struct {
	p        *responsePipeline
	terminal *turnTerminal
	attempt  *attemptSession
	facts    recvTurnFacts
	logs     *strings.Builder
}

func newCorrectionHarness() *correctionHarness {
	h := &correctionHarness{logs: &strings.Builder{}, terminal: steerableTerminal(), attempt: &attemptSession{}, facts: correctionFacts()}
	h.p = newResponsePipeline()
	h.p.log = slog.New(slog.NewTextHandler(h.logs, nil))
	h.attempt.bleg.BLegID = correctionBLegID
	return h
}

func TestToolNameCorrection_RailsDeclineInOrder(t *testing.T) {
	t.Parallel()
	visible := func(h *correctionHarness) {
		h.attempt.observeProducedOutput(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "text"})
	}
	reasoning := func(h *correctionHarness) {
		h.attempt.observeProducedOutput(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: "reasoning"})
	}
	history := func(h *correctionHarness) { h.facts.ingressCall = correctionIngress() }

	cases := []struct {
		name           string
		setup          func(h *correctionHarness)
		used           int64
		allowReasoning bool
		wantAccept     bool
		wantReason     string
	}{
		{name: "accept", wantAccept: true, wantReason: "unknown_tool_name"},
		{name: "budget spent", setup: func(h *correctionHarness) { h.terminal.acceptToolCorrection() }, used: 1, wantReason: "budget_spent"},
		{
			name:  "budget spent wins",
			setup: func(h *correctionHarness) { h.terminal.acceptToolCorrection(); visible(h); history(h) },
			used:  1, wantReason: "budget_spent",
		},
		{name: "visible output", setup: visible, wantReason: "visible_output"},
		{name: "visible output wins over reasoning", setup: func(h *correctionHarness) { reasoning(h); visible(h) }, wantReason: "visible_output"},
		{name: "reasoning output", setup: reasoning, wantReason: "reasoning_output"},
		{name: "reasoning allowed", setup: reasoning, allowReasoning: true, wantAccept: true, wantReason: "unknown_tool_name"},
		{name: "history repeat", setup: history, wantReason: "history_repeat"},
		{name: "history repeat wins over continuation", setup: func(h *correctionHarness) { history(h); h.terminal.supportsContinuation = false }, wantReason: "history_repeat"},
		{name: "unsupported protocol", setup: func(h *correctionHarness) { h.terminal.supportsContinuation = false }, wantReason: "continuation_unsupported"},
		{name: "interleaved thinker", setup: func(h *correctionHarness) { h.terminal.setInterleavedThinker() }, wantReason: "continuation_unsupported"},
		{name: "completion gates present", setup: func(h *correctionHarness) { h.p.runtimeSnapshot = correctionGatesSnapshot() }, wantReason: "continuation_unsupported"},
		{name: "steering writer unavailable", setup: func(h *correctionHarness) { h.terminal.steeringWriterFactory = nil }, wantReason: "steering_unavailable"},
		{name: "conversation reader unavailable", setup: func(h *correctionHarness) { h.terminal.conversationReader = nil }, wantReason: "steering_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newCorrectionHarness()
			if tc.setup != nil {
				tc.setup(h)
			}
			req := correctionReq()
			req.AllowAfterReasoning = tc.allowReasoning
			accepted := h.p.toolNameCorrectionGate(context.Background(), h.facts, h.terminal, h.attempt, "c1")(correctionName, req)

			wantUsed := tc.used
			if tc.wantAccept {
				wantUsed++
			}
			if accepted != tc.wantAccept || h.attempt.isWithholding() != tc.wantAccept || h.terminal.toolCorrectionsAccepted() != wantUsed {
				t.Fatalf("accepted=%v marked=%v used=%d, want %v/%v/%d",
					accepted, h.attempt.isWithholding(), h.terminal.toolCorrectionsAccepted(), tc.wantAccept, tc.wantAccept, wantUsed)
			}
			wantOutcome := "delivered"
			if tc.wantAccept {
				wantOutcome = "corrected"
			}
			record := h.logs.String()
			if !strings.Contains(record, "msg=tool_name_correction") || !strings.Contains(record, "outcome="+wantOutcome) || !strings.Contains(record, "reason="+tc.wantReason) {
				t.Fatalf("record = %q, want outcome=%s reason=%s", record, wantOutcome, tc.wantReason)
			}
			if !strings.Contains(record, "tool_name_hash="+correctionHash) ||
				!strings.Contains(record, "trace_id="+correctionTraceID) ||
				!strings.Contains(record, "b_leg_id="+correctionBLegID) ||
				strings.Contains(record, correctionName) {
				t.Fatalf("record lacks the hash/lineage or carries the raw name: %q", record)
			}
		})
	}
}

func TestToolNameCorrection_ProducedOutputClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		event     aipapi.Event
		visible   bool
		reasoning bool
	}{
		{name: "own started", event: aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1"}},
		{name: "other started", event: aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "other"}, visible: true},
		{name: "other args delta", event: aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "other"}, visible: true},
		{name: "finished does not commit", event: aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "other"}},
		{name: "text delta", event: aipapi.Event{Kind: aipapi.EventTextDelta}, visible: true},
		{name: "decision result", event: aipapi.Event{Kind: aipapi.EventDecisionResult}, visible: true},
		{name: "assistant image ref", event: aipapi.Event{Kind: aipapi.EventAssistantImageRef}, visible: true},
		{name: "reasoning delta", event: aipapi.Event{Kind: aipapi.EventReasoningDelta}, reasoning: true},
		{name: "reasoning opaque delta", event: aipapi.Event{Kind: aipapi.EventReasoningOpaqueDelta}, reasoning: true},
		{name: "reasoning part", event: aipapi.Event{Kind: aipapi.EventReasoningPart}, reasoning: true},
		{name: "reasoning signature", event: aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempt := &attemptSession{}
			attempt.observeProducedOutput(tc.event)
			if attempt.producedVisibleBefore("c1") != tc.visible || attempt.producedReasoningOutput() != tc.reasoning {
				t.Fatalf("visible=%v reasoning=%v, want %v/%v",
					attempt.producedVisibleBefore("c1"), attempt.producedReasoningOutput(), tc.visible, tc.reasoning)
			}
		})
	}
}

func TestToolNameCorrection_PrepareRecvEventWiring(t *testing.T) {
	t.Parallel()
	started := aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: "c1", ToolName: correctionName}
	delta := aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "c1", Delta: `{}`}
	finish := aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: "c1", ToolName: correctionName}
	cases := []struct {
		name   string
		before []aipapi.Event
		wantOn bool
	}{
		{name: "finished call is corrected", before: []aipapi.Event{started, delta}, wantOn: true},
		{name: "visible text blocks the correction", before: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "preamble"}, started, delta}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			attempt := &attemptSession{toolFinal: newToolCallAssembler(
				[]toolcall.Finalizer{&steerFin{id: "steer", order: 10, result: steerResult()}}, 0, steerCatalog())}
			terminal, p, facts, ctx := steerableTerminal(), newResponsePipeline(), correctionFacts(), context.Background()
			if gate := p.toolNameCorrectionGateForEvent(ctx, facts, terminal, attempt, aipapi.Event{Kind: aipapi.EventTextDelta}); gate != nil {
				t.Fatal("a non-final event must not allocate a steer gate")
			}
			for _, ev := range tc.before {
				if prepared := p.prepareRecvEvent(ctx, facts, attempt, ev, p.toolNameCorrectionGateForEvent(ctx, facts, terminal, attempt, ev)); prepared.err != nil {
					t.Fatalf("prepare %s: %v", ev.Kind, prepared.err)
				}
			}
			prepared := p.prepareRecvEvent(ctx, facts, attempt, finish, p.toolNameCorrectionGateForEvent(ctx, facts, terminal, attempt, finish))
			if prepared.err != nil || !prepared.swallowed {
				t.Fatalf("finished: swallowed=%v err=%v, want the assembler to hold the call", prepared.swallowed, prepared.err)
			}
			used := terminal.toolCorrectionsAccepted()
			if attempt.isWithholding() != tc.wantOn || (used > 0) != tc.wantOn {
				t.Fatalf("withholding=%v used=%d, want on=%v", attempt.isWithholding(), used, tc.wantOn)
			}
		})
	}
}

func TestToolNameCorrection_TurnScopeAndAttemptLocality(t *testing.T) {
	t.Parallel()

	t.Run("replacement attempt cannot restart the turn budget", func(t *testing.T) {
		t.Parallel()
		h := newCorrectionHarness()
		decide := func(attempt *attemptSession) bool {
			return h.p.toolNameCorrectionGate(context.Background(), h.facts, h.terminal, attempt, "c1")(correctionName, correctionReq())
		}
		if !decide(h.attempt) {
			t.Fatal("first correction must be accepted")
		}
		if decide(&attemptSession{}) {
			t.Fatal("a replacement attempt must not restart the accepted-correction budget")
		}
	})

	t.Run("replacement attempt starts with clean produced-output facts", func(t *testing.T) {
		t.Parallel()
		produced := &attemptSession{}
		produced.observeProducedOutput(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "replaced attempt output"})
		if !produced.producedVisibleBefore("c1") {
			t.Fatal("fixture must record visible output on the replaced attempt")
		}
		p := newResponsePipeline()
		if !p.toolNameCorrectionGate(context.Background(), correctionFacts(), steerableTerminal(), &attemptSession{}, "c1")(correctionName, correctionReq()) {
			t.Fatal("a replacement attempt must start without the replaced attempt's produced output")
		}
	})
}

type correctionDeactivateProbe struct {
	deactivated []steering.OverlayID
}

func (w *correctionDeactivateProbe) Put(context.Context, steering.PutRequest) (steering.State, error) {
	return steering.State{}, nil
}

func (w *correctionDeactivateProbe) Deactivate(_ context.Context, id steering.OverlayID) (steering.State, error) {
	w.deactivated = append(w.deactivated, id)
	return steering.State{}, nil
}

// TestToolNameCorrection_OverlayRegistrationRacesFinish proves D4: registration
// and response-end cleanup are atomic. When the response finishes before
// registration, publishing deactivates the overlay immediately instead of
// leaking it; the normal order deactivates once at finish.
func TestToolNameCorrection_OverlayRegistrationRacesFinish(t *testing.T) {
	t.Parallel()
	newTerminalWithProbe := func() (*turnTerminal, *correctionDeactivateProbe) {
		probe := &correctionDeactivateProbe{}
		terminal := newTurnTerminal()
		terminal.steeringWriterFactory = func(context.Context, string, SteeringWriterResolver) (steering.Writer, error) {
			return probe, nil
		}
		return terminal, probe
	}
	t.Run("finish before registration deactivates immediately", func(t *testing.T) {
		t.Parallel()
		terminal, probe := newTerminalWithProbe()
		terminal.finishResponse(newResponsePipeline(), &attemptSession{})
		terminal.publishToolNameCorrectionOverlay("a-leg-1", "trace-1")
		found := false
		for _, id := range probe.deactivated {
			if id == toolNameCorrectionOverlayID {
				found = true
			}
		}
		if !found {
			t.Fatalf("deactivated = %v, want the correction overlay removed when publish races finish", probe.deactivated)
		}
	})
	t.Run("normal order deactivates at finish", func(t *testing.T) {
		t.Parallel()
		terminal, probe := newTerminalWithProbe()
		terminal.publishToolNameCorrectionOverlay("a-leg-1", "trace-1")
		terminal.finishResponse(newResponsePipeline(), &attemptSession{})
		count := 0
		for _, id := range probe.deactivated {
			if id == toolNameCorrectionOverlayID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("deactivated correction overlay %d times, want exactly once", count)
		}
	})
}

// catalogProbeFin records the catalog the assembler passes to Finalize and
// never steers, so the test observes the wiring without tripping continuation.
type catalogProbeFin struct {
	id      string
	catalog []string
}

func (f *catalogProbeFin) ID() string { return f.id }

func (f *catalogProbeFin) Order() int { return 10 }

func (f *catalogProbeFin) Finalize(_ context.Context, _ toolcall.CompletedCall, _ aipapi.ToolDef, catalog []aipapi.ToolDef, _ toolcall.Meta) (toolcall.Result, error) {
	f.catalog = nil
	for _, td := range catalog {
		f.catalog = append(f.catalog, td.Name)
	}
	return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
}

type catalogMutatingHook struct {
	add    string
	remove string
}

func (h catalogMutatingHook) ID() string                      { return "catalog-mutating-hook" }
func (catalogMutatingHook) Order() int                        { return 0 }
func (catalogMutatingHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (h catalogMutatingHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	if h.remove != "" {
		kept := call.Tools[:0]
		for _, td := range call.Tools {
			if td.Name != h.remove {
				kept = append(kept, td)
			}
		}
		call.Tools = kept
	}
	if h.add != "" {
		call.Tools = append(call.Tools, aipapi.ToolDef{Name: h.add, Parameters: []byte(`{"type":"object"}`)})
	}
	return nil
}

// TestToolNameCorrection_FinalizerSeesBackendTools proves D5: the assembler
// catalog is the backend-bound tools, not the baseline. A hook-added tool must
// be visible (a call to it is not steered as unknown), and a hook-removed tool
// must be invisible (a call to it is steered).
func TestToolNameCorrection_FinalizerSeesBackendTools(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		baseline  []string
		hook      catalogMutatingHook
		callName  string
		wantKnown bool
	}{
		{name: "added tool is known", baseline: []string{"client-tool"}, hook: catalogMutatingHook{add: "hook-added-tool"}, callName: "hook-added-tool", wantKnown: true},
		{name: "removed tool is unknown", baseline: []string{"client-tool", "hook-removed-tool"}, hook: catalogMutatingHook{remove: "hook-removed-tool"}, callName: "hook-removed-tool", wantKnown: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			probe := &catalogProbeFin{id: "catalog-probe"}
			ex := TestExecutor()
			ex.Store = st
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{tc.hook}})
			ex.Rand = routing.NewSeededRng(2)
			ex.SetToolCallFinalizers([]toolcall.Finalizer{probe}, 64*1024)
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
					TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
						Operation: aipapi.OperationOpenAIChatCompletions,
						Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
					}),
					Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						return aipapi.NewFixedEventStream(append([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventMessageStarted},
						}, append(twCallEvents("c1", tc.callName, `{"q":"x"}`, 1), aipapi.Event{Kind: aipapi.EventResponseFinished})...)), nil
					},
				},
			}
			var baselineTools []aipapi.ToolDef
			for _, name := range tc.baseline {
				baselineTools = append(baselineTools, aipapi.ToolDef{Name: name, Parameters: []byte(`{"type":"object"}`)})
			}
			call := &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
				Tools: baselineTools,
				Invocation: aipapi.Invocation{
					Operation:    aipapi.OperationOpenAIChatCompletions,
					DeliveryMode: aipapi.DeliveryModeNonStreaming,
				},
			}
			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if _, err := aipapi.CollectUnbounded(context.Background(), stream); err != nil {
				t.Fatalf("collect: %v", err)
			}
			if len(probe.catalog) == 0 {
				t.Fatal("probe finalizer never ran: the assembler catalog was not observed")
			}
			known := slices.Contains(probe.catalog, tc.callName)
			if known != tc.wantKnown {
				t.Fatalf("catalog %v contains %q = %v, want known=%v: finalizer must see backend-bound tools", probe.catalog, tc.callName, known, tc.wantKnown)
			}
		})
	}
}
