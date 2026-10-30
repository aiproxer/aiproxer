package runtime

// Task 7 serving integration (Requirements 1.3, 2.3, 3.4, 4.3, 4.4, 4.5):
// the bound provider stream and the capability-preserving adapter chain,
// already wired into both successful-open paths by tasks 5 and 6, must reach
// the existing live/checkpoint/terminal consumers on the real serving path.
// Every test below drives ExecuteLargeBody with a REAL Responses provider
// parser (openairesponses.CompletionEvents over provider JSON) and the
// production evidence stream/buffer APIs, then reads the existing terminal
// sink. The parallel-race winner case additionally repairs the missing
// evidence projection through parallelBridgeStream, which replaces the
// winner inner after a successful (already bound) open.

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/openai/openai-go/v3/responses"
)

// task7ResponsesJSON builds one real provider Responses payload carrying the
// literal native usage quantities every serving test asserts.
func task7ResponsesJSON(input, output int) string {
	return `{
  "id": "resp_task7_native",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_task7_native",
      "status": "completed",
      "role": "assistant",
      "content": [{"type": "output_text", "text": "task7 served output"}]
    }
  ],
  "usage": {
    "input_tokens": ` + strconv.Itoa(input) + `,
    "output_tokens": ` + strconv.Itoa(output) + `,
    "total_tokens": ` + strconv.Itoa(input+output) + `
  }
}`
}

// task7ParseResponses runs the real provider JSON parse the production
// backend runs before its parser: raw bytes into the SDK Response type.
func task7ParseResponses(t *testing.T, raw string) responses.Response {
	t.Helper()
	var resp responses.Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal provider Responses payload: %v", err)
	}
	return resp
}

// task7ParserEvents runs the real Responses provider parser over one
// provider payload: canonical events including the usage event.
func task7ParserEvents(t *testing.T, raw string) []aipapi.Event {
	t.Helper()
	events, err := openairesponses.CompletionEvents(task7ParseResponses(t, raw))
	if err != nil {
		t.Fatalf("real Responses parser CompletionEvents: %v", err)
	}
	return events
}

// task7ParserUsageEvent extracts the canonical usage event the real parser
// produced and proves its literal native quantities at the parser seam.
func task7ParserUsageEvent(t *testing.T, events []aipapi.Event, input, output int) aipapi.Event {
	t.Helper()
	for _, event := range events {
		if event.Kind != aipapi.EventUsageDelta {
			continue
		}
		if event.InputTokens != input || event.OutputTokens != output || event.TotalTokens != input+output {
			t.Fatalf("real parser usage = %d/%d/%d, want literal %d/%d/%d",
				event.InputTokens, event.OutputTokens, event.TotalTokens, input, output, input+output)
		}
		return event
	}
	t.Fatalf("real Responses parser produced no usage event in %+v", events)
	return aipapi.Event{}
}

// task7ServingStream wraps real parser events in the production evidence
// stream the real Responses backend returns from Open: drafts captured
// before Open returns are pre-read evidence until the runtime bind makes
// them attributable.
func task7ServingStream(t *testing.T, input, output int) aipapi.ManagedEventStream {
	t.Helper()
	events := task7ParserEvents(t, task7ResponsesJSON(input, output))
	task7ParserUsageEvent(t, events, input, output)
	return openaiusage.NewProviderEvidenceStream(events, "openai.responses.v2")
}

// task7TimedParserStream mirrors the production streaming adapter
// structure: it embeds the real provider buffer, serves real parser events
// through Recv, and captures one pre-read draft at construction (before the
// runtime bind). Optional behavior models the two timing hazards: a late
// corrected draft captured when the usage event is served (after the bind,
// after the first consumer drain), and a release gate holding every Recv
// until closed (a parallel-race loser that opened successfully but never
// produces before cancellation).
type task7TimedParserStream struct {
	mu        sync.Mutex
	events    []aipapi.Event
	index     int
	closed    bool
	buffer    *coremetering.ProviderEvidenceBuffer
	usage     aipapi.Event
	corrected *aipapi.Event
	release   chan struct{}
	lateAdded bool
}

func newTask7TimedParserStream(t *testing.T, input, output int, corrected *aipapi.Event, release chan struct{}) *task7TimedParserStream {
	t.Helper()
	events := task7ParserEvents(t, task7ResponsesJSON(input, output))
	usage := task7ParserUsageEvent(t, events, input, output)
	stream := &task7TimedParserStream{
		events:    events,
		buffer:    coremetering.NewProviderEvidenceBuffer(),
		usage:     usage,
		corrected: corrected,
		release:   release,
	}
	stream.buffer.Add(openaiusage.ProviderEvidenceDraft(usage, "openai.responses.v2", usage.Accounting.DedupeKey))
	return stream
}

func newTask7LateParserStream(t *testing.T) *task7TimedParserStream {
	t.Helper()
	correctedEvents := task7ParserEvents(t, task7ResponsesJSON(10, 3))
	corrected := task7ParserUsageEvent(t, correctedEvents, 10, 3)
	return newTask7TimedParserStream(t, 10, 2, &corrected, nil)
}

func newTask7BlockingParserStream(t *testing.T, input, output int, release chan struct{}) *task7TimedParserStream {
	t.Helper()
	return newTask7TimedParserStream(t, input, output, nil, release)
}

func (s *task7TimedParserStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	} else if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.index >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	if event.Kind == aipapi.EventUsageDelta && s.corrected != nil && !s.lateAdded {
		s.lateAdded = true
		s.buffer.Add(openaiusage.ProviderEvidenceDraft(*s.corrected, "openai.responses.v2", s.usage.Accounting.DedupeKey))
	}
	return event, nil
}

func (s *task7TimedParserStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *task7TimedParserStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	_ = s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *task7TimedParserStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s != nil && s.buffer != nil {
		s.buffer.BindEconomicEvidence(identity)
	}
}

func (s *task7TimedParserStream) DrainEconomicObservations() []sdkmetering.Observation {
	if s == nil || s.buffer == nil {
		return nil
	}
	return s.buffer.DrainEconomicObservations()
}

// task7RichServingStream serves canonical completion events while carrying
// rich partial/unsupported records built from real parser drafts through the
// real provider buffer. The buffer is bound by the serving path, so drained
// observations carry the current attempt lineage; coverage travels on the
// record carrier exactly like a production rich adapter.
type task7RichServingStream struct {
	aipapi.ManagedEventStream
	buffer    *coremetering.ProviderEvidenceBuffer
	coverage  map[string][2]string
	bindCalls atomic.Int32
}

func newTask7RichServingStream(t *testing.T) *task7RichServingStream {
	t.Helper()
	first := task7ParserEvents(t, task7ResponsesJSON(10, 2))
	second := task7ParserEvents(t, task7ResponsesJSON(4, 1))
	firstUsage := task7ParserUsageEvent(t, first, 10, 2)
	secondUsage := task7ParserUsageEvent(t, second, 4, 1)
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.Add(openaiusage.ProviderEvidenceDraft(firstUsage, "openai.responses.v2", "task7-rich-partial"))
	buffer.Add(openaiusage.ProviderEvidenceDraft(secondUsage, "openai.responses.v2", "task7-rich-unsupported"))
	return &task7RichServingStream{
		ManagedEventStream: aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(first)},
		buffer:             buffer,
		coverage: map[string][2]string{
			"task7-rich-partial":     {"partial", "task7 partial reason"},
			"task7-rich-unsupported": {"unsupported", "task7 unsupported reason"},
		},
	}
}

func (s *task7RichServingStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s == nil {
		return
	}
	s.bindCalls.Add(1)
	if s.buffer != nil {
		s.buffer.BindEconomicEvidence(identity)
	}
}

func (s *task7RichServingStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	if s == nil || s.buffer == nil {
		return nil
	}
	var records []execbackend.EconomicEvidence
	for _, observation := range s.buffer.DrainEconomicObservations() {
		coverage, ok := s.coverage[observation.SourceEventKey]
		if !ok {
			continue
		}
		records = append(records, execbackend.EconomicEvidence{
			Observation: observation, Coverage: coverage[0], CoverageReason: coverage[1],
		})
	}
	return records
}

func (s *task7RichServingStream) AccountingEvidenceEnabled() bool { return s != nil }

// task7SetupServing wires the existing serving fixtures: test executor with
// one trusted store resolver and the existing terminal sink capture.
func task7SetupServing(t *testing.T, storeID string, resolveCalls *atomic.Int32) (*Executor, *capturingTerminalUsageSink) {
	t.Helper()
	ex, _, _ := setupTestExecutor(t)
	ex.BillingIdentity.StoreID = func(context.Context) string {
		if resolveCalls != nil {
			resolveCalls.Add(1)
		}
		return storeID
	}
	sink := &capturingTerminalUsageSink{}
	ex.TerminalUsageSink = sink
	return ex, sink
}

func task7ServingContext(requestID, traceID string) context.Context {
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-task7"})
	return largebody.WithWireIdentity(ctx, requestID, traceID)
}

// task7ServeToTerminal runs the full serving path through the existing
// live/checkpoint/terminal consumers: receive to EOF, then close.
func task7ServeToTerminal(t *testing.T, ctx context.Context, ex *Executor, acc largebody.Assessment, src *testSource) largebody.ExecutionResult { //nolint:revive // test helper keeps t first
	t.Helper()
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	for {
		if _, rerr := res.Stream.Recv(context.Background()); rerr != nil {
			break
		}
	}
	if err := res.Stream.Close(); err != nil {
		t.Fatalf("served stream Close failed: %v", err)
	}
	return res
}

func task7SinkLegs(sink *capturingTerminalUsageSink) []usageevidence.CallLegUsageRecord {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]usageevidence.CallLegUsageRecord(nil), sink.legs...)
}

// task7NativeObservations selects genuine native provider observations on
// one leg: the production evidence mapping, never the V1 compatibility
// projection derived from canonical events.
func task7NativeObservations(t *testing.T, leg usageevidence.CallLegUsageRecord) []sdkmetering.Observation {
	t.Helper()
	var native []sdkmetering.Observation
	for _, observation := range leg.Observations {
		if observation.Origin != sdkmetering.OriginProvider {
			continue
		}
		if observation.StreamID != "openai.responses.v2" {
			continue
		}
		native = append(native, observation)
	}
	return native
}

// task7TokenMeasures reads the literal token quantities off one observation.
func task7TokenMeasures(observation sdkmetering.Observation) (input, output, total string) {
	for _, measure := range observation.Measures {
		if measure.Value == nil {
			continue
		}
		switch measure.Key.Component {
		case sdkmetering.ComponentInputToken:
			input = measure.Value.CanonicalString()
		case sdkmetering.ComponentOutputToken:
			output = measure.Value.CanonicalString()
		case sdkmetering.ComponentTotalToken:
			total = measure.Value.CanonicalString()
		}
	}
	return input, output, total
}

func task7LegByBackend(t *testing.T, legs []usageevidence.CallLegUsageRecord, backend string) usageevidence.CallLegUsageRecord {
	t.Helper()
	for _, leg := range legs {
		if leg.BackendID == backend {
			return leg
		}
	}
	t.Fatalf("no terminal leg for backend %q in %+v", backend, legs)
	return usageevidence.CallLegUsageRecord{}
}

// TestTask7WireServingRealParserTerminalReceivesNativeQuantities proves
// requirements 4.2, 4.3 and 4.4 on the serving path: the real Responses
// parser output, bound once with the frozen trusted lineage, reaches the
// existing terminal sink with literal 10 input / 2 output / 12 total and
// exactly one trusted per-attempt attribution.
func TestTask7WireServingRealParserTerminalReceivesNativeQuantities(t *testing.T) {
	t.Parallel()
	var resolveCalls atomic.Int32
	ex, sink := task7SetupServing(t, " task7-store ", &resolveCalls)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"native quantities"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	backend := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return task7ServingStream(t, 10, 2), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{"default": {OpenWire: backend.OpenWire}}

	ctx := task7ServingContext("req-task7-native", "trace-task7-native")
	task7ServeToTerminal(t, ctx, ex, acc, src)

	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("trusted store resolutions = %d, want exactly one per request", got)
	}
	legs := task7SinkLegs(sink)
	if len(legs) != 1 {
		t.Fatalf("terminal legs = %d, want exactly one", len(legs))
	}
	leg := legs[0]
	native := task7NativeObservations(t, leg)
	if len(native) != 1 {
		t.Fatalf("native provider observations = %d, want exactly one trusted per-attempt attribution", len(native))
	}
	observation := native[0]
	if gotIn, gotOut, gotTotal := task7TokenMeasures(observation); gotIn != "10/0" || gotOut != "2/0" || gotTotal != "12/0" {
		t.Fatalf("native quantities = %s/%s/%s, want literal 10/0 2/0 12/0", gotIn, gotOut, gotTotal)
	}
	if observation.Subject.StoreID != "task7-store" {
		t.Errorf("native StoreID = %q, want frozen trusted task7-store", observation.Subject.StoreID)
	}
	if observation.Subject.BLegID == "" || observation.Subject.BLegID != leg.BLegID {
		t.Errorf("native BLegID = %q, want current attempt %q", observation.Subject.BLegID, leg.BLegID)
	}
	if observation.Subject.AttemptSeq != uint64(leg.AttemptSeq) {
		t.Errorf("native AttemptSeq = %d, want leg sequence %d", observation.Subject.AttemptSeq, leg.AttemptSeq)
	}
	if callID := strings.TrimSpace(leg.CallID.String()); callID == "" {
		t.Error("terminal leg carries no billing call identity for the attribution")
	}
}

// TestTask7WireServingPreReadAndLateEvidenceReachConsumersOnce proves
// requirement 4.3 through serving: a pre-read draft captured before the
// successful-open bind and a late draft captured after the first consumer
// drain each reach the terminal sink exactly once.
func TestTask7WireServingPreReadAndLateEvidenceReachConsumersOnce(t *testing.T) {
	t.Parallel()
	var resolveCalls atomic.Int32
	ex, sink := task7SetupServing(t, "task7-store", &resolveCalls)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"pre-read and late"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	backend := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return newTask7LateParserStream(t), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{"default": {OpenWire: backend.OpenWire}}

	ctx := task7ServingContext("req-task7-timing", "trace-task7-timing")
	task7ServeToTerminal(t, ctx, ex, acc, src)

	legs := task7SinkLegs(sink)
	if len(legs) != 1 {
		t.Fatalf("terminal legs = %d, want exactly one", len(legs))
	}
	native := task7NativeObservations(t, legs[0])
	if len(native) != 2 {
		t.Fatalf("native observations = %d, want pre-read plus late evidence exactly once each", len(native))
	}
	seen := map[string]int{}
	for _, observation := range native {
		gotIn, gotOut, gotTotal := task7TokenMeasures(observation)
		seen[gotIn+"/"+gotOut+"/"+gotTotal]++
		if observation.Subject.StoreID != "task7-store" || observation.Subject.BLegID != legs[0].BLegID {
			t.Errorf("mistimed observation attribution = %+v, want frozen store and current B-leg", observation.Subject)
		}
	}
	if seen["10/0/2/0/12/0"] != 1 || seen["10/0/3/0/13/0"] != 1 {
		t.Fatalf("native quantity sets = %+v, want one pre-read 10/2/12 and one late 10/3/13", seen)
	}
}

// TestTask7WireServingReplacementKeepsSiblingAttribution proves requirement
// 4.4 through serving: the permitted pre-output replacement shares the
// frozen request lineage but the survivor's native evidence stays on its
// own B-leg attempt and never leaks onto the abandoned sibling.
func TestTask7WireServingReplacementKeepsSiblingAttribution(t *testing.T) {
	t.Parallel()
	var resolveCalls atomic.Int32
	ex, sink := task7SetupServing(t, "task7-store", &resolveCalls)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"replacement siblings"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:gpt-4o", "b2:gpt-4o")
	acc.WireRequest.CandidateModel = "b1:gpt-4o | b2:gpt-4o"
	first := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return nil, aipapi.RecoverablePreOutputError(io.ErrUnexpectedEOF)
		},
	}
	second := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return task7ServingStream(t, 10, 2), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: first.OpenWire},
		"b2": {OpenWire: second.OpenWire},
	}

	ctx := task7ServingContext("req-task7-replace", "trace-task7-replace")
	task7ServeToTerminal(t, ctx, ex, acc, src)

	if got := resolveCalls.Load(); got != 1 {
		t.Fatalf("trusted store resolutions = %d, want exactly one across replacement attempts", got)
	}
	if got := len(first.Calls()); got != 1 {
		t.Fatalf("abandoned attempt opens = %d, want one", got)
	}
	if got := len(second.Calls()); got != 1 {
		t.Fatalf("replacement attempt opens = %d, want one", got)
	}
	legs := task7SinkLegs(sink)
	survivor := task7LegByBackend(t, legs, "b2")
	native := task7NativeObservations(t, survivor)
	if len(native) != 1 {
		t.Fatalf("survivor native observations = %d, want one", len(native))
	}
	if gotIn, gotOut, gotTotal := task7TokenMeasures(native[0]); gotIn != "10/0" || gotOut != "2/0" || gotTotal != "12/0" {
		t.Fatalf("survivor quantities = %s/%s/%s, want literal 10/0 2/0 12/0", gotIn, gotOut, gotTotal)
	}
	if native[0].Subject.StoreID != "task7-store" || native[0].Subject.BLegID != survivor.BLegID {
		t.Errorf("survivor attribution = %+v, want frozen store and replacement B-leg", native[0].Subject)
	}
	for _, leg := range legs {
		if leg.BLegID == survivor.BLegID {
			continue
		}
		if leaked := task7NativeObservations(t, leg); len(leaked) != 0 {
			t.Errorf("abandoned sibling leg %q carries %d native observations, want sibling attribution separate", leg.BLegID, len(leaked))
		}
		if strings.TrimSpace(leg.CallID.String()) != strings.TrimSpace(survivor.CallID.String()) {
			t.Errorf("sibling CallID = %q, survivor = %q, want one frozen billing call", leg.CallID.String(), survivor.CallID.String())
		}
	}
}

// TestTask7WireServingParallelLosersKeepSiblingAttribution proves requirement
// 4.4 for the parallel race on the serving path: the bridged winner keeps
// its own bound native evidence while each loser keeps its own, with no
// mixing across sibling B-leg attempts.
func TestTask7WireServingParallelLosersKeepSiblingAttribution(t *testing.T) {
	t.Parallel()
	var resolveCalls atomic.Int32
	ex, sink := task7SetupServing(t, "task7-store", &resolveCalls)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"parallel siblings"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, false, "b1:m1", "b2:m2")
	acc.WireRequest.CandidateModel = "b1:m1!b2:m2"
	secondEntered := make(chan struct{})
	firstOpened := make(chan struct{})
	var enterOnce, openOnce sync.Once
	winner := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			<-secondEntered
			openOnce.Do(func() { close(firstOpened) })
			return task7ServingStream(t, 10, 2), nil
		},
	}
	releaseLoser := make(chan struct{})
	defer close(releaseLoser)
	loser := &stubWireBackend{
		openFunc: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			enterOnce.Do(func() { close(secondEntered) })
			<-firstOpened
			return newTask7BlockingParserStream(t, 5, 1, releaseLoser), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"b1": {OpenWire: winner.OpenWire},
		"b2": {OpenWire: loser.OpenWire},
	}

	ctx := task7ServingContext("req-task7-parallel", "trace-task7-parallel")
	task7ServeToTerminal(t, ctx, ex, acc, src)

	legs := task7SinkLegs(sink)
	winnerLeg := task7LegByBackend(t, legs, "b1")
	loserLeg := task7LegByBackend(t, legs, "b2")
	if winnerLeg.BLegID == loserLeg.BLegID {
		t.Fatalf("winner and loser share B-leg %q, want distinct attempt identities", winnerLeg.BLegID)
	}
	winnerNative := task7NativeObservations(t, winnerLeg)
	if len(winnerNative) != 1 {
		t.Fatalf("winner native observations = %d, want the bound 10/2/12 exactly once", len(winnerNative))
	}
	if gotIn, gotOut, gotTotal := task7TokenMeasures(winnerNative[0]); gotIn != "10/0" || gotOut != "2/0" || gotTotal != "12/0" {
		t.Fatalf("winner quantities = %s/%s/%s, want literal 10/0 2/0 12/0", gotIn, gotOut, gotTotal)
	}
	if winnerNative[0].Subject.BLegID != winnerLeg.BLegID || winnerNative[0].Subject.StoreID != "task7-store" {
		t.Errorf("winner attribution = %+v, want frozen store and winner B-leg", winnerNative[0].Subject)
	}
	loserNative := task7NativeObservations(t, loserLeg)
	if len(loserNative) != 1 {
		t.Fatalf("loser native observations = %d, want its own pre-read 5/1/6 exactly once", len(loserNative))
	}
	if gotIn, gotOut, gotTotal := task7TokenMeasures(loserNative[0]); gotIn != "5/0" || gotOut != "1/0" || gotTotal != "6/0" {
		t.Fatalf("loser quantities = %s/%s/%s, want literal 5/0 1/0 6/0", gotIn, gotOut, gotTotal)
	}
	if loserNative[0].Subject.BLegID != loserLeg.BLegID || loserNative[0].Subject.StoreID != "task7-store" {
		t.Errorf("loser attribution = %+v, want frozen store and loser B-leg", loserNative[0].Subject)
	}
}

// TestTask7WireServingCancellationKeepsAttribution proves requirement 4.4
// for the canceled attempt: provider-only pre-read evidence survives
// cancellation on its own B-leg exactly once and never leaks to a sibling.
func TestTask7WireServingCancellationKeepsAttribution(t *testing.T) {
	t.Parallel()
	ex, sink := task7SetupServing(t, "task7-store", nil)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"cancel attribution"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	backend := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return task7ServingStream(t, 10, 2), nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{"default": {OpenWire: backend.OpenWire}}

	ctx := task7ServingContext("req-task7-cancel", "trace-task7-cancel")
	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody failed: %v", err)
	}
	if _, err := res.Stream.Recv(context.Background()); err != nil {
		t.Fatalf("first served Recv failed: %v", err)
	}
	managed, ok := res.Stream.(aipapi.ManagedEventStream)
	if !ok {
		t.Fatalf("served stream %T does not support Cancel", res.Stream)
	}
	managed.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelContextDone})
	for {
		if _, rerr := res.Stream.Recv(context.Background()); rerr != nil {
			break
		}
	}
	if err := res.Stream.Close(); err != nil {
		t.Fatalf("canceled stream Close failed: %v", err)
	}

	legs := task7SinkLegs(sink)
	if len(legs) != 1 {
		t.Fatalf("terminal legs after cancel = %d, want exactly one canceled attempt", len(legs))
	}
	if legs[0].Outcome != usageevidence.LegOutcomeCanceled {
		t.Fatalf("canceled leg outcome = %q, want canceled", legs[0].Outcome)
	}
	native := task7NativeObservations(t, legs[0])
	if len(native) != 1 {
		t.Fatalf("canceled native observations = %d, want pre-read evidence exactly once on its own B-leg", len(native))
	}
	if native[0].Subject.BLegID != legs[0].BLegID || native[0].Subject.StoreID != "task7-store" {
		t.Errorf("canceled attribution = %+v, want frozen store and canceled B-leg", native[0].Subject)
	}
}

// TestTask7WireServingRichPartialUnsupportedUnchanged proves requirements
// 4.3 and 4.5 through serving: rich partial/unsupported coverage and
// reasons pass the adapter chain and the terminal handoff unchanged, with
// no invented coverage on the lifted path.
func TestTask7WireServingRichPartialUnsupportedUnchanged(t *testing.T) {
	t.Parallel()
	ex, sink := task7SetupServing(t, "task7-store", nil)
	content := `{"model":"gpt-4o","messages":[{"role":"user","content":"rich coverage"}]}`
	src := newTestSource(content)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	rich := newTask7RichServingStream(t)
	backend := &stubWireBackend{
		openFunc: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			return rich, nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{"default": {OpenWire: backend.OpenWire}}

	ctx := task7ServingContext("req-task7-rich", "trace-task7-rich")
	task7ServeToTerminal(t, ctx, ex, acc, src)

	if got := rich.bindCalls.Load(); got != 1 {
		t.Fatalf("rich stream bindings = %d, want exactly one successful-open bind", got)
	}
	legs := task7SinkLegs(sink)
	if len(legs) != 1 {
		t.Fatalf("terminal legs = %d, want exactly one", len(legs))
	}
	dispositions := map[string]string{}
	for _, disposition := range legs[0].EconomicDispositions {
		dispositions[string(disposition.Coverage)] = disposition.CoverageReason
	}
	if dispositions["partial"] != "task7 partial reason" {
		t.Errorf("partial disposition reason = %q, want task7 partial reason unchanged", dispositions["partial"])
	}
	if dispositions["unsupported"] != "task7 unsupported reason" {
		t.Errorf("unsupported disposition reason = %q, want task7 unsupported reason unchanged", dispositions["unsupported"])
	}
	if len(dispositions) != 2 {
		t.Fatalf("rich dispositions = %+v, want exactly the partial and unsupported records", dispositions)
	}
	// Each disposition must reference a retained terminal observation with
	// the real parser quantities, proving coverage traveled with its
	// evidence instead of being invented or dropped.
	byIdentity := map[string]sdkmetering.Observation{}
	for _, observation := range legs[0].Observations {
		byIdentity[observation.IdentityKey()] = observation
	}
	wantMeasures := map[string]string{"partial": "10/0/2/0/12/0", "unsupported": "4/0/1/0/5/0"}
	for _, disposition := range legs[0].EconomicDispositions {
		observation, ok := byIdentity[disposition.ObservationIdentity]
		if !ok {
			t.Errorf("disposition %q references no retained observation", disposition.Coverage)
			continue
		}
		gotIn, gotOut, gotTotal := task7TokenMeasures(observation)
		if got := gotIn + "/" + gotOut + "/" + gotTotal; got != wantMeasures[string(disposition.Coverage)] {
			t.Errorf("disposition %q observation quantities = %s, want %s", disposition.Coverage, got, wantMeasures[string(disposition.Coverage)])
		}
		if observation.Subject.StoreID != "task7-store" || observation.Subject.BLegID != legs[0].BLegID {
			t.Errorf("disposition %q attribution = %+v, want frozen store and current B-leg", disposition.Coverage, observation.Subject)
		}
	}
}
