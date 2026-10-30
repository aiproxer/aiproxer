package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestClock_Deterministic exercises the virtual clock and ID generator.
func TestClock_Deterministic(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	clock := NewClock(start)
	if !clock.Now().Equal(start) {
		t.Fatalf("now: %v", clock.Now())
	}
	clock.Advance(5 * time.Minute)
	want := start.Add(5 * time.Minute)
	if !clock.Now().Equal(want) {
		t.Fatalf("advanced: %v", clock.Now())
	}
	clock.Set(start)
	if !clock.Now().Equal(start) {
		t.Fatalf("set: %v", clock.Now())
	}

	gen := NewIDGenerator("req", clock)
	a := gen.Next()
	b := gen.Next()
	if a == b {
		t.Fatal("ids must differ")
	}
	if !strings.HasPrefix(a, "req_") {
		t.Fatalf("id prefix: %q", a)
	}
}

// TestClient_AccessorsAndHTTPError covers client bookkeeping and HTTP error mapping.
func TestClient_AccessorsAndHTTPError(t *testing.T) {
	t.Parallel()
	transport := RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","code":"internal_error","message":"boom","param":""}}`)),
			Request:    r,
		}, nil
	})
	cli := New(Config{BaseURL: "http://example.invalid", APIKey: "sk", HTTPClient: &http.Client{Transport: transport}})
	_, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T", err)
	}
	if !strings.Contains(httpErr.Error(), "500") || !strings.Contains(httpErr.Error(), "server_error") {
		t.Fatalf("HTTPError.Error(): %q", httpErr.Error())
	}
	if httpErr.ErrorObject == nil || httpErr.ErrorObject.Code != "internal_error" {
		t.Fatalf("error object: %+v", httpErr.ErrorObject)
	}
	if cli.LastStatusCode() != http.StatusInternalServerError {
		t.Fatalf("last status: %d", cli.LastStatusCode())
	}
	if cli.LastError() == nil {
		t.Fatal("last error must be set")
	}
	if cli.RequestCount() != 1 {
		t.Fatalf("request count: %d", cli.RequestCount())
	}
	if cli.LastRequest().Method != http.MethodPost {
		t.Fatalf("last request: %+v", cli.LastRequest())
	}
}

// TestCreateParams_FullUnmarshal covers every parsed request field.
func TestCreateParams_FullUnmarshal(t *testing.T) {
	t.Parallel()
	raw := `{
		"model": "m",
		"input": [{"type":"message","role":"user","content":"hi"}],
		"instructions": "be brief",
		"tools": [{"type":"function","name":"f","parameters":{"type":"object"}}],
		"tool_choice": "auto",
		"parallel_tool_calls": true,
		"temperature": 0.7,
		"top_p": 0.9,
		"max_output_tokens": 100,
		"max_tool_calls": 3,
		"truncation": "disabled",
		"text": {"format":{"type":"text"}},
		"reasoning": {"effort":"medium"},
		"store": true,
		"background": false,
		"previous_response_id": "resp_1",
		"metadata": {"org":"t"},
		"service_tier": "standard",
		"safety_identifier": "safe",
		"prompt_cache_key": "k",
		"prompt_cache_retention": "5m",
		"stream": true,
		"acme:routing": {"region":"eu"}
	}`
	var p CreateParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Model != "m" || len(p.Input.Items) != 1 || p.Instructions == nil || *p.Instructions != "be brief" {
		t.Fatalf("basic fields: %+v", p)
	}
	if len(p.Tools) != 1 || p.ParallelToolCalls == nil || !*p.ParallelToolCalls {
		t.Fatalf("tools/parallel: %+v", p)
	}
	if p.Temperature == nil || *p.Temperature != 0.7 || p.TopP == nil || *p.TopP != 0.9 {
		t.Fatalf("sampling: %+v", p)
	}
	if p.MaxOutputTokens == nil || *p.MaxOutputTokens != 100 || p.MaxToolCalls == nil || *p.MaxToolCalls != 3 {
		t.Fatalf("limits: %+v", p)
	}
	if p.Store == nil || !*p.Store || p.Background != nil && *p.Background {
		t.Fatalf("store/background: %+v", p)
	}
	if p.PreviousResponseID == nil || *p.PreviousResponseID != "resp_1" {
		t.Fatalf("previous: %+v", p.PreviousResponseID)
	}
	if p.ServiceTier != "standard" || p.SafetyIdentifier != "safe" || p.PromptCacheKey != "k" || p.PromptCacheRetention != "5m" {
		t.Fatalf("tier/cache: %+v", p)
	}
	if !p.Stream {
		t.Fatal("stream must be true")
	}
	if len(p.Extensions) != 1 || p.Extensions["acme:routing"] == nil {
		t.Fatalf("extensions: %+v", p.Extensions)
	}

	// Round trip preserves everything.
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"acme:routing"`) || !strings.Contains(string(b), `"reasoning"`) {
		t.Fatalf("round trip: %s", b)
	}
}

// TestContentPart_MarshalVariants covers marshal of every content part form.
func TestContentPart_MarshalVariants(t *testing.T) {
	t.Parallel()
	parts := []ContentPart{
		{Type: "input_text", Text: "t"},
		{Type: "refusal", Refusal: "no"},
		{Type: "summary_text", Summary: "s"},
		{Type: "input_image", ImageURL: json.RawMessage(`{"data":"x"}`)},
		{Type: "input_file", FileURL: json.RawMessage(`{"file_id":"f"}`)},
		{Type: "input_video", VideoURL: json.RawMessage(`{"url":"v"}`)},
		{Type: "output_text", Text: "o", Annotations: json.RawMessage(`[{"type":"url_citation"}]`), Logprobs: json.RawMessage(`[]`)},
		{Type: "acme:part", Opaque: json.RawMessage(`{"type":"acme:part","k":1}`)},
	}
	for _, p := range parts {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal %s: %v", p.Type, err)
		}
		var back ContentPart
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", p.Type, err)
		}
		if back.Type != p.Type {
			t.Fatalf("type: got %q want %q", back.Type, p.Type)
		}
	}
	if parts[7].Opaque == nil {
		t.Fatal("extension part opaque missing")
	}
}

// TestReasoningItem_RoundTrip covers reasoning item marshal/unmarshal.
func TestReasoningItem_RoundTrip(t *testing.T) {
	t.Parallel()
	raw := `{"type":"reasoning","id":"rs_1","status":"completed","summary":[{"type":"summary_text","text":"sum"}],"content":[{"type":"output_text","text":"trace"}],"encrypted_content":null}`
	var it Item
	if err := json.Unmarshal([]byte(raw), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Reasoning == nil {
		t.Fatal("reasoning missing")
	}
	if len(it.Reasoning.Summary) != 1 || it.Reasoning.Summary[0].Text != "sum" {
		t.Fatalf("summary: %+v", it.Reasoning.Summary)
	}
	if len(it.Reasoning.Content) != 1 || it.Reasoning.Content[0].Text != "trace" {
		t.Fatalf("content: %+v", it.Reasoning.Content)
	}
	if !it.Reasoning.EncryptedContentSet {
		t.Fatal("encrypted_content presence must be tracked")
	}

	b, err := json.Marshal(it.Reasoning)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"encrypted_content":null`) {
		t.Fatalf("marshal reasoning: %s", b)
	}
}

// TestResponseResource_OptionalPresence covers optional fields and null sampling.
func TestResponseResource_OptionalPresence(t *testing.T) {
	t.Parallel()
	raw := `{
		"id":"r1","object":"response","created_at":1,"status":"completed","completed_at":5,
		"model":"m","output":[],"parallel_tool_calls":false,"reasoning":null,"store":true,
		"background":false,"temperature":null,"text":{},"tool_choice":"auto","tools":[],
		"top_p":null,"truncation":"disabled",
		"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3},
		"metadata":{},"service_tier":"default","max_output_tokens":null,"max_tool_calls":null,
		"instructions":null,"previous_response_id":null,"error":null,"incomplete_details":null,
		"safety_identifier":"sid","prompt_cache_key":"pck","prompt_cache_retention":"10m"
	}`
	res, err := ParseResponseResource([]byte(raw), DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseResponseResource: %v", err)
	}
	if res.CompletedAt == nil || *res.CompletedAt != 5 {
		t.Fatalf("completed_at: %v", res.CompletedAt)
	}
	if res.Temperature != nil || res.TopP != nil {
		t.Fatalf("null sampling: %+v", res)
	}
	if res.SafetyIdentifier == nil || *res.SafetyIdentifier != "sid" {
		t.Fatalf("safety_identifier: %v", res.SafetyIdentifier)
	}
	if res.PromptCacheKey == nil || *res.PromptCacheKey != "pck" {
		t.Fatalf("prompt_cache_key: %v", res.PromptCacheKey)
	}
	if res.PromptCacheRetention == nil || *res.PromptCacheRetention != "10m" {
		t.Fatalf("prompt_cache_retention: %v", res.PromptCacheRetention)
	}
	if !res.Terminal() {
		t.Fatal("completed must be terminal")
	}
}

// TestParseResponseResourceLoose_NoPresence ensures loose parsing skips presence.
func TestParseResponseResourceLoose_NoPresence(t *testing.T) {
	t.Parallel()
	res, err := ParseResponseResourceLoose([]byte(`{"id":"partial","status":"in_progress"}`), DefaultParseOptions())
	if err != nil {
		t.Fatalf("loose parse: %v", err)
	}
	if res.ID != "partial" || res.Status != "in_progress" {
		t.Fatalf("loose: %+v", res)
	}
}

// TestEvent_ParseVariants covers error, extension, and lifecycle event branches.
func TestEvent_ParseVariants(t *testing.T) {
	t.Parallel()

	evt, err := ParseEvent([]byte(`{"type":"error","error":{"code":"x","type":"invalid_request","message":"m","param":"p"}}`), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !evt.IsError() || evt.Error == nil || evt.Error.Code != "x" {
		t.Fatalf("error event: %+v", evt)
	}

	ext, err := ParseEvent([]byte(`{"type":"acme:trace_event","sequence_number":2,"phase":"tool_resolution"}`), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if ext.Opaque == nil || !strings.Contains(string(ext.Opaque), "acme:trace_event") {
		t.Fatalf("extension event: %+v", ext)
	}

	args, err := ParseEvent([]byte(`{"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"arguments":"{\"a\":1}"}`), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if args.Arguments != `{"a":1}` {
		t.Fatalf("arguments: %q", args.Arguments)
	}

	ref, err := ParseEvent([]byte(`{"type":"response.refusal.delta","item_id":"m1","delta":"I can't"}`), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if ref.Delta != "I can't" {
		t.Fatalf("refusal delta: %q", ref.Delta)
	}

	failed, err := ParseEvent([]byte(`{"type":"response.failed","response":{"id":"r","status":"failed","object":"response"}}`), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !failed.IsTerminal() || failed.Response == nil || failed.Response.Status != "failed" {
		t.Fatalf("failed event: %+v", failed)
	}
}

// TestParse_MalformedHelpers exercises low-level parser error paths.
func TestParse_MalformedHelpers(t *testing.T) {
	t.Parallel()
	if _, err := rawString(json.RawMessage(`123`), true); err == nil {
		t.Fatal("rawString should reject non-string")
	}
	if _, err := rawInt64(json.RawMessage(`"x"`), false); err == nil {
		t.Fatal("rawInt64 should reject non-int")
	}
	if _, err := rawBool(json.RawMessage(`"x"`), false); err == nil {
		t.Fatal("rawBool should reject non-bool")
	}
	if got := truncate(json.RawMessage(`{"k":"` + strings.Repeat("a", 200) + `"}`)); len(got) > 90 {
		t.Fatalf("truncate should bound: %d", len(got))
	}
	if _, err := readBounded(strings.NewReader(strings.Repeat("x", 100)), 10); err == nil {
		t.Fatal("readBounded should reject oversized body")
	}
	if err := (&ParseError{Category: "x", Message: "m", Err: ErrMalformed}).Unwrap(); err != ErrMalformed {
		t.Fatalf("unwrap: %v", err)
	}
}

// TestItem_ExtensionEdgeCases covers OpaqueItem nil and marshal without opaque.
func TestItem_ExtensionEdgeCases(t *testing.T) {
	t.Parallel()
	msg := NewMessageItem("user", "input_text", "hi")
	if msg.OpaqueItem() != nil {
		t.Fatal("OpaqueItem must be nil for standard items")
	}
	ext := NewCustomItem("acme:raw", "")
	if ext.OpaqueItem() == nil {
		t.Fatal("custom item must preserve raw bytes")
	}
	var it Item
	if err := json.Unmarshal([]byte(`{"type":"acme:only_type"}`), &it); err != nil {
		t.Fatalf("unmarshal type-only extension: %v", err)
	}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"acme:only_type"`) {
		t.Fatalf("marshal type-only extension: %s", b)
	}
}

// TestWSTurn_ErrorPaths covers malformed frames, [DONE], and oversized frames.
func TestWSTurn_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("malformed_frame", func(t *testing.T) {
		srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
			t.Helper()
			t.Helper()
			t.Helper()
			_ = wsReadCreateEnvelope(t, conn)
			wsWrite(t, conn, map[string]any{"type": "response.output_item.added", "sequence_number": 1, "item": "not-an-object"})
		})
		sess := wsConn(t, srv)
		_, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
		if err == nil {
			t.Fatal("expected parse error for malformed item")
		}
	})

	t.Run("done_frame_ends_turn", func(t *testing.T) {
		srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
			t.Helper()
			_ = wsReadCreateEnvelope(t, conn)
			wsWrite(t, conn, map[string]any{"type": "response.created", "sequence_number": 0})
			_ = conn.WriteMessage(websocket.TextMessage, []byte("[DONE]"))
			wsHold(conn)
		})
		sess := wsConn(t, srv)
		turn, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
		if err != nil {
			t.Fatalf("Turn: %v", err)
		}
		if len(turn.Events) != 1 {
			t.Fatalf("events: %d", len(turn.Events))
		}
	})

	t.Run("oversized_frame", func(t *testing.T) {
		srv := wsTestServer(t, func(t *testing.T, conn *websocket.Conn) {
			t.Helper()
			_ = wsReadCreateEnvelope(t, conn)
			big := `{"type":"response.created","sequence_number":0,"x":"` + strings.Repeat("a", 2048) + `"}`
			_ = conn.WriteMessage(websocket.TextMessage, []byte(big))
		})
		sess, err := Dial(context.Background(), WSDialOptions{BaseURL: srv.URL, APIKey: "sk", ParseOptions: ParseOptions{MaxEventBytes: 256}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		if _, err := sess.Turn(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}); err == nil {
			t.Fatal("expected oversized-frame error")
		}
	})

	t.Run("dial_bad_url", func(t *testing.T) {
		if _, err := Dial(context.Background(), WSDialOptions{BaseURL: "not a url://"}); err == nil {
			t.Fatal("expected dial error for bad base URL")
		}
		if _, err := Dial(context.Background(), WSDialOptions{BaseURL: "http://127.0.0.1:1/x"}); err == nil {
			t.Fatal("expected dial error for unreachable host")
		}
	})
}

// TestSSE_CommentAndIDLines ensures comment/id lines are tolerated.
func TestSSE_CommentAndIDLines(t *testing.T) {
	t.Parallel()
	body := ": ping keepalive\nid: 5\nevent: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\ndata: [DONE]\n\n"
	events, done, err := ParseSSE([]byte(body), DefaultParseOptions())
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if !done || len(events) != 2 {
		t.Fatalf("done=%v events=%d", done, len(events))
	}
}

// TestSSEParser_DoneAccessor covers the Done() method.
func TestSSEParser_DoneAccessor(t *testing.T) {
	t.Parallel()
	body := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\ndata: [DONE]\n\n"
	p := NewSSEParser(strings.NewReader(body), DefaultParseOptions())
	if _, err := p.Next(); err != nil {
		t.Fatal(err)
	}
	if p.Done() {
		t.Fatal("not done yet")
	}
	if _, err := p.Next(); !errors.Is(err, ErrSSEDone) {
		t.Fatalf("expected ErrSSEDone, got %v", err)
	}
	if !p.Done() {
		t.Fatal("done must be true")
	}
}

// TestClient_StreamingContentTypeRejected asserts non-event-stream content types fail.
func TestClient_StreamingContentTypeRejected(t *testing.T) {
	t.Parallel()
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	cli := newTestClient(t, srv.URL)
	if _, err := cli.CreateStream(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}, nil); err == nil {
		t.Fatal("expected content-type rejection")
	}
}

// TestMarshalCreateEnvelope covers the WS turn envelope builder.
func TestMarshalCreateEnvelope(t *testing.T) {
	t.Parallel()
	b, err := marshalCreateEnvelope(CreateParams{Model: "m", Input: Input{Text: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["type"]) != `"response.create"` {
		t.Fatalf("envelope type: %s", m["type"])
	}
}
