package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func completeResponseJSON(usage string) string {
	if usage == "" {
		usage = `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`
	}
	return `{
		"id":"r","object":"response","created_at":1,"status":"completed","model":"m","output":[],
		"parallel_tool_calls":false,"reasoning":null,"store":true,"background":false,
		"temperature":1,"text":{},"tool_choice":"auto","tools":[],"top_p":1,
		"truncation":"disabled","usage":` + usage + `,"metadata":{},"service_tier":"default",
		"max_output_tokens":null,"max_tool_calls":null,"instructions":null,
		"previous_response_id":null,"error":null,"incomplete_details":null
	}`
}

// TestParseUsage_MalformedBranches covers token-detail validation failures.
func TestParseUsage_MalformedBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		usage string
	}{
		{"missing_input", `{"output_tokens":0,"total_tokens":0}`},
		{"missing_total", `{"input_tokens":0,"output_tokens":0}`},
		{"bad_input_count", `{"input_tokens":"x","output_tokens":0,"total_tokens":0}`},
		{"bad_cached", `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":"x"}}`},
		{"bad_reasoning", `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"output_tokens_details":{"reasoning_tokens":"x"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseResponseResource([]byte(completeResponseJSON(tc.usage)), DefaultParseOptions()); err == nil {
				t.Fatal("expected usage parse error")
			}
		})
	}
}

// TestParseResponse_ExtensionAndDetailBranches covers extension capture and subobject failures.
func TestParseResponse_ExtensionAndDetailBranches(t *testing.T) {
	t.Parallel()

	t.Run("top_level_extension", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"metadata":{}`, `"metadata":{},"acme:org":{"id":"x"}`, 1)
		res, err := ParseResponseResource([]byte(raw), DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		if res.Extensions["acme:org"] == nil {
			t.Fatal("top-level extension must be captured")
		}
	})

	t.Run("incomplete_details", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"incomplete_details":null`, `"incomplete_details":{"reason":"max_output_tokens"}`, 1)
		res, err := ParseResponseResource([]byte(raw), DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(res.IncompleteDetails), "max_output_tokens") {
			t.Fatalf("incomplete_details: %s", res.IncompleteDetails)
		}
	})

	t.Run("output_malformed", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"output":[]`, `"output":"nope"`, 1)
		if _, err := ParseResponseResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected output parse error")
		}
	})

	t.Run("tools_malformed", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"tools":[]`, `"tools":"nope"`, 1)
		if _, err := ParseResponseResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected tools parse error")
		}
	})

	t.Run("terminal_incomplete", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"status":"completed"`, `"status":"incomplete"`, 1)
		res, err := ParseResponseResource([]byte(raw), DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Terminal() {
			t.Fatal("incomplete must be terminal")
		}
	})
}

// TestParseEvent_ErrorBranches covers event validation failures.
func TestParseEvent_ErrorBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
		opts ParseOptions
	}{
		{"missing_type", `{"sequence_number":1}`, ParseOptions{}},
		{"unknown_unprefixed", `{"type":"random_event"}`, ParseOptions{}},
		{"oversized", `{"type":"response.created","x":"` + strings.Repeat("a", 1000) + `"}`, ParseOptions{MaxEventBytes: 128}},
		{"bad_output_index", `{"type":"response.output_item.added","output_index":"x"}`, ParseOptions{}},
		{"bad_content_index", `{"type":"response.content_part.added","content_index":"x"}`, ParseOptions{}},
		{"bad_item", `{"type":"response.output_item.added","item":"nope"}`, ParseOptions{}},
		{"bad_part", `{"type":"response.content_part.added","part":"nope"}`, ParseOptions{}},
		{"bad_error", `{"type":"error","error":"nope"}`, ParseOptions{}},
		{"bad_response", `{"type":"response.completed","response":"nope"}`, ParseOptions{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseEvent([]byte(tc.data), tc.opts); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}

	// Null sequence number defaults to zero.
	evt, err := ParseEvent([]byte(`{"type":"response.created","sequence_number":null}`), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if evt.SequenceNumber != 0 {
		t.Fatalf("sequence: %d", evt.SequenceNumber)
	}
}

// TestParseSSE_FieldErrors covers SSE framing validation failures.
func TestParseSSE_FieldErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data string
	}{
		{"unknown_field", "foo: bar\n\ndata: [DONE]\n\n"},
		{"duplicate_terminal", "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":2}\n\ndata: [DONE]\n\n"},
		{"data_after_terminal", "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":2}\n\ndata: [DONE]\n\n"},
		{"missing_event_header", "data: {\"type\":\"response.created\"}\n\n"},
		{"done_before_terminal", "data: [DONE]\n\n"},
		{"event_header_mismatch", "event: response.output_text.delta\ndata: {\"type\":\"response.created\",\"sequence_number\":1}\n\ndata: [DONE]\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := ParseSSE([]byte(tc.data), DefaultParseOptions()); err == nil {
				t.Fatal("expected SSE error")
			}
		})
	}
}

// TestReasoningItem_DirectAndNested covers ReasoningItem.UnmarshalJSON directly and
// nested `reasoning` objects on message items.
func TestReasoningItem_DirectAndNested(t *testing.T) {
	t.Parallel()
	var r ReasoningItem
	if err := json.Unmarshal([]byte(`{"summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null}`), &r); err != nil {
		t.Fatalf("direct unmarshal: %v", err)
	}
	if len(r.Summary) != 1 || !r.EncryptedContentSet {
		t.Fatalf("reasoning: %+v", r)
	}

	var it Item
	if err := json.Unmarshal([]byte(`{"type":"message","role":"assistant","reasoning":{"content":[{"type":"output_text","text":"t"}]}}`), &it); err != nil {
		t.Fatalf("nested reasoning: %v", err)
	}
	if it.Reasoning == nil || len(it.Reasoning.Content) != 1 {
		t.Fatalf("nested reasoning item: %+v", it)
	}

	var bad ReasoningItem
	if err := json.Unmarshal([]byte(`{"encrypted_content":123}`), &bad); err == nil {
		t.Fatal("expected encrypted_content type error")
	}
}

// TestItem_NullAndExtensionBranches covers null-tolerant item fields.
func TestItem_NullAndExtensionBranches(t *testing.T) {
	t.Parallel()
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"function_call","arguments":null,"output":null,"content":null}`), &it); err != nil {
		t.Fatalf("null fields: %v", err)
	}
	if it.Arguments != "" || it.Output != nil {
		t.Fatalf("null fields must be empty: %+v", it)
	}
	if it.Type != string(ItemFunctionCall) {
		t.Fatalf("type: %q", it.Type)
	}
}

// TestNewCustomItem_InvalidRaw covers non-object raw preservation.
func TestNewCustomItem_InvalidRaw(t *testing.T) {
	t.Parallel()
	it := NewCustomItem("acme:chunk", "not-json")
	if string(it.Opaque) != "not-json" {
		t.Fatalf("invalid raw must be preserved: %q", it.Opaque)
	}
}

// TestNewCustomItem_NullRawPreserved covers JSON null, which unmarshals into a
// nil map without error: the discriminator merge must not write into it.
func TestNewCustomItem_NullRawPreserved(t *testing.T) {
	t.Parallel()
	it := NewCustomItem("acme:chunk", "null")
	if string(it.Opaque) != "null" {
		t.Fatalf("null raw must be preserved: %q", it.Opaque)
	}
}

// TestScenarioDescriptor_ValidateErrors covers validation rejections.
func TestScenarioDescriptor_ValidateErrors(t *testing.T) {
	t.Parallel()
	cases := []ScenarioDescriptor{
		{ID: "", Kind: ScenarioJSONText, Description: "x"},
		{ID: "a", Kind: "bogus", Description: "x"},
		{ID: "a", Kind: ScenarioJSONText, Description: "  "},
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Errorf("expected validation error for %+v", tc)
		}
	}
	good := ScenarioDescriptor{ID: "scenario-good", Kind: ScenarioJSONText, Description: "ok"}
	if err := good.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestHTTPError_WithoutErrorObject covers the plain-status rendering.
func TestHTTPError_WithoutErrorObject(t *testing.T) {
	t.Parallel()
	transport := RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header:     http.Header{},
			Body:       http.NoBody,
			Request:    r,
		}, nil
	})
	cli := New(Config{BaseURL: "http://example.invalid", APIKey: "sk", HTTPClient: &http.Client{Transport: transport}})
	_, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
	if err == nil {
		t.Fatal("expected error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError: %v", err)
	}
	if httpErr.ErrorObject != nil {
		t.Fatalf("no error object expected: %+v", httpErr.ErrorObject)
	}
	if !strings.Contains(httpErr.Error(), "404") {
		t.Fatalf("Error(): %q", httpErr.Error())
	}
}

// TestCreateStream_MalformedStream ensures SSE parse errors propagate from streaming.
func TestCreateStream_MalformedStream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":\"nope\"}\n\n"))
	}))
	t.Cleanup(srv.Close)
	cli := newTestClient(t, srv.URL)
	if _, err := cli.CreateStream(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}, nil); err == nil {
		t.Fatal("expected stream parse error")
	}
}

// TestRequestUnmarshal_ExtraBranches covers string input, nulls, and malformed values.
func TestRequestUnmarshal_ExtraBranches(t *testing.T) {
	t.Parallel()
	var p CreateParams
	if err := json.Unmarshal([]byte(`{"model":null,"input":"hello","previous_response_id":null}`), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Model != "" || !p.Input.TextSet || p.Input.Text != "hello" || p.PreviousResponseID != nil {
		t.Fatalf("params: %+v", p)
	}
	if err := json.Unmarshal([]byte(`{"temperature":"hot"}`), &p); err == nil {
		t.Fatal("expected temperature type error")
	}
	if err := json.Unmarshal([]byte(`{"input":123}`), &p); err == nil {
		t.Fatal("expected input type error")
	}
	if err := json.Unmarshal([]byte(`{"tools":"nope"}`), &p); err == nil {
		t.Fatal("expected tools type error")
	}
}

// TestBoundedReadHelper covers zero-max and small-cap paths.
func TestBoundedReadHelper(t *testing.T) {
	t.Parallel()
	if _, err := readBounded(strings.NewReader("small"), 0); err != nil {
		t.Fatalf("zero max must use default: %v", err)
	}
	if _, err := readBounded(strings.NewReader(strings.Repeat("a", 32)), 16); err == nil {
		t.Fatal("expected limit error")
	}
}

// TestItem_ContentStringParsing covers string content on messages.
func TestItem_ContentStringParsing(t *testing.T) {
	t.Parallel()
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"message","role":"user","content":"just text"}`), &it); err != nil {
		t.Fatal(err)
	}
	if len(it.Content) != 1 || it.Content[0].Text != "just text" || it.Content[0].Type != "input_text" {
		t.Fatalf("string content: %+v", it.Content)
	}
}

// TestItem_MarshalFullFields covers marshal of every portable item field.
func TestItem_MarshalFullFields(t *testing.T) {
	t.Parallel()
	it := Item{
		Type: "message", ID: "m1", Status: "completed", Role: "assistant", Phase: "final_answer",
		Content: []ContentPart{{Type: "output_text", Text: "hi"}},
	}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status":"completed"`, `"phase":"final_answer"`, `"role":"assistant"`, `"content":`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}

	call := Item{Type: "function_call", ID: "fc_1", CallID: "c1", Name: "f", Arguments: "{}", Output: json.RawMessage(`"x"`)}
	cb, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cb), `"call_id":"c1"`) || !strings.Contains(string(cb), `"name":"f"`) {
		t.Fatalf("function call marshal: %s", cb)
	}

	ref := Item{Type: "item_reference", EncapsulatedID: "resp_1"}
	rb, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rb), `"encapsulated_id":"resp_1"`) {
		t.Fatalf("reference marshal: %s", rb)
	}
}

// TestItem_MalformedFields covers item field validation failures.
func TestItem_MalformedFields(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, json string }{
		{"bad_role", `{"type":"message","role":123}`},
		{"bad_arguments", `{"type":"function_call","arguments":123}`},
		{"bad_content", `{"type":"message","content":123}`},
		{"bad_reasoning_summary", `{"type":"reasoning","summary":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var it Item
			if err := json.Unmarshal([]byte(tc.json), &it); err == nil {
				t.Fatal("expected item parse error")
			}
		})
	}
}

// TestParseCompactResource_ErrorsAndExtensions covers compact edge branches.
func TestParseCompactResource_ErrorsAndExtensions(t *testing.T) {
	t.Parallel()
	t.Run("malformed_output", func(t *testing.T) {
		raw := `{"id":"c","object":"response.compaction","created_at":1,"status":"completed","model":"m","output":"nope","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`
		if _, err := ParseCompactResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected output error")
		}
	})
	t.Run("extension", func(t *testing.T) {
		raw := `{"id":"c","object":"response.compaction","created_at":1,"status":"completed","model":"m","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0},"acme:meta":{"k":1}}`
		res, err := ParseCompactResource([]byte(raw), DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		if res.Extensions["acme:meta"] == nil {
			t.Fatal("extension missing")
		}
	})
	t.Run("missing_id", func(t *testing.T) {
		raw := `{"object":"response.compaction","created_at":1,"status":"completed","model":"m","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`
		if _, err := ParseCompactResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected missing id error")
		}
	})
}

// TestCreateParams_MarshalExtraFields covers the remaining marshal branches.
func TestCreateParams_MarshalExtraFields(t *testing.T) {
	t.Parallel()
	store := false
	p := CreateParams{
		Model: "m", Input: Input{Text: "x"},
		Truncation: "auto", Store: &store, Background: new(true),
		SafetyIdentifier: "safe", PromptCacheRetention: "10m",
		Extensions: map[string]json.RawMessage{"acme:k": json.RawMessage(`1`)},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"truncation":"auto"`, `"store":false`, `"background":true`, `"safety_identifier":"safe"`, `"prompt_cache_retention":"10m"`, `"acme:k":1`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

// TestParseResponseResource_NullAndMalformed covers null-tolerant and bad fields.
func TestParseResponseResource_NullAndMalformed(t *testing.T) {
	t.Parallel()
	t.Run("null_text", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"text":{}`, `"text":null`, 1)
		res, err := ParseResponseResource([]byte(raw), DefaultParseOptions())
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Text) != "null" {
			t.Fatalf("null text must be preserved: %s", res.Text)
		}
	})
	t.Run("bad_temperature", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"temperature":1`, `"temperature":"hot"`, 1)
		if _, err := ParseResponseResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected temperature error")
		}
	})
	t.Run("bad_max_tokens", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"max_output_tokens":null`, `"max_output_tokens":"x"`, 1)
		if _, err := ParseResponseResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected max_output_tokens error")
		}
	})
	t.Run("bad_error_object", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"error":null`, `"error":123`, 1)
		if _, err := ParseResponseResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected error object error")
		}
	})
	t.Run("bad_instructions", func(t *testing.T) {
		raw := strings.Replace(completeResponseJSON(""), `"instructions":null`, `"instructions":123`, 1)
		if _, err := ParseResponseResource([]byte(raw), DefaultParseOptions()); err == nil {
			t.Fatal("expected instructions error")
		}
	})
}

// TestResponse_TerminalDefault covers the non-terminal branch.
func TestResponse_TerminalDefault(t *testing.T) {
	t.Parallel()
	raw := strings.Replace(completeResponseJSON(""), `"status":"completed"`, `"status":"in_progress"`, 1)
	res, err := ParseResponseResource([]byte(raw), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Terminal() {
		t.Fatal("in_progress must not be terminal")
	}
}

// TestSSE_ScannerError covers the bounded line-scan failure path.
func TestSSE_ScannerError(t *testing.T) {
	t.Parallel()
	long := "event: response.created\ndata: " + strings.Repeat("a", 4096) + "\n\n"
	opts := DefaultParseOptions()
	opts.MaxLineBytes = 128
	if _, _, err := ParseSSE([]byte(long), opts); err == nil {
		t.Fatal("expected line-scan error")
	}
}

// TestWS_BinaryFrame covers binary frame event parsing.
func TestWS_BinaryFrame(t *testing.T) {
	t.Parallel()
	t.Run("binary_frame", func(t *testing.T) {
		srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
			t.Helper()
			_ = wsReadCreateEnvelope(t, conn)
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte(`{"type":"response.created","sequence_number":0}`))
			_ = conn.WriteMessage(websocket.BinaryMessage, []byte(`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_bin","object":"response","status":"completed"}}`))
			wsHold(conn)
		})
		sess := wsConn(t, srv)
		turn, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}
		if len(turn.Events) != 2 || turn.Response == nil || turn.Response.ID != "resp_bin" {
			t.Fatalf("binary turn: %+v", turn)
		}
	})
}

// TestDial_CustomDialer covers the custom dialer branch.
func TestDial_CustomDialer(t *testing.T) {
	t.Parallel()
	// A dialer pointed at an unusable address must fail cleanly.
	if _, err := Dial(context.Background(), WSDialOptions{
		BaseURL: "http://127.0.0.1:1",
		Dialer:  &websocket.Dialer{},
	}); err == nil {
		t.Fatal("expected dial failure")
	}
}

// TestDial_DoesNotMutateDialers guards against writing the handshake timeout
// onto websocket.DefaultDialer or a caller-supplied dialer. Concurrent sessions
// shared the same dialer, so any mutation is a data race (issue #262).
func TestDial_DoesNotMutateDialers(t *testing.T) {
	t.Parallel()

	// The default-dialer path is what racy sessions hit: wsConn dials with no
	// custom dialer, so the handshake timeout must never be written onto the
	// shared websocket.DefaultDialer.
	defaultBefore := websocket.DefaultDialer.HandshakeTimeout
	if _, err := Dial(context.Background(), WSDialOptions{BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("expected dial failure")
	}
	if got := websocket.DefaultDialer.HandshakeTimeout; got != defaultBefore {
		t.Fatalf("websocket.DefaultDialer.HandshakeTimeout mutated: got %v, want %v", got, defaultBefore)
	}

	// A caller-supplied dialer must keep its own configuration untouched.
	custom := &websocket.Dialer{HandshakeTimeout: 7 * time.Second}
	if _, err := Dial(context.Background(), WSDialOptions{
		BaseURL: "http://127.0.0.1:1",
		Dialer:  custom,
	}); err == nil {
		t.Fatal("expected dial failure")
	}
	if got := custom.HandshakeTimeout; got != 7*time.Second {
		t.Fatalf("caller-supplied dialer mutated: got %v, want 7s", got)
	}
}

// TestTool_UnmarshalBranches covers tool validation paths.
func TestTool_UnmarshalBranches(t *testing.T) {
	t.Parallel()
	var tool Tool
	if err := json.Unmarshal([]byte(`{"type":"acme:hosted","action":"search"}`), &tool); err != nil {
		t.Fatalf("extension tool: %v", err)
	}
	if tool.Opaque == nil || !strings.Contains(string(tool.Opaque), "acme:hosted") {
		t.Fatalf("extension tool opaque: %+v", tool)
	}
	if err := json.Unmarshal([]byte(`{"type":""}`), &tool); err == nil {
		t.Fatal("expected empty type error")
	}
	if err := json.Unmarshal([]byte(`{"type":"random_tool"}`), &tool); err == nil {
		t.Fatal("expected unknown unprefixed tool error")
	}
	if err := json.Unmarshal([]byte(`{"type":"function","strict":"x"}`), &tool); err == nil {
		t.Fatal("expected strict type error")
	}
	if err := json.Unmarshal([]byte(`{"type":"function","description":123}`), &tool); err == nil {
		t.Fatal("expected description type error")
	}
}

// TestTool_MarshalBranches covers tool marshal with description/strict.
func TestTool_MarshalBranches(t *testing.T) {
	t.Parallel()
	strict := true
	tool := Tool{Type: "function", Name: "f", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"strict":true`) || !strings.Contains(s, `"description":"d"`) || !strings.Contains(s, `"parameters"`) {
		t.Fatalf("tool marshal: %s", s)
	}
}

// TestRawHelpers_EmptyInput covers the empty-raw branches.
func TestRawHelpers_EmptyInput(t *testing.T) {
	t.Parallel()
	if v, err := rawInt64(nil, false); err != nil || v != 0 {
		t.Fatalf("rawInt64 empty: %d %v", v, err)
	}
	if v, err := rawBool(nil, false); err != nil || v {
		t.Fatalf("rawBool empty: %v %v", v, err)
	}
	if v, err := rawString(nil, false); err != nil || v != "" {
		t.Fatalf("rawString empty: %q %v", v, err)
	}
}

// TestItem_MalformedPhaseAndRef covers remaining item field errors.
func TestItem_MalformedPhaseAndRef(t *testing.T) {
	t.Parallel()
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"message","phase":123}`), &it); err == nil {
		t.Fatal("expected phase error")
	}
	if err := json.Unmarshal([]byte(`{"type":"item_reference","encapsulated_id":123}`), &it); err == nil {
		t.Fatal("expected encapsulated_id error")
	}
	if err := json.Unmarshal([]byte(`{"type":"message","content":[123]}`), &it); err == nil {
		t.Fatal("expected content part array error")
	}
}

// TestNewIDGenerator_NilClock covers the nil-clock fallback.
func TestNewIDGenerator_NilClock(t *testing.T) {
	t.Parallel()
	g := NewIDGenerator("x", nil)
	if got := g.Next(); got == "" {
		t.Fatal("nil clock must still produce ids")
	}
}

// TestCompactParams_MarshalBranches covers instructions and extensions.
func TestCompactParams_MarshalBranches(t *testing.T) {
	t.Parallel()
	ins := "be brief"
	p := CompactParams{
		Model:        "m",
		Input:        Input{Text: "compact"},
		Instructions: &ins,
		Extensions:   map[string]json.RawMessage{"acme:mode": json.RawMessage(`"fast"`)},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"instructions":"be brief"`) || !strings.Contains(s, `"acme:mode":"fast"`) {
		t.Fatalf("compact marshal: %s", s)
	}
}

// TestCreateStream_BoundedBody covers oversized non-streaming body rejection.
func TestCreateStream_BoundedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Repeat("event: response.created\n", 1) + "data: {\"type\":\"response.created\",\"x\":\"" + strings.Repeat("a", 4096) + "\"}\n\n"))
	}))
	t.Cleanup(srv.Close)
	opts := DefaultParseOptions()
	opts.MaxEventBytes = 128
	cli := New(Config{BaseURL: srv.URL, APIKey: "sk", ParseOptions: opts})
	if _, err := cli.CreateStream(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}, nil); err == nil {
		t.Fatal("expected bounded event error")
	}
}

// TestClient_CreateBoundedBody covers non-streaming oversized body rejection.
func TestClient_CreateBoundedBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	}))
	t.Cleanup(srv.Close)
	opts := DefaultParseOptions()
	opts.MaxBodyBytes = 128
	cli := New(Config{BaseURL: srv.URL, APIKey: "sk", ParseOptions: opts})
	if _, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}); err == nil {
		t.Fatal("expected bounded body error")
	}
}

// TestParseEvent_MalformedFields covers per-field event validation errors.
func TestParseEvent_MalformedFields(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, data string }{
		{"bad_item_id", `{"type":"response.content_part.added","item_id":123}`},
		{"bad_call_id", `{"type":"response.function_call_arguments.delta","call_id":123}`},
		{"bad_refusal", `{"type":"response.refusal.delta","refusal":123}`},
		{"bad_summary", `{"type":"response.reasoning_summary_text.delta","summary":123}`},
		{"bad_arguments", `{"type":"response.function_call_arguments.done","arguments":123}`},
		{"bad_delta", `{"type":"response.output_text.delta","delta":123}`},
		{"bad_text", `{"type":"response.output_text.done","text":123}`},
		{"bad_reasoning_delta", `{"type":"response.reasoning.delta","delta":123}`},
		{"bad_reasoning_done", `{"type":"response.reasoning.done","text":123}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseEvent([]byte(tc.data), DefaultParseOptions()); err == nil {
				t.Fatal("expected event field error")
			}
		})
	}
}

// TestParseEvent_ReasoningPinnedNames asserts the refclient parser accepts the
// pinned official reasoning event names with their required payloads and no
// longer classifies the legacy response.reasoning_text.* names as standard.
func TestParseEvent_ReasoningPinnedNames(t *testing.T) {
	t.Parallel()

	deltaPayload := `{"type":"response.reasoning.delta","sequence_number":5,"item_id":"rs_1","output_index":0,"content_index":0,"delta":"think"}`
	evt, err := ParseEvent([]byte(deltaPayload), DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseEvent(response.reasoning.delta) failed: %v", err)
	}
	if evt.Type != "response.reasoning.delta" {
		t.Fatalf("type = %q, want response.reasoning.delta", evt.Type)
	}
	if evt.SequenceNumber != 5 || evt.ItemID != "rs_1" || evt.Delta != "think" {
		t.Fatalf("delta event fields: seq=%d item_id=%q delta=%q", evt.SequenceNumber, evt.ItemID, evt.Delta)
	}
	if evt.OutputIndex == nil || *evt.OutputIndex != 0 {
		t.Fatalf("output_index = %v, want 0", evt.OutputIndex)
	}
	if evt.ContentIndex == nil || *evt.ContentIndex != 0 {
		t.Fatalf("content_index = %v, want 0", evt.ContentIndex)
	}

	donePayload := `{"type":"response.reasoning.done","sequence_number":6,"item_id":"rs_1","output_index":0,"content_index":0,"text":"think"}`
	evt, err = ParseEvent([]byte(donePayload), DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseEvent(response.reasoning.done) failed: %v", err)
	}
	if evt.Type != "response.reasoning.done" {
		t.Fatalf("type = %q, want response.reasoning.done", evt.Type)
	}
	if evt.SequenceNumber != 6 || evt.ItemID != "rs_1" || evt.Text != "think" {
		t.Fatalf("done event fields: seq=%d item_id=%q text=%q", evt.SequenceNumber, evt.ItemID, evt.Text)
	}
	if evt.OutputIndex == nil || *evt.OutputIndex != 0 {
		t.Fatalf("output_index = %v, want 0", evt.OutputIndex)
	}
	if evt.ContentIndex == nil || *evt.ContentIndex != 0 {
		t.Fatalf("content_index = %v, want 0", evt.ContentIndex)
	}

	// Legacy names are no longer standard: they are unprefixed and must be
	// rejected, otherwise conformance streams misparse as unknown events.
	for _, legacy := range []string{
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"think"}`,
		`{"type":"response.reasoning_text.done","item_id":"rs_1","output_index":0,"content_index":0,"text":"think"}`,
	} {
		if _, err := ParseEvent([]byte(legacy), DefaultParseOptions()); err == nil {
			t.Fatalf("legacy reasoning_text event accepted as standard: %s", legacy)
		}
	}
}

// TestContentPart_MalformedFields covers content part validation errors.
func TestContentPart_MalformedFields(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, data string }{
		{"bad_text", `{"type":"input_text","text":123}`},
		{"bad_refusal", `{"type":"refusal","refusal":123}`},
		{"bad_summary", `{"type":"summary_text","summary":123}`},
		{"missing_type", `{"text":"x"}`},
		{"unknown_unprefixed", `{"type":"weird_part"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var p ContentPart
			if err := json.Unmarshal([]byte(tc.data), &p); err == nil {
				t.Fatal("expected content part error")
			}
		})
	}
}

// TestRequestUnmarshal_MalformedControls covers request control validation errors.
func TestRequestUnmarshal_MalformedControls(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, data string }{
		{"bad_instructions", `{"instructions":123}`},
		{"bad_top_p", `{"top_p":"x"}`},
		{"bad_max_tool_calls", `{"max_tool_calls":"x"}`},
		{"bad_truncation", `{"truncation":1}`},
		{"bad_service_tier", `{"service_tier":1}`},
		{"bad_safety", `{"safety_identifier":1}`},
		{"bad_cache_retention", `{"prompt_cache_retention":1}`},
		{"bad_stream", `{"stream":"x"}`},
		{"bad_parallel", `{"parallel_tool_calls":"x"}`},
		{"bad_store", `{"store":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var p CreateParams
			if err := json.Unmarshal([]byte(tc.data), &p); err == nil {
				t.Fatal("expected control error")
			}
		})
	}
}

// TestParseResponseResource_FieldErrors covers response field type validation.
func TestParseResponseResource_FieldErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"bad_id", `{"id":1}`},
		{"bad_created_at", `{"created_at":"x"}`},
		{"bad_status", `{"status":1}`},
		{"bad_store", `{"store":"x"}`},
		{"bad_background", `{"background":"x"}`},
		{"bad_truncation", `{"truncation":1}`},
		{"bad_service_tier", `{"service_tier":1}`},
		{"bad_completed_at", `{"completed_at":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base := strings.Replace(completeResponseJSON(""), `"id":"r"`, `"id":"r"`, 1)
			// Inject the malformed field on top of a complete response.
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(base), &m); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.raw), &m); err != nil {
				t.Fatal(err)
			}
			merged, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseResponseResource(merged, DefaultParseOptions()); err == nil {
				t.Fatal("expected field error")
			}
		})
	}
}

// TestParseUsage_NonObjectDetails covers non-object token detail failures.
func TestParseUsage_NonObjectDetails(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":123}`,
		`{"input_tokens":0,"output_tokens":0,"total_tokens":0,"output_tokens_details":"x"}`,
	}
	for _, usage := range cases {
		if _, err := ParseResponseResource([]byte(completeResponseJSON(usage)), DefaultParseOptions()); err == nil {
			t.Fatal("expected usage detail error")
		}
	}
}

// TestSSE_OutputAfterDone covers post-[DONE] data rejection.
func TestSSE_OutputAfterDone(t *testing.T) {
	t.Parallel()
	body := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\ndata: [DONE]\n\ndata: {\"type\":\"response.output_item.added\"}\n\n"
	p := NewSSEParser(strings.NewReader(body), DefaultParseOptions())
	if _, err := p.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Next(); !errors.Is(err, ErrSSEDone) {
		t.Fatalf("expected ErrSSEDone, got %v", err)
	}
	if _, err := p.Next(); err == nil {
		t.Fatal("expected output-after-DONE error")
	}
}

// TestWSTurn_ServerAbort covers a mid-turn connection drop.
func TestWSTurn_ServerAbort(t *testing.T) {
	t.Parallel()
	srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
		t.Helper()
		_ = wsReadCreateEnvelope(t, conn)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","sequence_number":0}`))
		_ = conn.Close()
	})
	sess := wsConn(t, srv)
	if _, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}); err == nil {
		t.Fatal("expected read error after server abort")
	}
}
