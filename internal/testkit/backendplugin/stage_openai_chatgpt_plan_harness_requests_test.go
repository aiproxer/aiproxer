package backendplugin_test

// Task 9.1 baseline harness-shaped request proof (requirements 5.7, 11.1, 11.2).
//
// The proof starts at the real frontend, not at backend.Open: the request is
// decoded to a canonical Call, routed by the real core executor, and sent to
// the real staged openai-chatgpt-plan connector executable over the
// backend-plugin ABI. The connector then reaches a local reference provider.
//
// Two materially different harness shapes are exercised, both with function
// tools:
//
//   - OpenCode-shaped: the real OpenAI Chat Completions frontend, a system
//     message, nested function declarations, a chat history with an assistant
//     tool_calls entry and its tool result.
//   - Hermes-shaped: the real OpenAI Responses frontend, the public Hermes
//     identity sentence, a nested function declaration, and a plain-string
//     input.
//
// Each one must complete a canonical tool-capable stream: the provider calls a
// declared tool and the downstream stream terminates with that call (Responses
// response.completed / Chat Completions finish_reason=tool_calls). The provider
// must receive the client's instructions and tool surface exactly, with no
// Codex identity or compatibility bridge.
//
// `codex-client-compat` is disabled, not merely never composed: the standard
// feature registry builds the registration with Enabled:false, merges it, and
// projects it with ProjectHookConfig, exactly like the runtime composition. The
// projected request-part chain is asserted empty.
//
// The feature's gate compares sdk.PartMeta.BackendID, which is the CONFIGURED
// backend instance id, not the backend kind. This test configures the new
// connector as "plan-harness"; it does not claim that every instance of the
// new kind is exempt. An operator who configures an instance with the id
// "openai-codex" while the feature is enabled is still targeted (see
// internal/plugins/features/codexclientcompat/plugin.go). The point of this
// baseline is that harness requests do not need that feature.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	frontopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"gopkg.in/yaml.v3"
)

const (
	planHarnessInstanceID = "plan-harness"
	planHarnessModel      = "gpt-5"
	planHarnessStoreToken = "host-test-access-token"

	planHarnessWireResponses = "openai-responses"
	planHarnessWireChat      = "openai-chat-completions"
)

// TestHostIntegration_OpenAIChatGPTPlanHarnessRequestsWithoutCodexPromptBridges
// is the task 9.1 observable completion.
func TestHostIntegration_OpenAIChatGPTPlanHarnessRequestsWithoutCodexPromptBridges(t *testing.T) {
	t.Parallel()

	reference := newPlanHarnessReference(t)
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
	backend := buildCoexistenceBackend(t, reg, planKind, planHarnessInstanceID, planInstanceConfig(t, storePath))

	exec := newPlanHarnessExecutor(t, planHarnessInstanceID, backend)
	mux := http.NewServeMux()
	mux.Handle("POST /v1/responses", &frontopenairesponses.Handler{
		Exec:                 exec,
		DefaultRouteSelector: planHarnessInstanceID + ":" + planHarnessModel,
	})
	mux.Handle("POST /v1/chat/completions", &frontopenailegacy.Handler{
		Exec:                 exec,
		DefaultRouteSelector: planHarnessInstanceID + ":" + planHarnessModel,
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	for _, tc := range planHarnessCases() {
		t.Run(tc.name, func(t *testing.T) {
			reference.respondWith(func(map[string]any) string {
				return planHarnessToolCallSSE(tc.answer)
			})
			before := len(reference.captured())
			downstream := postPlanHarnessCreate(t, server, tc)

			requests := reference.captured()
			if len(requests) != before+1 {
				t.Fatalf("the provider must observe exactly one request for one harness turn, got %d", len(requests)-before)
			}
			assertPlanHarnessProviderRequest(t, requests[before], tc)
			assertPlanHarnessDownstreamToolStream(t, downstream, tc)
		})
	}
}

// planHarnessCase is one materially different harness wire shape and the exact
// provider-visible request it must produce.
type planHarnessCase struct {
	name      string
	wire      string
	path      string
	userAgent string
	// instructions is the exact instruction text the provider must receive.
	// The Responses shape carries it in `instructions`; the Chat Completions
	// shape carries it as the sole system message, which the canonical layer
	// folds into instructions and never emits as an input message.
	instructions string
	tools        []json.RawMessage
	// input is the Responses-shape conversation input.
	input json.RawMessage
	// messages is the Chat Completions-shape conversation.
	messages []map[string]any
	// wantTools is the exact function catalogue the request declared, in order.
	wantTools []planHarnessTool
	// wantInput is the conversation the provider must see after the additional
	// tools item, in order. No system/instruction item may be added.
	wantInput []planHarnessWireItem
	answer    planHarnessToolCall
}

type planHarnessTool struct {
	Name        string
	Description string
	Parameters  string
}

type planHarnessWireItem struct {
	Kind      string
	Role      string
	Text      string
	CallID    string
	Name      string
	Arguments string
	Output    string
}

type planHarnessToolCall struct {
	CallID    string
	ToolName  string
	Arguments string
}

func planHarnessCases() []planHarnessCase {
	openCodePrompt := strings.Join([]string{
		"You are opencode, the best coding agent on the planet.",
		"",
		"You are an interactive CLI tool that helps users with software engineering tasks. Use the instructions below and the tools available to you to assist the user.",
		"",
		"- Prefer the available tools over ad-hoc shell guesses.",
		"- Keep changes minimal and verify them.",
	}, "\n")
	hermesPrompt := strings.Join([]string{
		"You are Hermes Agent, an intelligent AI assistant created by Nous Research.",
		"",
		"You are an interactive terminal agent. Use the available tools to inspect the repository before answering.",
	}, "\n")

	return []planHarnessCase{
		{
			name:         "opencode-chat-shaped",
			wire:         planHarnessWireChat,
			path:         "/v1/chat/completions",
			userAgent:    "opencode/1.2.3",
			instructions: openCodePrompt,
			tools: []json.RawMessage{
				json.RawMessage(`{"type":"function","function":{"name":"bash","description":"Run a shell command in the workspace.","parameters":{"type":"object","properties":{"command":{"type":"string","description":"The command to execute."},"workdir":{"type":"string"}},"required":["command"],"additionalProperties":false}}}`),
				json.RawMessage(`{"type":"function","function":{"name":"edit","description":"Replace one exact string in one file.","parameters":{"type":"object","properties":{"filePath":{"type":"string"},"oldString":{"type":"string"},"newString":{"type":"string"}},"required":["filePath","oldString","newString"]}}}`),
			},
			messages: []map[string]any{
				{"role": "system", "content": openCodePrompt},
				{"role": "user", "content": "List the files in this repository."},
				{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
					"id": "call_prev_bash", "type": "function",
					"function": map[string]any{"name": "bash", "arguments": `{"command":"ls"}`},
				}}},
				{"role": "tool", "tool_call_id": "call_prev_bash", "content": "README.md\nmain.go"},
			},
			wantTools: []planHarnessTool{
				{
					Name:        "bash",
					Description: "Run a shell command in the workspace.",
					Parameters:  `{"type":"object","properties":{"command":{"type":"string","description":"The command to execute."},"workdir":{"type":"string"}},"required":["command"],"additionalProperties":false}`,
				},
				{
					Name:        "edit",
					Description: "Replace one exact string in one file.",
					Parameters:  `{"type":"object","properties":{"filePath":{"type":"string"},"oldString":{"type":"string"},"newString":{"type":"string"}},"required":["filePath","oldString","newString"]}`,
				},
			},
			wantInput: []planHarnessWireItem{
				{Kind: "message", Role: "user", Text: "List the files in this repository."},
				{Kind: "function_call", CallID: "call_prev_bash", Name: "bash", Arguments: `{"command":"ls"}`},
				{Kind: "function_call_output", CallID: "call_prev_bash", Output: "README.md\nmain.go"},
			},
			answer: planHarnessToolCall{CallID: "call_oc_bash", ToolName: "bash", Arguments: `{"command":"ls -la"}`},
		},
		{
			name:         "hermes-responses-shaped",
			wire:         planHarnessWireResponses,
			path:         "/v1/responses",
			userAgent:    "Hermes-Agent/0.9",
			instructions: hermesPrompt,
			tools: []json.RawMessage{
				json.RawMessage(`{"type":"function","function":{"name":"read_file","description":"Read a file from the workspace.","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}`),
			},
			input: json.RawMessage(`"Inspect the README and report its title."`),
			wantTools: []planHarnessTool{
				{
					Name:        "read_file",
					Description: "Read a file from the workspace.",
					Parameters:  `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
				},
			},
			wantInput: []planHarnessWireItem{
				{Kind: "message", Role: "user", Text: "Inspect the README and report its title."},
			},
			answer: planHarnessToolCall{CallID: "call_hermes_read", ToolName: "read_file", Arguments: `{"path":"README.md"}`},
		},
	}
}

// newPlanHarnessExecutor wires the production-shaped request runtime: a real
// core executor whose only feature contribution is the disabled
// codex-client-compat registration.
func newPlanHarnessExecutor(t *testing.T, instanceID string, backend execbackend.Backend) *coreruntime.Executor {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("create the B2BUA store: %v", err)
	}
	ex := coreruntime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(planDisabledCodexClientCompatHooks(t))
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{instanceID: backend}
	ex.DefaultBackend = instanceID
	testkit.WireConformanceExecutorSecureSession(t, ex)
	return ex
}

// planDisabledCodexClientCompatHooks builds the feature surface through the
// standard registry, merge and hook-projection path with the feature
// registration disabled, and proves the projected request-part chain is empty.
func planDisabledCodexClientCompatHooks(t *testing.T) hooks.Config {
	t.Helper()
	registry := pluginreg.NewRegistry()
	if err := standardplugins.InstallBundleOn(registry, standardplugins.StandardBundle()); err != nil {
		t.Fatalf("install the standard plugin bundle: %v", err)
	}
	var configNode yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: false\n"), &configNode); err != nil {
		t.Fatalf("decode the disabled codex-client-compat config: %v", err)
	}
	merged, err := featurebundle.MergeFeatureSurfacesWithHost(registry, []aipsdk.Registration{{
		ID:      codexclientcompat.ID,
		Kind:    aipsdk.PluginKindFeature,
		Enabled: false,
		Config:  aipsdk.ConfigPayload{Node: configNode},
	}}, featurebundle.HostContributions{})
	if err != nil {
		t.Fatalf("merge the disabled codex-client-compat feature: %v", err)
	}
	hookConfig := aipfeature.ProjectHookConfig(merged.Frozen, sdkhooks.ToolReactorErrorsFailOpen)
	if got := len(hookConfig.RequestPartHooks); got != 0 {
		t.Fatalf("the disabled codex-client-compat feature contributes %d request-part hooks, want none", got)
	}
	return hookConfig
}

// postPlanHarnessCreate posts one harness-shaped request to the real frontend
// and returns the complete downstream SSE body.
func postPlanHarnessCreate(t *testing.T, server *httptest.Server, tc planHarnessCase) string {
	t.Helper()
	var (
		payload []byte
		err     error
	)
	switch tc.wire {
	case planHarnessWireChat:
		payload, err = json.Marshal(struct {
			Model      string            `json:"model"`
			Stream     bool              `json:"stream"`
			Messages   []map[string]any  `json:"messages"`
			Tools      []json.RawMessage `json:"tools"`
			ToolChoice string            `json:"tool_choice"`
		}{
			Model:      planHarnessModel,
			Stream:     true,
			Messages:   tc.messages,
			Tools:      tc.tools,
			ToolChoice: "auto",
		})
	case planHarnessWireResponses:
		payload, err = json.Marshal(struct {
			Model        string            `json:"model"`
			Stream       bool              `json:"stream"`
			Instructions string            `json:"instructions"`
			Tools        []json.RawMessage `json:"tools"`
			Input        json.RawMessage   `json:"input"`
			ToolChoice   string            `json:"tool_choice"`
		}{
			Model:        planHarnessModel,
			Stream:       true,
			Instructions: tc.instructions,
			Tools:        tc.tools,
			Input:        tc.input,
			ToolChoice:   "auto",
		})
	default:
		t.Fatalf("unknown harness wire shape %q", tc.wire)
	}
	if err != nil {
		t.Fatalf("encode the harness request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+tc.path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build the harness request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", tc.userAgent)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("post the harness request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the downstream stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("downstream status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("downstream Content-Type = %q, want an SSE stream", contentType)
	}
	return string(body)
}

// assertPlanHarnessProviderRequest asserts the provider saw exactly the
// client's harness instructions and tool surface, with no prompt bridge.
func assertPlanHarnessProviderRequest(t *testing.T, request planHostRequest, tc planHarnessCase) {
	t.Helper()
	if request.Method != http.MethodPost {
		t.Fatalf("provider method = %s, want POST", request.Method)
	}
	if request.Path != "/v1/responses" {
		t.Fatalf("provider path = %q, want the public Responses path", request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer "+planHarnessStoreToken {
		t.Fatalf("provider Authorization = %q, want the staged registration's bearer token", got)
	}
	for _, forbidden := range []string{"originator", "chatgpt-account-id", "conversation_id", "session_id"} {
		if value := request.Header.Get(forbidden); value != "" {
			t.Fatalf("provider request carries the private header %q (value %q)", forbidden, value)
		}
	}
	raw := string(request.Body)
	for _, bridge := range []string{"You are Codex", "<user_instructions>", "compatibility mode"} {
		if strings.Contains(raw, bridge) {
			t.Fatalf("provider request carries the client-name prompt bridge fragment %q; harness instructions must pass through untouched", bridge)
		}
	}

	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("decode the provider request body: %v", err)
	}
	if stream, ok := body["stream"].(bool); !ok || !stream {
		t.Fatalf("provider request stream = %v, want true", body["stream"])
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("provider request store = %v, want false", body["store"])
	}
	if model, _ := body["model"].(string); model != planHarnessModel {
		t.Fatalf("provider request model = %q, want the native provider slug %q", model, planHarnessModel)
	}
	if got, _ := body["instructions"].(string); got != tc.instructions {
		t.Fatalf("provider instructions were not preserved exactly:\n got: %q\nwant: %q", got, tc.instructions)
	}

	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("provider input is not an array: %T", body["input"])
	}
	if len(input) != len(tc.wantInput)+1 {
		t.Fatalf("provider input has %d items, want the tool catalogue plus %d conversation items", len(input), len(tc.wantInput))
	}
	assertPlanHarnessToolsItem(t, planHarnessItem(t, input[0], 0), tc.wantTools)
	for i, want := range tc.wantInput {
		assertPlanHarnessWireItem(t, planHarnessItem(t, input[i+1], i+1), want, i+1)
	}
}

func assertPlanHarnessToolsItem(t *testing.T, item map[string]any, want []planHarnessTool) {
	t.Helper()
	if item["type"] != "additional_tools" || item["role"] != "developer" {
		t.Fatalf("provider input[0] = type %v role %v, want the developer additional_tools catalogue", item["type"], item["role"])
	}
	tools, ok := item["tools"].([]any)
	if !ok {
		t.Fatalf("provider input[0].tools is not an array: %T", item["tools"])
	}
	if len(tools) != len(want) {
		t.Fatalf("provider tool catalogue has %d entries, want %d", len(tools), len(want))
	}
	for i, wantTool := range want {
		tool := planHarnessItem(t, tools[i], i)
		if tool["type"] != "function" {
			t.Errorf("provider tools[%d].type = %v, want function", i, tool["type"])
		}
		if tool["name"] != wantTool.Name {
			t.Errorf("provider tools[%d].name = %v, want %q", i, tool["name"], wantTool.Name)
		}
		if tool["description"] != wantTool.Description {
			t.Errorf("provider tools[%d].description = %v, want %q", i, tool["description"], wantTool.Description)
		}
		var gotParams, wantParams any
		if err := json.Unmarshal(planHarnessMarshal(t, tool["parameters"]), &gotParams); err != nil {
			t.Fatalf("decode provider tools[%d].parameters: %v", i, err)
		}
		if err := json.Unmarshal([]byte(wantTool.Parameters), &wantParams); err != nil {
			t.Fatalf("decode expected tools[%d].parameters: %v", i, err)
		}
		if !reflect.DeepEqual(gotParams, wantParams) {
			t.Errorf("provider tools[%d].parameters = %v, want %v", i, gotParams, wantParams)
		}
	}
}

func assertPlanHarnessWireItem(t *testing.T, item map[string]any, want planHarnessWireItem, index int) {
	t.Helper()
	switch want.Kind {
	case "message":
		if item["type"] != "message" || item["role"] != want.Role {
			t.Fatalf("provider input[%d] = type %v role %v, want message role %q", index, item["type"], item["role"], want.Role)
		}
		content, ok := item["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("provider input[%d].content = %T with %d parts, want one text part", index, item["content"], len(content))
		}
		part, ok := content[0].(map[string]any)
		if !ok || part["type"] != "input_text" || part["text"] != want.Text {
			t.Fatalf("provider input[%d] text = %v, want %q", index, part["text"], want.Text)
		}
	case "function_call":
		if item["type"] != "function_call" || item["call_id"] != want.CallID || item["name"] != want.Name || item["arguments"] != want.Arguments {
			t.Fatalf("provider input[%d] = %+v, want function_call %s/%s with arguments %q", index, item, want.CallID, want.Name, want.Arguments)
		}
	case "function_call_output":
		if item["type"] != "function_call_output" || item["call_id"] != want.CallID || item["output"] != want.Output {
			t.Fatalf("provider input[%d] = %+v, want function_call_output %s carrying %q", index, item, want.CallID, want.Output)
		}
	default:
		t.Fatalf("unsupported expected wire item kind %q", want.Kind)
	}
}

// assertPlanHarnessDownstreamToolStream proves the canonical stream stayed
// tool-capable through the selected frontend encoder and terminated normally.
func assertPlanHarnessDownstreamToolStream(t *testing.T, raw string, tc planHarnessCase) {
	t.Helper()
	switch tc.wire {
	case planHarnessWireChat:
		assertPlanHarnessChatToolStream(t, raw, tc.answer)
	case planHarnessWireResponses:
		assertPlanHarnessResponsesToolStream(t, raw, tc.answer)
	default:
		t.Fatalf("unknown harness wire shape %q", tc.wire)
	}
}

// assertPlanHarnessChatToolStream asserts the Chat Completions encoder carried
// the model tool call through delta.tool_calls and finished with tool_calls.
func assertPlanHarnessChatToolStream(t *testing.T, raw string, want planHarnessToolCall) {
	t.Helper()
	var (
		gotID     string
		gotName   string
		gotFinish string
		arguments strings.Builder
	)
	for _, event := range planHarnessParseSSE(t, raw) {
		choices, _ := event.Data["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		calls, _ := delta["tool_calls"].([]any)
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			if id, _ := call["id"].(string); id != "" {
				gotID = id
			}
			function, _ := call["function"].(map[string]any)
			if name, _ := function["name"].(string); name != "" {
				gotName = name
			}
			if deltaArgs, _ := function["arguments"].(string); deltaArgs != "" {
				arguments.WriteString(deltaArgs)
			}
		}
		if finish, _ := choice["finish_reason"].(string); finish != "" {
			gotFinish = finish
		}
	}
	if gotID != want.CallID || gotName != want.ToolName {
		t.Fatalf("downstream chat tool call = id %q name %q, want %s/%s", gotID, gotName, want.CallID, want.ToolName)
	}
	if got := arguments.String(); got != want.Arguments {
		t.Fatalf("downstream chat tool arguments = %q, want %q", got, want.Arguments)
	}
	if gotFinish != "tool_calls" {
		t.Fatalf("downstream chat finish_reason = %q, want tool_calls", gotFinish)
	}
}

// assertPlanHarnessResponsesToolStream asserts the Responses encoder carried the
// model tool call through a function_call item and response.completed.
func assertPlanHarnessResponsesToolStream(t *testing.T, raw string, want planHarnessToolCall) {
	t.Helper()
	var added, argumentsDone, completed map[string]any
	for _, event := range planHarnessParseSSE(t, raw) {
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Data["item"].(map[string]any)
			if item["type"] == "function_call" {
				added = item
			}
		case "response.function_call_arguments.done":
			argumentsDone = event.Data
		case "response.completed":
			completed = event.Data
		}
	}
	if added == nil {
		t.Fatalf("downstream stream has no streamed function_call item; body: %s", raw)
	}
	if added["call_id"] != want.CallID || added["name"] != want.ToolName {
		t.Fatalf("downstream streamed function_call = call_id %v name %v, want %s/%s", added["call_id"], added["name"], want.CallID, want.ToolName)
	}
	if argumentsDone == nil {
		t.Fatalf("downstream stream has no function_call arguments completion; body: %s", raw)
	}
	if argumentsDone["name"] != want.ToolName || argumentsDone["arguments"] != want.Arguments {
		t.Fatalf("downstream function_call arguments = name %v arguments %v, want %s with %q", argumentsDone["name"], argumentsDone["arguments"], want.ToolName, want.Arguments)
	}
	if completed == nil {
		t.Fatalf("downstream stream did not terminate with response.completed; body: %s", raw)
	}
	response, _ := completed["response"].(map[string]any)
	output, _ := response["output"].([]any)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item["type"] != "function_call" || item["call_id"] != want.CallID {
			continue
		}
		if item["name"] != want.ToolName || item["arguments"] != want.Arguments {
			t.Fatalf("completed function_call = name %v arguments %v, want %s with %q", item["name"], item["arguments"], want.ToolName, want.Arguments)
		}
		return
	}
	t.Fatalf("response.completed does not carry the function_call %s; body: %s", want.CallID, raw)
}

type planHarnessEvent struct {
	Name string
	Data map[string]any
}

func planHarnessParseSSE(t *testing.T, raw string) []planHarnessEvent {
	t.Helper()
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	blocks := strings.Split(normalized, "\n\n")
	events := make([]planHarnessEvent, 0, len(blocks))
	for _, block := range blocks {
		var event planHarnessEvent
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "" || payload == "[DONE]" {
					continue
				}
				if err := json.Unmarshal([]byte(payload), &event.Data); err != nil {
					t.Fatalf("decode downstream SSE payload %q: %v", payload, err)
				}
			}
		}
		if event.Data != nil {
			events = append(events, event)
		}
	}
	return events
}

func planHarnessItem(t *testing.T, raw any, index int) map[string]any {
	t.Helper()
	item, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("provider input[%d] is not an object: %T", index, raw)
	}
	return item
}

func planHarnessMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal decoded JSON value: %v", err)
	}
	return raw
}

// planHarnessReference is one scripted local provider answer per harness turn.
// It records every request so the test can assert what the connector actually
// sent.
type planHarnessReference struct {
	server *httptest.Server

	mu       sync.Mutex
	respond  func(map[string]any) string
	requests []planHostRequest
}

func newPlanHarnessReference(t *testing.T) *planHarnessReference {
	t.Helper()
	reference := &planHarnessReference{}
	reference.server = httptest.NewServer(http.HandlerFunc(reference.handle))
	t.Cleanup(reference.server.Close)
	return reference
}

func (p *planHarnessReference) URL() string { return p.server.URL }

func (p *planHarnessReference) respondWith(respond func(map[string]any) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.respond = respond
}

func (p *planHarnessReference) captured() []planHostRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]planHostRequest(nil), p.requests...)
}

func (p *planHarnessReference) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	p.mu.Lock()
	p.requests = append(p.requests, planHostRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Header: r.Header.Clone(),
		Body:   body,
	})
	respond := p.respond
	p.mu.Unlock()

	if respond == nil {
		http.Error(w, "no plan harness answer is scripted", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("x-request-id", "req_plan_harness")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, respond(parsed)); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// planHarnessToolCallSSE is one complete public Responses tool-call stream.
func planHarnessToolCallSSE(answer planHarnessToolCall) string {
	finalItem := map[string]any{
		"id":        "fc_plan_harness",
		"type":      "function_call",
		"call_id":   answer.CallID,
		"name":      answer.ToolName,
		"arguments": answer.Arguments,
	}
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": "resp_plan_harness", "status": "in_progress"}},
		{"type": "response.output_item.added", "item": map[string]any{
			"id": "fc_plan_harness", "type": "function_call", "call_id": answer.CallID, "name": answer.ToolName,
		}},
		{"type": "response.function_call_arguments.delta", "item_id": "fc_plan_harness", "call_id": answer.CallID, "delta": answer.Arguments},
		{"type": "response.output_item.done", "item": finalItem},
		{"type": "response.completed", "response": map[string]any{
			"id":     "resp_plan_harness",
			"status": "completed",
			"output": []any{finalItem},
			"usage":  map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
		}},
	}
	var body strings.Builder
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			return ""
		}
		fmt.Fprintf(&body, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
	return body.String()
}
