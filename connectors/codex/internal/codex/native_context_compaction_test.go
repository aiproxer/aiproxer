package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type compactionTimeoutError struct{}

func (compactionTimeoutError) Error() string   { return "timeout details must not be surfaced" }
func (compactionTimeoutError) Timeout() bool   { return true }
func (compactionTimeoutError) Temporary() bool { return true }

func TestBuildCompactionRequest_MatchesDedicatedCodexCompactShape(t *testing.T) {
	t.Parallel()
	normal := Payload{
		Model: "m", Instructions: "instructions", Stream: true, Store: true,
		Input:     []inputItem{textMessageItem{Type: "message", Role: "user", Content: "old"}},
		Reasoning: &reasoningSpec{Effort: "high", Summary: "auto"},
		Include:   []string{"reasoning.encrypted_content"}, PromptCacheKey: "cache", PreviousResponseID: "previous",
	}
	cfg := Config{BaseURL: "https://selected.example", AccessToken: "secret", AccountID: "acct", NativeContext: &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: true}}
	request, err := buildCompactionRequest(normal, normal.Input, cfg, "conversation", nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.Payload.Store || request.Payload.Reasoning == nil || len(request.Payload.Include) != 0 || request.Payload.PromptCacheKey != "" || request.Payload.PreviousResponseID != "" {
		t.Fatalf("compaction control fields were copied from normal request: %+v", request.Payload)
	}
	if request.Account.AccessToken != "secret" || request.Account.BaseURL != cfg.BaseURL || request.Conversation != "conversation" {
		t.Fatalf("identity not preserved: %+v", request)
	}
	if len(request.Payload.Input) != 1 || isCompactionTrigger(request.Payload.Input[0]) {
		t.Fatalf("input = %#v", request.Payload.Input)
	}
	body, err := request.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret") || strings.Contains(string(body), `"stream"`) || strings.Contains(string(body), `"store"`) {
		t.Fatal("account secret entered payload")
	}
}

func TestBuildCompactionRequest_StripsReasoningResponseStatus(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"opaque","status":"completed"}`)
	normal := Payload{Model: "m", Input: []inputItem{opaqueResponseItem{raw: raw}}}
	request, err := buildCompactionRequest(normal, normal.Input, Config{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := request.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"status":"completed"`) {
		t.Fatalf("response-only reasoning status entered compaction payload: %s", body)
	}
}

func TestBuildCompactionRequest_DoesNotOverrideExplicitFalse(t *testing.T) {
	t.Parallel()
	normal := Payload{Model: "m", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "x"}}}
	request, err := buildCompactionRequest(normal, normal.Input, Config{NativeContext: &NativeContextConfig{Enabled: true, RequestEncryptedReasoning: false}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.Payload.Reasoning != nil || len(request.Payload.Include) != 0 {
		t.Fatalf("explicit false overridden: %+v", request.Payload)
	}
}

func TestBuildCompactionRequest_DeepCopiesMutablePayload(t *testing.T) {
	t.Parallel()
	include := []string{"one"}
	parallel := true
	tools := []toolPayload{{Type: "function", Name: "tool", Parameters: map[string]any{"nested": map[string]any{"value": "before"}}}}
	normal := Payload{
		Model: "m", Input: []inputItem{richMessageItem{Type: "message", Role: "user", Content: []contentBlock{inputTextPart{Type: "input_text", Text: "before"}}}},
		Tools: tools, Include: include, Reasoning: &reasoningSpec{Effort: "high"}, Text: &textSpec{}, ParallelToolCalls: &parallel,
	}
	request, err := buildCompactionRequest(normal, normal.Input, Config{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	include[0] = "changed"
	parallel = false
	nested, ok := tools[0].Parameters["nested"].(map[string]any)
	if !ok {
		t.Fatal("expected nested to be map[string]any")
	}
	nested["value"] = "changed"
	rich, ok := normal.Input[0].(richMessageItem)
	if !ok {
		t.Fatal("expected richMessageItem")
	}
	rich.Content[0] = inputTextPart{Type: "input_text", Text: "changed"}
	reqNested, ok := request.Payload.Tools[0].Parameters["nested"].(map[string]any)
	if !ok || reqNested["value"] != "before" {
		t.Fatal("payload clone aliases caller-owned values")
	}
	reqRich, ok := request.Payload.Input[0].(richMessageItem)
	if !ok {
		t.Fatal("expected richMessageItem in request")
	}
	reqText, ok := reqRich.Content[0].(inputTextPart)
	if !ok || reqText.Text != "before" {
		t.Fatal("input clone aliases caller-owned values")
	}
}

func TestCompactionErrorsAreTypedAndContentSafe(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		compactionProtocolError("invalid_event"),
		compactionTransportError(errors.New("tls private details")),
		newCompactionHTTPError(400),
	} {
		if !errors.Is(err, errCompactionProtocol) && !errors.Is(err, errCompactionTransport) && !errors.Is(err, errCompactionHTTP) {
			t.Fatalf("unclassified error: %v", err)
		}
		if strings.Contains(err.Error(), "opaque") || strings.Contains(err.Error(), "provider") || strings.Contains(err.Error(), "tls") {
			t.Fatalf("payload leaked: %v", err)
		}
	}
}

func TestBuildCompactionRequest_RejectsExistingTrigger(t *testing.T) {
	t.Parallel()
	_, err := buildCompactionRequest(Payload{}, []inputItem{opaqueResponseItem{raw: compactionTriggerRaw()}}, Config{}, "", nil)
	if !errors.Is(err, errCompactionProtocol) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompactionClient_UsesDedicatedEndpointAndJSONResponse(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/responses/compact" {
			t.Errorf("path = %q, want /responses/compact", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if _, ok := body["stream"]; ok {
			t.Error("dedicated compact request included stream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"r","object":"response.compaction","created_at":1,"output":[{"type":"message","id":"msg-retained","role":"user","status":"completed","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction_summary","id":"cmp","encrypted_content":"opaque"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	request, err := buildCompactionRequest(Payload{Model: "m", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "x"}}}, []inputItem{textMessageItem{Type: "message", Role: "user", Content: "x"}}, Config{AccessToken: "token", BaseURL: server.URL}, "conv", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := newCompactionClient(server.Client(), server.URL+"/responses/compact").Compact(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "r" || len(result.Output) != 2 || inputItemType(result.Output[1]) != "compaction_summary" {
		t.Fatalf("result = %+v", result)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestParseCompactionJSONRequiresExactlyOneCompactionSummary(t *testing.T) {
	t.Parallel()
	base := `{"id":"r","object":"response.compaction","output":[{"type":"message","id":"msg-retained","role":"user","status":"completed","content":[{"type":"input_text","text":"retained"}]},%s],"usage":{"input_tokens":1}}`
	item := `{"type":"compaction_summary","id":"cmp","encrypted_content":"opaque"}`
	result, err := parseCompactionJSON(fmt.Appendf(nil, base, item))
	if err != nil || len(result.Output) != 2 || inputItemType(result.Output[1]) != "compaction_summary" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := parseCompactionJSON(fmt.Appendf(nil, base, item+","+item)); !errors.Is(err, errCompactionProtocol) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestParseCompactionJSONAcceptsObservedUnaryShapes(t *testing.T) {
	t.Parallel()
	raw := `{"id":"r-array","object":"response.compaction","created_at":1,"output":[{"type":"message","id":"msg-developer","role":"developer","status":"completed","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction_summary","id":"cmp-array","encrypted_content":"opaque","status":"completed"}],"usage":{"input_tokens":1}}`
	result, err := parseCompactionJSON([]byte(raw))
	if err != nil || len(result.Output) != 2 || inputItemType(result.Output[1]) != "compaction_summary" {
		t.Fatalf("shape rejected: err=%v output=%#v", err, result.Output)
	}
}

func TestParseCompactionJSONUsageIsOptionalButValidatedWhenPresent(t *testing.T) {
	t.Parallel()
	base := `{"id":"r","object":"response.compaction","output":[{"type":"compaction_summary","id":"cmp","encrypted_content":"opaque"}]%s}`
	cases := []struct {
		name      string
		usage     string
		wantUsage bool
		wantErr   bool
	}{
		{name: "absent", usage: ``, wantUsage: false},
		{name: "null", usage: `,"usage":null`, wantUsage: false},
		{name: "present", usage: `,"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}`, wantUsage: true},
		{name: "non_object", usage: `,"usage":[]`, wantErr: true},
		{name: "wrong_field_type", usage: `,"usage":{"input_tokens":"seven"}`, wantErr: true},
		{name: "negative", usage: `,"usage":{"input_tokens":-1}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := fmt.Sprintf(base, tc.usage)
			result, err := parseCompactionJSON([]byte(raw))
			if tc.wantErr {
				if !errors.Is(err, errCompactionProtocol) {
					t.Fatalf("error = %v, want protocol failure", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (result.Usage != nil) != tc.wantUsage {
				t.Fatalf("usage present = %t, want %t", result.Usage != nil, tc.wantUsage)
			}
		})
	}
}

func TestParseCompactionJSONRejectsNonCompletedCompactionStatus(t *testing.T) {
	t.Parallel()
	raw := `{"id":"r","object":"response.compaction","output":[{"type":"compaction_summary","id":"cmp","encrypted_content":"opaque","status":"in_progress"}],"usage":{"input_tokens":1}}`
	if _, err := parseCompactionJSON([]byte(raw)); !errors.Is(err, errCompactionProtocol) {
		t.Fatalf("error = %v, want protocol failure", err)
	}
}

func TestParseCompactionJSONRetainedMessageValidation(t *testing.T) {
	t.Parallel()
	base := `{"id":"r","object":"response.compaction","output":[%s,{"type":"compaction_summary","id":"cmp","encrypted_content":"opaque"}],"usage":{"input_tokens":1}}`
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"assistant_final", `{"type":"message","id":"m-assistant","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"ok","annotations":[]}]}`, true},
		{"input_image", `{"type":"message","id":"m-image","role":"user","content":[{"type":"input_image","image_url":"https://example.test/image.png","detail":"auto"}]}`, true},
		{"missing_id", `{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}`, false},
		{"tool_role", `{"type":"message","id":"m-tool","role":"tool","content":[{"type":"input_text","text":"x"}]}`, false},
		{"bad_status", `{"type":"message","id":"m-status","role":"user","status":"in_progress","content":[{"type":"input_text","text":"x"}]}`, false},
		{"unknown_field", `{"type":"message","id":"m-extra","role":"user","content":[{"type":"input_text","text":"x"}],"metadata":{}}`, false},
		{"bad_content", `{"type":"message","id":"m-content","role":"user","content":[{"type":"function_call","name":"x"}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseCompactionJSON(fmt.Appendf(nil, base, tc.msg))
			if (err == nil) != tc.want {
				t.Fatalf("err=%v wantAccepted=%v", err, tc.want)
			}
		})
	}
}

func FuzzParseCompactionRetainedMessage(f *testing.F) {
	f.Add([]byte(`{"type":"message","id":"m","role":"user","content":[{"type":"input_text","text":"x"}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, err := parseCompactionRetainedMessage(raw)
		if err != nil && len(err.Error()) > 128 {
			t.Fatalf("error was not content-safe: %q", err)
		}
	})
}

func TestCompactionClient_CancellationClosesResponseBody(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		close(started)
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	request, err := buildCompactionRequest(Payload{Model: "m", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "x"}}}, []inputItem{textMessageItem{Type: "message", Role: "user", Content: "x"}}, Config{BaseURL: server.URL}, "conv", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := newCompactionClient(server.Client(), server.URL+"/responses/compact").Compact(ctx, request)
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; err == nil || !errors.Is(err, errCompactionCanceled) || errors.Is(err, errCompactionProtocol) {
		t.Fatalf("cancellation error = %v", err)
	}
	<-done
	server.Close()
}

func TestCollectCompactionSSE_RequiresOneCompletedCompaction(t *testing.T) {
	t.Parallel()
	item := `{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`
	stream := fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[%s]}}\n", item, item)
	result, err := collectCompactionSSE(context.Background(), strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if result.ResponseID != "r" || result.Item.raw == nil {
		t.Fatalf("result = %+v", result)
	}
}

func TestCollectCompactionSSEUsageIsOptionalButValidatedWhenPresent(t *testing.T) {
	t.Parallel()
	item := `{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`
	base := `data: {"type":"response.created","response":{"id":"r"}}
data: {"type":"response.output_item.done","item":%s}
data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[%s]%s}}
`
	cases := []struct {
		name    string
		usage   string
		wantErr bool
	}{
		{name: "absent"},
		{name: "null", usage: `,"usage":null`},
		{name: "present", usage: `,"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}`},
		{name: "non_object", usage: `,"usage":[]`, wantErr: true},
		{name: "negative", usage: `,"usage":{"input_tokens":-1}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stream := fmt.Sprintf(base, item, item, tc.usage)
			result, err := collectCompactionSSE(context.Background(), strings.NewReader(stream))
			if tc.wantErr {
				if !errors.Is(err, errCompactionProtocol) {
					t.Fatalf("error = %v, want protocol failure", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "present" && result.Usage == nil {
				t.Fatal("present usage was discarded")
			}
			if tc.name != "present" && result.Usage != nil {
				t.Fatalf("usage = %#v, want absent", result.Usage)
			}
		})
	}
}

func TestCompactionCollector_AcceptsSSEDataWithoutOptionalSpace(t *testing.T) {
	t.Parallel()
	item := `{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`
	stream := fmt.Sprintf("data:{\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\ndata:{\"type\":\"response.output_item.done\",\"item\":%s}\ndata:{\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[%s]}}\n", item, item)
	if _, err := collectCompactionSSE(context.Background(), strings.NewReader(stream)); err != nil {
		t.Fatal(err)
	}
}

func TestBuildReplacement_CodexPredicateBoundsAndOrder(t *testing.T) {
	t.Parallel()
	history := NativeHistory{Items: []inputItem{
		textMessageItem{Type: "message", Role: "system", Content: "system"},
		textMessageItem{Type: "message", Role: "user", Content: "user"},
		textMessageItem{Type: "message", Role: "assistant", Content: "commentary", phase: "commentary"},
		textMessageItem{Type: "message", Role: "assistant", Content: "final", phase: "final_answer"},
		functionCallItem{Type: "function_call", CallID: "call-1", Name: "tool", Arguments: "{}"},
		functionCallOutputItem{Type: "function_call_output", CallID: "call-1", Output: "result"},
	}}
	result, err := buildReplacement(history, opaqueResponseItem{raw: json.RawMessage(`{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`)}, ReplacementConfig{PerAgentMessageTokens: 100, TotalMessageTokens: 100, MaxImages: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || inputItemType(result.Items[len(result.Items)-1]) != "compaction" {
		t.Fatalf("replacement items = %v", result.Items)
	}
	if inputItemType(result.Items[0]) != "message" || inputItemType(result.Items[1]) != "message" {
		t.Fatalf("retained order = %v", result.Items)
	}
}

func TestCollectCompactionSSE_RejectsMissingAssistantAndDuplicateTerminal(t *testing.T) {
	t.Parallel()
	item := `{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`
	cases := []string{
		`data: {"type":"response.created","response":{"id":"r"}}
data: {"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}
`,
		fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\ndata: {\"type\":\"response.output_item.done\",\"item\":%s}\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[%s]}}\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[%s]}}\n", item, item, item),
	}
	for _, stream := range cases {
		if _, err := collectCompactionSSE(context.Background(), strings.NewReader(stream)); !errors.Is(err, errCompactionProtocol) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestCollectCompactionSSE_CancellationIsSafe(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := collectCompactionSSE(ctx, strings.NewReader("data: {}\n"))
	if !errors.Is(err, errCompactionCanceled) || errors.Is(err, errCompactionProtocol) || strings.Contains(err.Error(), "opaque") {
		t.Fatalf("error = %v", err)
	}
}

func FuzzCollectCompactionSSE_BoundsAndContentSafeErrors(f *testing.F) {
	f.Add("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n")
	f.Add("data: response.output_text.delta\n")
	f.Fuzz(func(t *testing.T, input string) {
		_, err := collectCompactionSSE(context.Background(), strings.NewReader(input))
		if err != nil && len(err.Error()) > 128 {
			t.Fatalf("error was not bounded: %d bytes", len(err.Error()))
		}
	})
}
