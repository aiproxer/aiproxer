// End-to-end preferred-protocol acceptance fixtures for slice 10.1A of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Testing /
// Runtime / Acceptance Matrix rows 1, 2, and 20; requirements 2.1-2.7,
// 3.1-3.6, 4.1-4.7, 5.1-5.7, 6.1-6.7, 12.5).
//
// Every fixture in this file drives the complete production path for one
// deployment: a real frontend handler mounted on a real httptest origin, the
// real runtime executor, a real backend adapter, and a real
// reference-provider origin emulator. No live or billable call is made.
//
// The Agent Loop Guard generation is compiled through the production feature
// registry, the enabled-surface merge, and the production request-snapshot
// builder, so the control provider and terminal provider under test are the
// ones the factory actually composes. Nothing here assigns a provider fake.
//
// Slice 10.1A owns two cells of the 10.1 matrix:
//
//  1. completion-only: the model emits only the private control call, and the
//     client receives the bounded result as ordinary assistant text exactly
//     once;
//  2. streamed text then completion: the already-committed assistant text is
//     delivered exactly once and the bounded result is not appended.
//
// Plus the explicit early-stream observation assertion the task validation
// line requires: the final-stream observation of every canonical event happens
// after the real response-part hook ran for it and before the frontend encoder
// emitted it, and the pending result is released through the accepted terminal
// owner ahead of the response completion the client observes.
//
// Slice 10.1B appends five more cells for the message-authority,
// OpenAI-Responses protocol column, in the section at the end of this file: the
// ordinary client tool then completion, the malformed control call, the multiple
// control calls, the native completion collision, and the missing signal.
//
// Slice 10.1C appends the six remaining matrix cells in the section at the end of
// this file: the one-reprompt user-input case followed by a second unmarked stop,
// unsupported backend tools, the ToolChoice matrix, the item-authority twins of
// acceptance-matrix rows 1 and 2, the Anthropic and Gemini client-facing
// protocol columns, and the legacy-versus-preferred end-to-end contrast of
// requirement 9.6.
//
// Not covered by 10.1 and NOT claimed here: the Anthropic and Gemini BACKEND
// repair-leg columns. The Anthropic and Gemini backend adapters have no
// developer wire role, so those two columns cannot be certified as preferred
// strategy repair legs; that limitation is recorded for task 12.2. The two
// client-facing protocol columns below are therefore paired with the
// OpenAI-Responses backend, whose adapter is already proven.
//
// The item-authority ORDINARY-TOOL row is also not claimed, for a reason that is
// NOT specific to this strategy and was therefore measured rather than assumed: a
// canonical turn carrying an ordinary tool call FOLLOWED BY ordinary assistant
// text is rejected by the real OpenResponses frontend with 502 even when NO Agent
// Loop Guard generation is composed at all. Its mapper only re-opens a message item
// after reasoning, so any text delta that follows a closed tool-call item fails
// with "received text delta without active message item". That is a pre-existing
// item-authority limitation in internal/plugins/protocols/openresponses, outside
// task 10.1's boundary and outside the preferred protocol's semantics, so the
// completion-only and streamed item-authority twins are claimed here and the
// ordinary-tool item-authority twin is left to that frontend's owner.
//
// Known soundness defect in the slice 10.1A streamed cell, NOT fixed here
// because that cell is independently approved and immutable: the comparison
// "clientSawText < finishObserved" orders an index recorded by the client
// reader goroutine against an index recorded by the response-pipeline goroutine.
// Nothing in the production path creates a happens-before edge between them,
// because the pipeline never blocks on the client reader, so a run in which the
// reader goroutine is not scheduled until after the pipeline finished violates
// the assertion. It reproduces on committed HEAD 04ffc3a7 with this file
// reverted (see the slice 10.1B status report), so it is a pre-existing harness
// race rather than a defect of the cells below. The 10.1B cells are
// deliberately serial so they do not amplify that scheduling window.
package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/sdkadapter"
	refopenaichat "github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	refopenairesponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// algControlToolName, algControlCallID, and algControlItemID are the private
// protocol facts these fixtures deliberately plant upstream. They exist only so
// the fixtures can prove they never reach the client.
const (
	algControlToolName = "attempt_completion"
	algControlCallID   = "call_alg_private_control"
	algControlItemID   = "fc_alg_private_control"
)

// algCompletionResult is the bounded result the model returns from the private
// control call.
const algCompletionResult = "Migration finished: schema applied and backfill verified."

// algStreamedText is ordinary assistant text the model streams BEFORE the
// private control call.
const algStreamedText = "Schema applied; backfill verification is running."

// The three real seams whose relative order this file certifies.
const (
	algTraceStageHook    = "response_part_hook"
	algTraceStageObserve = "final_stream_observer"
	algTraceStageClient  = "client_wire"
)

// --- observation trace --------------------------------------------------------

// algTraceItem is one observation of one canonical event or one client-visible
// wire frame. text is the assistant delta for a text frame, tool carries the
// effective tool name for a tool lifecycle frame, and seq is the observation
// ordinal in a single ordering shared by all three stages.
type algTraceItem struct {
	seq   int
	stage string
	kind  string
	text  string
	tool  string
}

// algTrace is the single ordered observation log shared by the real
// response-part hook, the real final-stream observer, and the client-side wire
// reader. One mutex orders all three because they genuinely run on different
// goroutines: the hook and the observer run inside the response pipeline, the
// wire reader runs in the client.
type algTrace struct {
	mu    sync.Mutex
	items []algTraceItem
}

func (tr *algTrace) add(stage, kind, text, tool string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.items = append(tr.items, algTraceItem{seq: len(tr.items), stage: stage, kind: kind, text: text, tool: tool})
}

func (tr *algTrace) snapshot() []algTraceItem {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]algTraceItem(nil), tr.items...)
}

// firstIndex returns the sequence number of the first matching observation, or -1.
func (tr *algTrace) firstIndex(stage, kind, text string) int {
	for _, it := range tr.snapshot() {
		if it.stage == stage && it.kind == kind && it.text == text {
			return it.seq
		}
	}
	return -1
}

// algFirstKind returns the sequence number of the first observation of one kind
// in one stage, or -1.
func algFirstKind(items []algTraceItem, stage, kind string) int {
	for _, it := range items {
		if it.stage == stage && it.kind == kind {
			return it.seq
		}
	}
	return -1
}

// algTraceHook is a real sdkhooks.ResponsePartHook. It records and mutates
// nothing, so every assertion below observes the production hook chain rather
// than a stubbed recorder.
type algTraceHook struct{ tr *algTrace }

func (algTraceHook) ID() string                        { return "alg-e2e-trace-hook" }
func (algTraceHook) Order() int                        { return 0 }
func (algTraceHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (h algTraceHook) HandleEvent(_ context.Context, ev *aipapi.Event, _ sdkhooks.PartMeta) error {
	if ev == nil {
		return nil
	}
	h.tr.add(algTraceStageHook, string(ev.Kind), ev.Delta, ev.ToolName)
	return nil
}

// algTraceObserverFactory is a real response.StreamObserverFactory contributed
// through the real feature plane, so its Observe and Finish calls are driven by
// extensions.RunFinalStreamObservationStage at the production observation point.
type algTraceObserverFactory struct{ tr *algTrace }

func (algTraceObserverFactory) ID() string                        { return "alg-e2e-trace-observer" }
func (algTraceObserverFactory) Order() int                        { return 0 }
func (algTraceObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (f algTraceObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return algTraceObserver(f), nil
}

type algTraceObserver struct{ tr *algTrace }

func (o algTraceObserver) Observe(_ context.Context, ev aipapi.Event) error {
	o.tr.add(algTraceStageObserve, string(ev.Kind), ev.Delta, ev.ToolName)
	return nil
}

func (algTraceObserver) Finish(context.Context, response.StreamOutcome) error { return nil }

// --- deployment seam ----------------------------------------------------------

// algDeployPreferred composes one deployment whose executor carries the real
// preferred-strategy Agent Loop Guard generation plus the observation factory,
// installed through the production registry, merge, and snapshot path, and
// returns the shared observation trace.
func algDeployPreferred(t *testing.T, transport ClientTransport, origin http.Handler) (*Deployment, *algTrace) {
	t.Helper()

	return algDeployColumn(t, algColumn{
		Strategy:  AgentLoopGuardStrategyAttemptCompletion,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: transport,
		Origin:    origin,
	})
}

// algColumn is one fully specified end-to-end cell: the client-facing protocol
// column (Frontend plus its A-leg path), the backend family, and the Agent Loop
// Guard strategy whose REAL generation is composed through the production
// feature registry, enabled-surface merge, and production request-snapshot
// builder. Every slice 10.1 cell deploys through this one selector, so the
// message-authority/OpenAI-Responses column and the item-authority,
// Anthropic, Gemini, legacy-strategy and unsupported-backend columns are proved
// through one identical environment rather than through per-column wiring.
type algColumn struct {
	// Strategy selects the real ALG generation to compose.
	Strategy string
	// Frontend and Backend are authoritative harness identities.
	Frontend  string
	Backend   string
	ProfileID string
	// Transport selects the client entrypoint.
	Transport ClientTransport
	// Origin is the real reference-provider origin handler the backend reaches.
	Origin http.Handler
	// Candidates appends additional real backend candidates, each with its own
	// scripted origin, so a failover or parallel-race cell can script both sides of
	// the race. Nil keeps the deployment's default single-backend route selector.
	Candidates []Candidate
	// AgentLoopGuardConfig appends extra YAML lines to the feature block the
	// production registry composes (for example, protocol limits). Empty keeps
	// the generation defaults, so every existing cell is unaffected.
	AgentLoopGuardConfig string
}

// algDeployColumn composes one deployment whose executor carries the real ALG
// generation for col, plus the shared observation trace, and returns both.
//
// The generation is installed twice on purpose and both installations are real:
// [Deploy] proves the production snapshot builder publishes it, and the
// re-derivation below re-runs [AgentLoopGuardFeaturePlanes] and replays the same
// planes into a contribution set that additionally carries the observation
// factory, so the executor snapshot still comes from the production builder
// rather than from a hand-assembled provider list. No provider fake is ever
// assigned: the control provider and the terminal provider under test are the
// ones the production feature factory composes.
func algDeployColumn(t *testing.T, col algColumn) (*Deployment, *algTrace) {
	t.Helper()

	d := Deploy(t, DeploymentSpec{
		Frontend:               col.Frontend,
		Backend:                col.Backend,
		ProfileID:              col.ProfileID,
		Transport:              col.Transport,
		OriginHandler:          col.Origin,
		Candidates:             col.Candidates,
		AgentLoopGuardStrategy: col.Strategy,
	})
	if d == nil {
		t.Fatalf("Deploy returned nil for the %q/%q/%q cell", col.Frontend, col.Backend, col.Strategy)
	}

	tr := &algTrace{}
	bus := hooks.New(hooks.Config{ResponsePartHooks: []sdkhooks.ResponsePartHook{algTraceHook{tr: tr}}})
	d.Exec.Bus = bus

	// Extend the generation Deploy installed with the observation factory through
	// the same real plane merge, so the executor snapshot still comes from the
	// production snapshot builder rather than a hand-assembled provider list.
	planes, err := AgentLoopGuardFeaturePlanesWithConfig(t, col.Strategy, col.AgentLoopGuardConfig)
	if err != nil {
		t.Fatalf("AgentLoopGuardFeaturePlanes: %v", err)
	}
	cs := aipfeature.NewContributionSet()
	if err := planes.ReplayTo(cs, "alg-e2e-observer"); err != nil {
		t.Fatalf("ReplayTo: %v", err)
	}
	if err := aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "alg-e2e-observer", []response.StreamObserverFactory{algTraceObserverFactory{tr: tr}}); err != nil {
		t.Fatalf("Contribute stream observer factories: %v", err)
	}
	d.Exec.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{FeaturePlanes: cs.Freeze()})
	algAssertNoCompletionGateInPreferredPath(t, col.Strategy, d.Exec.RuntimeSnapshot)
	algWireContinuationPorts(t, d)
	return d, tr
}

// algAssertNoCompletionGateInPreferredPath is the measured proof of requirement
// 5.7 and design Repair 1 ("rejected whole-response completion gates because
// they buffer normal streaming") for every preferred cell in this file.
//
// The strategy is proven by MEASUREMENT of the real composed generation, not by
// reading a comment: the request-runtime snapshot the production builder just
// published is asked directly how many whole-response completion gates it
// carries. Zero means the preferred protocol's completion decision cannot come
// from a whole-response gate, because no gate exists on its path at all; the
// decision is made by mid-stream control-call capture and terminal evidence.
//
// The same measurement is taken for the legacy column so the contrast is recorded
// rather than assumed, and the recorded contrast is worth stating precisely: the
// legacy column ALSO measures zero gates, because the legacy strategy has never
// buffered a whole response either. The two columns differ in exactly the plane
// that matters — the legacy column has no control-tool provider at all — so a zero
// gate count proves the preferred path is gate-free but does NOT by itself prove
// which strategy is composed. That is why the provider assertion below is
// mandatory rather than decorative.
func algAssertNoCompletionGateInPreferredPath(t *testing.T, strategy string, snap *extensions.RequestRuntimeSnapshot) {
	t.Helper()
	if snap == nil {
		t.Fatalf("alg: no request runtime snapshot for strategy %q", strategy)
	}
	gates := snap.CompletionGates()
	if strategy == AgentLoopGuardStrategyAttemptCompletion {
		if len(gates) != 0 {
			t.Fatalf("preferred strategy must carry zero whole-response completion gates, measured %d", len(gates))
		}
		if snap.ControlToolProvider() == nil {
			t.Fatalf("preferred strategy must carry the control-tool provider; a gate count of %d alone would not prove the preferred mechanism", len(gates))
		}
	}
	t.Logf("alg: strategy=%q measured completion_gates=%d control_tool_provider_present=%v",
		strategy, len(gates), snap.ControlToolProvider() != nil)
}

// algWireContinuationPorts composes the two real conversation-view ports the
// production host wires in build_executor.go: the trajectory reader and the
// steering-writer factory, both over the real in-memory conversation-view store
// and the real production steering-writer adapter.
//
// The generic continuation transaction that the missing-signal policy drives needs
// them, so without them a bounded protocol-repair leg cannot be admitted and the
// deployment degrades to the unfactored host shape. This composes no terminal
// provider and no control provider: those still come only from the production
// feature registry, merge, and snapshot builder. It is applied by every cell that
// uses algDeployPreferred so all of them observe one identical environment.
func algWireContinuationPorts(t *testing.T, d *Deployment) {
	t.Helper()

	store := conversationview.NewReferenceStore()
	d.Exec.ConversationViewReader = algAutoRegisteringReader{store: store}
	d.Exec.SteeringWriterFactory = func(_ context.Context, aLegID string, resolver runtime.SteeringWriterResolver) (steering.Writer, error) {
		return sdkadapter.NewWriter(store, aLegID, sdkadapter.TrajectoryResolver(resolver))
	}
}

// algAutoRegisteringReader is the same on-demand A-leg registration and unknown-leg
// tolerance the production feature-host store wrapper applies, so a request whose
// A-leg never registered reads as an empty snapshot instead of failing the turn.
type algAutoRegisteringReader struct {
	store *conversationview.ReferenceStore
}

func (r algAutoRegisteringReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	if err := r.store.CreateALeg(ctx, aLegID); err != nil {
		return conversationprojection.Snapshot{}, err
	}
	return r.store.Snapshot(ctx, aLegID)
}

// --- upstream wire fixtures ---------------------------------------------------

// algControlArgs renders the private control-call arguments exactly as a model
// would emit them: one JSON object with exactly one string `result` member.
func algControlArgs(t *testing.T, result string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"result": result})
	if err != nil {
		t.Fatalf("marshal control args: %v", err)
	}
	return string(raw)
}

// algCompletionOnlyOrigin is the upstream reference-provider wire for matrix
// row 1: the model's only output is the private control call.
func algCompletionOnlyOrigin(t *testing.T) http.Handler {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id":         "resp_alg_control_only",
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output": []any{map[string]any{
			"type":      "function_call",
			"id":        algControlItemID,
			"call_id":   algControlCallID,
			"name":      algControlToolName,
			"arguments": algControlArgs(t, algCompletionResult),
		}},
		"usage": map[string]any{"input_tokens": 21, "output_tokens": 9, "total_tokens": 30},
	})
	if err != nil {
		t.Fatalf("marshal completion-only resource: %v", err)
	}
	return refopenairesponses.NewHandler(refopenairesponses.Config{NonStreamJSON: string(body)})
}

// algStreamedTextThenCompletionOrigin is the upstream reference-provider wire
// for matrix row 2: ordinary assistant text first, then the private control
// call, then the terminal event.
func algStreamedTextThenCompletionOrigin(t *testing.T) http.Handler {
	t.Helper()
	frame := func(name string, payload map[string]any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal sse frame %s: %v", name, err)
		}
		return "event: " + name + "\ndata: " + string(raw) + "\n\n"
	}
	var sse strings.Builder
	seq := 0
	next := func() int { seq++; return seq }
	sse.WriteString(frame("response.created", map[string]any{
		"type": "response.created", "sequence_number": next(),
		"response": map[string]any{"id": "resp_alg_streamed", "object": "response", "created_at": 1715620000, "status": "in_progress", "model": "gpt-4o-mini"},
	}))
	sse.WriteString(frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": "msg_alg_streamed", "status": "in_progress", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": ""}},
		},
	}))
	sse.WriteString(frame("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "sequence_number": next(), "item_id": "msg_alg_streamed",
		"output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""},
	}))
	sse.WriteString(frame("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "sequence_number": next(), "item_id": "msg_alg_streamed",
		"output_index": 0, "content_index": 0, "delta": algStreamedText,
	}))
	sse.WriteString(frame("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "sequence_number": next(), "item_id": "msg_alg_streamed",
		"output_index": 0, "content_index": 0, "text": algStreamedText,
	}))
	sse.WriteString(frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": "msg_alg_streamed", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": algStreamedText}},
		},
	}))
	sse.WriteString(frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 1,
		"item": map[string]any{"type": "function_call", "id": algControlItemID, "call_id": algControlCallID, "name": algControlToolName, "arguments": ""},
	}))
	sse.WriteString(frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 1,
		"item": map[string]any{"type": "function_call", "id": algControlItemID, "call_id": algControlCallID, "name": algControlToolName, "arguments": algControlArgs(t, algCompletionResult)},
	}))
	sse.WriteString(frame("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": next(),
		"response": map[string]any{
			"id": "resp_alg_streamed", "object": "response", "created_at": 1715620000, "status": "completed", "model": "gpt-4o-mini",
			"usage": map[string]any{"input_tokens": 31, "output_tokens": 14, "total_tokens": 45},
		},
	}))
	sse.WriteString("data: [DONE]\n\n")
	return refopenairesponses.NewHandler(refopenairesponses.Config{StreamSSE: sse.String()})
}

// --- client drivers -----------------------------------------------------------

// algCreateBody is the exact A-leg request the fixtures post to the real
// OpenAI Responses frontend.
func algCreateBody(model string, stream bool) string {
	body, err := json.Marshal(map[string]any{
		"model":  model,
		"input":  "apply the schema, verify the backfill, then report the result",
		"stream": stream,
	})
	if err != nil {
		panic("marshal alg create body: " + err.Error())
	}
	return string(body)
}

// algWireFrame is one client-visible frame: either the non-streaming response
// resource or one SSE frame.
type algWireFrame struct {
	Type  string
	Delta string
	Text  string
	Raw   string
}

// algPostCreate drives one client request over the real HTTP boundary and, when
// tr is non-nil, records every delivered frame into the shared trace as it is
// read off the wire.
func algPostCreate(ctx context.Context, d *Deployment, stream bool, tr *algTrace) (int, []algWireFrame, error) {
	return algPostCreateBody(ctx, d, algCreateBody("gpt-4o-mini", stream), stream, tr)
}

// algPostCreateBody is the raw-body form of algPostCreate. It is the same driver
// over the same real HTTP boundary and the same trace; only the A-leg document is
// supplied by the caller, so a multi-turn cell can post its own client tool
// catalog, tool results, and echoed model items.
func algPostCreateBody(ctx context.Context, d *Deployment, body string, stream bool, tr *algTrace) (int, []algWireFrame, error) {
	return algPostCreateAtPath(ctx, d, "/v1/responses", body, stream, tr)
}

// algPostCreateAtPath is the path-parameterized form of algPostCreateBody. It is
// the same driver over the same real HTTP boundary, the same canonical
// OpenAI-Responses-shaped response resource, and the same shared trace; only the
// client-facing protocol column's A-leg path differs, which is what lets the
// item-authority column reuse every slice 10.1A/10.1B assertion unchanged.
func algPostCreateAtPath(ctx context.Context, d *Deployment, path, body string, stream bool, tr *algTrace) (int, []algWireFrame, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL()+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Route", d.RouteSelector)
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if !stream {
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp.StatusCode, nil, err
		}
		var decoded struct {
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return resp.StatusCode, nil, fmt.Errorf("decode create resource: %w", err)
		}
		var b strings.Builder
		for _, item := range decoded.Output {
			for _, part := range item.Content {
				if part.Type == "output_text" {
					b.WriteString(part.Text)
				}
			}
		}
		if tr != nil {
			tr.add(algTraceStageClient, "json_resource", "", "")
		}
		return resp.StatusCode, []algWireFrame{{Type: "json_resource", Text: b.String(), Raw: string(raw)}}, nil
	}

	var frames []algWireFrame
	br := bufio.NewReader(resp.Body)
	var data strings.Builder
	flush := func() error {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		var decoded struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Text  string `json:"text"`
		}
		if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
			return fmt.Errorf("decode sse frame: %w", err)
		}
		frames = append(frames, algWireFrame{Type: decoded.Type, Delta: decoded.Delta, Text: decoded.Text, Raw: payload})
		if tr != nil {
			tr.add(algTraceStageClient, decoded.Type, decoded.Delta, "")
		}
		return nil
	}
	for {
		line, readErr := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "data:"):
			payload := strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " ")
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(payload)
		case trimmed == "":
			if err := flush(); err != nil {
				return resp.StatusCode, frames, err
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return resp.StatusCode, frames, readErr
			}
			break
		}
	}
	if err := flush(); err != nil {
		return resp.StatusCode, frames, err
	}
	return resp.StatusCode, frames, nil
}

// --- assertions ---------------------------------------------------------------

// algWireText concatenates every assistant text fragment the client observed.
func algWireText(frames []algWireFrame) string {
	var b strings.Builder
	for _, f := range frames {
		switch f.Type {
		case "json_resource":
			b.WriteString(f.Text)
		case "response.output_text.delta":
			b.WriteString(f.Delta)
		}
	}
	return b.String()
}

// algCountWireType counts client-visible frames of one wire type.
func algCountWireType(frames []algWireFrame, typ string) int {
	n := 0
	for _, f := range frames {
		if f.Type == typ {
			n++
		}
	}
	return n
}

// algAssertNoPrivateControlLeak fails when any client-visible frame or the whole
// wire body carries the private control name, call id, item id, argument member,
// or any tool-call lifecycle frame at all.
func algAssertNoPrivateControlLeak(t *testing.T, frames []algWireFrame) {
	t.Helper()
	var body strings.Builder
	for _, f := range frames {
		body.WriteString(f.Raw)
		body.WriteByte('\n')
	}
	raw := body.String()
	for _, secret := range []string{algControlToolName, algControlCallID, algControlItemID, `"result"`, `"function_call"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("client wire leaked private control fact %q:\n%s", secret, raw)
		}
	}
	for _, f := range frames {
		if strings.Contains(f.Type, "function_call") || strings.Contains(f.Type, "tool") {
			t.Fatalf("client wire exposed a tool lifecycle frame %q:\n%s", f.Type, raw)
		}
	}
}

// algAssertObservationOrder proves the explicit ordering the task validation
// line requires: the real response-part hook ran first for a canonical event,
// the real final-stream observation ran second, the frontend encoder emitted
// the frame third, and the accepted terminal owner released the private pending
// result before the response completion the client observes.
func algAssertObservationOrder(t *testing.T, tr *algTrace, frames []algWireFrame, publishedText string) {
	t.Helper()

	items := tr.snapshot()
	hookAt := tr.firstIndex(algTraceStageHook, string(aipapi.EventTextDelta), publishedText)
	obsAt := tr.firstIndex(algTraceStageObserve, string(aipapi.EventTextDelta), publishedText)
	if hookAt < 0 {
		t.Fatalf("response-part hook never observed the published text %q; trace=%+v", publishedText, items)
	}
	if obsAt < 0 {
		t.Fatalf("final-stream observation never observed the published text %q; trace=%+v", publishedText, items)
	}
	if hookAt >= obsAt {
		t.Fatalf("final-stream observation must run after the response-part hook; hook=%d observe=%d trace=%+v", hookAt, obsAt, items)
	}

	finishObserved := algFirstKind(items, algTraceStageObserve, string(aipapi.EventResponseFinished))
	if finishObserved < 0 {
		t.Fatalf("final-stream observation never saw the accepted response finish; trace=%+v", items)
	}
	if obsAt >= finishObserved {
		t.Fatalf("the private result must be observed before the accepted response finish; result=%d finish=%d trace=%+v", obsAt, finishObserved, items)
	}

	// The response-part hook runs in two real phases here and both are covered by
	// the checks above. The raw upstream finish passes the hook when it enters the
	// pipeline; the synthesized result frames pass it later, during the accepted
	// terminal's publication preparation, and it is that later hook observation
	// that must precede the result's observation stage (hookAt < obsAt above). The
	// raw finish's hook position is therefore deliberately not compared against
	// the result's.

	// The client-observed terminal frame must arrive after the private result was
	// already observed, which is only possible when the accepted terminal owner
	// released it before completing the response.
	clientTerminal := algFirstKind(items, algTraceStageClient, "response.completed")
	if clientTerminal < 0 {
		clientTerminal = algFirstKind(items, algTraceStageClient, "json_resource")
	}
	if clientTerminal < 0 {
		t.Fatalf("client never observed a terminal frame; frames=%+v", frames)
	}
	if obsAt >= clientTerminal {
		t.Fatalf("the private result must be released before the client observes the terminal; result=%d terminal=%d trace=%+v", obsAt, clientTerminal, items)
	}

	// A streaming cell additionally proves the encoder emitted the published
	// frame only after the observation stage ran for it.
	if streamFrames := algClientTextFrames(frames); streamFrames != nil {
		clientAt := algFirstKind(items, algTraceStageClient, "response.output_text.delta")
		if clientAt < 0 || obsAt >= clientAt {
			t.Fatalf("the client frame must be emitted after the final-stream observation; observe=%d client=%d trace=%+v", obsAt, clientAt, items)
		}
	}

	for _, it := range items {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// algClientTextFrames returns the client-visible text frames of a streaming
// cell, or nil for a non-streaming resource cell.
func algClientTextFrames(frames []algWireFrame) []algWireFrame {
	var out []algWireFrame
	for _, f := range frames {
		if f.Type == "response.output_text.delta" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- matrix cells -------------------------------------------------------------

// TestPreferredProtocolE2E_completionOnlyPublishesResultAsAssistantText is
// acceptance-matrix row 1: proxy completion as the only model output. The client
// receives the bounded result as ordinary assistant text exactly once and no
// private control fact appears anywhere on the wire.
func TestPreferredProtocolE2E_completionOnlyPublishesResultAsAssistantText(t *testing.T) {
	t.Parallel()

	d, tr := algDeployPreferred(t, TransportJSON, algCompletionOnlyOrigin(t))

	status, frames, err := algPostCreate(t.Context(), d, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, frames[0].Raw)
	}
	if got := algWireText(frames); got != algCompletionResult {
		t.Fatalf("client assistant text = %q, want exactly %q", got, algCompletionResult)
	}
	algAssertNoPrivateControlLeak(t, frames)
	algAssertObservationOrder(t, tr, frames, algCompletionResult)
}

// TestPreferredProtocolE2E_streamedTextThenCompletionDoesNotDuplicate is
// acceptance-matrix row 2 plus row 20: ordinary assistant text is committed and
// delivered exactly once before the private control call, and the bounded result
// is not appended or duplicated. The early-stream observation assertion proves
// the ordinary text the client already saw was observed before the backend
// terminal rather than buffered until completion.
func TestPreferredProtocolE2E_streamedTextThenCompletionDoesNotDuplicate(t *testing.T) {
	t.Parallel()

	d, tr := algDeployPreferred(t, TransportSSE, algStreamedTextThenCompletionOrigin(t))

	status, frames, err := algPostCreate(t.Context(), d, true, tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if n := algCountWireType(frames, "response.completed"); n != 1 {
		t.Fatalf("response.completed frames = %d, want exactly 1; frames=%+v", n, frames)
	}
	if got := algWireText(frames); got != algStreamedText {
		t.Fatalf("client assistant text = %q, want exactly the committed streamed text %q", got, algStreamedText)
	}
	if strings.Contains(algWireText(frames), algCompletionResult) {
		t.Fatal("the committed assistant text was duplicated with the completion result")
	}
	algAssertNoPrivateControlLeak(t, frames)

	// Row 20: the ordinary text is observed through the real seams before the
	// backend terminal, not buffered until completion because the strategy is on.
	items := tr.snapshot()
	textObserved := tr.firstIndex(algTraceStageObserve, string(aipapi.EventTextDelta), algStreamedText)
	if textObserved < 0 {
		t.Fatalf("final-stream observation never observed the streamed text; trace=%+v", items)
	}
	finishObserved := algFirstKind(items, algTraceStageObserve, string(aipapi.EventResponseFinished))
	if finishObserved < 0 || textObserved >= finishObserved {
		t.Fatalf("ordinary text must be observed before the backend terminal; text=%d finish=%d trace=%+v", textObserved, finishObserved, items)
	}
	textHooked := tr.firstIndex(algTraceStageHook, string(aipapi.EventTextDelta), algStreamedText)
	if textHooked < 0 || textHooked >= textObserved {
		t.Fatalf("the response-part hook must run before the final-stream observation; hook=%d observe=%d trace=%+v", textHooked, textObserved, items)
	}
	// The client-side ordering must be read from the wire frames themselves, not
	// from the shared trace: the trace is appended by the response-pipeline
	// goroutine AND the client-reader goroutine under one mutex, so a trace index
	// on one side and an index on the other side have no happens-before edge
	// between them. Wire order is the only sound client-side ordering source.
	deltaAt := slices.IndexFunc(frames, func(f algWireFrame) bool {
		return f.Type == "response.output_text.delta" && f.Delta == algStreamedText
	})
	completedAt := slices.IndexFunc(frames, func(f algWireFrame) bool {
		return f.Type == "response.completed"
	})
	if deltaAt < 0 {
		t.Fatalf("the client never received the ordinary text delta on the wire; frames=%+v", frames)
	}
	if completedAt < 0 {
		t.Fatalf("the client never received the response completion frame; frames=%+v", frames)
	}
	if deltaAt >= completedAt {
		t.Fatalf("the client must receive the ordinary text delta before the response completion frame; delta=%d completed=%d frames=%+v", deltaAt, completedAt, frames)
	}
	for _, it := range items {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// =============================================================================
// Slice 10.1B — message authority, OpenAI-Responses protocol column
// =============================================================================
//
// The five cells below extend the same real seam slice 10.1A deployed. Each drives
// a complete deployment: real frontend handler on a real httptest origin, real
// runtime executor, real backend adapter, and the real reference-provider origin
// emulator. No provider fake is assigned anywhere; the control provider and the
// terminal provider under test are still the ones the production feature factory
// composes, and the observation trace is still driven by the real response-part
// hook and the real final-stream observation stage.
//
// Cases owned here (design Runtime / Acceptance Matrix rows 3, 4, 12, 15; the
// remaining rows and columns are still unclaimed):
//
//  1. ordinary client tool then completion — the ordinary tool lifecycle stays
//     ordinary and the client's answer is honored;
//  2. malformed control call — swallowed privately, never published and never
//     executed as an ordinary client tool;
//  3. multiple control calls — one response admits one control call, so the
//     duplicate revokes the completion and the turn fails closed;
//  4. native completion collision — a client-owned completion tool is never
//     relabelled or intercepted by name, and its native evidence still allows the
//     terminal;
//  5. missing signal — one bounded hidden protocol repair, then the terminal.

// --- slice 10.1B planted facts ------------------------------------------------

// The ordinary client-owned tool facts these cells plant upstream. Unlike the
// private control facts they are REQUIRED to reach the client untouched.
const (
	algOrdinaryToolName  = "run_backfill_check"
	algOrdinaryCallID    = "call_alg_ordinary_backfill"
	algOrdinaryItemID    = "fc_alg_ordinary_backfill"
	algOrdinaryToolArgs  = `{"scope":"full"}`
	algOrdinaryToolInput = "run the backfill check the deployment plan asks for"
	algToolResultOutput  = "backfill verified: 0 rows out of sync"
)

// The client-owned explicit-completion tool facts of the native-collision cell.
// The CLIENT declares this tool, so the proxy owns nothing here and must not
// intercept the call by name alone.
const (
	algNativeCallID = "call_alg_client_owned_completion"
	algNativeItemID = "fc_alg_client_owned_completion"
	algNativeArgs   = `{"result":"All requested work is complete."}`
	algNativeResult = "All requested work is complete."
)

// algUnmarkedText is ordinary assistant text the model commits in the malformed,
// multiple-control-call, and missing-signal turns. It exists so "no result was
// invented" is observable as exact text equality rather than as an absence.
const algUnmarkedText = "Deployment summary draft is ready for review."

// algRepairText is the ordinary assistant text the model commits on the bounded
// protocol-repair leg that follows an unmarked first turn. It is deliberately
// different from algUnmarkedText so the client-visible answer of a two-leg turn
// is exact text equality over both commits, which is what distinguishes
// "two model turns were preserved" from "the proxy duplicated one".
const algRepairText = "Continuing the outstanding verification before reporting."

// algProtocolInstructionMarker and algRepairInstructionMarker are the two private
// instruction markers the proxy may add to the B-leg. They are backend-visible
// control text (design Accounting/Traffic) and must never reach the client.
const (
	algProtocolInstructionMarker = "<task-completion-protocol>"
	algRepairInstructionMarker   = "<automated-completion-protocol-repair>"
)

// --- slice 10.1B upstream wire fixtures ---------------------------------------

// algUpstreamTurn is one planted upstream response for a scripted origin.
type algUpstreamTurn struct {
	JSON string
	SSE  string
}

// algUpstreamLog records every upstream request body the scripted origin served,
// in arrival order. The origin emulator serves requests on its own goroutine, so
// the log owns one mutex.
type algUpstreamLog struct {
	mu     sync.Mutex
	bodies []string
}

func (l *algUpstreamLog) record(body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bodies = append(l.bodies, string(body))
}

func (l *algUpstreamLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

func (l *algUpstreamLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.bodies...)
}

// at returns the body of upstream request i (0-based), failing when it was never
// served.
func (l *algUpstreamLog) at(t *testing.T, i int) string {
	t.Helper()
	bodies := l.all()
	if i < 0 || i >= len(bodies) {
		t.Fatalf("upstream request %d was never served; served=%d bodies=%q", i, len(bodies), bodies)
	}
	return bodies[i]
}

// algScriptedOrigin returns one stateful multi-request reference-provider origin
// plus the log of every upstream body it served. Turn i answers with turns[i], and
// any request past the end of the script repeats the final turn, so an unexpected
// extra upstream leg stays observable in the count instead of becoming an error
// the fixture cannot assert on.
func algScriptedOrigin(t *testing.T, turns ...algUpstreamTurn) (http.Handler, *algUpstreamLog) {
	t.Helper()
	if len(turns) == 0 {
		t.Fatal("algScriptedOrigin requires at least one scripted turn")
	}
	log := &algUpstreamLog{}
	handler := refopenairesponses.NewHandler(refopenairesponses.Config{
		Responder: func(req refopenairesponses.Request) refopenairesponses.Response {
			log.record(req.Body)
			turn := turns[min(max(int(req.Sequence)-1, 0), len(turns)-1)]
			return refopenairesponses.Response{JSON: turn.JSON, SSE: turn.SSE}
		},
	})
	return handler, log
}

// algResourceJSON renders one completed upstream non-streaming response carrying
// exactly the planted output items.
func algResourceJSON(t *testing.T, id string, output []any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output":     output,
		"usage":      map[string]any{"input_tokens": 24, "output_tokens": 11, "total_tokens": 35},
	})
	if err != nil {
		t.Fatalf("marshal upstream resource %q: %v", id, err)
	}
	return string(raw)
}

// algMessageOutput returns the upstream output item for ordinary assistant text.
func algMessageOutput(id, text string) map[string]any {
	return map[string]any{
		"type": "message", "id": id, "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text}},
	}
}

// algControlOutput returns the upstream output item for one private control call.
// Distinct item and call identities let a cell plant two of them in one response.
func algControlOutput(itemID, callID, args string) map[string]any {
	return map[string]any{
		"type": "function_call", "id": itemID, "call_id": callID,
		"name": algControlToolName, "arguments": args,
	}
}

// algOrdinaryToolOutput returns the upstream output item for the ordinary
// client-owned tool call.
func algOrdinaryToolOutput() map[string]any {
	return map[string]any{
		"type": "function_call", "id": algOrdinaryItemID, "call_id": algOrdinaryCallID,
		"name": algOrdinaryToolName, "arguments": algOrdinaryToolArgs,
	}
}

// algCompletionTurn is the upstream wire of a valid private completion with no
// other model output.
func algCompletionTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
		algControlOutput(algControlItemID, algControlCallID, algControlArgs(t, algCompletionResult)),
	})}
}

// algOrdinaryToolTurn is the upstream wire of one ordinary client tool call and
// nothing else.
func algOrdinaryToolTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{algOrdinaryToolOutput()})}
}

// algUnmarkedStopTurn is the upstream wire of ordinary assistant text followed by
// a clean stop with no completion signal.
func algUnmarkedStopTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
		algMessageOutput("msg_"+id, algUnmarkedText),
	})}
}

// algRepairStopTurn is the upstream wire of the bounded protocol-repair leg that
// requirement 7.1 requests after an unmarked first turn: the model commits
// ordinary text and again ends cleanly WITHOUT the completion signal, which is
// the situation requirement 7.4 says must allow the new terminal instead of
// forcing an unbounded sequence of repair turns.
func algRepairStopTurn(t *testing.T, id string) algUpstreamTurn {
	t.Helper()
	return algUpstreamTurn{JSON: algResourceJSON(t, id, []any{
		algMessageOutput("msg_"+id, algRepairText),
	})}
}

// algScriptedMissingSignalOrigin is the approved two-leg missing-signal script
// used by every cell whose first turn carries no trusted completion signal: an
// unmarked first turn, then the unmarked repair leg. Both legs are scripted
// explicitly, so a cell can assert the exact upstream leg count instead of
// accepting whatever the origin happens to repeat.
func algScriptedMissingSignalOrigin(t *testing.T, first algUpstreamTurn, firstID string) (http.Handler, *algUpstreamLog) {
	t.Helper()
	return algScriptedOrigin(t, first, algRepairStopTurn(t, firstID+"_repair"))
}

// --- slice 10.1B client request bodies ----------------------------------------

// algClientTool is one client-declared tool definition on the A-leg. The
// parameterless schema keeps the A-leg wire small; the proxy owns no part of it.
func algClientTool(name string) map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        name,
		"description": "client-owned tool",
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"scope": map[string]any{"type": "string"}},
		},
	}
}

// algCreateBodyWith renders an A-leg create body with an arbitrary input document
// and an explicit client tool catalog.
func algCreateBodyWith(t *testing.T, input any, stream bool, tools ...string) string {
	t.Helper()
	doc := map[string]any{"model": "gpt-4o-mini", "input": input, "stream": stream}
	if len(tools) > 0 {
		names := make([]any, 0, len(tools))
		for _, name := range tools {
			names = append(names, algClientTool(name))
		}
		doc["tools"] = names
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal alg create body: %v", err)
	}
	return string(raw)
}

// algToolResultInput is the client answer to one tool call: the official
// function_call_output item the real frontend decodes into a canonical tool result.
func algToolResultInput(callID, output string) map[string]any {
	return map[string]any{"type": "function_call_output", "call_id": callID, "output": output}
}

// algEchoedToolCallInput is the client echo of one model tool call, which is what
// makes a correlated client-owned explicit-completion fact reachable at all.
func algEchoedToolCallInput(callID, name, args string) map[string]any {
	return map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": args}
}

// --- slice 10.1B assertions ---------------------------------------------------

// algWireBody is every client-visible frame payload joined, so a hygiene check
// reads exactly what the client received.
func algWireBody(frames []algWireFrame) string {
	var b strings.Builder
	for _, f := range frames {
		b.WriteString(f.Raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// algAssertNoPrivateControlFacts fails when any client-visible frame carries a
// private control name, call id, item id, or argument member. It tolerates
// ordinary client tool frames, which several slice 10.1B cells require.
func algAssertNoPrivateControlFacts(t *testing.T, frames []algWireFrame) {
	t.Helper()
	raw := algWireBody(frames)
	for _, secret := range []string{algControlToolName, algControlCallID, algControlItemID, `"result"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("client wire leaked private control fact %q:\n%s", secret, raw)
		}
	}
}

// algAssertNoPrivateControlIdentity is the narrower form used by the native
// collision cell, where the CLIENT legitimately owns a tool called
// attempt_completion: the private call identity and the private argument member
// must still be absent, because the proxy injected and called nothing there.
func algAssertNoPrivateControlIdentity(t *testing.T, frames []algWireFrame) {
	t.Helper()
	raw := algWireBody(frames)
	for _, secret := range []string{algControlCallID, algControlItemID, `"result"`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("client wire leaked private control fact %q:\n%s", secret, raw)
		}
	}
}

// algAssertNoPrivateControlStage fails when any internal observation stage — the
// real response-part hook or the real final-stream observer — exposed the private
// control tool name. The client stage is covered by the wire checks above.
func algAssertNoPrivateControlStage(t *testing.T, tr *algTrace) {
	t.Helper()
	for _, it := range tr.snapshot() {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// algAssertOrdinaryToolLifecycle proves the ordinary client tool lifecycle was
// neither suppressed nor duplicated: the real response-part hook and the real
// final-stream observation each saw exactly one start and one finish.
//
// The two stages are counted separately on purpose. They are two genuinely
// distinct observation points on two distinct goroutines, so one canonical start
// is expected to be seen once by each; counting them together would make the
// assertion about their sum rather than about either stage.
//
// Starts are matched by the client-owned tool name, because the canonical
// tool_call_started event carries it. Finishes are counted per stage without a name
// filter, because the canonical tool_call_finished event carries no tool name at
// all; each fixture below plants exactly one ordinary client tool call, so that
// per-stage total is that call's lifecycle and suppression or duplication of the
// finish event stays detectable.
func algAssertOrdinaryToolLifecycle(t *testing.T, tr *algTrace, wantName string, wantStarts, wantFinishes int) {
	t.Helper()
	starts := map[string]int{algTraceStageHook: 0, algTraceStageObserve: 0}
	finishes := map[string]int{algTraceStageHook: 0, algTraceStageObserve: 0}
	for _, it := range tr.snapshot() {
		switch it.kind {
		case string(aipapi.EventToolCallStarted):
			if it.tool == wantName {
				starts[it.stage]++
			}
		case string(aipapi.EventToolCallFinished):
			finishes[it.stage]++
		}
	}
	for _, stage := range []string{algTraceStageHook, algTraceStageObserve} {
		if starts[stage] != wantStarts {
			t.Fatalf("%s %q starts = %d, want %d; trace=%+v", stage, wantName, starts[stage], wantStarts, tr.snapshot())
		}
		if finishes[stage] != wantFinishes {
			t.Fatalf("%s tool-call finishes = %d, want %d; trace=%+v", stage, finishes[stage], wantFinishes, tr.snapshot())
		}
	}
}

// algAssertOrdinaryToolEvent proves the ordinary tool lifecycle really crossed the
// client boundary as an ordinary tool event, in the hook, in the observation
// stage, and on the wire.
func algAssertOrdinaryToolEvent(t *testing.T, tr *algTrace, frames []algWireFrame, name string) {
	t.Helper()
	items := tr.snapshot()
	hookStart, obsStart := -1, -1
	for _, it := range items {
		if it.tool != name {
			continue
		}
		if it.kind != string(aipapi.EventToolCallStarted) {
			continue
		}
		switch it.stage {
		case algTraceStageHook:
			if hookStart < 0 {
				hookStart = it.seq
			}
		case algTraceStageObserve:
			if obsStart < 0 {
				obsStart = it.seq
			}
		}
	}
	if hookStart < 0 || obsStart < 0 {
		t.Fatalf("the ordinary tool start never crossed both internal stages; trace=%+v", items)
	}
	if hookStart >= obsStart {
		t.Fatalf("the ordinary tool start must pass the response-part hook before the observation stage; hook=%d observe=%d trace=%+v", hookStart, obsStart, items)
	}
	algAssertOrdinaryToolLifecycle(t, tr, name, 1, 1)
	if !strings.Contains(algWireBody(frames), `"name":"`+name+`"`) {
		t.Fatalf("the ordinary tool call never reached the client as an ordinary tool event; wire=%s", algWireBody(frames))
	}
}

// algAssertBoundedUpstreamCount fails when the turn used more upstream legs than
// the approved bound, which is how an unbounded repair sequence would show up.
func algAssertBoundedUpstreamCount(t *testing.T, log *algUpstreamLog, want int) {
	t.Helper()
	if got := log.count(); got != want {
		t.Fatalf("upstream request count = %d, want %d; bodies=%q", got, want, log.all())
	}
}

// algAssertUpstreamCarries fails when an upstream request body does NOT carry text
// it must. The B-leg body is the only place the private protocol instruction and
// the private control tool legitimately appear, so this is the positive direction
// of the B-leg-only visibility contract.
func algAssertUpstreamCarries(t *testing.T, log *algUpstreamLog, i int, marker string) {
	t.Helper()
	if body := log.at(t, i); !strings.Contains(body, marker) {
		t.Fatalf("upstream request %d did not carry %q although the protocol was active:\n%s", i, marker, body)
	}
}

// algAssertUpstreamLacks fails when an upstream request body carries text it must
// not. The B-leg body is where a private instruction legitimately appears, so this
// is used only for the negative direction.
func algAssertUpstreamLacks(t *testing.T, log *algUpstreamLog, marker string) {
	t.Helper()
	for i, body := range log.all() {
		if strings.Contains(body, marker) {
			t.Fatalf("upstream request %d carried %q although the protocol is inactive:\n%s", i, marker, body)
		}
	}
}

// --- slice 10.1B matrix cells -------------------------------------------------

// TestPreferredProtocolE2E_ordinaryClientToolThenCompletionPreservesToolLifecycle
// is acceptance-matrix row 3: an ordinary client-visible tool call first, the
// client's answer honored, and the private completion on the following turn.
//
// The first turn carries no completion signal, so requirement 7.1 legitimately
// suppresses its terminal for ONE bounded repair leg, and requirement 7.4 allows
// the new terminal when that leg also stops unmarked. The ordinary tool call is
// NOT held back by that: requirement 5.1 forbids buffering ordinary client tool
// events until terminal, and requirement 5.3 keeps existing client-release
// behavior authoritative. So the cell pins that the ordinary lifecycle crossed the
// client boundary exactly once on the first turn, that the client's answer was
// forwarded upstream exactly once and never re-executed (requirement 8.3), and
// that the bounded result is published exactly once at the accepted terminal of the
// completing turn. The private control call must still never appear anywhere.
func TestPreferredProtocolE2E_ordinaryClientToolThenCompletionPreservesToolLifecycle(t *testing.T) {
	origin, upstream := algScriptedOrigin(t,
		algOrdinaryToolTurn(t, "resp_alg_tool_turn"),
		algRepairStopTurn(t, "resp_alg_tool_repair"),
		algCompletionTurn(t, "resp_alg_tool_completion"),
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	firstBody := algCreateBodyWith(t, algOrdinaryToolInput, false, algOrdinaryToolName)
	status, firstFrames, err := algPostCreateBody(t.Context(), d, firstBody, false, tr)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("first status = %d, want 200; wire=%s", status, algWireBody(firstFrames))
	}

	// Turn one: the client gets exactly one ordinary tool call and no invented
	// assistant answer beyond the repair leg's own committed text.
	algAssertOrdinaryToolEvent(t, tr, firstFrames, algOrdinaryToolName)
	if got := algWireText(firstFrames); got != algRepairText {
		t.Fatalf("client assistant text on the tool turn = %q, want only the repair leg's committed text %q", got, algRepairText)
	}
	if strings.Contains(algWireBody(firstFrames), algCompletionResult) {
		t.Fatal("a completion result was published on the turn that did not complete")
	}
	algAssertNoPrivateControlFacts(t, firstFrames)
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// The client answers its own tool on the next turn. The answer is forwarded
	// upstream exactly once, so the proxy neither dropped nor duplicated it.
	secondBody := algCreateBodyWith(t, []any{algToolResultInput(algOrdinaryCallID, algToolResultOutput)}, false, algOrdinaryToolName)
	status, secondFrames, err := algPostCreateBody(t.Context(), d, secondBody, false, tr)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("second status = %d, want 200; wire=%s", status, algWireBody(secondFrames))
	}
	if got := strings.Count(upstream.at(t, 2), algOrdinaryCallID); got != 1 {
		t.Fatalf("client tool result call id appears %d times upstream, want exactly 1; body=%s", got, upstream.at(t, 2))
	}
	if strings.Contains(upstream.at(t, 2), algToolResultOutput) && strings.Count(upstream.at(t, 2), algToolResultOutput) != 1 {
		t.Fatalf("the client tool result was forwarded more than once upstream; body=%s", upstream.at(t, 2))
	}

	// The bounded result is published exactly once on the completing turn.
	if got := algWireText(secondFrames); got != algCompletionResult {
		t.Fatalf("client assistant text on the completing turn = %q, want exactly %q", got, algCompletionResult)
	}
	if strings.Contains(algWireBody(secondFrames), algOrdinaryCallID) {
		t.Fatalf("the ordinary tool call was replayed to the client on the completing turn:\n%s", algWireBody(secondFrames))
	}
	algAssertNoPrivateControlFacts(t, secondFrames)

	// The ordinary lifecycle ran once in total across both client requests, was
	// never duplicated, and the private control name never entered any stage.
	algAssertOrdinaryToolLifecycle(t, tr, algOrdinaryToolName, 1, 1)
	algAssertNoPrivateControlStage(t, tr)
	algAssertBoundedUpstreamCount(t, upstream, 3)
}

// algMalformedControlCallCase is one planted control-call argument shape the
// frozen control contract must reject. Requirement 5.5 and the design handler
// semantics pin every one of them to the same bounded outcome: the call is
// swallowed privately and never becomes client tool execution.
type algMalformedControlCallCase struct {
	name string
	args string
}

// algMalformedControlCallCases covers the missing, extra, wrong-typed, empty, and
// trailing-value argument shapes plus one oversized result. Every case must produce
// the same observable outcome, so the table is also the cell's discrimination
// matrix: no case may publish a result, and no case may reach the client as a tool.
func algMalformedControlCallCases() []algMalformedControlCallCase {
	oversized := strings.Repeat("a", controltool.MaxResultTextBytes+1)
	return []algMalformedControlCallCase{
		{name: "missing_result", args: `{"summary":"the work is done"}`},
		{name: "extra_member", args: `{"result":"` + algUnmarkedText + `","command":"reboot the proxy"}`},
		{name: "wrong_typed_result", args: `{"result":42}`},
		{name: "empty_result", args: `{"result":"   "}`},
		{name: "trailing_value", args: `{"result":"` + algUnmarkedText + `"}{"result":"` + algUnmarkedText + `"}`},
		{name: "oversized_result", args: `{"result":"` + oversized + `"}`},
	}
}

// TestPreferredProtocolE2E_malformedControlCallIsNeverPublishedOrExecuted is the
// malformed/oversized half of acceptance-matrix row 12.
//
// Requirement 5.5 and the design handler semantics pin the outcome of an invalid
// control-call argument shape: the call is swallowed privately and never becomes
// client tool execution, and no trusted completion fact is recorded (requirement
// 6.1 requires strict valid JSON with one non-empty bounded `result`). A turn that
// therefore carries no trusted completion signal is exactly requirement 7.1's
// missing-signal case: the clean terminal is suppressed and ONE bounded hidden
// protocol-repair continuation is requested. The repair leg itself again ends
// without a completion signal, so requirement 7.4 requires the new terminal to be
// allowed rather than an unbounded repair sequence. The client therefore sees both
// of the model's own committed texts, exactly once each, and nothing else.
func TestPreferredProtocolE2E_malformedControlCallIsNeverPublishedOrExecuted(t *testing.T) {
	for _, tc := range algMalformedControlCallCases() {
		t.Run(tc.name, func(t *testing.T) {
			first := algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_malformed_"+tc.name, []any{
				algMessageOutput("msg_alg_malformed", algUnmarkedText),
				algControlOutput(algControlItemID, algControlCallID, tc.args),
			})}
			origin, upstream := algScriptedMissingSignalOrigin(t, first, "resp_alg_malformed_repair_"+tc.name)
			d, tr := algDeployPreferred(t, TransportJSON, origin)

			body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
			status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			// Requirement 7.4 with requirement 5.6: the bounded repair leg is spent
			// and the turn then terminates normally with its committed output
			// preserved. A provider error surfacing here would be a leak.
			if status != http.StatusOK {
				t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
			}

			// Exactly the two approved legs: the unmarked turn and one repair leg.
			// A third leg would be the unbounded repair sequence requirement 7.4
			// forbids.
			algAssertBoundedUpstreamCount(t, upstream, 2)

			// Requirement 7.2: the repair leg carries the fixed recovery control
			// text, which requirement 3.1 keeps on the B-leg only.
			algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
			algAssertNoPrivateControlStage(t, tr)

			// No result may be published and no ordinary text may be duplicated:
			// exact equality over both committed texts proves neither an invented
			// result nor a replayed commit.
			if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
				t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q", got, want)
			}
			// The malformed call must not become client tool execution, and no
			// private control fact may reach the client.
			algAssertNoPrivateControlLeak(t, frames)
			algAssertNoPrivateControlFacts(t, frames)
		})
	}
}

// TestPreferredProtocolE2E_multipleControlCallsRevokeTheCompletion is the multiple
// half of acceptance-matrix row 12.
//
// The design capture rules pin V1 to "only one control call may be active per
// response" and "multiple control calls mark protocol invalid and are swallowed
// rather than leaked to client execution". The first call therefore never becomes a
// trusted completion fact (requirement 6.1), so the turn carries no completion
// signal and requirement 7.1 suppresses its terminal for ONE bounded repair leg;
// that leg again ends unmarked, so requirement 7.4 allows the new terminal. The
// client sees the two model texts exactly once each, no completion result from the
// revoked call, and neither control call as client tool execution.
func TestPreferredProtocolE2E_multipleControlCallsRevokeTheCompletion(t *testing.T) {
	const secondCallID = "call_alg_private_control_second"
	const secondItemID = "fc_alg_private_control_second"
	first := algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_multiple_calls", []any{
		algMessageOutput("msg_alg_multiple", algUnmarkedText),
		algControlOutput(algControlItemID, algControlCallID, algControlArgs(t, algCompletionResult)),
		algControlOutput(secondItemID, secondCallID, algControlArgs(t, algCompletionResult)),
	})}
	origin, upstream := algScriptedMissingSignalOrigin(t, first, "resp_alg_multiple_repair")
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
	}

	// Exactly one repair leg, then the allowed terminal.
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// The revoked completion publishes nothing: the answer is exactly the two
	// ordinary model texts and neither completion result.
	if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q with no result", got, want)
	}
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("the revoked completion was still published:\n%s", algWireBody(frames))
	}
	// Neither control call may become client tool execution, and neither private
	// identity may leak or appear in any internal stage.
	algAssertNoPrivateControlLeak(t, frames)
	algAssertNoPrivateControlFacts(t, frames)
	algAssertNoPrivateControlStage(t, tr)
	if strings.Contains(algWireBody(frames), secondCallID) {
		t.Fatalf("the second control call reached the client:\n%s", algWireBody(frames))
	}
}

// TestPreferredProtocolE2E_nativeCompletionCollisionStaysClientOwned is
// acceptance-matrix row 15. The CLIENT declares its own attempt_completion tool, so
// proxy injection fails closed on the name collision and the client's tool stays
// client-owned. The model calls it natively, the client answers it, and the
// correlated native explicit-completion fact must allow the terminal without a
// hidden repair leg and without any second, proxy-owned answer.
func TestPreferredProtocolE2E_nativeCompletionCollisionStaysClientOwned(t *testing.T) {
	origin, upstream := algScriptedOrigin(t,
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_native_call", []any{
			algControlOutput(algNativeItemID, algNativeCallID, algNativeArgs),
		})},
		algUnmarkedStopTurn(t, "alg_native_answer"),
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	firstBody := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false, algControlToolName)
	status, firstFrames, err := algPostCreateBody(t.Context(), d, firstBody, false, tr)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("first status = %d, want 200; wire=%s", status, algWireBody(firstFrames))
	}

	// The client's own tool is not relabelled or intercepted by name: its call
	// reaches the client as an ordinary tool event.
	algAssertOrdinaryToolEvent(t, tr, firstFrames, algControlToolName)
	algAssertNoPrivateControlIdentity(t, firstFrames)

	// Proxy injection failed closed on the collision: no private instruction and no
	// second copy of the tool were added to the B-leg.
	algAssertUpstreamLacks(t, upstream, algProtocolInstructionMarker)

	// No missing-signal repair leg was created for the inactive protocol.
	algAssertBoundedUpstreamCount(t, upstream, 1)

	// The client answers its own tool; the correlated native fact allows the
	// terminal, and nothing is invented on top of it.
	secondBody := algCreateBodyWith(t, []any{
		algEchoedToolCallInput(algNativeCallID, algControlToolName, algNativeArgs),
		algToolResultInput(algNativeCallID, algNativeResult),
	}, false, algControlToolName)
	status, secondFrames, err := algPostCreateBody(t.Context(), d, secondBody, false, tr)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("second status = %d, want 200; wire=%s", status, algWireBody(secondFrames))
	}
	if got := algWireText(secondFrames); got != algUnmarkedText {
		t.Fatalf("client assistant text = %q, want only the model's own answer %q with no duplicate answer", got, algUnmarkedText)
	}
	algAssertNoPrivateControlIdentity(t, secondFrames)
	algAssertBoundedUpstreamCount(t, upstream, 2)
	algAssertUpstreamLacks(t, upstream, algRepairInstructionMarker)
}

// TestPreferredProtocolE2E_missingSignalIsBoundedAndInventsNothing is the
// clean-stop-without-a-control-call case of acceptance-matrix row 4 and the
// terminal half of requirement 7.4.
//
// The model stops cleanly with no control call at all. Requirement 7.1 requires the
// clean terminal to be suppressed and ONE bounded hidden protocol-repair
// continuation to be requested; the repair leg also stops without a completion
// signal, so requirement 7.4 requires the new terminal to be ALLOWED. The cell
// pins all four approved properties at once: no invented result, exactly one
// bounded repair leg carrying the fixed requirement 7.2 control text, both model
// texts delivered exactly once, and no private fact anywhere.
func TestPreferredProtocolE2E_missingSignalIsBoundedAndInventsNothing(t *testing.T) {
	origin, upstream := algScriptedMissingSignalOrigin(t, algUnmarkedStopTurn(t, "resp_alg_missing_signal"), "resp_alg_missing_signal_repair")
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
	}

	// Bounded: requirement 10.1 fixes the default reprompt budget at one, so the
	// repaired turn is the last upstream leg and the client gets its terminal.
	algAssertBoundedUpstreamCount(t, upstream, 2)

	// Requirement 7.2: the bounded repair leg carries the fixed recovery control
	// text on the B-leg. Requirement 7.4: no third leg follows it.
	algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)

	// Nothing was invented and nothing was duplicated: the client answer is exactly
	// the two ordinary texts the model itself committed, once each.
	if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q", got, want)
	}
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a completion result was invented on a turn with no control call:\n%s", algWireBody(frames))
	}
	// Requirement 3.1/3.5: the private control name stays off every stage and off
	// the wire even though the protocol was active and advertised on the B-leg.
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// =============================================================================
// Slice 10.1C — remaining matrix rows, protocol columns and the legacy contrast
// =============================================================================
//
// The six cells below reuse the same real seam slice 10.1A deployed, now selected
// per cell by algColumn. Each drives a complete deployment: real frontend handler
// on a real httptest origin, real runtime executor, real backend adapter, and the
// real reference-provider origin emulator. No provider fake is assigned
// anywhere; the control provider and the terminal provider under test are the
// ones the production feature registry, enabled-surface merge, and production
// request-snapshot builder compose.
//
// Cases owned here (design Runtime / Acceptance Matrix rows 5, 13, 14, 19; plus
// requirement 9.6):
//
//  1. user-input-needed stop -> one repair -> second unmarked stop: the default
//     cap of one is spent on the repair leg and the A-side terminal is allowed;
//  2. backend tools unsupported: the protocol stays inactive for that B-leg and a
//     client that demands tools from a tool-less backend fails explicitly;
//  3. the ToolChoice matrix: which choices make the control tool eligible and
//     which suppress it, and that a suppressed choice never weakens the client;
//  4. item-authority twins of rows 1 and 2 under the OpenResponses frontend;
//  5. the Anthropic client-facing protocol column;
//  6. the Gemini client-facing protocol column;
//  7. the legacy-versus-preferred end-to-end contrast of requirement 9.6.

// --- slice 10.1C planted facts -----------------------------------------------

// algUserInputText is the ordinary assistant text the model commits when it asks
// the user for the information it legitimately needs (acceptance-matrix row 5,
// design Legitimate User-Input Case). It is deliberately a user-facing question
// so the cell's second leg is the "recognizes user input is required, repeats the
// request and stops unmarked" shape the design names.
const algUserInputText = "Which environment should the migration target: staging or production?"

// algUserInputRepairText is the ordinary assistant text the model commits on the
// bounded repair leg that still needs user input and again ends unmarked.
const algUserInputRepairText = "I still need the target environment before I can apply the migration."

// algRepairSignalClause and algRepairUserInputClause are the two semantically
// fixed requirement 7.2/7.3 clauses the cells below assert byte-exactly on the
// bounded repair leg. They are quoted from the production instruction in its
// BACKEND-WIRE form, because the B-leg request body is JSON and therefore carries
// the instruction's newlines as the two characters \ and n; the clause wording
// itself is never restated as a weakened paraphrase.
const (
	algRepairSignalClause     = "The previous model turn ended without the required `attempt_completion` signal."
	algRepairUserInputClause  = "If further progress requires user input, permission, credentials, clarification,\\nor a choice, request that input normally and end. Do not assume it."
	algUnsupportedToolsCode   = `"code":"unsupported_parameter"`
	algUnsupportedToolsDetail = "missing required capabilities: tools"
)

// algChatTextTurn renders one chat/completions upstream turn carrying exactly one
// assistant message. A compatible-profile backend in the `openai-chat-compatible`
// family speaks that wire, not the Responses wire the scripted origin above uses,
// so this is the minimal fixture for acceptance-matrix row 13.
func algChatTextTurn(t *testing.T, id, text string) algUpstreamTurn {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion", "created": 1715620000, "model": "gpt-4o-mini",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19},
	})
	if err != nil {
		t.Fatalf("marshal chat turn %q: %v", id, err)
	}
	return algUpstreamTurn{JSON: string(raw)}
}

// algScriptedChatOrigin is [algScriptedOrigin] over the real chat/completions
// reference-provider origin, so a compatible-profile backend column is scripted
// and counted exactly like the Responses column.
func algScriptedChatOrigin(t *testing.T, turns ...algUpstreamTurn) (http.Handler, *algUpstreamLog) {
	t.Helper()
	if len(turns) == 0 {
		t.Fatal("algScriptedChatOrigin requires at least one scripted turn")
	}
	log := &algUpstreamLog{}
	handler := refopenaichat.NewHandler(refopenaichat.Config{
		Responder: func(req refopenaichat.Request) refopenaichat.Response {
			log.record(req.Body)
			turn := turns[min(max(int(req.Sequence)-1, 0), len(turns)-1)]
			return refopenaichat.Response{JSON: turn.JSON, SSE: turn.SSE}
		},
	})
	return handler, log
}

// --- slice 10.1C client drivers ----------------------------------------------

// algPostProtocolBody drives one client request against a client-facing protocol
// column whose response resource is not OpenAI-Responses-shaped, and returns the
// exact body the client received. Anthropic and Gemini encode their assistant
// text in their own members, so this driver reads the wire instead of projecting
// it, which is what keeps the columns honest about what those clients see.
func algPostProtocolBody(ctx context.Context, d *Deployment, path, body string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL()+path, strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Route", d.RouteSelector)
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(raw), nil
}

// algAnthropicText decodes the assistant text an Anthropic client received.
func algAnthropicText(t *testing.T, raw string) string {
	t.Helper()
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode anthropic message resource: %v; wire=%s", err, raw)
	}
	var b strings.Builder
	for _, block := range decoded.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// algGeminiText decodes the assistant text a Gemini client received.
func algGeminiText(t *testing.T, raw string) string {
	t.Helper()
	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode gemini generateContent resource: %v; wire=%s", err, raw)
	}
	var b strings.Builder
	for _, candidate := range decoded.Candidates {
		for _, part := range candidate.Content.Parts {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// --- slice 10.1C assertions ---------------------------------------------------

// algAssertUpstreamInjectsControlTool fails when the B-leg body does NOT carry the
// proxy-owned control tool definition. It is the positive direction of the
// activation contract: an active protocol advertises the tool to the selected
// B-leg model only (requirement 3.1).
func algAssertUpstreamInjectsControlTool(t *testing.T, log *algUpstreamLog, i int) {
	t.Helper()
	if body := log.at(t, i); !strings.Contains(body, algControlToolName) {
		t.Fatalf("upstream request %d did not advertise the proxy-owned control tool although the protocol was active:\n%s", i, body)
	}
}

// algAssertUpstreamDoesNotInjectControlTool fails when ANY upstream request body
// carries the proxy-owned control tool name. An ineligible or inactive protocol
// must leave the backend-effective tool catalog exactly as the client declared it
// (requirements 4.1, 4.3, 4.4, 4.5 and design V1 Eligibility Matrix).
func algAssertUpstreamDoesNotInjectControlTool(t *testing.T, log *algUpstreamLog) {
	t.Helper()
	for i, body := range log.all() {
		if strings.Contains(body, algControlToolName) {
			t.Fatalf("upstream request %d advertised the proxy-owned control tool although the protocol was inactive:\n%s", i, body)
		}
	}
}

// algAssertProtocolInactive is the shared inactive-protocol proof used by every
// slice 10.1C eligibility cell: the terminal was allowed on the FIRST upstream
// leg, no private instruction was injected, no proxy-owned tool was advertised,
// and no private fact reached the client wire.
//
// Requirement 7.6 is the load-bearing half: a candidate whose protocol was never
// active must not receive a missing-signal continuation merely because a
// completion marker is absent, and requirement 4.5 forbids falling back to the
// legacy semantic verifier, which would also show up here as an extra upstream
// leg.
func algAssertProtocolInactive(t *testing.T, d *Deployment, upstream *algUpstreamLog, backendID string, tr *algTrace, frames []algWireFrame) {
	t.Helper()
	algAssertBoundedUpstreamCount(t, upstream, 1)
	algAssertUpstreamLacks(t, upstream, algProtocolInstructionMarker)
	algAssertUpstreamLacks(t, upstream, algRepairInstructionMarker)
	algAssertUpstreamDoesNotInjectControlTool(t, upstream)
	if tr != nil {
		algAssertNoPrivateControlStage(t, tr)
	}
	algAssertNoPrivateControlFacts(t, frames)
	if got := d.RequestCount(backendID); got != 1 {
		t.Fatalf("harness origin request count = %d, want exactly 1", got)
	}
}

// algAssertEarlyStreamOrder is the sound form of the explicit early-stream
// observation assertion the task validation line requires, for the streaming
// item-authority twin.
//
// The internal ordering is read from the shared trace, where the response-part
// hook and the final-stream observation are two ordered stages of the same
// response pipeline. The client-side ordering is read from the wire frames
// themselves rather than from the trace: the trace is appended by the
// response-pipeline goroutine AND the client-reader goroutine under one mutex, so
// one side's trace index has no happens-before edge against the other side's.
// Frame order is the only sound client-side ordering source, and it still proves
// streaming rather than withholding because the text delta precedes the response
// completion the client reads.
func algAssertEarlyStreamOrder(t *testing.T, tr *algTrace, frames []algWireFrame, text string) {
	t.Helper()

	items := tr.snapshot()
	hookAt := tr.firstIndex(algTraceStageHook, string(aipapi.EventTextDelta), text)
	obsAt := tr.firstIndex(algTraceStageObserve, string(aipapi.EventTextDelta), text)
	if hookAt < 0 || obsAt < 0 {
		t.Fatalf("the streamed text never crossed both internal stages (hook=%d observe=%d); trace=%+v", hookAt, obsAt, items)
	}
	if hookAt >= obsAt {
		t.Fatalf("the response-part hook must run before the final-stream observation; hook=%d observe=%d trace=%+v", hookAt, obsAt, items)
	}
	finishObserved := algFirstKind(items, algTraceStageObserve, string(aipapi.EventResponseFinished))
	if finishObserved < 0 || obsAt >= finishObserved {
		t.Fatalf("ordinary text must be observed before the backend terminal; text=%d finish=%d trace=%+v", obsAt, finishObserved, items)
	}

	deltaAt := slices.IndexFunc(frames, func(f algWireFrame) bool {
		return f.Type == "response.output_text.delta" && f.Delta == text
	})
	completedAt := slices.IndexFunc(frames, func(f algWireFrame) bool {
		return f.Type == "response.completed"
	})
	if deltaAt < 0 || completedAt < 0 {
		t.Fatalf("the client never received both the text delta and the response completion; delta=%d completed=%d frames=%+v", deltaAt, completedAt, frames)
	}
	if deltaAt >= completedAt {
		t.Fatalf("the client must receive the ordinary text delta before the response completion frame; delta=%d completed=%d frames=%+v", deltaAt, completedAt, frames)
	}
	for _, it := range items {
		if it.tool == algControlToolName {
			t.Fatalf("%s exposed the private control tool name at seq %d: %+v", it.stage, it.seq, it)
		}
	}
}

// --- slice 10.1C cells --------------------------------------------------------

// TestPreferredProtocolE2E_userInputStopRepromptsOnceThenStops is
// acceptance-matrix row 5 and requirement 12.5's "second unmarked stop after
// repair".
//
// The design's Legitimate User-Input Case names the exact sequence this cell
// pins:
//
//	B1 asks the user for required information and stops unmarked
//	  -> hidden protocol repair B2
//	B2 recognizes user input is required, repeats the request and stops unmarked
//	  -> cap exhausted -> A-side terminal is allowed
//
// V1 deliberately spends one hidden B-leg to distinguish an accidental stop from a
// stable unmarked stop. So the approved bounded behavior is: requirement 7.1
// suppresses the first terminal for exactly ONE repair leg, requirement 7.2/7.3
// make that leg state both the missing signal and the requirement that the worker
// ask for user input normally instead of assuming it, requirement 7.4 and the
// requirement 10.1 default cap of one allow the new terminal instead of forcing a
// second repair leg, and requirement 4.6 keeps the outcome conservative: the two
// user-facing requests the model itself committed reach the client exactly once
// each, and no completion signal is invented.
func TestPreferredProtocolE2E_userInputStopRepromptsOnceThenStops(t *testing.T) {
	origin, upstream := algScriptedOrigin(t,
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_userinput_b1", []any{
			algMessageOutput("msg_alg_userinput_b1", algUserInputText),
		})},
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_userinput_b2", []any{
			algMessageOutput("msg_alg_userinput_b2", algUserInputRepairText),
		})},
	)
	d, tr := algDeployPreferred(t, TransportJSON, origin)

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false)
	status, frames, err := algPostCreateBody(t.Context(), d, body, false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Requirement 7.4 with requirement 5.6: the bounded repair leg is spent and
	// the turn then terminates normally with its committed output preserved.
	if status != http.StatusOK {
		t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, algWireBody(frames))
	}

	// Bounded: the requirement 10.1 default cap is one reprompt, so the repaired
	// turn is the last upstream leg. A third leg would be the unbounded repair
	// sequence requirement 7.4 forbids, and this is the assertion that proves the
	// cap rather than merely counting legs.
	algAssertBoundedUpstreamCount(t, upstream, 2)

	// Requirement 7.2: the repair leg carries the fixed recovery control text, and
	// requirement 7.3 keeps the clause that lets the worker ask the user instead of
	// claiming completion. Both are asserted byte-exactly, so a shortened or
	// reordered instruction cannot pass.
	algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
	algAssertUpstreamCarries(t, upstream, 1, algRepairSignalClause)
	algAssertUpstreamCarries(t, upstream, 1, algRepairUserInputClause)
	// The first leg advertised the base protocol (requirement 3.1).
	algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)

	// Nothing was invented and nothing was duplicated: the client answer is exactly
	// the two user-facing texts the model itself committed, once each. The repair
	// leg did not fabricate a completion result just because it ran out of budget.
	if got, want := algWireText(frames), algUserInputText+algUserInputRepairText; got != want {
		t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q", got, want)
	}
	if strings.Contains(algWireBody(frames), algCompletionResult) {
		t.Fatalf("a completion result was invented on a turn that ended without the signal:\n%s", algWireBody(frames))
	}
	// Requirement 3.1/3.5: the private control name stays off every stage and off
	// the wire even though the protocol was active and advertised on the B-leg.
	algAssertNoPrivateControlStage(t, tr)
	algAssertNoPrivateControlLeak(t, frames)
}

// TestPreferredProtocolE2E_unsupportedBackendToolsStayInactiveOrFailExplicitly is
// acceptance-matrix row 13, requirement 4.1, and the design V1 Eligibility Matrix
// row "backend lacks tools".
//
// The backend is a real compatible-provider profile whose declared capabilities
// disable tools, driven through the real standard family compiler and the real
// OpenAI-Responses client frontend. Two subcases pin both halves of the approved
// explicit capability-mismatch behavior:
//
//   - a client that DEMANDS tools from a tool-less backend fails explicitly at
//     admission with a bounded, classified error and no upstream leg at all,
//     rather than having its required semantics silently dropped;
//   - a client that demands no tools gets a normal answer, and the completion
//     protocol stays INACTIVE for that B-leg: nothing is advertised, no prose
//     emulation is added, and the unmarked stop is NOT turned into a missing-signal
//     continuation (requirement 7.6) and NOT turned into a legacy verifier fallback
//     (requirement 4.5).
func TestPreferredProtocolE2E_unsupportedBackendToolsStayInactiveOrFailExplicitly(t *testing.T) {
	const profile = "morph"

	t.Run("client_tools_fail_explicitly", func(t *testing.T) {
		origin, upstream := algScriptedChatOrigin(t, algChatTextTurn(t, "chatcmpl_alg_tools", algUnmarkedText))
		d, tr := algDeployColumn(t, algColumn{
			Strategy:  AgentLoopGuardStrategyAttemptCompletion,
			Frontend:  FrontendOpenAIResponses,
			Backend:   BackendCompatibleOpenAI,
			ProfileID: profile,
			Transport: TransportJSON,
			Origin:    origin,
		})

		status, frames, err := algPostCreateBody(t.Context(), d, algCreateBodyWith(t, algOrdinaryToolInput, false, algOrdinaryToolName), false, tr)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want the explicit capability rejection, never a silently degraded answer; wire=%s", status, algWireBody(frames))
		}
		wire := algWireBody(frames)
		if !strings.Contains(wire, algUnsupportedToolsDetail) || !strings.Contains(wire, algUnsupportedToolsCode) {
			t.Fatalf("the capability mismatch was not reported as a classified, bounded client error; wire=%s", wire)
		}
		// The candidate never opened upstream, so nothing was advertised anywhere.
		algAssertBoundedUpstreamCount(t, upstream, 0)
		algAssertUpstreamDoesNotInjectControlTool(t, upstream)
		if strings.Contains(wire, algCompletionResult) {
			t.Fatalf("a completion result leaked on a rejected candidate:\n%s", wire)
		}
	})

	t.Run("protocol_inactive_without_client_tools", func(t *testing.T) {
		origin, upstream := algScriptedChatOrigin(t, algChatTextTurn(t, "chatcmpl_alg_notools", algUnmarkedText))
		d, tr := algDeployColumn(t, algColumn{
			Strategy:  AgentLoopGuardStrategyAttemptCompletion,
			Frontend:  FrontendOpenAIResponses,
			Backend:   BackendCompatibleOpenAI,
			ProfileID: profile,
			Transport: TransportJSON,
			Origin:    origin,
		})

		status, frames, err := algPostCreateBody(t.Context(), d, algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false), false, tr)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want the conservative final answer for an inactive protocol; wire=%s", status, algWireBody(frames))
		}
		algAssertProtocolInactive(t, d, upstream, BackendCompatibleOpenAI, tr, frames)
		if got := algWireText(frames); got != algUnmarkedText {
			t.Fatalf("client assistant text = %q, want exactly the model's own committed text %q with nothing invented", got, algUnmarkedText)
		}
	})
}

// algToolChoiceCase is one row of acceptance-matrix row 14: a client tool-choice
// dialect the A-leg may legitimately send, plus the activation the design V1
// Eligibility Matrix pins for it.
//
// wantActive is read straight off that matrix: only the omitted/default-auto and
// explicit-auto rows may activate the proxy-owned completion tool. Every other row
// is ineligible, and eligibility is decided from the canonical ToolChoice the
// real frontend decoded, not from the raw wire string, so a dialect that maps
// onto the same canonical mode (`required` on the Responses wire decodes to the
// arbitrary-required mode) is covered by the canonical behaviour.
type algToolChoiceCase struct {
	name       string
	frontend   string
	path       string
	toolChoice any
	declare    bool
	wantActive bool
}

// algToolChoiceCases is the full approved matrix. The allowed-tools subset row
// lives on the item-authority OpenResponses column because the OpenAI-Responses
// wire has no allowed-tools dialect; every other row is exercised on the
// message-authority OpenAI-Responses column.
func algToolChoiceCases() []algToolChoiceCase {
	return []algToolChoiceCase{
		{name: "unset_tool_choice_is_eligible", toolChoice: nil, declare: true, wantActive: true},
		{name: "explicit_auto_is_eligible", toolChoice: "auto", declare: true, wantActive: true},
		{name: "none_suppresses_the_control_tool", toolChoice: "none", declare: false},
		{name: "arbitrary_required_suppresses_the_control_tool", toolChoice: "required", declare: true},
		{name: "named_required_suppresses_the_control_tool", toolChoice: map[string]any{"type": "function", "function": map[string]any{"name": algOrdinaryToolName}}, declare: true},
		{
			name:       "allowed_tools_subset_suppresses_the_control_tool",
			frontend:   FrontendOpenResponses,
			path:       "/openresponses/v1/responses",
			toolChoice: map[string]any{"type": "allowed_tools", "mode": "auto", "tools": []any{map[string]any{"type": "function", "name": algOrdinaryToolName}}},
			declare:    true,
		},
	}
}

// TestPreferredProtocolE2E_toolChoiceMatrixActivatesOnlyForAuto is
// acceptance-matrix row 14 with requirements 4.2, 4.3, 4.5 and 2.3-2.7 as the
// activation contract.
//
// Requirement 4.3 is the point of the matrix: a client constraint that hidden tool
// injection would broaden must suppress the proxy-owned completion tool, and the
// constraint itself must not be weakened. So an eligible row is proved by the
// positive evidence that the B-leg carries both the base instruction and the
// frozen tool definition, plus the bounded missing-signal repair leg that only an
// active protocol may request; an ineligible row is proved by the absence of all
// three, which also proves requirement 7.6 and the requirement 4.5 refusal to fall
// back to the legacy semantic verifier.
//
// The two eligible rows additionally pin the frozen model-facing contract of
// requirements 2.3-2.7 on the real wire: the tool is named exactly
// `attempt_completion`, its schema requires exactly one string `result`, it
// rejects additional properties, it exposes no `command` parameter, and its
// description states the call-only-after-completion rule.
func TestPreferredProtocolE2E_toolChoiceMatrixActivatesOnlyForAuto(t *testing.T) {
	for _, tc := range algToolChoiceCases() {
		t.Run(tc.name, func(t *testing.T) {
			origin, upstream := algScriptedOrigin(t,
				algUnmarkedStopTurn(t, "resp_alg_matrix_"+tc.name),
				algRepairStopTurn(t, "resp_alg_matrix_"+tc.name+"_repair"),
			)
			frontend, path := FrontendOpenAIResponses, "/v1/responses"
			if tc.frontend != "" {
				frontend, path = tc.frontend, tc.path
			}
			d, tr := algDeployColumn(t, algColumn{
				Strategy:  AgentLoopGuardStrategyAttemptCompletion,
				Frontend:  frontend,
				Backend:   BackendOpenAIResponses,
				Transport: TransportJSON,
				Origin:    origin,
			})

			doc := map[string]any{"model": "gpt-4o-mini", "input": "apply the schema, verify the backfill, then report the result", "stream": false}
			if frontend == FrontendOpenResponses {
				doc["store"] = false
			}
			if tc.declare {
				tools := make([]any, 0, 1)
				tools = append(tools, algClientTool(algOrdinaryToolName))
				doc["tools"] = tools
			}
			if tc.toolChoice != nil {
				doc["tool_choice"] = tc.toolChoice
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal tool-choice matrix body: %v", err)
			}
			status, frames, err := algPostCreateAtPath(t.Context(), d, path, string(raw), false, tr)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
			}

			if !tc.wantActive {
				// Requirement 4.3 + 4.5 + 7.6: no hidden tool, no hidden
				// instruction, no hidden repair leg, no invented result, and the
				// client's own answer preserved exactly once.
				algAssertProtocolInactive(t, d, upstream, BackendOpenAIResponses, tr, frames)
				if got, want := algWireText(frames), algUnmarkedText; got != want {
					t.Fatalf("client assistant text = %q, want exactly the model's own committed text %q", got, want)
				}
				return
			}

			// Requirement 4.2: the active rows advertise the protocol on the B-leg
			// only, and spend exactly the one bounded repair leg.
			algAssertBoundedUpstreamCount(t, upstream, 2)
			algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
			algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
			algAssertUpstreamInjectsControlTool(t, upstream, 0)

			// Requirement 2.1/2.2/2.6: the frozen model-facing ABI on the real wire.
			first := upstream.at(t, 0)
			for _, want := range []string{
				`"name":"` + algControlToolName + `"`,
				`"required":["result"]`,
				`"additionalProperties":false`,
				`"properties":{"result":`,
			} {
				if !strings.Contains(first, want) {
					t.Fatalf("the frozen control-tool contract member %s is absent from the active B-leg:\n%s", want, first)
				}
			}
			if strings.Contains(first, `"command"`) {
				t.Fatalf("the frozen control-tool contract must expose no command parameter:\n%s", first)
			}
			// Requirement 2.3: the description states the completion rule, and
			// requirement 2.7 keeps the instruction free of per-turn volatile data
			// (no timestamp, request id, or attempt counter appears in it).
			if !strings.Contains(first, "only when all work requested by the user for the current task is complete") {
				t.Fatalf("the control-tool description does not state the call-only-after-completion rule:\n%s", first)
			}
			if strings.Contains(first, "attempt_id") || strings.Contains(first, "trace_id") || strings.Contains(first, "timestamp") {
				t.Fatalf("the protocol instruction carries volatile per-turn data:\n%s", first)
			}

			if got, want := algWireText(frames), algUnmarkedText+algRepairText; got != want {
				t.Fatalf("client assistant text = %q, want exactly the two committed model texts %q", got, want)
			}
			algAssertNoPrivateControlStage(t, tr)
			algAssertNoPrivateControlLeak(t, frames)
		})
	}
}

// algOpenResponsesCreateBody renders one A-leg create document for the
// item-authority OpenResponses client wire. store:false keeps the cell on the
// single-request path so no continuation record is involved.
func algOpenResponsesCreateBody(t *testing.T, stream bool) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"model":  "gpt-4o-mini",
		"input":  "apply the schema, verify the backfill, then report the result",
		"stream": stream,
		"store":  false,
	})
	if err != nil {
		t.Fatalf("marshal openresponses create body: %v", err)
	}
	return string(raw)
}

// TestPreferredProtocolE2E_itemAuthorityCompletionOnlyPublishesResult is the
// item-authority twin of acceptance-matrix row 1.
//
// Design row 19 requires message-authority and item-authority frontends/backends
// to satisfy the same protocol guarantees, and the design Authority-Neutral
// Instruction Projection requires item authority to materialize the identical
// normative instruction text as a leading system/developer item before mutable
// history while never mutating A-leg baseline truth. This cell therefore reuses
// slice 10.1A's completion-only fixture and every slice 10.1A completion-only
// assertion unchanged, with only the client-facing protocol column swapped for
// the real OpenResponses frontend:
//
//   - the bounded result becomes the client-visible assistant text exactly once
//     (requirement 6.3), released through the real response-part hook, the real
//     final-stream observation, and the accepted terminal owner in that order;
//   - the instruction and the frozen tool reach the B-leg only, and the A-leg
//     request the client sent never carried them (requirement 3.1);
//   - the proxy-owned call never becomes client-visible and no private fact
//     appears on the item-authority wire (requirement 3.5).
func TestPreferredProtocolE2E_itemAuthorityCompletionOnlyPublishesResult(t *testing.T) {
	origin, upstream := algScriptedOrigin(t, algCompletionTurn(t, "resp_alg_item_completion"))
	d, tr := algDeployColumn(t, algColumn{
		Strategy:  AgentLoopGuardStrategyAttemptCompletion,
		Frontend:  FrontendOpenResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportJSON,
		Origin:    origin,
	})

	status, frames, err := algPostCreateAtPath(t.Context(), d, "/openresponses/v1/responses", algOpenResponsesCreateBody(t, false), false, tr)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}
	algAssertBoundedUpstreamCount(t, upstream, 1)
	if got := algWireText(frames); got != algCompletionResult {
		t.Fatalf("client assistant text = %q, want exactly %q", got, algCompletionResult)
	}
	// Requirement 3.1: the protocol is visible to the B-leg model only.
	algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
	algAssertUpstreamInjectsControlTool(t, upstream, 0)
	// Requirement 3.5: the proxy-owned lifecycle never becomes a client item.
	algAssertNoPrivateControlLeak(t, frames)
	algAssertObservationOrder(t, tr, frames, algCompletionResult)
}

// TestPreferredProtocolE2E_itemAuthorityStreamedTextThenCompletionDoesNotDuplicate
// is the item-authority twin of acceptance-matrix rows 2 and 20.
//
// The already-committed assistant text must be delivered exactly once before the
// private control call and must not be duplicated by the bounded result, and the
// early-stream observation assertion must hold under item authority too: the text
// crossed the real response-part hook and then the real final-stream observation
// before the backend terminal, and the client read the text delta before the
// response completion frame it also received.
func TestPreferredProtocolE2E_itemAuthorityStreamedTextThenCompletionDoesNotDuplicate(t *testing.T) {
	d, tr := algDeployColumn(t, algColumn{
		Strategy:  AgentLoopGuardStrategyAttemptCompletion,
		Frontend:  FrontendOpenResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportSSE,
		Origin:    algStreamedTextThenCompletionOrigin(t),
	})

	status, frames, err := algPostCreateAtPath(t.Context(), d, "/openresponses/v1/responses", algOpenResponsesCreateBody(t, true), true, tr)
	if err != nil {
		t.Fatalf("create stream: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if n := algCountWireType(frames, "response.completed"); n != 1 {
		t.Fatalf("response.completed frames = %d, want exactly 1; frames=%+v", n, frames)
	}
	// Requirement 6.4: committed assistant text is not duplicated to surface the
	// result, which stays completion evidence only.
	if got := algWireText(frames); got != algStreamedText {
		t.Fatalf("client assistant text = %q, want exactly the committed streamed text %q", got, algStreamedText)
	}
	if strings.Contains(algWireText(frames), algCompletionResult) {
		t.Fatal("the committed assistant text was duplicated with the completion result")
	}
	algAssertNoPrivateControlLeak(t, frames)
	// Row 20 under item authority: the ordinary text was observed before the
	// backend terminal, not buffered until completion, and reached the client in
	// wire order ahead of the completion frame.
	algAssertEarlyStreamOrder(t, tr, frames, algStreamedText)
	if got := d.RequestCount(BackendOpenAIResponses); got != 1 {
		t.Fatalf("harness origin request count = %d, want exactly 1", got)
	}
}

// --- slice 10.1C client-facing protocol columns -------------------------------

// algProtocolColumn is one client-facing protocol column: the frontend, its A-leg
// path, and the decoder for the assistant text that frontend's own wire carries.
type algProtocolColumn struct {
	name     string
	frontend string
	path     string
	request  string
	decode   func(*testing.T, string) string
}

// algAnthropicColumn and algGeminiColumn describe the two representative
// non-OpenAI client protocols. Both are paired with the OpenAI-Responses BACKEND,
// because this spec's constraint is that the Anthropic and Gemini BACKEND adapters
// have no developer wire role and therefore cannot be certified as preferred
// repair legs; pairing them on the client side exercises exactly what the task
// asked for, the protocol adapters between a client and the canonical model.
func algAnthropicColumn() algProtocolColumn {
	return algProtocolColumn{
		name:     "anthropic",
		frontend: FrontendAnthropic,
		path:     "/v1/messages",
		request:  `{"model":"claude-3-5-haiku-20241022","max_tokens":256,"messages":[{"role":"user","content":"apply the schema, verify the backfill, then report the result"}]}`,
		decode:   algAnthropicText,
	}
}

func algGeminiColumn() algProtocolColumn {
	return algProtocolColumn{
		name:     "gemini",
		frontend: FrontendGemini,
		path:     "/v1beta/models/gemini-2.0-flash:generateContent",
		request:  `{"contents":[{"role":"user","parts":[{"text":"apply the schema, verify the backfill, then report the result"}]}]}`,
		decode:   algGeminiText,
	}
}

// algRunProtocolColumn proves the protocol guarantees that must hold in EVERY
// client-facing protocol column, using only that column's own wire vocabulary.
//
// Two turns per column, both driven through the real frontend handler, the real
// runtime executor, and the real backend adapter:
//
//   - completion-only (acceptance-matrix row 1): the bounded result becomes the
//     client's assistant text exactly once through that protocol's own text member,
//     and no private control fact appears anywhere in that protocol's encoding;
//   - missing signal then the bounded repair leg (rows 4 and 20): exactly one
//     hidden repair leg carrying the requirement 7.2 control text, both committed
//     model texts delivered exactly once, and nothing invented.
//
// The protocol-neutral assertions (requirement 3.1 B-leg-only visibility,
// requirement 7.4's terminal bound, requirement 3.5's private-lifecycle absence)
// are shared; only the client decode and the A-leg document differ per column.
func algRunProtocolColumn(t *testing.T, col algProtocolColumn) {
	t.Helper()
	t.Run(col.name+"_completion_only", func(t *testing.T) {
		origin, upstream := algScriptedOrigin(t, algCompletionTurn(t, "resp_alg_col_"+col.name+"_c"))
		d, _ := algDeployColumn(t, algColumn{
			Strategy:  AgentLoopGuardStrategyAttemptCompletion,
			Frontend:  col.frontend,
			Backend:   BackendOpenAIResponses,
			Transport: TransportJSON,
			Origin:    origin,
		})
		status, wire, err := algPostProtocolBody(t.Context(), d, col.path, col.request)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; wire=%s", status, wire)
		}
		// Requirement 6.3 plus that column's own text member.
		if got := col.decode(t, wire); got != algCompletionResult {
			t.Fatalf("%s client assistant text = %q, want exactly %q; wire=%s", col.name, got, algCompletionResult, wire)
		}
		// Requirement 3.5 in this protocol's own vocabulary: the Anthropic client
		// must see no tool_use block and the Gemini client no functionCall part,
		// because the proxy-owned lifecycle never becomes client tool execution.
		for _, forbidden := range []string{"tool_use", "functionCall", `"tool_calls"`, "function_call"} {
			if strings.Contains(wire, forbidden) {
				t.Fatalf("the %s client wire exposed a client tool shape %q for a proxy-owned control call:\n%s", col.name, forbidden, wire)
			}
		}
		algAssertNoPrivateControlFacts(t, []algWireFrame{{Raw: wire}})
		algAssertBoundedUpstreamCount(t, upstream, 1)
		algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
		algAssertUpstreamInjectsControlTool(t, upstream, 0)
	})

	t.Run(col.name+"_missing_signal_then_repair", func(t *testing.T) {
		origin, upstream := algScriptedOrigin(t,
			algUnmarkedStopTurn(t, "resp_alg_col_"+col.name+"_u"),
			algRepairStopTurn(t, "resp_alg_col_"+col.name+"_u_repair"),
		)
		d, tr := algDeployColumn(t, algColumn{
			Strategy:  AgentLoopGuardStrategyAttemptCompletion,
			Frontend:  col.frontend,
			Backend:   BackendOpenAIResponses,
			Transport: TransportJSON,
			Origin:    origin,
		})
		status, wire, err := algPostProtocolBody(t.Context(), d, col.path, col.request)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, want the approved bounded-repair terminal; wire=%s", status, wire)
		}
		// Requirement 7.1/7.4 with the requirement 10.1 default cap of one.
		algAssertBoundedUpstreamCount(t, upstream, 2)
		algAssertUpstreamCarries(t, upstream, 0, algProtocolInstructionMarker)
		algAssertUpstreamCarries(t, upstream, 1, algRepairInstructionMarker)
		algAssertUpstreamCarries(t, upstream, 1, algRepairSignalClause)
		algAssertUpstreamInjectsControlTool(t, upstream, 0)

		if got, want := col.decode(t, wire), algUnmarkedText+algRepairText; got != want {
			t.Fatalf("%s client assistant text = %q, want exactly the two committed model texts %q; wire=%s", col.name, got, want, wire)
		}
		if strings.Contains(wire, algCompletionResult) {
			t.Fatalf("the %s client received an invented completion result:\n%s", col.name, wire)
		}
		algAssertNoPrivateControlFacts(t, []algWireFrame{{Raw: wire}})
		algAssertNoPrivateControlStage(t, tr)
	})
}

// TestPreferredProtocolE2E_anthropicProtocolColumnCertifiesBothTurns is the
// Anthropic half of "representative OpenAI/Anthropic/Gemini protocol adapters".
func TestPreferredProtocolE2E_anthropicProtocolColumnCertifiesBothTurns(t *testing.T) {
	algRunProtocolColumn(t, algAnthropicColumn())
}

// TestPreferredProtocolE2E_geminiProtocolColumnCertifiesBothTurns is the Gemini
// half of the same representative protocol requirement.
func TestPreferredProtocolE2E_geminiProtocolColumnCertifiesBothTurns(t *testing.T) {
	algRunProtocolColumn(t, algGeminiColumn())
}

// --- slice 10.1C legacy-versus-preferred contrast -----------------------------

// algStrategyRun is one observed outcome of the same deployment shape under one
// strategy. Every field is a decision requirement 9.6 actually compares: what the
// strategy puts on the B-leg, whether the turn was continued or terminated, and
// what the client was allowed to see.
type algStrategyRun struct {
	strategy string
	// status is the client-visible HTTP status of the turn.
	status int
	// injectedInstruction and injectedTool are the two B-leg-only effects that
	// requirement 3.1 places exclusively under the preferred protocol.
	injectedInstruction bool
	injectedTool        bool
	// upstreamLegs counts the B-legs the turn consumed, which is the observable
	// form of the terminal-versus-continuation decision.
	upstreamLegs int
	// clientText is the assistant text the client received, decoded from its wire.
	clientText string
	// clientSawControlTool records that a tool call named attempt_completion
	// crossed the client boundary as an ordinary tool event.
	clientSawControlTool bool
	// clientToolStarts counts the ordinary client tool lifecycle events that
	// crossed the client boundary in the response-part hook.
	clientToolStarts int
}

// algRunStrategy drives one identical deployment shape under one strategy: a model
// turn that commits ordinary assistant text and then emits a private
// attempt_completion call. The client declares no tools, so the ONLY way a tool
// call can reach the client is if the strategy chose to let it through. That makes
// the client-side tool visibility the sharpest observable of the difference
// requirement 1.2 and 1.3 pin down.
func algRunStrategy(t *testing.T, strategy string) algStrategyRun {
	t.Helper()
	origin, upstream := algScriptedOrigin(t, algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_cmp_"+strategy, []any{
		algMessageOutput("msg_alg_cmp_"+strategy, algUnmarkedText),
		algControlOutput(algControlItemID, algControlCallID, `{"result":"`+algCompletionResult+`"}`),
	})})
	d, tr := algDeployColumn(t, algColumn{
		Strategy:  strategy,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportJSON,
		Origin:    origin,
	})

	status, frames, err := algPostCreateBody(t.Context(), d, algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", false), false, tr)
	if err != nil {
		t.Fatalf("%s create: %v", strategy, err)
	}
	if status != http.StatusOK {
		t.Fatalf("%s status = %d, want 200; wire=%s", strategy, status, algWireBody(frames))
	}
	first := upstream.at(t, 0)
	starts := 0
	for _, it := range tr.snapshot() {
		if it.stage == algTraceStageObserve && it.kind == string(aipapi.EventToolCallStarted) {
			starts++
		}
	}
	return algStrategyRun{
		strategy:            strategy,
		status:              status,
		injectedInstruction: strings.Contains(first, algProtocolInstructionMarker),
		injectedTool:        strings.Contains(first, algControlToolName),
		upstreamLegs:        upstream.count(),
		clientText:          algWireText(frames),
		clientSawControlTool: strings.Contains(algWireBody(frames),
			`"name":"`+algControlToolName+`"`),
		clientToolStarts: starts,
	}
}

// TestPreferredProtocolE2E_legacyAndPreferredStrategiesDifferOnlyWhereSpecified
// is the end-to-end requirement 9.6 contrast between the legacy
// `semantic_verifier` strategy and the preferred `attempt_completion` strategy.
//
// Requirement 9.6 asks that the two strategies' observable decisions MATCH for the
// certified acceptance matrix, so the contrast is asserted in BOTH directions and
// each property is asserted separately so the cell discriminates precisely.
//
// WHERE THEY MUST DIFFER (requirements 1.2, 1.3, 3.1, 3.5, 5.4, 9.4). The model
// turn under test commits ordinary text and then calls attempt_completion. Only the
// preferred strategy injects the protocol instruction and the proxy-owned tool onto
// the B-leg; only the preferred strategy claims that call by trusted provenance and
// consumes it BEFORE ordinary client tool execution; and only the preferred strategy
// may therefore treat it as a trusted completion fact. The legacy strategy must
// construct and inject nothing, so it does not own the call, does not intercept it
// by name alone, and lets it reach the client as an ordinary tool event.
//
// WHERE THEY MUST AGREE (requirements 9.1, 9.5). Both strategies terminate the turn
// on its FIRST upstream leg through the same generic terminal owner, and both
// deliver the model's own committed ordinary text to the client exactly once.
// Requirement 4.6 pins the conservative floor both share: neither invents a
// completion signal for the client.
func TestPreferredProtocolE2E_legacyAndPreferredStrategiesDifferOnlyWhereSpecified(t *testing.T) {
	preferred := algRunStrategy(t, AgentLoopGuardStrategyAttemptCompletion)
	legacy := algRunStrategy(t, AgentLoopGuardStrategySemanticVerifier)

	// --- Where the spec requires the strategies to DIFFER --------------------

	// Requirement 3.1 / 9.4: only the preferred strategy puts the private protocol
	// instruction and the proxy-owned tool definition on the B-leg.
	if !preferred.injectedInstruction {
		t.Fatalf("the preferred strategy did not advertise the protocol instruction on the B-leg, although requirement 3.1 makes it B-leg-visible")
	}
	if legacy.injectedInstruction {
		t.Fatalf("the legacy strategy advertised the preferred protocol instruction, violating requirement 1.3 and 3.1")
	}
	if !preferred.injectedTool {
		t.Fatalf("the preferred strategy did not advertise the proxy-owned control tool on the B-leg")
	}
	if legacy.injectedTool {
		t.Fatalf("the legacy strategy advertised the proxy-owned control tool, violating requirement 1.3 and 3.1")
	}

	// Requirement 3.2 / 3.5 / 5.4: the preferred strategy identifies the call from
	// trusted request-local provenance and consumes it before ordinary client tool
	// policy, so the proxy-owned lifecycle never becomes client-visible. The legacy
	// strategy never established that provenance, so the same model call is
	// ordinary output for it and correctly crosses the client boundary.
	if preferred.clientSawControlTool {
		t.Fatalf("the preferred strategy let the proxy-owned completion call reach the client as a tool event, violating requirement 3.5")
	}
	if preferred.clientToolStarts != 0 {
		t.Fatalf("preferred client tool lifecycle starts = %d, want 0 because the proxy-owned call must be consumed before client tool execution", preferred.clientToolStarts)
	}
	if !legacy.clientSawControlTool {
		t.Fatalf("the legacy strategy intercepted or suppressed an attempt_completion call it never owned, violating requirement 1.3 and 3.2")
	}
	if legacy.clientToolStarts != 1 {
		t.Fatalf("legacy client tool lifecycle starts = %d, want exactly 1 because the unowned call is ordinary client output", legacy.clientToolStarts)
	}

	// --- Where the spec requires the strategies to AGREE ---------------------

	// Requirement 9.5: both reach their decision through the same generic terminal
	// owner, so the same committed turn is terminated on its first B-leg under both
	// strategies. Neither continues, replays, nor issues an auxiliary request.
	if preferred.upstreamLegs != 1 {
		t.Fatalf("preferred upstream legs = %d, want exactly 1; requirement 9.5 shares one terminal owner", preferred.upstreamLegs)
	}
	if legacy.upstreamLegs != 1 {
		t.Fatalf("legacy upstream legs = %d, want exactly 1; requirement 9.5 shares one terminal owner", legacy.upstreamLegs)
	}

	// Requirement 9.1 and 5.6: both deliver the model's own committed ordinary text
	// to the client exactly once, and neither discards or duplicates it.
	for _, run := range []algStrategyRun{preferred, legacy} {
		if run.clientText != algUnmarkedText {
			t.Fatalf("%s client assistant text = %q, want exactly the model's own committed text %q; requirement 6.4 forbids surfacing the result as a duplicate answer", run.strategy, run.clientText, algUnmarkedText)
		}
	}

	// Requirement 4.6: neither strategy invented a completion signal for the client
	// on a turn whose only completion evidence is the proxy-owned call, and the
	// ordinary client tool outcome is unaffected by the strategy choice.
	for _, run := range []algStrategyRun{preferred, legacy} {
		if strings.Contains(run.clientText, algCompletionResult) {
			t.Fatalf("%s invented a completion signal the model never delivered as client text", run.strategy)
		}
	}
}

// =============================================================================
// Requirement 7.7 — platform rejection of the protocol-repair continuation
// =============================================================================
//
// Every other cell in this file wires the continuation ports so the platform
// ADMITS the bounded repair leg (algWireContinuationPorts), which leaves
// requirement 7.7 unmeasured here. This section drives the one shape the
// requirement actually names: a strategy asks the platform for a semantic
// continuation and the PLATFORM refuses to do that continuation work.
//
// The refusal is planted on the narrowest seam that genuinely exists in the
// production path and it is planted in production shape rather than by faking
// any agent-loop-guard component. The real sdkadapter.Writer resolves the
// after_ingress_tail anchor over the real trajectory and builds a well-formed
// PutSteeringRequest; the conversation-view store behind it refuses to persist
// the overlay. That is the placement stage of the real continuation transaction,
// so the platform rejects the continuation exactly as it would on any other
// rejected continuation work, and it is strictly pre-admission, so no second
// B-leg can ever open behind it.
//
// One control keeps the rejection cell from being vacuous: the admitted cell
// runs the identical deployment, through the identical real writer and the
// identical store, with the store's refusal switched off, and proves the repair
// really was requested and really would have opened a second upstream leg. Every
// rejected assertion below is therefore measured against a deployment that is
// proven to request that continuation, not one that merely happens to end
// without a repair leg.
//
// No cross-strategy contrast is claimed here, because none is measurable in this
// deployment: the legacy semantic_verifier strategy never requests a
// continuation here (measured refusals=0), because its detached bounded verifier
// request fails closed at request-authority admission — the same characterization
// agentloopguard_preferred_telemetry_test.go gives that strategy's auxiliary
// lineage — so it returns a stop decision before the generic platform
// continuation transaction is ever entered. Its rejected-deployment outcome is
// therefore the ordinary accepted turn, not a rejected continuation, and
// comparing it against the preferred strategy's rejected outcome would compare
// two different scenarios rather than isolate ALG's contribution.

// algRefusingSteeringStore wraps a real conversation-view ReferenceStore and
// refuses only PutSteering. DeactivateSteering still reaches the real store, so
// the transaction's post-failure overlay deactivation is exercised for real
// instead of being short-circuited by the same refusal.
type algRefusingSteeringStore struct {
	inner     conversationview.SteeringStore
	refusals  int
	refusePut bool
}

func (s *algRefusingSteeringStore) PutSteering(ctx context.Context, aLegID string, req conversationview.PutSteeringRequest) (conversationview.SteeringState, error) {
	if s.refusePut {
		s.refusals++
		return conversationview.SteeringState{}, fmt.Errorf("conformance: conversation-view store refused steering overlay %q", req.OverlayID)
	}
	return s.inner.PutSteering(ctx, aLegID, req)
}

func (s *algRefusingSteeringStore) DeactivateSteering(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringState, error) {
	return s.inner.DeactivateSteering(ctx, aLegID, overlayID)
}

// algRejectedRun is one measured rejected-continuation deployment: the client
// outcome the platform actually produced, plus the upstream legs and the refusal
// count that prove which continuation stage rejected.
type algRejectedRun struct {
	strategy string
	stream   bool
	// wantText is the ONLY assistant text the client may observe: the text the
	// model itself committed on its single upstream leg.
	wantText string
	status   int
	frames   []algWireFrame
	upstream *algUpstreamLog
	refusals int
	trace    *algTrace
}

// outcome renders the whole client-visible outcome as one comparable string:
// the HTTP status plus the exact client wire body. Requirement 7.7 makes no claim
// about WHICH conservative outcome the platform picks — that is generic platform
// policy — so no assertion in this section pins a status the spec never states.
// This rendering is diagnostic context on the measured run: the two delivery
// modes legitimately publish different conservative outcomes after a rejection
// (measured: the streaming client keeps its committed text, the collected client
// receives an error body with none), and every obligation below is therefore
// asserted against a property both of them share.
func (r algRejectedRun) outcome() string {
	return fmt.Sprintf("status=%d body=%s", r.status, algWireBody(r.frames))
}

// algSSEUnmarkedStopTurn is the streamed upstream wire of a turn that commits
// ordinary assistant text and then cleanly stops with no completion signal. It is
// the SSE counterpart of algUnmarkedStopTurn, so the requirement 7.7 cells can
// measure the primary streaming path with the same planted evidence: no control
// call, no completion signal, one committed ordinary answer.
func algSSEUnmarkedStopTurn(t *testing.T, id, text string) algUpstreamTurn {
	t.Helper()
	frame := func(name string, payload map[string]any) string {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal sse frame %s: %v", name, err)
		}
		return "event: " + name + "\ndata: " + string(raw) + "\n\n"
	}
	var sse strings.Builder
	seq := 0
	next := func() int { seq++; return seq }
	itemID := "msg_" + id
	sse.WriteString(frame("response.created", map[string]any{
		"type": "response.created", "sequence_number": next(),
		"response": map[string]any{"id": id, "object": "response", "created_at": 1715620000, "status": "in_progress", "model": "gpt-4o-mini"},
	}))
	sse.WriteString(frame("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": itemID, "status": "in_progress", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": ""}},
		},
	}))
	sse.WriteString(frame("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "sequence_number": next(), "item_id": itemID,
		"output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""},
	}))
	sse.WriteString(frame("response.output_text.delta", map[string]any{
		"type": "response.output_text.delta", "sequence_number": next(), "item_id": itemID,
		"output_index": 0, "content_index": 0, "delta": text,
	}))
	sse.WriteString(frame("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "sequence_number": next(), "item_id": itemID,
		"output_index": 0, "content_index": 0, "text": text,
	}))
	sse.WriteString(frame("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "sequence_number": next(), "output_index": 0,
		"item": map[string]any{
			"type": "message", "id": itemID, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		},
	}))
	sse.WriteString(frame("response.completed", map[string]any{
		"type": "response.completed", "sequence_number": next(),
		"response": map[string]any{
			"id": id, "object": "response", "created_at": 1715620000, "status": "completed", "model": "gpt-4o-mini",
			"usage": map[string]any{"input_tokens": 24, "output_tokens": 11, "total_tokens": 35},
		},
	}))
	sse.WriteString("data: [DONE]\n\n")
	return algUpstreamTurn{SSE: sse.String()}
}

// algRejectedContinuationRun drives one deployment for one strategy and one A-leg
// delivery mode, and returns the measured outcome. refusePut is the only
// difference between the admitted control and the rejected cell.
func algRejectedContinuationRun(t *testing.T, strategy string, stream bool, refusePut bool) algRejectedRun {
	t.Helper()
	var origin http.Handler
	var log *algUpstreamLog
	if stream {
		origin, log = algScriptedOrigin(t,
			algSSEUnmarkedStopTurn(t, "resp_alg_rejected_stream", algUnmarkedText),
			algSSEUnmarkedStopTurn(t, "resp_alg_rejected_stream_repair", algRepairText),
		)
	} else {
		origin, log = algScriptedMissingSignalOrigin(t, algUnmarkedStopTurn(t, "resp_alg_rejected_repair"), "resp_alg_rejected_repair")
	}
	d, tr := algDeployColumn(t, algColumn{
		Strategy:  strategy,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportJSON,
		Origin:    origin,
	})

	// Identical environment to algWireContinuationPorts, with one difference: the
	// store behind the real steering writer refuses the continuation overlay. The
	// reader port and the writer-factory shape are unchanged, so the generation,
	// the control provider, and the terminal provider under test remain exactly
	// the ones the production feature registry and snapshot builder compose.
	store := conversationview.NewReferenceStore()
	d.Exec.ConversationViewReader = algAutoRegisteringReader{store: store}
	steeringStore := &algRefusingSteeringStore{inner: store, refusePut: refusePut}
	d.Exec.SteeringWriterFactory = func(_ context.Context, aLegID string, resolver runtime.SteeringWriterResolver) (steering.Writer, error) {
		return sdkadapter.NewWriter(steeringStore, aLegID, sdkadapter.TrajectoryResolver(resolver))
	}

	body := algCreateBodyWith(t, "apply the schema, verify the backfill, then report the result", stream)
	status, frames, err := algPostCreateBody(t.Context(), d, body, stream, tr)
	if err != nil {
		t.Fatalf("%s create: %v", strategy, err)
	}
	return algRejectedRun{
		strategy: strategy,
		stream:   stream,
		wantText: algUnmarkedText,
		status:   status,
		frames:   frames,
		upstream: log,
		refusals: steeringStore.refusals,
		trace:    tr,
	}
}

// algLogicalResponseFrameType is the client wire frame that opens one logical
// client response. Counting it pins "exactly one logical client response is
// opened". A continuation is projected onto the *same* logical client response,
// so this count does not itself detect a second continuation; that obligation
// is proven by the upstream-leg assertions in
// algAssertRejectedContinuationIsAccepted. The count is pinned per delivery
// mode because the two client-facing encodings differ, and requirement 7.7
// does not state which conservative outcome the platform publishes once the
// continuation is rejected.
func algLogicalResponseFrameType(stream bool) string {
	if stream {
		return "response.created"
	}
	return "json_resource"
}

// algAssertRejectedContinuationIsAccepted is requirement 7.7's obligation,
// asserted once per strategy on a run whose rejection is proven to have happened.
func algAssertRejectedContinuationIsAccepted(t *testing.T, run algRejectedRun) {
	t.Helper()
	if run.refusals == 0 {
		t.Fatalf("strategy %q: the platform never refused the continuation overlay, so the rejection path was not exercised; outcome=%s", run.strategy, run.outcome())
	}
	t.Logf("strategy %q stream=%t rejected outcome: status=%d client_text=%q upstream_legs=%d refusals=%d",
		run.strategy, run.stream, run.status, algWireText(run.frames), run.upstream.count(), run.refusals)

	// (b) NO second continuation authority. The strategy did ask for one — the
	// admitted control below opens a repair leg from the identical deployment — so
	// an admitted continuation is observable as a second upstream leg. The
	// rejected continuation must open none, and the fixed repair instruction must
	// therefore never have reached any upstream request.
	algAssertBoundedUpstreamCount(t, run.upstream, 1)
	algAssertUpstreamLacks(t, run.upstream, algRepairInstructionMarker)

	// (a) ALG accepts the platform's conservative final outcome: the client
	// logical response is opened exactly once, and ALG adds nothing of its own to
	// it. The response-opening count pins "exactly one logical client response";
	// "no second continuation authority" is proven solely by the two upstream-leg
	// assertions above — a second continuation opens a second upstream leg while
	// the client response stays one — and it is deliberately the
	// OPENING frame rather than the terminal frame, because requirement 7.7 does
	// not state which conservative outcome the platform publishes after a rejection.
	if got := algCountWireType(run.frames, algLogicalResponseFrameType(run.stream)); got != 1 {
		t.Fatalf("strategy %q stream=%t: client logical responses opened = %d, want exactly one; outcome=%s", run.strategy, run.stream, got, run.outcome())
	}

	// (c) NO result is published. The turn produced no completion signal, the
	// continuation that would have asked for one was refused, and the accepted
	// conservative outcome must not manufacture the bounded result the client
	// never received from the model. This is checked before the broader text
	// equality below so a published result is reported as exactly that.
	if strings.Contains(algWireBody(run.frames), algCompletionResult) {
		t.Fatalf("strategy %q stream=%t: a completion result was published although the platform rejected the continuation:\n%s", run.strategy, run.stream, algWireBody(run.frames))
	}

	// ALG contributed no answer of its own. The client-boundary text is either the
	// model's own committed answer or nothing at all, and never more: the platform
	// chooses WHICH conservative outcome it publishes after a rejection (measured
	// here: the streaming client keeps its committed text, the collected client
	// receives an error body with none), and requirement 7.7 constrains ALG's
	// acceptance of that outcome, not the platform's choice of it. A second
	// continuation authority, a replay, or an invented repair answer would each
	// add or duplicate text here and fail.
	got := algWireText(run.frames)
	if got != "" && got != run.wantText {
		t.Fatalf("strategy %q stream=%t: client assistant text = %q, want either nothing or exactly the model's own committed text %q; ALG added text of its own. outcome=%s", run.strategy, run.stream, got, run.wantText, run.outcome())
	}

	algAssertNoPrivateControlStage(t, run.trace)
	algAssertNoPrivateControlLeak(t, run.frames)
}

// TestPreferredProtocolE2E_platformRejectsProtocolRepairAndALGAcceptsIt is
// requirement 7.7: "When the platform rejects continuation admission, placement,
// authority, protocol legality, or lifecycle work, ALG shall accept the platform's
// conservative final outcome and shall not create a second continuation
// authority."
func TestPreferredProtocolE2E_platformRejectsProtocolRepairAndALGAcceptsIt(t *testing.T) {
	t.Run("admitted continuation is the control", func(t *testing.T) {
		run := algRejectedContinuationRun(t, AgentLoopGuardStrategyAttemptCompletion, false, false)
		if run.refusals != 0 {
			t.Fatalf("control refusals = %d, want 0: the control must place the continuation overlay", run.refusals)
		}
		if run.status != http.StatusOK {
			t.Fatalf("control status = %d, want 200; outcome=%s", run.status, run.outcome())
		}
		algAssertBoundedUpstreamCount(t, run.upstream, 2)
		algAssertUpstreamCarries(t, run.upstream, 1, algRepairInstructionMarker)
		if got, want := algWireText(run.frames), algUnmarkedText+algRepairText; got != want {
			t.Fatalf("control client assistant text = %q, want the two committed model texts %q", got, want)
		}
	})

	t.Run("rejected preferred continuation is accepted", func(t *testing.T) {
		algAssertRejectedContinuationIsAccepted(t, algRejectedContinuationRun(t, AgentLoopGuardStrategyAttemptCompletion, false, true))
	})

	// Streaming is the primary A-leg path, and it is where a second continuation
	// authority would be most visible: a second terminal frame plus a second body
	// of deltas on the same logical response. The same three obligations are
	// therefore asserted again on the streaming path rather than assumed to follow
	// from the collected one.
	t.Run("rejected preferred continuation is accepted while streaming", func(t *testing.T) {
		algAssertRejectedContinuationIsAccepted(t, algRejectedContinuationRun(t, AgentLoopGuardStrategyAttemptCompletion, true, true))
	})
}
