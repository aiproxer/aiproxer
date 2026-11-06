package openresponsescompat

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func testSpec() BackendSpec {
	return BackendSpec{
		ID:             "my-or",
		BaseURL:        "https://api.example.com/openresponses/v1",
		RequestLimits:  defaultRequestLimits(),
		ResponseLimits: defaultResponseLimits(),
		Caps:           aipapi.NewBackendCaps(defaultCapabilities...),
		DialectSupport: aipapi.NormalizeDialectSupport(dialectSupportFromConfig(Config{Dialects: defaultDialects()})),
	}
}

func itemAuthorityCreateCall() aipapi.Call {
	return aipapi.Call{
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenResponsesCreate,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
		Tools: []aipapi.ToolDef{{
			Name:        "get_weather",
			Description: "Get the current weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Options: aipapi.GenerationOptions{
			Temperature:       new(0.5),
			TopP:              new(0.9),
			MaxOutputTokens:   new(128),
			ParallelToolCalls: new(true),
		},
		Items: []aipapi.Item{
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "msg_1",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartText,
					Text: "What is the weather in Paris?",
				}},
			},
			{
				Kind:   aipapi.ItemKindToolCall,
				ID:     "fc_1",
				Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{
					CallID:    "call_1",
					Name:      "get_weather",
					Arguments: json.RawMessage(`{"location":"Paris"}`),
				},
			},
			{
				Kind:   aipapi.ItemKindToolResult,
				ID:     "fo_1",
				Status: aipapi.ItemStatusCompleted,
				ToolResult: &aipapi.ToolResultItem{
					CallID: "call_1",
					Name:   "get_weather",
					Output: "Sunny 22C",
				},
			},
			{
				Kind:   aipapi.ItemKindMessage,
				ID:     "msg_2",
				Status: aipapi.ItemStatusCompleted,
				Role:   aipapi.RoleAssistant,
				Phase:  aipapi.AssistantPhaseCommentary,
				Content: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartText,
					Text: "Let me check.",
				}},
			},
		},
	}
}

func TestBuildCreateRequest_ReasoningEffortRoundTrips(t *testing.T) {
	call := itemAuthorityCreateCall()
	call.Options.ReasoningEffort = "low"
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if string(got["reasoning"]) != `{"effort":"low"}` {
		t.Fatalf("reasoning=%s", got["reasoning"])
	}
}

//go:fix inline
func floatPtr(f float64) *float64 { return new(f) }

//go:fix inline
func intPtr(i int) *int { return new(i) }

//go:fix inline
func boolPtr(b bool) *bool { return new(b) }

func TestBuildCreateRequest_OrderedPortableItemsAndControls(t *testing.T) {
	t.Parallel()
	call := itemAuthorityCreateCall()
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(body))
	}
	if got := string(payload["model"]); got != `"model-x"` {
		t.Fatalf("model = %s, want model-x", got)
	}
	if got := string(payload["temperature"]); got != "0.5" {
		t.Fatalf("temperature = %s, want 0.5", got)
	}
	if got := string(payload["top_p"]); got != "0.9" {
		t.Fatalf("top_p = %s, want 0.9", got)
	}
	if got := string(payload["max_output_tokens"]); got != "128" {
		t.Fatalf("max_output_tokens = %s, want 128", got)
	}
	if got := string(payload["parallel_tool_calls"]); got != "true" {
		t.Fatalf("parallel_tool_calls = %s, want true", got)
	}
	if got := string(payload["tool_choice"]); got != `"auto"` {
		t.Fatalf("tool_choice = %s, want auto", got)
	}

	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(payload["tools"], &tools); err != nil || len(tools) != 1 {
		t.Fatalf("tools = %v err=%v", tools, err)
	}
	if string(tools[0]["type"]) != `"function"` || string(tools[0]["name"]) != `"get_weather"` {
		t.Fatalf("tool[0] = %s", string(payload["tools"]))
	}

	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	if len(input) != 4 {
		t.Fatalf("input items = %d, want 4", len(input))
	}
	assertField := func(i int, field, want string) {
		t.Helper()
		if got := string(input[i][field]); got != want {
			t.Fatalf("input[%d].%s = %s, want %s (item=%s)", i, field, got, want, string(payload["input"]))
		}
	}
	assertField(0, "type", `"message"`)
	assertField(0, "role", `"user"`)
	assertField(1, "type", `"function_call"`)
	assertField(1, "name", `"get_weather"`)
	assertField(1, "call_id", `"call_1"`)
	assertField(2, "type", `"function_call_output"`)
	assertField(2, "output", `"Sunny 22C"`)
	assertField(3, "type", `"message"`)
	assertField(3, "role", `"assistant"`)
	assertField(3, "phase", `"commentary"`)

	if string(input[1]["arguments"]) != `"{\"location\":\"Paris\"}"` {
		t.Fatalf("arguments must be a JSON string on the wire, got %s", string(input[1]["arguments"]))
	}
	if string(input[0]["content"]) != `[{"type":"input_text","text":"What is the weather in Paris?"}]` {
		t.Fatalf("user content = %s", string(input[0]["content"]))
	}
	if string(input[3]["content"]) != `[{"type":"output_text","text":"Let me check.","annotations":[]}]` {
		t.Fatalf("assistant content = %s", string(input[3]["content"]))
	}
}

func TestBuildCreateRequest_InstructionsMapToLeadingSystemItem(t *testing.T) {
	t.Parallel()
	call := itemAuthorityCreateCall()
	call.Items = append([]aipapi.Item{{
		Kind:    aipapi.ItemKindMessage,
		Status:  aipapi.ItemStatusCompleted,
		Role:    aipapi.RoleSystem,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "Be concise."}},
	}}, call.Items...)
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(body))
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatalf("input unmarshal: %v", err)
	}
	if len(input) < 1 || string(input[0]["type"]) != `"message"` || string(input[0]["role"]) != `"system"` {
		t.Fatalf("instructions must forward as a leading system message item, got %+v", input)
	}
	if string(input[0]["content"]) != `[{"type":"input_text","text":"Be concise."}]` {
		t.Fatalf("leading system content = %s", string(input[0]["content"]))
	}
}

func TestBuildCreateRequest_NeverForwardsProxyOrArbitraryFields(t *testing.T) {
	t.Parallel()
	call := itemAuthorityCreateCall()
	call.ID = "proxy_call_1"
	call.Session = aipapi.SessionRef{
		ClientSessionID:        "client-session-1",
		AuthoritativeSessionID: "auth-session-1",
		ALegID:                 "aleg-1",
		ResumeToken:            "resume-secret",
	}
	call.Route = aipapi.RouteIntent{Selector: "model-x"}
	call.Extensions = map[string]json.RawMessage{
		"openresponses.model": json.RawMessage(`"model-x"`),
		"acme:proprietary":    json.RawMessage(`{"a":1}`),
	}
	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"previous_response_id", "store", "stream", "background",
		"client_session", "authoritative_session", "resume_token",
		"aleg", "proxy_call_1", "client-session-1", "auth-session-1",
		"resume-secret", "openresponses.model", "acme:proprietary",
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("request must not forward %q: %s", forbidden, string(body))
		}
	}
}

func TestBuildCreateRequest_UnrepresentableContentRejected(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*aipapi.Call){
		"annotation_content": func(c *aipapi.Call) {
			c.Items[0].Content = []aipapi.ContentPart{{Kind: aipapi.ContentPartAnnotation, Annotation: &aipapi.AnnotationPart{Type: "url_citation"}}}
		},
		"assistant_ref_content": func(c *aipapi.Call) {
			c.Items[0].Content = []aipapi.ContentPart{{Kind: aipapi.ContentPartAssistantRef, AssistantRef: "file_1"}}
		},
		"unprefixed_extension": func(c *aipapi.Call) {
			c.Items = []aipapi.Item{{Kind: aipapi.ItemKindExtension, Extension: &aipapi.OpaqueExtension{Namespace: "acme", Type: "widget", Data: json.RawMessage(`{"k":1}`)}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := itemAuthorityCreateCall()
			mutate(&c)
			_, err := buildCreateRequest("my-or", testSpec(), c, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected unrepresentable rejection")
			}
			if !strings.Contains(err.Error(), ErrUnrepresentable.Error()) {
				t.Fatalf("error = %v, want ErrUnrepresentable", err)
			}
		})
	}
}

func TestBuildCreateRequest_MissingModelRejected(t *testing.T) {
	t.Parallel()
	_, err := buildCreateRequest("my-or", testSpec(), itemAuthorityCreateCall(), routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected missing model rejection")
	}
}

func TestBuildCreateRequest_ReasoningEffortMappedToWire(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		effort string
		want   string
	}{
		{name: "none", effort: "none", want: `{"effort":"none"}`},
		{name: "low", effort: "low", want: `{"effort":"low"}`},
		{name: "medium", effort: "medium", want: `{"effort":"medium"}`},
		{name: "high", effort: "high", want: `{"effort":"high"}`},
		{name: "xhigh", effort: "xhigh", want: `{"effort":"xhigh"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := itemAuthorityCreateCall()
			call.Options.ReasoningEffort = tc.effort
			body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("body is not valid JSON: %v body=%s", err, string(body))
			}
			got := string(payload["reasoning"])
			if got != tc.want {
				t.Fatalf("reasoning = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBuildCreateRequest_ResponseMIMETypeMappedToTextFormat(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		mime string
		want string
	}{
		{name: "application_json", mime: "application/json", want: `{"format":{"type":"json_object"}}`},
		{name: "text_plain", mime: "text/plain", want: `{"format":{"type":"text"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := itemAuthorityCreateCall()
			call.Options.ResponseMIMEType = tc.mime
			spec := testSpec()
			spec.Caps = aipapi.NewBackendCaps(append([]aipapi.Capability{}, defaultCapabilities...)...)
			spec.Caps[aipapi.CapabilityStructuredOutputs] = struct{}{}
			body, err := buildCreateRequest("my-or", spec, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("body is not valid JSON: %v body=%s", err, string(body))
			}
			got := string(payload["text"])
			if got != tc.want {
				t.Fatalf("text = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestBuildCreateRequest_UnrepresentableControlsRejected(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*aipapi.Call){
		"verbosity": func(c *aipapi.Call) {
			c.Options.Verbosity = aipapi.VerbosityHigh
		},
		"unsupported_mime": func(c *aipapi.Call) {
			c.Options.ResponseMIMEType = "application/xml"
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call := itemAuthorityCreateCall()
			mutate(&call)
			_, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
			if err == nil {
				t.Fatal("expected unrepresentable rejection")
			}
			if !errors.Is(err, ErrUnrepresentable) {
				t.Fatalf("error = %v, want ErrUnrepresentable", err)
			}
		})
	}
}

func TestBuildCreateRequest_RequestLimitsEnforced(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	spec.RequestLimits.MaxItems = 1
	_, err := buildCreateRequest("my-or", spec, itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected item count limit rejection")
	}

	spec = testSpec()
	spec.RequestLimits.MaxTools = 0
	_, err = buildCreateRequest("my-or", spec, itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected tools limit rejection")
	}

	spec = testSpec()
	spec.RequestLimits.MaxItemBytes = 1
	_, err = buildCreateRequest("my-or", spec, itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected per-item byte limit rejection")
	}

	spec = testSpec()
	spec.RequestLimits.MaxContentParts = 0
	_, err = buildCreateRequest("my-or", spec, itemAuthorityCreateCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err == nil {
		t.Fatal("expected content parts limit rejection")
	}
}

func TestMapping_ReasoningAndExtensionItems(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	spec.DialectSupport = aipapi.NormalizeDialectSupport(aipapi.DialectSupport{
		ItemDialects: []aipapi.DialectRequirement{
			{Kind: "item", Dialect: "openresponses.2026-04-24"},
			{Kind: "item", Dialect: "openresponses.reasoning.v1"},
			{Kind: "item", Dialect: "acme:widget", Implementor: "acme-vendor"},
		},
		ReasoningDialects: []aipapi.DialectRequirement{
			{Kind: "reasoning", Dialect: "openresponses.reasoning.v1"},
		},
		ExtensionTypes: []aipapi.ExtensionRequirement{
			{Namespace: "acme", Type: "acme:widget", Implementor: "acme-vendor"},
		},
	})
	call := itemAuthorityCreateCall()
	call.Items = append(
		call.Items,
		aipapi.Item{Kind: aipapi.ItemKindReasoning, ID: "rs_1", Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Dialect: "openresponses.reasoning.v1", Text: "think"}}},
		aipapi.Item{Kind: aipapi.ItemKindExtension, ID: "ext_1", Extension: &aipapi.OpaqueExtension{Namespace: "acme", Type: "acme:widget", Implementor: "acme-vendor", Data: json.RawMessage(`{"k":1}`)}},
	)
	if err := checkRequirements("my-or", call, spec.Caps, spec.DialectSupport); err != nil {
		t.Fatalf("requirements must pass with declared dialects: %v", err)
	}
	body, err := buildCreateRequest("my-or", spec, call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(payload["input"], &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 6 {
		t.Fatalf("input items = %d, want 6", len(input))
	}
	if string(input[4]["type"]) != `"reasoning"` || string(input[4]["reasoning"]) != `"think"` {
		t.Fatalf("reasoning item = %s", string(payload["input"]))
	}
	if string(input[5]["type"]) != `"acme:widget"` || string(input[5]["namespace"]) != `"acme"` {
		t.Fatalf("extension item = %s", string(payload["input"]))
	}
}

func TestMapping_UnsupportedDialectRejected(t *testing.T) {
	t.Parallel()
	spec := testSpec() // default dialects: item openresponses.2026-04-24 only
	call := itemAuthorityCreateCall()
	call.Items = append(call.Items, aipapi.Item{Kind: aipapi.ItemKindReasoning, ID: "rs_1", Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Dialect: "openresponses.reasoning.v1", Text: "think"}}})
	err := checkRequirements("my-or", call, spec.Caps, spec.DialectSupport)
	if err == nil {
		t.Fatal("expected requirement rejection for undeclared reasoning dialect")
	}
}

func TestMapping_WireToolArguments_Normalization(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "plain_object_wrapped", in: `{"location":"Paris"}`, want: `"{\"location\":\"Paris\"}"`},
		{name: "json_string_kept", in: `"{\"location\":\"Paris\"}"`, want: `"{\"location\":\"Paris\"}"`},
		{name: "whitespace_document_preserved", in: ` { "location" : "Paris" } `, want: `" { \"location\" : \"Paris\" } "`},
		{name: "empty_absent", in: ``, want: ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := wireToolArguments([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("wireToolArguments(%q) = %q, want %q", tc.in, string(got), tc.want)
			}
		})
	}
}

// TestMapping_WireMessageToolArguments_WrapsExactlyOnce proves the legacy
// message-authority conversion always wraps the document once, including a JSON
// string document, so the upstream decodes the exact argument text.
func TestMapping_WireMessageToolArguments_WrapsExactlyOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "object", in: `{"location":"Paris"}`, want: `"{\"location\":\"Paris\"}"`},
		{name: "string document", in: `"hello"`, want: `"\"hello\""`},
		{name: "whitespace preserved", in: ` { "a" : 1 } `, want: `" { \"a\" : 1 } "`},
		{name: "empty_absent", in: ``, want: ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := wireMessageToolArguments([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("wireMessageToolArguments(%q) = %q, want %q", tc.in, string(got), tc.want)
			}
		})
	}
}

func TestMapping_ResolveCreateEndpoint_PathTraversalRejected(t *testing.T) {
	t.Parallel()
	ep, err := resolveCreateEndpoint("https://api.example.com/openresponses/v1")
	if err != nil {
		t.Fatal(err)
	}
	if ep != "https://api.example.com/openresponses/v1/responses" {
		t.Fatalf("endpoint = %q", ep)
	}
	for _, base := range []string{
		"https://api.example.com/v1/../x",
		"https://api.example.com/%2e%2e/x",
	} {
		if _, err := resolveCreateEndpoint(base); err == nil {
			t.Fatalf("expected traversal rejection for %q", base)
		}
	}
}
