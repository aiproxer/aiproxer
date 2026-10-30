package backendplugin_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// TestHostIntegration_OpenAIChatGPTPlanReplaysAToolMessageTurn proves the
// staged connector receives and forwards a model tool call and its result.
// The connector's declared surface is message authority; the shared bridge
// must therefore preserve the message-part carriers.
func TestHostIntegration_OpenAIChatGPTPlanReplaysAToolMessageTurn(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	storePath := stagePlanHostStore(t)
	root := bpkit.StageOpenAIChatGPTPlanReference(t, reference.URL())

	res, reg := resolveCoexistenceCatalog(t, root)
	if res.CatalogErr != nil {
		t.Fatalf("catalog error: %v", res.CatalogErr)
	}
	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	if err := runtimebundle.InstallDiscoveredExports(reg, host, res.Installable, runtimebundle.DiscoveredInstallOptions{}); err != nil {
		t.Fatalf("install discovered exports: %v", err)
	}
	instanceConfig, err := yaml.Marshal(map[string]string{"profile_store_path": storePath})
	if err != nil {
		t.Fatalf("marshal instance config: %v", err)
	}
	backend := buildCoexistenceBackend(t, reg, planKind, "plan-tool-message", string(instanceConfig))

	result := "the widget is available"
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		ID:      "plan-tool-message",
		Session: aipapi.SessionRef{ALegID: "aleg-plan-tool-message"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("find the widget")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind: aipapi.PartJSON, ToolCallID: "call_widget_1", ToolName: "widget_lookup",
				Content: json.RawMessage(`{"id":"w1"}`),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind: aipapi.PartToolResult, ToolCallID: "call_widget_1", ToolName: "widget_lookup",
				Text: result, Content: resultJSON,
			}}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("summarize it")}},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the staged tool-loop call is not canonical-valid: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, err := backend.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "plan-tool-message", Model: planKind + "/gpt-5"},
		Key:     "plan-tool-message:" + planKind + "/gpt-5",
	})
	if err != nil {
		t.Fatalf("open the staged connector: %v", err)
	}
	if _, err := aipapi.Collect(ctx, stream); err != nil {
		t.Fatalf("collect the downstream result: %v", err)
	}
	requests := reference.captured()
	if len(requests) != 1 {
		t.Fatalf("the provider must observe exactly one request, got %d", len(requests))
	}
	var body map[string]any
	if err := json.Unmarshal(requests[0].Body, &body); err != nil {
		t.Fatalf("decode provider request body: %v", err)
	}
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("provider input is not an array: %T", body["input"])
	}
	callIndex, resultIndex := -1, -1
	for i, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch item["type"] {
		case "function_call":
			if item["call_id"] != "call_widget_1" || item["name"] != "widget_lookup" {
				t.Errorf("provider function_call identity = call_id %v name %v", item["call_id"], item["name"])
			}
			if item["arguments"] != `{"id":"w1"}` {
				t.Errorf("provider function_call arguments = %v", item["arguments"])
			}
			callIndex = i
		case "function_call_output":
			if item["call_id"] != "call_widget_1" {
				t.Errorf("provider function_call_output call_id = %v", item["call_id"])
			}
			if item["output"] != "the widget is available" {
				t.Errorf("provider function_call_output output = %v", item["output"])
			}
			resultIndex = i
		}
	}
	if callIndex < 0 || resultIndex < 0 {
		t.Fatalf("provider saw an incomplete tool trajectory: call=%d result=%d", callIndex, resultIndex)
	}
	if callIndex > resultIndex {
		t.Fatalf("the tool result was reordered before its call: call=%d result=%d", callIndex, resultIndex)
	}
}
