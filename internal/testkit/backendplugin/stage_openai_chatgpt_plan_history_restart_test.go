package backendplugin_test

// The full-history proof for the official ChatGPT-plan connector (task 8.1).
//
// The public Responses route has no previous_response_id: correctness is the
// canonical history the client sends, not provider-side continuation state.
// This proof drives the real staged connector executable through the host's
// backend-plugin ABI for two turns and restarts the connector process between
// them. The reference server observes ordinary public HTTP requests proving
// that the restarted process:
//
//   - replays the first turn's user message and model-authored answer;
//   - replays an assistant function call and its result in canonical order;
//   - replays a long history block verbatim instead of a truncated prefix;
//   - sends no previous_response_id, no private compaction dialect, and no
//     conversation/session affinity header.
//
// The connector's only durable state is the same protected registration store
// the test staged; the second process is launched from a closed first process,
// so provider response-id/checkpoint state cannot be carrying the conversation.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// The provider answer is the model-authored text replayed by the second turn.
const planHistoryProviderAnswer = "Hello from the plan."

// TestHostIntegration_OpenAIChatGPTPlanFullHistorySurvivesConnectorRestart is
// the task 8.1 observable completion: multi-turn canonical history, a tool
// trajectory and long context survive a real connector restart over public
// HTTP without provider continuation state.
func TestHostIntegration_OpenAIChatGPTPlanFullHistorySurvivesConnectorRestart(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	storePath := stagePlanHostStore(t)
	root := bpkit.StageOpenAIChatGPTPlanReference(t, reference.URL())

	res, reg := resolveCoexistenceCatalog(t, root)
	if res.CatalogErr != nil {
		t.Fatalf("catalog error: %v", res.CatalogErr)
	}
	var launches atomic.Int32
	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	host.SetLaunchProbe(func() { launches.Add(1) })
	if err := runtimebundle.InstallDiscoveredExports(reg, host, res.Installable, runtimebundle.DiscoveredInstallOptions{}); err != nil {
		t.Fatalf("install discovered exports: %v", err)
	}
	instanceConfig := planInstanceConfig(t, storePath)
	const instanceID = "plan-history-restart"
	candidate := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: instanceID, Model: planKind + "/gpt-5"},
		Key:     instanceID + ":" + planKind + "/gpt-5",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Turn one runs on the first connector process and produces the
	// model-authored answer that the next turn must replay.
	firstQuestion := "First turn: which widget is available?"
	first, closeFirst := buildRestartablePlanBackend(t, reg, planKind, instanceID, instanceConfig)
	firstCall := aipapi.Call{
		ID:         "plan-history-1",
		Session:    aipapi.SessionRef{ALegID: "aleg-plan-history"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeNonStreaming},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(firstQuestion)}}},
	}
	if err := firstCall.Validate(); err != nil {
		t.Fatalf("the first-turn call is not canonical-valid: %v", err)
	}
	firstStream, err := first.Open(ctx, firstCall, candidate)
	if err != nil {
		t.Fatalf("open the first turn: %v", err)
	}
	firstResult, err := aipapi.Collect(ctx, firstStream)
	if err != nil {
		t.Fatalf("collect the first turn: %v", err)
	}
	if got := firstResult.Text.String(); got != planHistoryProviderAnswer {
		t.Fatalf("first-turn text = %q, want the provider answer %q", got, planHistoryProviderAnswer)
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("first-turn connector launches = %d, want exactly one", got)
	}

	// Restart: close the real connector process and build the factory again
	// against the same protected registration store. A second physical launch
	// is the restart this proof claims.
	if err := closeFirst(); err != nil {
		t.Fatalf("close the first connector process: %v", err)
	}
	second, _ := buildRestartablePlanBackend(t, reg, planKind, instanceID, instanceConfig)
	if got := launches.Load(); got != 2 {
		t.Fatalf("connector launches after restart = %d, want exactly two", got)
	}

	// Turn two carries the complete canonical history: both turns, a model
	// tool call and its result, and a long block that must arrive verbatim.
	longContext := planLongHistory()
	if len(longContext) < 128<<10 {
		t.Fatalf("the long-history fixture is too small to prove long context: %d bytes", len(longContext))
	}
	const toolCallID = "call_plan_widget_1"
	const toolName = "widget_lookup"
	const toolResult = "widget w7 is available"
	finalQuestion := "Use that result to summarize the plan."
	secondCall := aipapi.Call{
		ID:         "plan-history-2",
		Session:    aipapi.SessionRef{ALegID: "aleg-plan-history"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses, DeliveryMode: aipapi.DeliveryModeNonStreaming},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(firstQuestion)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart(firstResult.Text.String())}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(longContext)}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind: aipapi.PartJSON, ToolCallID: toolCallID, ToolName: toolName,
				Content: json.RawMessage(`{"id":"w7"}`),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind: aipapi.PartToolResult, ToolCallID: toolCallID, ToolName: toolName, Text: toolResult,
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(finalQuestion)}},
		},
		Tools: []aipapi.ToolDef{{
			Name:        toolName,
			Description: "Look up one widget.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`),
		}},
	}
	if err := secondCall.Validate(); err != nil {
		t.Fatalf("the second-turn call is not canonical-valid: %v", err)
	}
	secondStream, err := second.Open(ctx, secondCall, candidate)
	if err != nil {
		t.Fatalf("open the second turn after restart: %v", err)
	}
	secondResult, err := aipapi.Collect(ctx, secondStream)
	if err != nil {
		t.Fatalf("collect the second turn after restart: %v", err)
	}
	if got := secondResult.Text.String(); got != planHistoryProviderAnswer {
		t.Fatalf("second-turn text after restart = %q, want %q", got, planHistoryProviderAnswer)
	}

	requests := reference.captured()
	if len(requests) != 2 {
		t.Fatalf("the provider must observe one request per turn, got %d", len(requests))
	}
	// The provider path and postures are the public route on both sides of
	// the restart; no private continuation or compaction endpoint was used.
	for i, request := range requests {
		if request.Method != http.MethodPost {
			t.Errorf("turn %d provider method = %s, want POST", i+1, request.Method)
		}
		if request.Path != "/v1/responses" {
			t.Errorf("turn %d provider path = %q, want the public Responses path", i+1, request.Path)
		}
		if bytes.Contains(request.Body, []byte("previous_response_id")) {
			t.Errorf("turn %d carries previous_response_id; the public route takes history in input", i+1)
		}
		if bytes.Contains(request.Body, []byte("codex.responses.compaction.v2")) || bytes.Contains(request.Body, []byte("/responses/compact")) {
			t.Errorf("turn %d carries a private Codex compaction dialect", i+1)
		}
		if bytes.Contains(request.Body, []byte("verbosity")) || bytes.Contains(request.Body, []byte("plan_type_hint")) {
			t.Errorf("turn %d carries retired verbosity or plan-downgrade state", i+1)
		}
		for _, forbidden := range []string{"conversation_id", "session_id", "chatgpt-account-id", "originator", "x-codex-task-type"} {
			if value := request.Header.Get(forbidden); value != "" {
				t.Errorf("turn %d carries the private header %q (value %q)", i+1, forbidden, value)
			}
		}
	}

	// The restarted process received the whole second-turn history, in
	// canonical order and verbatim. A connector that relied on provider state,
	// truncated the history or dropped the tool trajectory fails here.
	body := planRequestBody(t, requests[1].Body)
	input := planInput(t, body)
	if len(input) != 7 {
		t.Fatalf("second-turn provider input has %d items, want 7 (tools, history, tool trajectory, new turn)", len(input))
	}
	tools := planItem(t, input[0], 0)
	if tools["type"] != "additional_tools" {
		t.Fatalf("provider input[0].type = %v, want the replayed tool catalogue", tools["type"])
	}
	planRequireMessage(t, input[1], 1, aipapi.RoleUser, firstQuestion)
	planRequireMessage(t, input[2], 2, aipapi.RoleAssistant, planHistoryProviderAnswer)
	planRequireMessage(t, input[3], 3, aipapi.RoleUser, longContext)
	call := planItem(t, input[4], 4)
	if call["type"] != "function_call" || call["call_id"] != toolCallID || call["name"] != toolName || call["arguments"] != `{"id":"w7"}` {
		t.Fatalf("replayed function_call = %+v, want %s/%s with its arguments", call, toolCallID, toolName)
	}
	output := planItem(t, input[5], 5)
	if output["type"] != "function_call_output" || output["call_id"] != toolCallID || output["output"] != toolResult {
		t.Fatalf("replayed function_call_output = %+v, want %s carrying %q", output, toolCallID, toolResult)
	}
	planRequireMessage(t, input[6], 6, aipapi.RoleUser, finalQuestion)

	// The first provider response id is provider state. The restarted process
	// must not reference it: the conversation is the canonical history.
	if bytes.Contains(requests[1].Body, []byte("resp_host")) {
		t.Fatal("the second-turn request references the first provider response id")
	}
}

// planInstanceConfig renders the connector's production per-instance config.
func planInstanceConfig(t *testing.T, storePath string) string {
	t.Helper()
	raw, err := yaml.Marshal(map[string]string{"profile_store_path": storePath})
	if err != nil {
		t.Fatalf("marshal instance config: %v", err)
	}
	return string(raw)
}

// buildRestartablePlanBackend configures one discovered plan factory and
// returns the real cleanup handle so a test can close the connector process
// and build a fresh one.
func buildRestartablePlanBackend(t *testing.T, reg *pluginreg.Registry, kind, instanceID, configYAML string) (execbackend.Backend, func() error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(configYAML), &node); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(kind, instanceID, node, http.DefaultClient, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("configure %q: %v", kind, err)
	}
	closeBackend := sync.OnceValue(func() error {
		if res.Cleanup == nil {
			return nil
		}
		return res.Cleanup()
	})
	t.Cleanup(func() { _ = closeBackend() })
	return res.Backend, closeBackend
}

// planLongHistory is a deterministic long canonical block with markers at both
// ends, so a truncation cannot pass as an exact match.
func planLongHistory() string {
	const marker = "END-OF-LONG-HISTORY-8-1"
	return "BEGIN-LONG-HISTORY-8-1\n" + strings.Repeat("Replay this ordinary canonical history verbatim. ", 4096) + marker
}

// planRequestBody decodes one captured provider request body.
func planRequestBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode provider request body: %v", err)
	}
	return body
}

// planInput returns the captured request's input array.
func planInput(t *testing.T, body map[string]any) []any {
	t.Helper()
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("provider input is not an array: %T", body["input"])
	}
	return input
}

// planItem returns one input item as an object.
func planItem(t *testing.T, raw any, index int) map[string]any {
	t.Helper()
	item, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("provider input[%d] is not an object: %T", index, raw)
	}
	return item
}

// planRequireMessage asserts one replayed message item and its exact text.
func planRequireMessage(t *testing.T, raw any, index int, role aipapi.Role, text string) {
	t.Helper()
	item := planItem(t, raw, index)
	if item["type"] != "message" || item["role"] != string(role) {
		t.Fatalf("provider input[%d] = type %v role %v, want message role %q", index, item["type"], item["role"], role)
	}
	content, ok := item["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("provider input[%d] content = %T with %d parts, want one text part", index, item["content"], len(content))
	}
	part, ok := content[0].(map[string]any)
	if !ok || part["type"] != "input_text" || part["text"] != text {
		t.Fatalf("provider input[%d] text part does not match the replayed %q history", index, role)
	}
}
