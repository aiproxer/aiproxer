package compatibleparity

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// FailurePhase classifies where a compatible parity failure surfaced.
type FailurePhase string

const (
	FailurePhaseNone FailurePhase = ""
	FailurePhaseOpen FailurePhase = "open"
	FailurePhaseRecv FailurePhase = "recv"
)

// OutcomeSnapshot captures canonical fields compared between essential and generic adapters.
type OutcomeSnapshot struct {
	Kinds           []aipapi.EventKind
	Text            string
	Reasoning       string
	Signature       string
	ToolName        string
	ToolCallID      string
	ToolArgs        string
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	ReasoningTokens int
	FinishReason    string
	TerminalKind    aipapi.EventKind
	TerminalCount   int
	FailurePhase    FailurePhase
	StreamErrCode   string
	StreamErrMsg    string
	EventErrCode    string
	EventErrMsg     string
	HTTPRequests    int
}

// CollectOutcome opens be for fx, drains events, and returns a comparable snapshot.
func CollectOutcome(t *testing.T, be execbackend.Backend, fx Fixture, ws *WireServer) OutcomeSnapshot {
	t.Helper()
	events, err := openAndCollectFixture(t, be, fx)
	return snapshotFrom(events, err, ws.RequestCount())
}

func openAndCollectFixture(t *testing.T, be execbackend.Backend, fx Fixture) ([]aipapi.Event, error) {
	t.Helper()
	if fx.CancelAfterOpen {
		return openAndCancelWithCall(t, be, fx, fx.Call)
	}
	es, err := be.Open(context.Background(), fx.Call, CandidateFor(fx))
	if err != nil {
		return nil, err
	}
	return DrainEvents(t, es)
}

func snapshotFrom(events []aipapi.Event, err error, httpRequests int) OutcomeSnapshot {
	out := OutcomeSnapshot{
		Kinds:           EventKinds(events),
		Text:            TextFromEvents(events),
		Reasoning:       ReasoningFromEvents(events),
		Signature:       SignatureFromEvents(events),
		ToolName:        ToolNameFromEvents(events),
		ToolCallID:      ToolCallIDFromEvents(events),
		ToolArgs:        ToolArgsFromEvents(events),
		InputTokens:     UsageField(events, usageInput),
		OutputTokens:    UsageField(events, usageOutput),
		TotalTokens:     UsageField(events, usageTotal),
		ReasoningTokens: UsageField(events, usageReasoning),
		FinishReason:    FinishReasonFromEvents(events),
		HTTPRequests:    httpRequests,
	}
	out.TerminalKind, out.TerminalCount = terminalStats(events)
	if err != nil {
		if len(events) == 0 {
			out.FailurePhase = FailurePhaseOpen
		} else {
			out.FailurePhase = FailurePhaseRecv
		}
		if streamErr, ok := errors.AsType[*aipapi.StreamError](err); ok {
			out.StreamErrCode = streamErr.Code
			out.StreamErrMsg = streamErr.Message
		}
	}
	if evErr, ok := eventErrorFrom(events); ok {
		out.EventErrCode = evErr.ErrorCode
		out.EventErrMsg = evErr.ErrorMessage
		if out.FailurePhase == FailurePhaseNone {
			out.FailurePhase = FailurePhaseRecv
		}
	}
	if err != nil && out.StreamErrCode == "" && out.EventErrCode == "" {
		// Preserve stable classification for wrapped SDK/transport failures.
		out.StreamErrMsg = stableErrTail(err)
	}
	return out
}

type usageField int

const (
	usageInput usageField = iota
	usageOutput
	usageTotal
	usageReasoning
)

func UsageField(events []aipapi.Event, field usageField) int {
	// Canonical usage accumulates across events: a stream may project
	// start-only input in one event and output-only delta quantities in the
	// next, so the snapshot must sum like every other canonical consumer
	// instead of reading the first usage event.
	var col aipapi.Collected
	for _, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		col.AccumulateUsage(ev)
	}
	switch field {
	case usageInput:
		return col.InputTokens
	case usageOutput:
		return col.OutputTokens
	case usageTotal:
		return col.TotalTokens
	case usageReasoning:
		return col.ReasoningTokens
	}
	return 0
}

func SignatureFromEvents(events []aipapi.Event) string {
	var b strings.Builder
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventReasoningSignatureDelta:
			b.WriteString(ev.Signature)
		case aipapi.EventReasoningPart:
			if ev.Reasoning != nil {
				b.WriteString(ev.Reasoning.Signature)
			}
		}
	}
	return b.String()
}

func ToolCallIDFromEvents(events []aipapi.Event) string {
	for _, ev := range events {
		if ev.Kind == aipapi.EventToolCallStarted && ev.ToolCallID != "" {
			return ev.ToolCallID
		}
	}
	return ""
}

func ToolArgsFromEvents(events []aipapi.Event) string {
	var b strings.Builder
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventToolCallArgsDelta:
			b.WriteString(ev.Delta)
		case aipapi.EventToolCallFinished:
			if ev.Delta != "" {
				b.WriteString(ev.Delta)
			}
		}
	}
	return b.String()
}

func FinishReasonFromEvents(events []aipapi.Event) string {
	for _, event := range slices.Backward(events) {
		if event.Kind == aipapi.EventResponseFinished {
			return event.FinishReason
		}
	}
	return ""
}

func terminalStats(events []aipapi.Event) (aipapi.EventKind, int) {
	var terminal aipapi.EventKind
	count := 0
	for _, ev := range events {
		if ev.Kind == aipapi.EventResponseFinished || ev.Kind == aipapi.EventError {
			count++
			terminal = ev.Kind
		}
	}
	return terminal, count
}

func eventErrorFrom(events []aipapi.Event) (aipapi.Event, bool) {
	for _, ev := range events {
		if ev.Kind == aipapi.EventError {
			return ev, true
		}
	}
	return aipapi.Event{}, false
}

func stableErrTail(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if idx := strings.LastIndex(msg, ": "); idx >= 0 {
		return msg[idx+2:]
	}
	return msg
}

// AssertOutcomesEqual compares essential and generic canonical snapshots.
func AssertOutcomesEqual(t *testing.T, fx Fixture, essential, generic OutcomeSnapshot) {
	t.Helper()
	if fx.ExpectFailure() {
		assertFailureParity(t, fx, essential, generic)
		return
	}
	assertSuccessParity(t, fx, essential, generic)
}

func assertFailureParity(t *testing.T, fx Fixture, essential, generic OutcomeSnapshot) {
	t.Helper()
	if essential.FailurePhase == FailurePhaseNone && generic.FailurePhase == FailurePhaseNone {
		t.Fatal("expected failure outcome for both adapters")
	}
	if essential.FailurePhase != generic.FailurePhase {
		t.Fatalf("failure phase mismatch essential=%q generic=%q", essential.FailurePhase, generic.FailurePhase)
	}
	if fx.Scenario == ScenarioPostOutputError || fx.Scenario == ScenarioNoRetryAfterOutput {
		if fx.Delivery != aipapi.DeliveryModeNonStreaming {
			if essential.Text == "" || generic.Text == "" {
				t.Fatalf("post-output failure must preserve partial text essential=%q generic=%q", essential.Text, generic.Text)
			}
			if essential.Text != generic.Text {
				t.Fatalf("partial text mismatch essential=%q generic=%q", essential.Text, generic.Text)
			}
		}
		if fx.Scenario == ScenarioNoRetryAfterOutput {
			if fx.Delivery != aipapi.DeliveryModeNonStreaming {
				if essential.HTTPRequests != 1 || generic.HTTPRequests != 1 {
					t.Fatalf("no-retry-after-output requires exactly one upstream request essential=%d generic=%d", essential.HTTPRequests, generic.HTTPRequests)
				}
			} else if essential.HTTPRequests != generic.HTTPRequests {
				t.Fatalf("no-retry-after-output request count mismatch essential=%d generic=%d", essential.HTTPRequests, generic.HTTPRequests)
			}
		}
	}
	if essential.StreamErrCode != "" || generic.StreamErrCode != "" {
		if essential.StreamErrCode != generic.StreamErrCode {
			t.Fatalf("stream error code mismatch essential=%q generic=%q", essential.StreamErrCode, generic.StreamErrCode)
		}
	}
	if essential.EventErrCode != "" || generic.EventErrCode != "" {
		if essential.EventErrCode != generic.EventErrCode {
			t.Fatalf("event error code mismatch essential=%q generic=%q", essential.EventErrCode, generic.EventErrCode)
		}
	}
	if len(essential.Kinds) > 0 && len(generic.Kinds) > 0 {
		if len(essential.Kinds) != len(generic.Kinds) {
			t.Fatalf("failure kind length mismatch essential=%v generic=%v", essential.Kinds, generic.Kinds)
		}
		for i := range essential.Kinds {
			if essential.Kinds[i] != generic.Kinds[i] {
				t.Fatalf("failure kind[%d] essential=%v generic=%v", i, essential.Kinds[i], generic.Kinds[i])
			}
		}
	}
}

func assertSuccessParity(t *testing.T, fx Fixture, essential, generic OutcomeSnapshot) {
	t.Helper()
	if essential.FailurePhase != FailurePhaseNone || generic.FailurePhase != FailurePhaseNone {
		t.Fatalf("unexpected failure essential=%+v generic=%+v", essential, generic)
	}
	if len(essential.Kinds) != len(generic.Kinds) {
		t.Fatalf("kind length mismatch essential=%v generic=%v", essential.Kinds, generic.Kinds)
	}
	for i := range essential.Kinds {
		if essential.Kinds[i] != generic.Kinds[i] {
			t.Fatalf("kind[%d] essential=%v generic=%v\nessential=%v\ngeneric=%v", i, essential.Kinds[i], generic.Kinds[i], essential.Kinds, generic.Kinds)
		}
	}
	if fx.Scenario == ScenarioTerminalOrdering {
		AssertTerminalLast(t, kindsToEvents(essential.Kinds))
		AssertTerminalLast(t, kindsToEvents(generic.Kinds))
	}
	if fx.WantText != "" {
		if essential.Text != generic.Text || !strings.Contains(essential.Text, fx.WantText) {
			t.Fatalf("text mismatch essential=%q generic=%q want contains %q", essential.Text, generic.Text, fx.WantText)
		}
	}
	if fx.WantReason != "" {
		if essential.Reasoning != generic.Reasoning || !strings.Contains(essential.Reasoning, fx.WantReason) {
			t.Fatalf("reasoning mismatch essential=%q generic=%q want contains %q", essential.Reasoning, generic.Reasoning, fx.WantReason)
		}
	}
	if fx.WantSignature != "" {
		if essential.Signature != generic.Signature || !strings.Contains(essential.Signature, fx.WantSignature) {
			t.Fatalf("signature mismatch essential=%q generic=%q want contains %q", essential.Signature, generic.Signature, fx.WantSignature)
		}
	}
	if fx.WantTool != "" {
		if essential.ToolName != generic.ToolName || essential.ToolName != fx.WantTool {
			t.Fatalf("tool name mismatch essential=%q generic=%q want %q", essential.ToolName, generic.ToolName, fx.WantTool)
		}
	}
	if fx.WantToolArgs != "" {
		if essential.ToolArgs != generic.ToolArgs || !strings.Contains(essential.ToolArgs, fx.WantToolArgs) {
			t.Fatalf("tool args mismatch essential=%q generic=%q want contains %q", essential.ToolArgs, generic.ToolArgs, fx.WantToolArgs)
		}
	}
	if fx.WantFinishReason != "" {
		if essential.FinishReason != generic.FinishReason {
			t.Fatalf("finish reason mismatch essential=%q generic=%q", essential.FinishReason, generic.FinishReason)
		}
		if essential.FinishReason != "" && essential.FinishReason != fx.WantFinishReason {
			t.Fatalf("finish reason mismatch essential=%q generic=%q want %q", essential.FinishReason, generic.FinishReason, fx.WantFinishReason)
		}
	}
	if fx.WantInputTokens > 0 {
		if essential.InputTokens != generic.InputTokens || essential.InputTokens != fx.WantInputTokens {
			t.Fatalf("input tokens mismatch essential=%d generic=%d want %d", essential.InputTokens, generic.InputTokens, fx.WantInputTokens)
		}
	}
	if fx.WantOutputTokens > 0 {
		if essential.OutputTokens != generic.OutputTokens || essential.OutputTokens != fx.WantOutputTokens {
			t.Fatalf("output tokens mismatch essential=%d generic=%d want %d", essential.OutputTokens, generic.OutputTokens, fx.WantOutputTokens)
		}
	}
	if fx.WantTotalTokens > 0 {
		if essential.TotalTokens != generic.TotalTokens || essential.TotalTokens != fx.WantTotalTokens {
			t.Fatalf("total tokens mismatch essential=%d generic=%d want %d", essential.TotalTokens, generic.TotalTokens, fx.WantTotalTokens)
		}
	}
	if fx.WantReasoningTokens > 0 {
		if essential.ReasoningTokens != generic.ReasoningTokens || essential.ReasoningTokens != fx.WantReasoningTokens {
			t.Fatalf("reasoning tokens mismatch essential=%d generic=%d want %d", essential.ReasoningTokens, generic.ReasoningTokens, fx.WantReasoningTokens)
		}
	}
	if essential.TerminalCount != 1 || generic.TerminalCount != 1 {
		t.Fatalf("expected exactly one terminal outcome essential=%d generic=%d", essential.TerminalCount, generic.TerminalCount)
	}
	if essential.TerminalKind != aipapi.EventResponseFinished || generic.TerminalKind != aipapi.EventResponseFinished {
		t.Fatalf("terminal kind mismatch essential=%v generic=%v", essential.TerminalKind, generic.TerminalKind)
	}
}

func kindsToEvents(kinds []aipapi.EventKind) []aipapi.Event {
	out := make([]aipapi.Event, len(kinds))
	for i, k := range kinds {
		out[i] = aipapi.Event{Kind: k}
	}
	return out
}

// AssertMultimodalRequestMapped verifies upstream request carried the image reference.
func AssertMultimodalRequestMapped(t *testing.T, ws *WireServer, family Family) {
	t.Helper()
	bodies := ws.Bodies()
	if len(bodies) == 0 {
		t.Fatal("expected upstream request body")
	}
	body := string(bodies[0])
	switch family {
	case FamilyAnthropic:
		if !strings.Contains(body, "cdn.example/parity.png") && !strings.Contains(body, "image") {
			t.Fatalf("anthropic multimodal request missing image mapping: %q", body)
		}
	default:
		if !strings.Contains(body, "cdn.example/parity.png") && !strings.Contains(body, "image_url") {
			t.Fatalf("openai multimodal request missing image mapping: %q", body)
		}
	}
}

// ApplyDeliveryMode sets invocation transport fields on a fixture call copy.
func ApplyDeliveryMode(call aipapi.Call, mode aipapi.DeliveryMode) aipapi.Call {
	call.Invocation.DeliveryMode = mode
	call.Invocation.TransportMode = aipapi.PreferredTransportMode(mode)
	return call
}

// WithDelivery returns a fixture copy using the given delivery mode.
func (fx Fixture) WithDelivery(mode aipapi.DeliveryMode) Fixture {
	out := fx
	out.Delivery = mode
	out.Call = ApplyDeliveryMode(fx.Call, mode)
	if mode == aipapi.DeliveryModeNonStreaming {
		out.Name = fx.Name + "/non_streaming"
	} else if fx.Delivery == aipapi.DeliveryModeNonStreaming {
		out.Name = strings.TrimSuffix(fx.Name, "/non_streaming")
	}
	return out
}

// ExpectFailure reports whether the fixture expects Open/Recv failure.
func (fx Fixture) ExpectFailure() bool {
	switch fx.Scenario {
	case ScenarioError, ScenarioCancellation, ScenarioPostOutputError, ScenarioNoRetryAfterOutput:
		return true
	default:
		return fx.OpenFails
	}
}

// OpenAndCollectWithCall opens using an explicit call (for delivery-mode variants).
func OpenAndCollectWithCall(t *testing.T, be execbackend.Backend, fx Fixture, call aipapi.Call) ([]aipapi.Event, error) {
	t.Helper()
	if fx.CancelAfterOpen {
		return openAndCancelWithCall(t, be, fx, call)
	}
	es, err := be.Open(context.Background(), call, CandidateFor(fx))
	if err != nil {
		return nil, err
	}
	return DrainEvents(t, es)
}

func openAndCancelWithCall(t *testing.T, be execbackend.Backend, fx Fixture, call aipapi.Call) ([]aipapi.Event, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(25*time.Millisecond, cancel)
	es, err := be.Open(ctx, call, CandidateFor(fx))
	if err != nil {
		return nil, err
	}
	return drainEventsWithContext(ctx, es)
}
