package standardplugins_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/internal/refbackend/openaichat"
	"github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Profile-built Chat backend against strict thinking-mode emulator.
// Without the quirk the wire 400s (RED); with it the emulator returns 200.
func TestStrictQuirks_ProfileChatAgainstStrictEmulator(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	srv := httptest.NewServer(openaichat.NewHandler(openaichat.Config{StrictReasoningContent: true}))
	t.Cleanup(srv.Close)

	build := func(t *testing.T, quirks []providerprofiles.QuirkID) aipapi.ManagedEventStream {
		t.Helper()
		profile := providerprofiles.Profile{
			APIVersion: providerprofiles.APIVersionV1,
			ID:         "strict-chat-probe",
			Family:     providerprofiles.FamilyOpenAIChat,
			Endpoint:   providerprofiles.Endpoint{BaseURL: srv.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
			Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
			Models:     providerprofiles.ModelDiscovery{Policy: providerprofiles.DiscoveryFamilyDefault, Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve}},
			Quirks:     quirks,
		}
		compiled, err := standardplugins.CompileProviderProfile(profile)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		be, err := standardplugins.BuildProviderProfileBackend(compiled, "strict-chat", srv.Client(), pluginreg.BackendFactoryDeps{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		call := aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Content: []byte(`"ok"`)}}},
			},
			Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
			Invocation: aipapi.Invocation{
				Operation:     aipapi.OperationOpenAIChatCompletions,
				DeliveryMode:  aipapi.DeliveryModeNonStreaming,
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
		stream, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "strict-chat", Model: "deepseek-chat"}})
		if err != nil {
			t.Fatalf("open with quirk: %v", err)
		}
		return stream
	}

	// Without quirk: strict emulator must 400.
	if stream, err := func() (aipapi.ManagedEventStream, error) {
		profile := providerprofiles.Profile{
			APIVersion: providerprofiles.APIVersionV1,
			ID:         "strict-chat-plain",
			Family:     providerprofiles.FamilyOpenAIChat,
			Endpoint:   providerprofiles.Endpoint{BaseURL: srv.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
			Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
			Models:     providerprofiles.ModelDiscovery{Policy: providerprofiles.DiscoveryFamilyDefault, Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve}},
		}
		compiled, err := standardplugins.CompileProviderProfile(profile)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		be, err := standardplugins.BuildProviderProfileBackend(compiled, "strict-chat", srv.Client(), pluginreg.BackendFactoryDeps{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		call := aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Content: []byte(`"ok"`)}}},
			},
			Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
			Invocation: aipapi.Invocation{
				Operation:     aipapi.OperationOpenAIChatCompletions,
				DeliveryMode:  aipapi.DeliveryModeNonStreaming,
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
		return be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "strict-chat", Model: "deepseek-chat"}})
	}(); err == nil {
		_ = stream.Close()
		t.Fatal("without quirk, strict emulator must 400")
	}

	// With quirk: 200.
	stream := build(t, []providerprofiles.QuirkID{providerprofiles.QuirkOpenAIChatReasoningContent})
	if stream == nil {
		t.Fatal("with quirk, expected stream, got nil with no error")
	}
	_ = stream.Close()
}

// Profile-built Responses backend against strict adjacency emulator.
func TestStrictQuirks_ProfileResponsesAgainstStrictEmulator(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	srv := httptest.NewServer(openairesponses.NewHandler(openairesponses.Config{StrictToolOutputAdjacency: true}))
	t.Cleanup(srv.Close)

	mkCall := func() aipapi.Call {
		return aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "c1", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"c1","name":"w","arguments":"{}"}`)}}},
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("trapped")}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "c1", Content: []byte(`"ok"`)}}},
			},
			Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
			Invocation: aipapi.Invocation{
				Operation:     aipapi.OperationOpenAIResponses,
				DeliveryMode:  aipapi.DeliveryModeNonStreaming,
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
	}
	openWith := func(t *testing.T, quirks []providerprofiles.QuirkID) error {
		t.Helper()
		profile := providerprofiles.Profile{
			APIVersion: providerprofiles.APIVersionV1,
			ID:         "strict-resp-probe",
			Family:     providerprofiles.FamilyOpenAIResponses,
			Endpoint:   providerprofiles.Endpoint{BaseURL: srv.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
			Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
			Models: providerprofiles.ModelDiscovery{
				Policy:    providerprofiles.DiscoveryStatic,
				Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
				Static:    []providerprofiles.Model{{CanonicalID: "m", NativeID: "m", DisplayName: "M"}},
			},
			Quirks: quirks,
		}
		compiled, err := standardplugins.CompileProviderProfile(profile)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		be, err := standardplugins.BuildProviderProfileBackend(compiled, "strict-resp", srv.Client(), pluginreg.BackendFactoryDeps{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		stream, err := be.Open(context.Background(), mkCall(), routing.AttemptCandidate{Primary: routing.Primary{Backend: "strict-resp", Model: "m"}})
		if err != nil {
			return err
		}
		_ = stream.Close()
		return nil
	}

	if err := openWith(t, nil); err == nil {
		t.Fatal("without quirk, strict emulator must 400")
	}
	if err := openWith(t, []providerprofiles.QuirkID{providerprofiles.QuirkOpenAIResponsesToolAdjacency}); err != nil {
		t.Fatalf("with quirk, expected 200, got: %v", err)
	}

	// Interleaved review case (P1 on PR #1102): reasoning between two calls,
	// [callA, reasoning, callB, outA, outB], must also normalize through the
	// real serializer and satisfy the strict emulator.
	mkInterleaved := func() aipapi.Call {
		return aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartReasoning, Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: json.RawMessage(`{"id":"rs_1","summary":[]}`)}},
					{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)},
				}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
			},
			Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
			Invocation: aipapi.Invocation{
				Operation:     aipapi.OperationOpenAIResponses,
				DeliveryMode:  aipapi.DeliveryModeNonStreaming,
				TransportMode: aipapi.TransportModeNonStreaming,
			},
		}
	}
	openInterleaved := func(t *testing.T, quirks []providerprofiles.QuirkID) error {
		t.Helper()
		profile := providerprofiles.Profile{
			APIVersion: providerprofiles.APIVersionV1,
			ID:         "strict-resp-interleaved",
			Family:     providerprofiles.FamilyOpenAIResponses,
			Endpoint:   providerprofiles.Endpoint{BaseURL: srv.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
			Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
			Models: providerprofiles.ModelDiscovery{
				Policy:    providerprofiles.DiscoveryStatic,
				Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
				Static:    []providerprofiles.Model{{CanonicalID: "m", NativeID: "m", DisplayName: "M"}},
			},
			Quirks: quirks,
		}
		compiled, err := standardplugins.CompileProviderProfile(profile)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		be, err := standardplugins.BuildProviderProfileBackend(compiled, "strict-resp", srv.Client(), pluginreg.BackendFactoryDeps{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		stream, err := be.Open(context.Background(), mkInterleaved(), routing.AttemptCandidate{Primary: routing.Primary{Backend: "strict-resp", Model: "m"}})
		if err != nil {
			return err
		}
		_ = stream.Close()
		return nil
	}
	if err := openInterleaved(t, nil); err == nil {
		t.Fatal("interleaved without quirk, strict emulator must 400")
	}
	// With the quirk the interleaved-reasoning history is refused locally with a
	// classified error rather than forwarded in a rewritten order.
	if err := openInterleaved(t, []providerprofiles.QuirkID{providerprofiles.QuirkOpenAIResponsesToolAdjacency}); err == nil {
		t.Fatal("interleaved reasoning with quirk must be refused, not forwarded")
	} else if !errors.Is(err, openaicompat.ErrResponsesAdjacencyRefused) {
		t.Fatalf("expected classified adjacency refusal, got %v", err)
	}
}

// Quirk-off profile wire must equal the generic custom-compatible factory
// wire for streaming and non-streaming alike: the independent reference is a
// different construction path (BuildCompatible), not the same struct twice.
// A golden assertion pins the pre-change shape (no reasoning_content).
func TestStrictQuirks_OffMatchesGenericFactoryBothModes(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	var captured [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = append(captured, append([]byte(nil), b...))
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chat-stream\",\"object\":\"chat.completion.chunk\",\"created\":1715620000,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"g","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	t.Cleanup(srv.Close)

	profile := providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "off-ref-probe",
		Family:     providerprofiles.FamilyOpenAIChat,
		Endpoint:   providerprofiles.Endpoint{BaseURL: srv.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
		Models:     providerprofiles.ModelDiscovery{Policy: providerprofiles.DiscoveryFamilyDefault, Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve}},
	}
	node, err := standardplugins.ProfileConfigNode(profile)
	if err != nil {
		t.Fatalf("config node: %v", err)
	}
	generic, err := openaicompat.BuildCompatible("off-ref-probe", "custom-openai-legacy-compatible", node, srv.Client(), openaicompat.FlavorChat, openaicompat.CompatibleTransportCaps(openaicompat.FlavorChat))
	if err != nil {
		t.Fatalf("generic build: %v", err)
	}
	compiled, err := standardplugins.CompileProviderProfile(profile)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	profileOff, err := standardplugins.BuildProviderProfileBackend(compiled, "off-ref-probe", srv.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("profile build: %v", err)
	}
	mkCall := func(mode aipapi.TransportMode) aipapi.Call {
		return aipapi.Call{
			Messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "w", Content: []byte(`{"id":"call_1","type":"function","function":{"name":"w","arguments":"{}"}}`)}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call_1", Content: []byte(`"ok"`)}}},
			},
			Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
			Invocation: aipapi.Invocation{
				Operation:     aipapi.OperationOpenAIChatCompletions,
				DeliveryMode:  aipapi.DeliveryModeNonStreaming,
				TransportMode: mode,
			},
		}
	}
	cand := routing.AttemptCandidate{Primary: routing.Primary{Backend: "off-ref-probe", Model: "m"}}
	for _, mode := range []aipapi.TransportMode{aipapi.TransportModeNonStreaming, aipapi.TransportModeStreaming} {
		captured = nil
		for _, be := range []execbackend.Backend{generic, profileOff} {
			stream, err := be.Open(context.Background(), mkCall(mode), cand)
			if err != nil {
				t.Fatalf("mode %v open: %v", mode, err)
			}
			_, _ = aipapi.Collect(context.Background(), stream)
			_ = stream.Close()
		}
		if len(captured) != 2 {
			t.Fatalf("mode %v: captured %d bodies, want 2", mode, len(captured))
		}
		if string(captured[0]) != string(captured[1]) {
			t.Fatalf("mode %v: generic vs profile-off differ:\n%s\n%s", mode, captured[0], captured[1])
		}
		if strings.Contains(string(captured[0]), "reasoning_content") {
			t.Fatalf("mode %v: quirk-off wire must not carry reasoning_content: %s", mode, captured[0])
		}
		if mode == aipapi.TransportModeStreaming && !strings.Contains(string(captured[0]), `"stream":true`) {
			t.Fatalf("streaming wire must set stream:true: %s", captured[0])
		}
	}
}

// Overlapping pending groups (re-review P1): [callA, callB, outA, callC,
// messageX, outC, outB] must be resolved in one pass, not partially.
func TestStrictQuirks_OverlappingPendingGroups(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	srv := httptest.NewServer(openairesponses.NewHandler(openairesponses.Config{StrictToolOutputAdjacency: true}))
	t.Cleanup(srv.Close)

	call := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("start")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cA", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cA","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cB", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cB","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cA", Content: []byte(`"ok-a"`)}}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, ToolCallID: "cC", ToolName: "w", Content: []byte(`{"type":"function_call","call_id":"cC","name":"w","arguments":"{}"}`)}}},
			{Role: aipapi.RoleDeveloper, Parts: []aipapi.Part{aipapi.TextPart("X")}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cC", Content: []byte(`"ok-c"`)}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "cB", Content: []byte(`"ok-b"`)}}},
		},
		Tools: []aipapi.ToolDef{{Name: "w", Parameters: []byte(`{}`)}},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIResponses,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
	open := func(t *testing.T, quirks []providerprofiles.QuirkID) error {
		t.Helper()
		profile := providerprofiles.Profile{
			APIVersion: providerprofiles.APIVersionV1,
			ID:         "overlap-probe",
			Family:     providerprofiles.FamilyOpenAIResponses,
			Endpoint:   providerprofiles.Endpoint{BaseURL: srv.URL + "/v1", PathPolicy: providerprofiles.PathPolicyFamilyDefault},
			Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: "DEEPSEEK_API_KEY"},
			Models: providerprofiles.ModelDiscovery{
				Policy:    providerprofiles.DiscoveryStatic,
				Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
				Static:    []providerprofiles.Model{{CanonicalID: "m", NativeID: "m", DisplayName: "M"}},
			},
			Quirks: quirks,
		}
		compiled, err := standardplugins.CompileProviderProfile(profile)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		be, err := standardplugins.BuildProviderProfileBackend(compiled, "overlap", srv.Client(), pluginreg.BackendFactoryDeps{})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		stream, err := be.Open(context.Background(), call, routing.AttemptCandidate{Primary: routing.Primary{Backend: "overlap", Model: "m"}})
		if err != nil {
			return err
		}
		_ = stream.Close()
		return nil
	}
	if err := open(t, nil); err == nil {
		t.Fatal("without quirk, strict emulator must 400 on overlapping pending groups")
	}
	if err := open(t, []providerprofiles.QuirkID{providerprofiles.QuirkOpenAIResponsesToolAdjacency}); err != nil {
		t.Fatalf("with quirk, expected 200, got: %v", err)
	}
}
