package backend

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Connector terminal-semantics matrix (issue #1104).
//
// This file adds executable connector rows to the terminal-semantics matrix
// WITHOUT touching the two existing matrices
// (frontend terminal_semantics_matrix_test.go, backend
// terminal_semantics_matrix_test.go). It covers optional executable connectors
// under connectors/ and asserts the same five concerns through the connector
// boundary (backendTerminalConcern / allBackendTerminalConcerns are shared, so
// the concern set cannot drift).
//
// Keying: rows are keyed by connector factory kind parsed from
// connectors/*/release.yaml (the packaging index source per ADR 0008). The
// factory kind is the discovered backend identity (BackendSourceDiscovered
// kind referenced by config PluginConfig.Kind); the manifest plugin ID is
// kept alongside for diagnostics. Optional connectors are NEVER added to the
// essential/standard_table fixed tables (arch guard below + ADR 0008).
//
// ABI transport and connector conformance are separate obligations. The ABI
// preserves FinishReason/ResponseStatus (adapter/terminal_fields_test.go), but
// that does not make connectors emit metadata or provide staged-executable
// proofs. Remaining connector cells explicitly track that work in issue #1104;
// the completeness guard still rejects stale claims that the ABI is absent.

// connectorTerminalCell is one matrix cell. run is a compile-time reference
// to the covering test; na is an explicit not-applicable reason; sameAs
// delegates a connector to an essential protocol-family row in
// backendTerminalMatrix where the canonical terminal mapping is identical.
type connectorTerminalCell struct {
	run    func(t *testing.T)
	na     string
	sameAs string
}

// connectorBlockedABIMarker is the stable substring identifying ABI-blocked na
// cells. The completeness test uses it to fail once the ABI lands.
const connectorBlockedABIMarker = "blocked on connector ABI"

const connectorTerminalHarnessReason = "issue #1104: terminal ABI transport is available; connector metadata emission and staged-executable terminal proof remain unverified"

const connectorDeferredHarnessReason = "no in-process connector harness without new prod code; dedicated staged-executable cell deferred"

const connectorLocalStubErrorReason = "local-stub has no upstream; connector error path needs staged-executable harness; deferred"

// connectorDeferredRow is the default row for connectors with no executable
// harness yet. ABI availability alone is not evidence that the connector
// emits the required terminal fields; issue #1104 owns that remaining proof.
func connectorDeferredRow() map[backendTerminalConcern]connectorTerminalCell {
	return map[backendTerminalConcern]connectorTerminalCell{
		backendConcernCleanText:     {na: connectorTerminalHarnessReason},
		backendConcernCleanToolCall: {na: connectorTerminalHarnessReason},
		backendConcernOutputLimit:   {na: connectorTerminalHarnessReason},
		backendConcernContentFilter: {na: connectorTerminalHarnessReason},
		backendConcernEmptyTurn:     {na: connectorTerminalHarnessReason},
		backendConcernCancellation:  {na: connectorDeferredHarnessReason},
		backendConcernUpstreamError: {na: connectorDeferredHarnessReason},
		backendConcernUsageReported: {na: connectorDeferredHarnessReason},
	}
}

// connectorFinishReasonConcerns are the matrix concerns whose essential cells
// assert the terminal FinishReason/ResponseStatus taxonomy verbatim (clean,
// tool, limit, refusal, empty). Connector event paths emit a bare
// EventResponseFinished today (connector-support/openaicompat stream.go End
// plus decodeNonStream, connectors/localstub service.go canonicalEvents), so
// delegating these concerns via sameAs would execute the essential family's
// proof without proving the connector emits the taxonomy. sameAs stays
// available for usage/cancellation/upstream_error with a per-row justification;
// these five require a dedicated staged-executable run (issue #1104).
var connectorFinishReasonConcerns = map[backendTerminalConcern]bool{
	backendConcernCleanText:     true,
	backendConcernCleanToolCall: true,
	backendConcernOutputLimit:   true,
	backendConcernContentFilter: true,
	backendConcernEmptyTurn:     true,
}

// stagedLocalStubUsageCell is the first staged-executable proof in the
// connector matrix (issue #1104): it drives the real localstub executable
// (built once per test binary inside its module, launched over gRPC through
// the production host adapter) and asserts the canonical usage-before-finish
// shape with explicit presence. The stub ignores call content and has no
// upstream, so there is no probe assertion — the boundary crossing itself is
// the claim: metadata (usage with presence) survives the connector wire.
func stagedLocalStubUsageCell(t *testing.T) {
	t.Helper()
	be := conformance.StagedConnectorBackend(t, conformance.BackendLocalStub, "")
	call := aipapi.Call{
		ID:       "terminal-usage-reported",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "local-stub", Model: "stub-default"}}
	stream, err := be.Open(context.Background(), call, cand)
	if err != nil {
		t.Fatalf("local-stub usage_reported: staged open: %v", err)
	}
	var events []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("local-stub usage_reported: staged recv: %v", err)
		}
		events = append(events, ev)
	}
	_ = stream.Close()
	usageIdx, finishIdx := -1, -1
	for i, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta && ev.UsagePresence.Any() {
			usageIdx = i
		}
		if ev.Kind == aipapi.EventResponseFinished {
			finishIdx = i
		}
	}
	if usageIdx < 0 {
		t.Fatalf("local-stub usage_reported: no EventUsageDelta with explicit presence through staged executable; events=%v", eventKinds(events))
	}
	if finishIdx < 0 {
		t.Fatalf("local-stub usage_reported: no terminal ResponseFinished through staged executable; events=%v", eventKinds(events))
	}
	if usageIdx > finishIdx {
		t.Fatalf("local-stub usage_reported: usage delta after finish through staged executable (usage=%d finish=%d)", usageIdx, finishIdx)
	}
}

// stagedChatOrigin serves one terminal concern's chat payloads through
// the reference OpenAI-chat emulator (existing Config override hooks, no
// emulator change).
func stagedChatOrigin(t *testing.T, probe *standardProbe, nonStreamJSON, streamSSE string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(countingHandler(probe, openaichat.NewHandler(openaichat.Config{
		AllowMissingBearer: true,
		NonStreamJSON:      nonStreamJSON,
		StreamSSE:          streamSSE,
	})))
}

// driveStagedChatConnector drives one real chat-flavor connector executable
// against a scripted origin for one concern over both transports, asserting
// exactly one upstream call per mode. The terminal taxonomy assertion lives
// in check.
func driveStagedChatConnector(t *testing.T, harnessID, backendID, model string, concern backendTerminalConcern, nonStreamJSON, streamSSE string, check func(*testing.T, aipapi.TransportMode, []aipapi.Event)) {
	t.Helper()
	probe := &standardProbe{}
	srv := stagedChatOrigin(t, probe, nonStreamJSON, streamSSE)
	t.Cleanup(srv.Close)
	be := conformance.StagedConnectorBackend(t, harnessID, srv.URL)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backendID, Model: model}}
	for _, mode := range []aipapi.TransportMode{aipapi.TransportModeNonStreaming, aipapi.TransportModeStreaming} {
		probe.Reset()
		call := aipapi.Call{
			ID:       "terminal-" + string(concern),
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{
				TransportMode: mode,
			},
		}
		stream, err := be.Open(context.Background(), call, cand)
		if err != nil {
			t.Fatalf("%s %s %s: staged open: %v", backendID, concern, mode, err)
		}
		var events []aipapi.Event
		for {
			ev, err := stream.Recv(context.Background())
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s %s %s: staged recv: %v", backendID, concern, mode, err)
			}
			events = append(events, ev)
		}
		_ = stream.Close()
		if probe.RequestCount() != 1 {
			t.Fatalf("%s %s %s: upstream calls=%d, want 1", backendID, concern, mode, probe.RequestCount())
		}
		check(t, mode, events)
	}
}

// stagedChatTerminal returns the terminal event or fails.
func stagedChatTerminal(t *testing.T, backendID string, concern backendTerminalConcern, mode aipapi.TransportMode, events []aipapi.Event) *aipapi.Event {
	t.Helper()
	fin := lastResponseFinished(events)
	if fin == nil {
		t.Fatalf("%s %s %s: no terminal ResponseFinished through staged executable; events=%v", backendID, concern, mode, eventKinds(events))
	}
	return fin
}

// stagedChatPayloads returns the scripted origin payloads for one shared
// chat-mapping concern. Payloads are connector-independent: every
// chat-flavor openaicompat connector speaks the same upstream vocabulary.
func stagedChatPayloads(concern backendTerminalConcern) (nonStreamJSON, streamSSE string) {
	switch concern {
	case backendConcernCleanText:
		return `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`,
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}` + "\n\n" +
				"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
	case backendConcernCleanToolCall:
		return `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_ab","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n" +
				"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" + "data: [DONE]\n\n"
	case backendConcernOutputLimit:
		return `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"cut"},"finish_reason":"length"}]}`,
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"cut"},"finish_reason":null}]}` + "\n\n" +
				"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}` + "\n\n" + "data: [DONE]\n\n"
	case backendConcernContentFilter:
		return `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"I cannot help with that"},"finish_reason":"content_filter"}]}`,
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"I cannot help with that"},"finish_reason":null}]}` + "\n\n" +
				"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}` + "\n\n" + "data: [DONE]\n\n"
	case backendConcernEmptyTurn:
		return `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`,
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
	case backendConcernUsageReported:
		return `{"id":"c","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}` + "\n\n" +
				"data: " + `{"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" + "data: [DONE]\n\n"
	default:
		return "", ""
	}
}

// checkStagedChatConcern asserts the terminal taxonomy for one shared
// chat-mapping concern: identical across connectors because the mapping
// lives in the shared openaicompat module.
func checkStagedChatConcern(t *testing.T, backendID string, concern backendTerminalConcern, mode aipapi.TransportMode, events []aipapi.Event) {
	t.Helper()
	if concern == backendConcernCleanToolCall {
		found := false
		for _, ev := range events {
			if ev.Kind == aipapi.EventToolCallFinished && ev.ToolCallID == "call_ab" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s clean_tool_call %s: no ToolCallFinished for call_ab; events=%v", backendID, mode, eventKinds(events))
		}
	}
	if concern == backendConcernEmptyTurn {
		for _, ev := range events {
			switch ev.Kind {
			case aipapi.EventTextDelta, aipapi.EventReasoningDelta,
				aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
				t.Fatalf("%s empty_turn %s: visible %s event on an empty turn", backendID, mode, ev.Kind)
			case aipapi.EventError:
				t.Fatalf("%s empty_turn %s: terminal EventError on an empty turn", backendID, mode)
			}
		}
	}
	fin := stagedChatTerminal(t, backendID, concern, mode, events)
	if concern == backendConcernUsageReported {
		usageIdx, finishIdx := -1, -1
		for i, ev := range events {
			if ev.Kind == aipapi.EventUsageDelta && ev.UsagePresence.Any() {
				usageIdx = i
			}
			if ev.Kind == aipapi.EventResponseFinished {
				finishIdx = i
			}
		}
		if usageIdx < 0 {
			t.Fatalf("%s usage_reported %s: no EventUsageDelta with explicit presence; events=%v", backendID, mode, eventKinds(events))
		}
		if finishIdx < 0 || usageIdx > finishIdx {
			t.Fatalf("%s usage_reported %s: usage must precede finish (usage=%d finish=%d)", backendID, mode, usageIdx, finishIdx)
		}
		return
	}
	switch concern {
	case backendConcernCleanText, backendConcernCleanToolCall, backendConcernEmptyTurn:
		if fin.FinishReason != "" {
			t.Fatalf("%s %s %s: FinishReason=%q, want empty", backendID, concern, mode, fin.FinishReason)
		}
		if fin.ResponseStatus == "incomplete" {
			t.Fatalf("%s %s %s: ResponseStatus=%q, want not incomplete", backendID, concern, mode, fin.ResponseStatus)
		}
	case backendConcernOutputLimit:
		if fin.ResponseStatus != "incomplete" {
			t.Fatalf("%s output_limit %s: ResponseStatus=%q, want incomplete", backendID, mode, fin.ResponseStatus)
		}
		if fin.FinishReason != "length" {
			t.Fatalf("%s output_limit %s: FinishReason=%q, want length", backendID, mode, fin.FinishReason)
		}
	case backendConcernContentFilter:
		if fin.ResponseStatus != "" {
			t.Fatalf("%s content_filter %s: ResponseStatus=%q, want empty", backendID, mode, fin.ResponseStatus)
		}
		if fin.FinishReason != "content_filter" {
			t.Fatalf("%s content_filter %s: FinishReason=%q, want content_filter (verbatim, never length)", backendID, mode, fin.FinishReason)
		}
	}
}

// driveStagedChatFamilyCell drives one concern for one chat-flavor connector
// through its real executable: the one-line cell body for every staged row
// sharing the openaicompat chat mapping.
func driveStagedChatFamilyCell(t *testing.T, harnessID, backendID, model string, concern backendTerminalConcern) {
	t.Helper()
	nonStreamJSON, streamSSE := stagedChatPayloads(concern)
	if nonStreamJSON == "" {
		t.Fatalf("%s %s: no staged chat payloads for concern", backendID, concern)
	}
	driveStagedChatConnector(t, harnessID, backendID, model, concern, nonStreamJSON, streamSSE, func(t *testing.T, mode aipapi.TransportMode, events []aipapi.Event) {
		t.Helper()
		checkStagedChatConcern(t, backendID, concern, mode, events)
	})
}

func stagedLlamacppCleanText(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLlamaCpp, "llamacpp", "default", backendConcernCleanText)
}

func stagedLlamacppCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLlamaCpp, "llamacpp", "default", backendConcernCleanToolCall)
}

func stagedLlamacppOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLlamaCpp, "llamacpp", "default", backendConcernOutputLimit)
}

func stagedLlamacppContentFilter(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLlamaCpp, "llamacpp", "default", backendConcernContentFilter)
}

func stagedLlamacppEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLlamaCpp, "llamacpp", "default", backendConcernEmptyTurn)
}

func stagedLlamacppUsageReported(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLlamaCpp, "llamacpp", "default", backendConcernUsageReported)
}

func stagedLmstudioCleanText(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLmstudio, "lmstudio", "default", backendConcernCleanText)
}

func stagedLmstudioCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLmstudio, "lmstudio", "default", backendConcernCleanToolCall)
}

func stagedLmstudioOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLmstudio, "lmstudio", "default", backendConcernOutputLimit)
}

func stagedLmstudioContentFilter(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLmstudio, "lmstudio", "default", backendConcernContentFilter)
}

func stagedLmstudioEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLmstudio, "lmstudio", "default", backendConcernEmptyTurn)
}

func stagedLmstudioUsageReported(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendLmstudio, "lmstudio", "default", backendConcernUsageReported)
}

func stagedVllmCleanText(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendVllm, "vllm", "default", backendConcernCleanText)
}

func stagedVllmCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendVllm, "vllm", "default", backendConcernCleanToolCall)
}

func stagedVllmOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendVllm, "vllm", "default", backendConcernOutputLimit)
}

func stagedVllmContentFilter(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendVllm, "vllm", "default", backendConcernContentFilter)
}

func stagedVllmEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendVllm, "vllm", "default", backendConcernEmptyTurn)
}

func stagedVllmUsageReported(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendVllm, "vllm", "default", backendConcernUsageReported)
}

// stagedResponsesPayloads returns the scripted origin payloads for one shared
// responses-mapping concern. Payloads mirror the essential responses
// vocabulary and are connector-independent.
func stagedResponsesPayloads(concern backendTerminalConcern) (nonStreamJSON, streamSSE string) {
	textOut := func(text string) string {
		return `{"id":"resp","object":"response","created_at":0,"status":"completed","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	}
	textSSE := func(text string) string {
		return "data: " + `{"type":"response.output_text.delta","delta":"` + text + `"}` + "\n\n" +
			"data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" + "data: [DONE]\n\n"
	}
	incompleteJSON := func(reason, text string) string {
		return `{"id":"resp","object":"response","created_at":0,"status":"incomplete","model":"m","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"` + text + `"}]}],"incomplete_details":{"reason":"` + reason + `"},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	}
	incompleteSSE := func(reason, text string) string {
		return "data: " + `{"type":"response.output_text.delta","delta":"` + text + `"}` + "\n\n" +
			"data: " + `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"` + reason + `"},"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" + "data: [DONE]\n\n"
	}
	switch concern {
	case backendConcernCleanText:
		return textOut("hello"), textSSE("hello")
	case backendConcernCleanToolCall:
		toolJSON := `{"id":"resp","object":"response","created_at":0,"status":"completed","model":"m","output":[{"type":"function_call","id":"fc_1","call_id":"call_ab","name":"get_weather","arguments":"{}","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		toolSSE := "data: " + `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_ab","name":"get_weather","arguments":""}}` + "\n\n" +
			"data: " + `{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{}"}` + "\n\n" +
			"data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" + "data: [DONE]\n\n"
		return toolJSON, toolSSE
	case backendConcernOutputLimit:
		return incompleteJSON("max_output_tokens", "cut"), incompleteSSE("max_output_tokens", "cut")
	case backendConcernContentFilter:
		return incompleteJSON("content_filter", "I cannot help with that"), incompleteSSE("content_filter", "I cannot help with that")
	case backendConcernEmptyTurn:
		return textOut(""), "data: " + `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" + "data: [DONE]\n\n"
	case backendConcernUsageReported:
		return textOut("hello"), textSSE("hello")
	default:
		return "", ""
	}
}

// checkStagedResponsesConcern asserts the terminal taxonomy for one shared
// responses-mapping concern.
func checkStagedResponsesConcern(t *testing.T, backendID string, concern backendTerminalConcern, mode aipapi.TransportMode, events []aipapi.Event) {
	t.Helper()
	if concern == backendConcernCleanToolCall {
		found := false
		for _, ev := range events {
			if ev.Kind == aipapi.EventToolCallFinished && ev.ToolCallID == "call_ab" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s clean_tool_call %s: no ToolCallFinished for call_ab; events=%v", backendID, mode, eventKinds(events))
		}
	}
	if concern == backendConcernEmptyTurn {
		for _, ev := range events {
			switch ev.Kind {
			case aipapi.EventTextDelta, aipapi.EventReasoningDelta,
				aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
				t.Fatalf("%s empty_turn %s: visible %s event on an empty turn", backendID, mode, ev.Kind)
			case aipapi.EventError:
				t.Fatalf("%s empty_turn %s: terminal EventError on an empty turn", backendID, mode)
			}
		}
	}
	fin := stagedChatTerminal(t, backendID, concern, mode, events)
	if concern == backendConcernUsageReported {
		usageIdx, finishIdx := -1, -1
		for i, ev := range events {
			if ev.Kind == aipapi.EventUsageDelta && ev.UsagePresence.Any() {
				usageIdx = i
			}
			if ev.Kind == aipapi.EventResponseFinished {
				finishIdx = i
			}
		}
		if usageIdx < 0 {
			t.Fatalf("%s usage_reported %s: no EventUsageDelta with explicit presence; events=%v", backendID, mode, eventKinds(events))
		}
		if finishIdx < 0 || usageIdx > finishIdx {
			t.Fatalf("%s usage_reported %s: usage must precede finish (usage=%d finish=%d)", backendID, mode, usageIdx, finishIdx)
		}
		return
	}
	switch concern {
	case backendConcernCleanText, backendConcernCleanToolCall, backendConcernEmptyTurn:
		if fin.ResponseStatus == "incomplete" {
			t.Fatalf("%s %s %s: ResponseStatus=%q, want not incomplete", backendID, concern, mode, fin.ResponseStatus)
		}
		if fin.FinishReason == "length" {
			t.Fatalf("%s %s %s: FinishReason=%q, want never length taxonomy", backendID, concern, mode, fin.FinishReason)
		}
	case backendConcernOutputLimit:
		if fin.ResponseStatus != "incomplete" {
			t.Fatalf("%s output_limit %s: ResponseStatus=%q, want incomplete", backendID, mode, fin.ResponseStatus)
		}
		if fin.FinishReason != "length" {
			t.Fatalf("%s output_limit %s: FinishReason=%q, want length", backendID, mode, fin.FinishReason)
		}
	case backendConcernContentFilter:
		if fin.ResponseStatus != "incomplete" {
			t.Fatalf("%s content_filter %s: ResponseStatus=%q, want incomplete", backendID, mode, fin.ResponseStatus)
		}
		if fin.FinishReason != "content_filter" {
			t.Fatalf("%s content_filter %s: FinishReason=%q, want content_filter (verbatim, never length)", backendID, mode, fin.FinishReason)
		}
	}
}

// driveStagedDualFlavorCell drives one concern for a dual-flavor connector
// through its real executable in both flavors: chat payloads with the chat
// operation and responses payloads with the responses operation, each over
// both transports. Taxonomy assertions are flavor-specific but identical in
// contract to the single-flavor drivers.
func driveStagedDualFlavorCell(t *testing.T, harnessID, backendID, model string, concern backendTerminalConcern) {
	t.Helper()
	chatNS, chatSSE := stagedChatPayloads(concern)
	if chatNS == "" {
		t.Fatalf("%s %s: no staged chat payloads for concern", backendID, concern)
	}
	respNS, respSSE := stagedResponsesPayloads(concern)
	if respNS == "" {
		t.Fatalf("%s %s: no staged responses payloads for concern", backendID, concern)
	}
	chatProbe := &standardProbe{}
	chatSrv := httptest.NewServer(countingHandler(chatProbe, openaichat.NewHandler(openaichat.Config{
		AllowMissingBearer: true,
		NonStreamJSON:      chatNS,
		StreamSSE:          chatSSE,
	})))
	t.Cleanup(chatSrv.Close)
	respProbe := &standardProbe{}
	respSrv := httptest.NewServer(countingHandler(respProbe, openairesponses.NewHandler(openairesponses.Config{
		AllowMissingBearer: true,
		NonStreamJSON:      respNS,
		StreamSSE:          respSSE,
	})))
	t.Cleanup(respSrv.Close)
	// One staged executable serves both flavors: base_url points at the
	// flavor origin per drive.
	for _, fd := range []struct {
		name      string
		operation aipapi.Operation
		srv       *httptest.Server
		probe     *standardProbe
		check     func(*testing.T, aipapi.TransportMode, []aipapi.Event)
	}{
		{"chat", aipapi.OperationOpenAIChatCompletions, chatSrv, chatProbe, func(t *testing.T, mode aipapi.TransportMode, events []aipapi.Event) {
			t.Helper()
			checkStagedChatConcern(t, backendID, concern, mode, events)
		}},
		{"responses", aipapi.OperationOpenAIResponses, respSrv, respProbe, func(t *testing.T, mode aipapi.TransportMode, events []aipapi.Event) {
			t.Helper()
			checkStagedResponsesConcern(t, backendID, concern, mode, events)
		}},
	} {
		be := conformance.StagedConnectorBackend(t, harnessID, fd.srv.URL)
		cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backendID, Model: model}}
		for _, mode := range []aipapi.TransportMode{aipapi.TransportModeNonStreaming, aipapi.TransportModeStreaming} {
			fd.probe.Reset()
			call := aipapi.Call{
				ID:       "terminal-" + string(concern),
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
				Invocation: aipapi.Invocation{
					Operation:     fd.operation,
					TransportMode: mode,
				},
			}
			stream, err := be.Open(context.Background(), call, cand)
			if err != nil {
				t.Fatalf("%s %s %s/%s: staged open: %v", backendID, concern, fd.name, mode, err)
			}
			var events []aipapi.Event
			for {
				ev, err := stream.Recv(context.Background())
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("%s %s %s/%s: staged recv: %v", backendID, concern, fd.name, mode, err)
				}
				events = append(events, ev)
			}
			_ = stream.Close()
			if fd.probe.RequestCount() != 1 {
				t.Fatalf("%s %s %s/%s: upstream calls=%d, want 1", backendID, concern, fd.name, mode, fd.probe.RequestCount())
			}
			fd.check(t, mode, events)
		}
	}
}

// driveStagedCancelCell proves cancellation crosses the connector boundary:
// with an already-canceled context the staged open must fail (or yield a
// stream with no successful terminal) and must never dial upstream.
func driveStagedCancelCell(t *testing.T, harnessID, backendID, model string) {
	t.Helper()
	probe := &standardProbe{}
	srv := httptest.NewServer(countingHandler(probe, openaichat.NewHandler(openaichat.Config{
		AllowMissingBearer: true,
	})))
	t.Cleanup(srv.Close)
	be := conformance.StagedConnectorBackend(t, harnessID, srv.URL)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backendID, Model: model}}
	call := aipapi.Call{
		ID:       "terminal-cancellation",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			TransportMode: aipapi.TransportModeStreaming,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, openErr := be.Open(ctx, call, cand)
	if openErr != nil {
		// Eager cancellation: the attempt never dialed upstream.
		if probe.RequestCount() != 0 {
			t.Fatalf("%s cancellation: upstream calls=%d, want 0 (canceled attempt must not dial)", backendID, probe.RequestCount())
		}
		return
	}
	if stream == nil {
		t.Fatalf("%s cancellation: nil stream with nil error", backendID)
	}
	defer func() { _ = stream.Close() }()
	// Lazy open: the dial happens on Recv, which must observe the canceled
	// context and fail before (or without) completing.
	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			break
		}
		if ev.Kind == aipapi.EventResponseFinished {
			t.Fatalf("%s cancellation: successful ResponseFinished on canceled attempt", backendID)
		}
	}
	if probe.RequestCount() != 0 {
		t.Fatalf("%s cancellation: upstream calls=%d, want 0 (canceled attempt must not dial)", backendID, probe.RequestCount())
	}
}

// drainStagedStream collects a staged stream to EOF, returning the terminal
// receive error (EOF maps to nil).
func drainStagedStream(stream aipapi.EventStream) ([]aipapi.Event, error) {
	var events []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
}

// driveStagedUpstreamErrorCell proves upstream failures cross the connector
// boundary: a 500 origin must surface as an upstream failure with no
// successful terminal, under the same shared contract the essential error
// cells assert (assertBackendUpstreamFailureTerminal).
func driveStagedUpstreamErrorCell(t *testing.T, harnessID, backendID, model string) {
	t.Helper()
	probe := &standardProbe{}
	srv := httptest.NewServer(countingHandler(probe, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
	})))
	t.Cleanup(srv.Close)
	be := conformance.StagedConnectorBackend(t, harnessID, srv.URL)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backendID, Model: model}}
	call := aipapi.Call{
		ID:       "terminal-upstream-error",
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIChatCompletions,
			TransportMode: aipapi.TransportModeStreaming,
		},
	}
	stream, openErr := be.Open(context.Background(), call, cand)
	// The connector open is lazy: the upstream dial happens on first Recv,
	// so the probe assertion runs after the drain.
	var events []aipapi.Event
	var recvErr error
	if stream != nil {
		events, recvErr = drainStagedStream(stream)
		_ = stream.Close()
	}
	if probe.RequestCount() != 1 {
		t.Fatalf("%s upstream_error: upstream calls=%d, want 1 (openErr=%v recvErr=%v)", backendID, probe.RequestCount(), openErr, recvErr)
	}
	terminalErr := openErr
	if terminalErr == nil {
		terminalErr = recvErr
	}
	assertBackendUpstreamFailureTerminal(t, backendID, events, terminalErr)
}

func driveStagedResponsesFamilyCell(t *testing.T, harnessID, backendID, model string, concern backendTerminalConcern) {
	t.Helper()
	nonStreamJSON, streamSSE := stagedResponsesPayloads(concern)
	if nonStreamJSON == "" {
		t.Fatalf("%s %s: no staged responses payloads for concern", backendID, concern)
	}
	probe := &standardProbe{}
	srv := httptest.NewServer(countingHandler(probe, openairesponses.NewHandler(openairesponses.Config{
		AllowMissingBearer: true,
		NonStreamJSON:      nonStreamJSON,
		StreamSSE:          streamSSE,
	})))
	t.Cleanup(srv.Close)
	be := conformance.StagedConnectorBackend(t, harnessID, srv.URL)
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: backendID, Model: model}}
	for _, mode := range []aipapi.TransportMode{aipapi.TransportModeNonStreaming, aipapi.TransportModeStreaming} {
		probe.Reset()
		call := aipapi.Call{
			ID:       "terminal-" + string(concern),
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
			Invocation: aipapi.Invocation{
				Operation:     aipapi.OperationOpenAIResponses,
				TransportMode: mode,
			},
		}
		stream, err := be.Open(context.Background(), call, cand)
		if err != nil {
			t.Fatalf("%s %s %s: staged open: %v", backendID, concern, mode, err)
		}
		var events []aipapi.Event
		for {
			ev, err := stream.Recv(context.Background())
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s %s %s: staged recv: %v", backendID, concern, mode, err)
			}
			events = append(events, ev)
		}
		_ = stream.Close()
		if probe.RequestCount() != 1 {
			t.Fatalf("%s %s %s: upstream calls=%d, want 1", backendID, concern, mode, probe.RequestCount())
		}
		checkStagedResponsesConcern(t, backendID, concern, mode, events)
	}
}

func stagedOpenrouterCleanText(t *testing.T) {
	t.Helper()
	driveStagedResponsesFamilyCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto", backendConcernCleanText)
}

func stagedOpenrouterCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedResponsesFamilyCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto", backendConcernCleanToolCall)
}

func stagedOpenrouterOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedResponsesFamilyCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto", backendConcernOutputLimit)
}

func stagedOpenrouterContentFilter(t *testing.T) {
	t.Helper()
	driveStagedResponsesFamilyCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto", backendConcernContentFilter)
}

func stagedOpenrouterEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedResponsesFamilyCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto", backendConcernEmptyTurn)
}

func stagedOpenrouterUsageReported(t *testing.T) {
	t.Helper()
	driveStagedResponsesFamilyCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto", backendConcernUsageReported)
}

func stagedOllamaCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendOllama, "ollama", "default", backendConcernCleanText)
}

func stagedOllamaCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendOllama, "ollama", "default", backendConcernCleanToolCall)
}

func stagedOllamaOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendOllama, "ollama", "default", backendConcernOutputLimit)
}

func stagedOllamaContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendOllama, "ollama", "default", backendConcernContentFilter)
}

func stagedOllamaEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendOllama, "ollama", "default", backendConcernEmptyTurn)
}

func stagedOllamaUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendOllama, "ollama", "default", backendConcernUsageReported)
}

func stagedOllamaCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendOllama, "ollama", "default")
}

func stagedOllamaUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendOllama, "ollama", "default")
}

func stagedHuggingfaceCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendHuggingFace, "huggingface", "default", backendConcernCleanText)
}

func stagedHuggingfaceCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendHuggingFace, "huggingface", "default", backendConcernCleanToolCall)
}

func stagedHuggingfaceOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendHuggingFace, "huggingface", "default", backendConcernOutputLimit)
}

func stagedHuggingfaceContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendHuggingFace, "huggingface", "default", backendConcernContentFilter)
}

func stagedHuggingfaceEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendHuggingFace, "huggingface", "default", backendConcernEmptyTurn)
}

func stagedHuggingfaceUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendHuggingFace, "huggingface", "default", backendConcernUsageReported)
}

func stagedHuggingfaceCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendHuggingFace, "huggingface", "default")
}

func stagedHuggingfaceUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendHuggingFace, "huggingface", "default")
}

func stagedInfomaniakCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default", backendConcernCleanText)
}

func stagedInfomaniakCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default", backendConcernCleanToolCall)
}

func stagedInfomaniakOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default", backendConcernOutputLimit)
}

func stagedInfomaniakContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default", backendConcernContentFilter)
}

func stagedInfomaniakEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default", backendConcernEmptyTurn)
}

func stagedInfomaniakUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default", backendConcernUsageReported)
}

func stagedInfomaniakCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default")
}

func stagedInfomaniakUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendInfomaniak, "infomaniak-ai", "default")
}

func stagedCommandCodeOpenAICleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default", backendConcernCleanText)
}

func stagedCommandCodeOpenAICleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default", backendConcernCleanToolCall)
}

func stagedCommandCodeOpenAIOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default", backendConcernOutputLimit)
}

func stagedCommandCodeOpenAIContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default", backendConcernContentFilter)
}

func stagedCommandCodeOpenAIEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default", backendConcernEmptyTurn)
}

func stagedCommandCodeOpenAIUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default", backendConcernUsageReported)
}

func stagedCommandCodeOpenAICancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default")
}

func stagedCommandCodeOpenAIUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendCommandCodeOpenAI, "commandcode-openai", "default")
}

func stagedLlamacppCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendLlamaCpp, "llamacpp", "default")
}

func stagedLlamacppUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendLlamaCpp, "llamacpp", "default")
}

func stagedLmstudioCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendLmstudio, "lmstudio", "default")
}

func stagedLmstudioUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendLmstudio, "lmstudio", "default")
}

func stagedVllmCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendVllm, "vllm", "default")
}

func stagedVllmUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendVllm, "vllm", "default")
}

func stagedOpenrouterCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto")
}

func stagedOpenrouterUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendOpenRouter, "openrouter", "openrouter/auto")
}

func stagedLocalStubCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendLocalStub, "local-stub", "stub-default")
}

func stagedNousPortalCleanText(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendNousPortal, "nous-portal", "default", backendConcernCleanText)
}

func stagedNousPortalCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendNousPortal, "nous-portal", "default", backendConcernCleanToolCall)
}

func stagedNousPortalOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendNousPortal, "nous-portal", "default", backendConcernOutputLimit)
}

func stagedNousPortalContentFilter(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendNousPortal, "nous-portal", "default", backendConcernContentFilter)
}

func stagedNousPortalEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendNousPortal, "nous-portal", "default", backendConcernEmptyTurn)
}

func stagedNousPortalUsageReported(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendNousPortal, "nous-portal", "default", backendConcernUsageReported)
}

func stagedNousPortalCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendNousPortal, "nous-portal", "default")
}

func stagedNousPortalUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendNousPortal, "nous-portal", "default")
}

func stagedQwenOAuthCleanText(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default", backendConcernCleanText)
}

func stagedQwenOAuthCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default", backendConcernCleanToolCall)
}

func stagedQwenOAuthOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default", backendConcernOutputLimit)
}

func stagedQwenOAuthContentFilter(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default", backendConcernContentFilter)
}

func stagedQwenOAuthEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default", backendConcernEmptyTurn)
}

func stagedQwenOAuthUsageReported(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default", backendConcernUsageReported)
}

func stagedQwenOAuthCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default")
}

func stagedQwenOAuthUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendQwenOAuth, "qwen-oauth", "default")
}

func stagedXaiOAuthCleanText(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default", backendConcernCleanText)
}

func stagedXaiOAuthCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default", backendConcernCleanToolCall)
}

func stagedXaiOAuthOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default", backendConcernOutputLimit)
}

func stagedXaiOAuthContentFilter(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default", backendConcernContentFilter)
}

func stagedXaiOAuthEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default", backendConcernEmptyTurn)
}

func stagedXaiOAuthUsageReported(t *testing.T) {
	t.Helper()
	driveStagedChatFamilyCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default", backendConcernUsageReported)
}

func stagedXaiOAuthCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default")
}

func stagedXaiOAuthUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendXaiOAuth, "xai-oauth", "default")
}

func stagedAzureOpenAICleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep", backendConcernCleanText)
}

func stagedAzureOpenAICleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep", backendConcernCleanToolCall)
}

func stagedAzureOpenAIOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep", backendConcernOutputLimit)
}

func stagedAzureOpenAIContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep", backendConcernContentFilter)
}

func stagedAzureOpenAIEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep", backendConcernEmptyTurn)
}

func stagedAzureOpenAIUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep", backendConcernUsageReported)
}

func stagedAzureOpenAICancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep")
}

func stagedAzureOpenAIUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendAzureOpenAI, "azure-openai", "azure-openai/test-dep")
}

func stagedCloudflareCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCloudflare, "cloudflare", "default", backendConcernCleanText)
}

func stagedCloudflareCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCloudflare, "cloudflare", "default", backendConcernCleanToolCall)
}

func stagedCloudflareOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCloudflare, "cloudflare", "default", backendConcernOutputLimit)
}

func stagedCloudflareContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCloudflare, "cloudflare", "default", backendConcernContentFilter)
}

func stagedCloudflareEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCloudflare, "cloudflare", "default", backendConcernEmptyTurn)
}

func stagedCloudflareUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendCloudflare, "cloudflare", "default", backendConcernUsageReported)
}

func stagedCloudflareCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendCloudflare, "cloudflare", "default")
}

func stagedCloudflareUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendCloudflare, "cloudflare", "default")
}

func stagedDatabricksCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendDatabricks, "databricks-ai", "default", backendConcernCleanText)
}

func stagedDatabricksCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendDatabricks, "databricks-ai", "default", backendConcernCleanToolCall)
}

func stagedDatabricksOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendDatabricks, "databricks-ai", "default", backendConcernOutputLimit)
}

func stagedDatabricksContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendDatabricks, "databricks-ai", "default", backendConcernContentFilter)
}

func stagedDatabricksEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendDatabricks, "databricks-ai", "default", backendConcernEmptyTurn)
}

func stagedDatabricksUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendDatabricks, "databricks-ai", "default", backendConcernUsageReported)
}

func stagedDatabricksCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendDatabricks, "databricks-ai", "default")
}

func stagedDatabricksUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendDatabricks, "databricks-ai", "default")
}

func stagedSnowflakeCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default", backendConcernCleanText)
}

func stagedSnowflakeCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default", backendConcernCleanToolCall)
}

func stagedSnowflakeOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default", backendConcernOutputLimit)
}

func stagedSnowflakeContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default", backendConcernContentFilter)
}

func stagedSnowflakeEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default", backendConcernEmptyTurn)
}

func stagedSnowflakeUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default", backendConcernUsageReported)
}

func stagedSnowflakeCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default")
}

func stagedSnowflakeUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendSnowflake, "snowflake-cortex", "default")
}

func stagedNvidiaCleanText(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendNVIDIA, "nvidia", "default", backendConcernCleanText)
}

func stagedNvidiaCleanToolCall(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendNVIDIA, "nvidia", "default", backendConcernCleanToolCall)
}

func stagedNvidiaOutputLimit(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendNVIDIA, "nvidia", "default", backendConcernOutputLimit)
}

func stagedNvidiaContentFilter(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendNVIDIA, "nvidia", "default", backendConcernContentFilter)
}

func stagedNvidiaEmptyTurn(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendNVIDIA, "nvidia", "default", backendConcernEmptyTurn)
}

func stagedNvidiaUsageReported(t *testing.T) {
	t.Helper()
	driveStagedDualFlavorCell(t, conformance.BackendNVIDIA, "nvidia", "default", backendConcernUsageReported)
}

func stagedNvidiaCancellation(t *testing.T) {
	t.Helper()
	driveStagedCancelCell(t, conformance.BackendNVIDIA, "nvidia", "default")
}

func stagedNvidiaUpstreamError(t *testing.T) {
	t.Helper()
	driveStagedUpstreamErrorCell(t, conformance.BackendNVIDIA, "nvidia", "default")
}

// connectorTerminalMatrix is the registry of every optional executable
// connector factory kind x every terminal concern. A missing row (new
// connector manifest) or a stale row (removed connector) fails the build via
// TestConnectorTerminalSemantics_Completeness.
var connectorTerminalMatrix = map[string]map[backendTerminalConcern]connectorTerminalCell{
	"acp":       connectorDeferredRow(),
	"agycliacp": connectorDeferredRow(),
	"azure-openai": {
		// Staged dual-flavor proof (issue #1155 remnant sweep).
		backendConcernCleanText:     {run: stagedAzureOpenAICleanText},
		backendConcernCleanToolCall: {run: stagedAzureOpenAICleanToolCall},
		backendConcernOutputLimit:   {run: stagedAzureOpenAIOutputLimit},
		backendConcernContentFilter: {run: stagedAzureOpenAIContentFilter},
		backendConcernEmptyTurn:     {run: stagedAzureOpenAIEmptyTurn},
		backendConcernCancellation:  {run: stagedAzureOpenAICancellation},
		backendConcernUpstreamError: {run: stagedAzureOpenAIUpstreamError},
		backendConcernUsageReported: {run: stagedAzureOpenAIUsageReported},
	},
	"claudeagentacp":   connectorDeferredRow(),
	"claudeagentmodel": connectorDeferredRow(),
	"cloudflare": {
		// Staged dual-flavor proof (issue #1155 remnant sweep).
		backendConcernCleanText:     {run: stagedCloudflareCleanText},
		backendConcernCleanToolCall: {run: stagedCloudflareCleanToolCall},
		backendConcernOutputLimit:   {run: stagedCloudflareOutputLimit},
		backendConcernContentFilter: {run: stagedCloudflareContentFilter},
		backendConcernEmptyTurn:     {run: stagedCloudflareEmptyTurn},
		backendConcernCancellation:  {run: stagedCloudflareCancellation},
		backendConcernUpstreamError: {run: stagedCloudflareUpstreamError},
		backendConcernUsageReported: {run: stagedCloudflareUsageReported},
	},
	"cohere":                connectorDeferredRow(),
	"commandcode-anthropic": connectorDeferredRow(),
	"commandcode-openai": {
		// Staged dual-flavor proof through the real binary against scripted
		// chat + responses origins (issue #1155 sweep).
		backendConcernCleanText:     {run: stagedCommandCodeOpenAICleanText},
		backendConcernCleanToolCall: {run: stagedCommandCodeOpenAICleanToolCall},
		backendConcernOutputLimit:   {run: stagedCommandCodeOpenAIOutputLimit},
		backendConcernContentFilter: {run: stagedCommandCodeOpenAIContentFilter},
		backendConcernEmptyTurn:     {run: stagedCommandCodeOpenAIEmptyTurn},
		backendConcernCancellation:  {run: stagedCommandCodeOpenAICancellation},
		backendConcernUpstreamError: {run: stagedCommandCodeOpenAIUpstreamError},
		backendConcernUsageReported: {run: stagedCommandCodeOpenAIUsageReported},
	},
	"cursorcliacp": connectorDeferredRow(),
	"databricks-ai": {
		// Staged dual-flavor proof (issue #1155 remnant sweep).
		backendConcernCleanText:     {run: stagedDatabricksCleanText},
		backendConcernCleanToolCall: {run: stagedDatabricksCleanToolCall},
		backendConcernOutputLimit:   {run: stagedDatabricksOutputLimit},
		backendConcernContentFilter: {run: stagedDatabricksContentFilter},
		backendConcernEmptyTurn:     {run: stagedDatabricksEmptyTurn},
		backendConcernCancellation:  {run: stagedDatabricksCancellation},
		backendConcernUpstreamError: {run: stagedDatabricksUpstreamError},
		backendConcernUsageReported: {run: stagedDatabricksUsageReported},
	},
	"geminicliacp": connectorDeferredRow(),
	"gitlab-duo":   connectorDeferredRow(),
	"huggingface": {
		// Staged dual-flavor proof through the real binary against scripted
		// chat + responses origins (issue #1155 sweep).
		backendConcernCleanText:     {run: stagedHuggingfaceCleanText},
		backendConcernCleanToolCall: {run: stagedHuggingfaceCleanToolCall},
		backendConcernOutputLimit:   {run: stagedHuggingfaceOutputLimit},
		backendConcernContentFilter: {run: stagedHuggingfaceContentFilter},
		backendConcernEmptyTurn:     {run: stagedHuggingfaceEmptyTurn},
		backendConcernCancellation:  {run: stagedHuggingfaceCancellation},
		backendConcernUpstreamError: {run: stagedHuggingfaceUpstreamError},
		backendConcernUsageReported: {run: stagedHuggingfaceUsageReported},
	},
	"infomaniak-ai": {
		// Staged dual-flavor proof through the real binary against scripted
		// chat + responses origins (issue #1155 sweep).
		backendConcernCleanText:     {run: stagedInfomaniakCleanText},
		backendConcernCleanToolCall: {run: stagedInfomaniakCleanToolCall},
		backendConcernOutputLimit:   {run: stagedInfomaniakOutputLimit},
		backendConcernContentFilter: {run: stagedInfomaniakContentFilter},
		backendConcernEmptyTurn:     {run: stagedInfomaniakEmptyTurn},
		backendConcernCancellation:  {run: stagedInfomaniakCancellation},
		backendConcernUpstreamError: {run: stagedInfomaniakUpstreamError},
		backendConcernUsageReported: {run: stagedInfomaniakUsageReported},
	},
	"llamacpp": {
		// Staged-executable proof through the real binary against scripted
		// chat origins (issue #1155): the shared openaicompat chat mapping
		// carries the terminal taxonomy over the connector boundary.
		backendConcernCleanText:     {run: stagedLlamacppCleanText},
		backendConcernCleanToolCall: {run: stagedLlamacppCleanToolCall},
		backendConcernOutputLimit:   {run: stagedLlamacppOutputLimit},
		backendConcernContentFilter: {run: stagedLlamacppContentFilter},
		backendConcernEmptyTurn:     {run: stagedLlamacppEmptyTurn},
		backendConcernCancellation:  {run: stagedLlamacppCancellation},
		backendConcernUpstreamError: {run: stagedLlamacppUpstreamError},
		backendConcernUsageReported: {run: stagedLlamacppUsageReported},
	},
	"lmstudio": {
		// Same staged proof as llamacpp: identical-shape chat-only connector
		// sharing the openaicompat mapping (issue #1155).
		backendConcernCleanText:     {run: stagedLmstudioCleanText},
		backendConcernCleanToolCall: {run: stagedLmstudioCleanToolCall},
		backendConcernOutputLimit:   {run: stagedLmstudioOutputLimit},
		backendConcernContentFilter: {run: stagedLmstudioContentFilter},
		backendConcernEmptyTurn:     {run: stagedLmstudioEmptyTurn},
		backendConcernCancellation:  {run: stagedLmstudioCancellation},
		backendConcernUpstreamError: {run: stagedLmstudioUpstreamError},
		backendConcernUsageReported: {run: stagedLmstudioUsageReported},
	},
	"minimax-oauth": connectorDeferredRow(),
	"nous-portal": {
		// Staged proof via static-token path against scripted chat origins
		// (issue #1155 custom batch): synthetic api_key secrets take the
		// static-token branch, so no OAuth issuer is involved.
		backendConcernCleanText:     {run: stagedNousPortalCleanText},
		backendConcernCleanToolCall: {run: stagedNousPortalCleanToolCall},
		backendConcernOutputLimit:   {run: stagedNousPortalOutputLimit},
		backendConcernContentFilter: {run: stagedNousPortalContentFilter},
		backendConcernEmptyTurn:     {run: stagedNousPortalEmptyTurn},
		backendConcernCancellation:  {run: stagedNousPortalCancellation},
		backendConcernUpstreamError: {run: stagedNousPortalUpstreamError},
		backendConcernUsageReported: {run: stagedNousPortalUsageReported},
	},
	"nvidia": {
		// Staged dual-flavor proof (issue #1155 remnant sweep).
		backendConcernCleanText:     {run: stagedNvidiaCleanText},
		backendConcernCleanToolCall: {run: stagedNvidiaCleanToolCall},
		backendConcernOutputLimit:   {run: stagedNvidiaOutputLimit},
		backendConcernContentFilter: {run: stagedNvidiaContentFilter},
		backendConcernEmptyTurn:     {run: stagedNvidiaEmptyTurn},
		backendConcernCancellation:  {run: stagedNvidiaCancellation},
		backendConcernUpstreamError: {run: stagedNvidiaUpstreamError},
		backendConcernUsageReported: {run: stagedNvidiaUsageReported},
	},
	"oci-generative-ai": connectorDeferredRow(),
	"ollama": {
		// Staged dual-flavor proof through the real binary against scripted
		// chat + responses origins (issue #1155 sweep).
		backendConcernCleanText:     {run: stagedOllamaCleanText},
		backendConcernCleanToolCall: {run: stagedOllamaCleanToolCall},
		backendConcernOutputLimit:   {run: stagedOllamaOutputLimit},
		backendConcernContentFilter: {run: stagedOllamaContentFilter},
		backendConcernEmptyTurn:     {run: stagedOllamaEmptyTurn},
		backendConcernCancellation:  {run: stagedOllamaCancellation},
		backendConcernUpstreamError: {run: stagedOllamaUpstreamError},
		backendConcernUsageReported: {run: stagedOllamaUsageReported},
	},
	"openai-chatgpt-plan": connectorDeferredRow(),
	"opencode-go":         connectorDeferredRow(),
	"openai-codex":        connectorDeferredRow(),
	"openrouter": {
		// Staged-executable proof through the real binary against scripted
		// responses origins (issue #1155): the shared openaicompat responses
		// mapping carries the terminal taxonomy over the connector boundary.
		backendConcernCleanText:     {run: stagedOpenrouterCleanText},
		backendConcernCleanToolCall: {run: stagedOpenrouterCleanToolCall},
		backendConcernOutputLimit:   {run: stagedOpenrouterOutputLimit},
		backendConcernContentFilter: {run: stagedOpenrouterContentFilter},
		backendConcernEmptyTurn:     {run: stagedOpenrouterEmptyTurn},
		backendConcernCancellation:  {run: stagedOpenrouterCancellation},
		backendConcernUpstreamError: {run: stagedOpenrouterUpstreamError},
		backendConcernUsageReported: {run: stagedOpenrouterUsageReported},
	},
	"qwen-oauth": {
		// Staged proof via static-token path against scripted chat origins
		// (issue #1155 custom batch): synthetic api_key secrets take the
		// static-token branch, so no OAuth issuer is involved.
		backendConcernCleanText:     {run: stagedQwenOAuthCleanText},
		backendConcernCleanToolCall: {run: stagedQwenOAuthCleanToolCall},
		backendConcernOutputLimit:   {run: stagedQwenOAuthOutputLimit},
		backendConcernContentFilter: {run: stagedQwenOAuthContentFilter},
		backendConcernEmptyTurn:     {run: stagedQwenOAuthEmptyTurn},
		backendConcernCancellation:  {run: stagedQwenOAuthCancellation},
		backendConcernUpstreamError: {run: stagedQwenOAuthUpstreamError},
		backendConcernUsageReported: {run: stagedQwenOAuthUsageReported},
	},
	"replicate": connectorDeferredRow(),
	"sagemaker": connectorDeferredRow(),
	"sapaicore": connectorDeferredRow(),
	"snowflake-cortex": {
		// Staged dual-flavor proof (issue #1155 remnant sweep).
		backendConcernCleanText:     {run: stagedSnowflakeCleanText},
		backendConcernCleanToolCall: {run: stagedSnowflakeCleanToolCall},
		backendConcernOutputLimit:   {run: stagedSnowflakeOutputLimit},
		backendConcernContentFilter: {run: stagedSnowflakeContentFilter},
		backendConcernEmptyTurn:     {run: stagedSnowflakeEmptyTurn},
		backendConcernCancellation:  {run: stagedSnowflakeCancellation},
		backendConcernUpstreamError: {run: stagedSnowflakeUpstreamError},
		backendConcernUsageReported: {run: stagedSnowflakeUsageReported},
	},
	"vertex": connectorDeferredRow(),
	"vllm": {
		// Same staged proof as llamacpp: identical-shape chat-only connector
		// sharing the openaicompat mapping (issue #1155).
		backendConcernCleanText:     {run: stagedVllmCleanText},
		backendConcernCleanToolCall: {run: stagedVllmCleanToolCall},
		backendConcernOutputLimit:   {run: stagedVllmOutputLimit},
		backendConcernContentFilter: {run: stagedVllmContentFilter},
		backendConcernEmptyTurn:     {run: stagedVllmEmptyTurn},
		backendConcernCancellation:  {run: stagedVllmCancellation},
		backendConcernUpstreamError: {run: stagedVllmUpstreamError},
		backendConcernUsageReported: {run: stagedVllmUsageReported},
	},
	"watsonx": connectorDeferredRow(),
	"xai-oauth": {
		// Staged proof via static-token path against scripted chat origins
		// (issue #1155 custom batch): synthetic api_key secrets take the
		// static-token branch, so no OAuth issuer is involved.
		backendConcernCleanText:     {run: stagedXaiOAuthCleanText},
		backendConcernCleanToolCall: {run: stagedXaiOAuthCleanToolCall},
		backendConcernOutputLimit:   {run: stagedXaiOAuthOutputLimit},
		backendConcernContentFilter: {run: stagedXaiOAuthContentFilter},
		backendConcernEmptyTurn:     {run: stagedXaiOAuthEmptyTurn},
		backendConcernCancellation:  {run: stagedXaiOAuthCancellation},
		backendConcernUpstreamError: {run: stagedXaiOAuthUpstreamError},
		backendConcernUsageReported: {run: stagedXaiOAuthUsageReported},
	},
	"local-stub": {
		backendConcernCleanText:     {na: connectorTerminalHarnessReason},
		backendConcernCleanToolCall: {na: connectorTerminalHarnessReason},
		backendConcernOutputLimit:   {na: connectorTerminalHarnessReason},
		backendConcernContentFilter: {na: connectorTerminalHarnessReason},
		backendConcernEmptyTurn:     {na: connectorTerminalHarnessReason},
		backendConcernCancellation:  {run: stagedLocalStubCancellation},
		backendConcernUpstreamError: {na: connectorLocalStubErrorReason},
		// local-stub usage is proven through the real staged executable
		// (built once per test binary, driven over gRPC): the first
		// staged-executable proof in this matrix (issue #1104).
		backendConcernUsageReported: {run: stagedLocalStubUsageCell},
	},
}

// connectorTerminalRegistry enumerates optional executable connectors from
// their release manifests (connectors/*/release.yaml), the packaging index
// source per ADR 0008. It returns factory kind -> plugin ID. A manifest
// without a factory kind fails loudly so the registry never silently drops a
// connector.
func connectorTerminalRegistry(t *testing.T) map[string]string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("connector terminal matrix: runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "..")
	matches, err := filepath.Glob(filepath.Join(root, "connectors", "*", "release.yaml"))
	if err != nil {
		t.Fatalf("connector terminal matrix: glob connectors/*/release.yaml: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("connector terminal matrix: no connector manifests under %s", filepath.Join(root, "connectors"))
	}
	out := make(map[string]string, len(matches))
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("connector terminal matrix: read %s: %v", m, err)
		}
		kind := scanReleaseField(string(data), "factory_kind")
		if kind == "" {
			t.Fatalf("connector terminal matrix: manifest %s has no factory_kind", m)
		}
		if prev, dup := out[kind]; dup {
			t.Fatalf("connector terminal matrix: duplicate factory_kind %q (%s and %s)", kind, prev, m)
		}
		out[kind] = scanReleaseField(string(data), "plugin_id")
	}
	return out
}

// scanReleaseField extracts a top-level `key: value` scalar from release.yaml
// without a YAML dependency; the release schema keeps these fields top-level.
func scanReleaseField(data, key string) string {
	for line := range strings.SplitSeq(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(trimmed, key+":"))
	}
	return ""
}

// connectorTerminalABIGranted reports whether the sibling ABI slice has
// landed: CanonicalEvent carries both FinishReason and ResponseStatus. While
// false, finish-bearing na cells with the blocked marker are the correct V1
// state; once true, the completeness test forces their conversion.
func connectorTerminalABIGranted() bool {
	rt := reflect.TypeFor[backendplugin.CanonicalEvent]()
	if _, ok := rt.FieldByName("FinishReason"); !ok {
		return false
	}
	_, ok := rt.FieldByName("ResponseStatus")
	return ok
}

// TestConnectorTerminalSemantics_Completeness is the point of the slice:
// adding a connector manifest without filling its matrix row fails go test,
// and a stale row for a removed connector fails too. It also guards ADR 0008
// (no optional connector in the essential tables) and forces blocked na
// cells to convert once the connector terminal ABI lands.
func TestConnectorTerminalSemantics_Completeness(t *testing.T) {
	t.Parallel()
	reg := connectorTerminalRegistry(t)
	abiGranted := connectorTerminalABIGranted()
	var missing []string
	for _, essential := range standardplugins.EssentialBackendKinds() {
		if pid, ok := reg[essential]; ok {
			missing = append(missing, fmt.Sprintf("(%s): optional connector plugin %q must not be an essential kind; do not add optional connectors to essential/standard_table fixed tables (ADR 0008)", essential, pid))
		}
	}
	var want []string
	for kind := range reg {
		want = append(want, kind)
	}
	sort.Strings(want)
	for _, id := range want {
		row, ok := connectorTerminalMatrix[id]
		if !ok {
			for _, c := range allBackendTerminalConcerns {
				missing = append(missing, fmt.Sprintf("(%s, %s): no matrix row for connector plugin %s", id, c, reg[id]))
			}
			continue
		}
		for _, c := range allBackendTerminalConcerns {
			cell, ok := row[c]
			if !ok {
				missing = append(missing, fmt.Sprintf("(%s, %s): no matrix cell", id, c))
				continue
			}
			hasRun := cell.run != nil
			hasNA := strings.TrimSpace(cell.na) != ""
			hasSameAs := strings.TrimSpace(cell.sameAs) != ""
			if hasRun && (hasNA || hasSameAs) {
				missing = append(missing, fmt.Sprintf("(%s, %s): run mutually exclusive with na/sameAs", id, c))
				continue
			}
			if hasNA && hasSameAs {
				missing = append(missing, fmt.Sprintf("(%s, %s): na mutually exclusive with sameAs", id, c))
				continue
			}
			if !hasRun && !hasNA && !hasSameAs {
				missing = append(missing, fmt.Sprintf("(%s, %s): run XOR na (or sameAs) violated", id, c))
				continue
			}
			if hasSameAs {
				target, ok := backendTerminalMatrix[cell.sameAs]
				if !ok {
					missing = append(missing, fmt.Sprintf("(%s, %s): sameAs %q is not an essential backend family", id, c, cell.sameAs))
					continue
				}
				if _, ok := target[c]; !ok {
					missing = append(missing, fmt.Sprintf("(%s, %s): sameAs %q lacks concern %s", id, c, cell.sameAs, c))
				}
				if connectorFinishReasonConcerns[c] {
					missing = append(missing, fmt.Sprintf("(%s, %s): sameAs forbidden for FinishReason-bearing concern; connector emits bare finished (openaicompat stream.go/localstub service.go) while essential asserts taxonomy — add a staged-executable run for issue #1104", id, c))
				}
			}
			if abiGranted && strings.Contains(cell.na, connectorBlockedABIMarker) {
				missing = append(missing, fmt.Sprintf("(%s, %s): connector terminal ABI landed; convert blocked na cell to run/sameAs", id, c))
			}
		}
		for c := range row {
			found := slices.Contains(allBackendTerminalConcerns, c)
			if !found {
				missing = append(missing, fmt.Sprintf("(%s, %s): unknown concern", id, c))
			}
		}
	}
	for id := range connectorTerminalMatrix {
		found := slices.Contains(want, id)
		if !found {
			missing = append(missing, fmt.Sprintf("(%s, *): stale row for unregistered connector", id))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("connector terminal-semantics matrix incomplete (%d cells); edit internal/testkit/contract/backend/terminal_connector_matrix_test.go:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestConnectorTerminalSemantics_Matrix executes every registered cell,
// resolving sameAs delegation to the essential protocol-family row. na cells
// skip with their explicit reason so the blocked/deferred V1 state stays
// visible. Subtests run sequentially: family cells use t.Setenv for
// compatible kinds, which cannot run under t.Parallel.
func TestConnectorTerminalSemantics_Matrix(t *testing.T) {
	var subjects []string
	for s := range connectorTerminalMatrix {
		subjects = append(subjects, s)
	}
	sort.Strings(subjects)
	for _, subject := range subjects {
		for _, c := range allBackendTerminalConcerns {
			cell := connectorTerminalMatrix[subject][c]
			switch {
			case cell.run != nil:
				t.Run(subject+"/"+string(c), func(t *testing.T) {
					cell.run(t)
				})
			case strings.TrimSpace(cell.sameAs) != "":
				run := resolveBackendCellOrFail(t, cell.sameAs, c)
				if run == nil {
					t.Run(subject+"/"+string(c), func(t *testing.T) {
						t.Skipf("delegation target %q has no executable cell for %s", cell.sameAs, c)
					})
					continue
				}
				t.Run(subject+"/"+string(c), func(t *testing.T) {
					run(t)
				})
			default:
				reason := strings.TrimSpace(cell.na)
				if reason == "" {
					reason = "missing cell (completeness must fail)"
				}
				t.Run(subject+"/"+string(c), func(t *testing.T) {
					t.Skipf("na: %s", reason)
				})
			}
		}
	}
}
