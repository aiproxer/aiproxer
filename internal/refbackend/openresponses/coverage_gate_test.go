package openresponses

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file closes the reachable branch coverage of the independent emulator
// (Requirement 12.13/12.14 style quality gate) so the package stays above the
// >=90% statement target for new deterministic emulator packages.

func TestVirtualClock_Deterministic(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	c := NewClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("now: %v", c.Now())
	}
	c.Advance(5 * time.Minute)
	if !c.Now().Equal(start.Add(5 * time.Minute)) {
		t.Fatalf("advanced: %v", c.Now())
	}
	c.Set(start)
	if !c.Now().Equal(start) {
		t.Fatalf("set: %v", c.Now())
	}
}

func TestResource_WithErrorAndClockAccessor(t *testing.T) {
	t.Parallel()
	res := NewResource("r", "m", 1, nil).WithError("failed", &ErrorObject{Type: "invalid_request", Code: "x", Message: "m"})
	if res.Status != "failed" || res.Error == nil || res.Error.Code != "x" {
		t.Fatalf("with error: %+v", res)
	}
	srv := NewServer(Options{})
	if srv.Clock() == nil {
		t.Fatal("clock accessor must be non-nil")
	}
}

func TestServer_ExplicitSSESteps(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-steps", Description: "explicit steps", Mode: ModeSSE,
		Expected: ExpectedRequest{Model: "m"},
		SSE: []WireStep{
			{Type: "response.created", Sequence: 0, Data: []byte(`{"response":{"id":"r","status":"in_progress","model":"m"}}`)},
			{Type: "response.completed", Sequence: 1, Data: []byte(`{"response":{"id":"r","status":"completed","model":"m"}}`)},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	if !strings.Contains(s, `"response":{"id":"r"`) || !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("explicit steps stream: %s", s)
	}
}

func TestWire_MarshalReasoningAndTool(t *testing.T) {
	t.Parallel()
	// Reasoning item marshal with encrypted content presence.
	r := Item{
		Type: "reasoning", ID: "rs_1", Status: "completed",
		Reasoning: &ReasoningItem{
			Content:             []ContentPart{{Type: "output_text", Text: "t"}},
			Summary:             []ContentPart{{Type: "summary_text", Text: "s"}},
			EncryptedContentSet: true,
			EncryptedContent:    "cipher",
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"encrypted_content":"cipher"`) {
		t.Fatalf("reasoning marshal: %s", b)
	}

	// Tool marshal with strict and extension opaque.
	strict := true
	tool := Tool{Type: "function", Name: "f", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}
	b, err = json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"strict":true`) {
		t.Fatalf("tool marshal: %s", b)
	}
	ext := Tool{Type: "acme:hosted", Opaque: json.RawMessage(`{"type":"acme:hosted","action":"x"}`)}
	b, err = json.Marshal(ext)
	if err != nil || !strings.Contains(string(b), "acme:hosted") {
		t.Fatalf("extension tool marshal: %s %v", b, err)
	}

	// Content part marshal: extension opaque, image/file/video/annotations/logprobs.
	parts := []ContentPart{
		{Type: "acme:part", Opaque: json.RawMessage(`{"type":"acme:part"}`)},
		{Type: "input_image", ImageURL: json.RawMessage(`{"url":"u"}`)},
		{Type: "input_file", FileURL: json.RawMessage(`{"file_id":"f"}`)},
		{Type: "input_video", VideoURL: json.RawMessage(`{"url":"v"}`)},
		{Type: "output_text", Text: "o", Annotations: json.RawMessage(`[]`), Logprobs: json.RawMessage(`[]`)},
	}
	for _, p := range parts {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal part %s: %v", p.Type, err)
		}
		var back ContentPart
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal part %s: %v", p.Type, err)
		}
		if back.Type != p.Type {
			t.Fatalf("part type: %q", back.Type)
		}
	}
}

func TestWire_ReasoningUnmarshalBranches(t *testing.T) {
	t.Parallel()
	var r ReasoningItem
	if err := json.Unmarshal([]byte(`{"content":[{"type":"output_text","text":"c"}],"summary":[{"type":"summary_text","text":"s"}],"encrypted_content":null}`), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Content) != 1 || len(r.Summary) != 1 || !r.EncryptedContentSet {
		t.Fatalf("reasoning: %+v", r)
	}
	if err := json.Unmarshal([]byte(`{"encrypted_content":123}`), &r); err == nil {
		t.Fatal("expected encrypted_content type error")
	}
	var bad ReasoningItem
	if err := json.Unmarshal([]byte(`{"content":123}`), &bad); err == nil {
		t.Fatal("expected content type error")
	}
}

func TestWire_ToolUnmarshalBranches(t *testing.T) {
	t.Parallel()
	var tool Tool
	if err := json.Unmarshal([]byte(`{"type":"function","name":"f","description":"d","parameters":{"type":"object"},"strict":true}`), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Strict == nil || !*tool.Strict || tool.Parameters == nil {
		t.Fatalf("tool: %+v", tool)
	}
	if err := json.Unmarshal([]byte(`{"type":"acme:hosted","action":"search"}`), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Opaque == nil {
		t.Fatal("extension tool opaque missing")
	}
	if err := json.Unmarshal([]byte(`{"type":"function","strict":"x"}`), &tool); err == nil {
		t.Fatal("expected strict type error")
	}
	if err := json.Unmarshal([]byte(`{"type":""}`), &tool); err == nil {
		t.Fatal("expected empty type error")
	}
}

func TestWire_ItemMoreBranches(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"type":"function_call","id":"fc","call_id":"c","name":"f","arguments":"{}","output":"x"}`,
		`{"type":"item_reference","id":"ref","encapsulated_id":"resp_1"}`,
		`{"type":"compaction","id":"cmp"}`,
		`{"type":"message","role":"assistant","reasoning":{"content":[{"type":"output_text","text":"t"}]}}`,
		`{"type":"message","content":"just text"}`,
		`{"type":"acme:raw"}`,
		`{"type":"acme:raw","opaque":{"k":1}}`,
	}
	for _, raw := range cases {
		var it Item
		if err := it.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if it.Type == "" {
			t.Fatalf("empty type for %s", raw)
		}
		_, _ = json.Marshal(it)
	}
	// Unknown unprefixed item rejected; missing type rejected.
	var it Item
	if err := it.UnmarshalJSON([]byte(`{"role":"user"}`)); err == nil {
		t.Fatal("expected missing-type error")
	}
	// function_call output non-null preserved.
	if err := it.UnmarshalJSON([]byte(`{"type":"function_call_output","output":{"text":"x"}}`)); err != nil {
		t.Fatal(err)
	}
	if it.Output == nil {
		t.Fatal("output must be preserved")
	}
}

func TestWire_ContentPartErrorBranches(t *testing.T) {
	t.Parallel()
	var p ContentPart
	if err := json.Unmarshal([]byte(`{"text":"x"}`), &p); err == nil {
		t.Fatal("expected missing-type error")
	}
	if err := json.Unmarshal([]byte(`{"type":123}`), &p); err == nil {
		t.Fatal("expected bad-type error")
	}
	if err := json.Unmarshal([]byte(`{"type":"bogus"}`), &p); err == nil {
		t.Fatal("expected unknown unprefixed error")
	}
}

func TestWS_FrameTypeErrorAndBinary(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-frame", Description: "frame types", Mode: ModeWebSocket,
		Expected: ExpectedRequest{Model: "m"},
		Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
	})
	conn := dialWS(t, ts, "sk-test")
	// Binary frames are accepted as turn envelopes.
	_ = conn.WriteMessage(2, []byte(`{"type":"response.create","model":"m","input":"hi"}`))
	types, _ := wsReadTurn(t, conn)
	if types[len(types)-1] != "response.completed" {
		t.Fatalf("binary turn: %v", types)
	}
	if srv.Capture().Total() != 1 {
		t.Fatalf("turn count: %d", srv.Capture().Total())
	}
}

func TestWS_MidTurnDisconnectWriteError(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-ws-close2", Description: "ws mid-turn close", Mode: ModeWebSocket,
		Delay:    DelayPlan{BetweenEvents: 20 * time.Millisecond},
		Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart(strings.Repeat("x", 256<<10)))}),
	})
	conn := dialWS(t, ts, "sk-test")
	_ = conn.WriteMessage(1, []byte(`{"type":"response.create","model":"m","input":"hi"}`))
	// Read one event then close abruptly so the server's next write fails.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, _ = conn.ReadMessage()
	_ = conn.Close()
	if !eventually(t, 2*time.Second, func() bool { return srv.WriteErrorCount() >= 1 || srv.CancelCount() >= 1 }) {
		t.Fatalf("server did not observe ws abort (writeErr=%d cancel=%d)", srv.WriteErrorCount(), srv.CancelCount())
	}
}

func TestServer_CompactRawBodyAndMalformedFallback(t *testing.T) {
	t.Parallel()
	fixture := readRefClientFixture(t, "scenarios/compact_resource.json")
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-craw", Description: "compact raw body", Mode: ModeCompact,
		Expected: ExpectedRequest{Model: "gpt-openresponses-1", MinInputItems: 1},
		RawBody:  fixture,
	})
	resp, raw := postJSON(t, ts.URL+"/responses/compact", `{"model":"gpt-openresponses-1","input":[{"type":"message","role":"user","content":"x"}]}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "response.compaction") {
		t.Fatalf("compact raw: %d %s", resp.StatusCode, raw)
	}

	// Malformed content-type with compact falls back to the compact resource.
	_, ts2 := startServer(t, Options{}, &Script{
		ID: "scenario-cct", Description: "compact wrong ct", Mode: ModeCompact,
		Expected:        ExpectedRequest{Model: "m", MinInputItems: 1},
		CompactResource: NewCompactResource("c", "m", 1, nil),
		Malformed:       MalformedContentType,
	})
	resp2, _ := postJSON(t, ts2.URL+"/responses/compact", `{"model":"m","input":[{"type":"message","role":"user","content":"x"}]}`)
	if resp2.StatusCode != http.StatusOK || !strings.Contains(resp2.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("compact malformed ct: %d %q", resp2.StatusCode, resp2.Header.Get("Content-Type"))
	}
}

func TestServer_JSONRawBody(t *testing.T) {
	t.Parallel()
	fixture := readRefClientFixture(t, "scenarios/response_text.json")
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-jraw", Description: "json raw body", Mode: ModeJSON,
		Expected: ExpectedRequest{Model: "gpt-4o-2024-06-13", MinInputItems: 1},
		RawBody:  fixture,
	})
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"gpt-4o-2024-06-13","input":[{"type":"message","role":"user","content":"x"}]}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "resp_5a3e04d550c84a63a1d4fc4e3e206abb") {
		t.Fatalf("json raw: %d %s", resp.StatusCode, raw)
	}
}

func TestCommonChecks_MoreBranches(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]}`)
	req := fakeRequest("POST", "/responses", "application/json", "")

	// AuthNone fails when a bearer is present.
	exp := ExpectedRequest{Auth: AuthNone}
	if fails := commonChecks(exp, fakeRequest("POST", "/responses", "application/json", "Bearer sk"), raw); len(fails) != 1 {
		t.Fatalf("authnone: %v", fails)
	}
	// Method mismatch and path mismatch.
	exp2 := ExpectedRequest{Method: "GET", PathSuffix: "/responses/compact"}
	if fails := commonChecks(exp2, req, raw); len(fails) != 2 {
		t.Fatalf("method/path: %v", fails)
	}
	// MustOmit hit.
	exp3 := ExpectedRequest{MustOmit: []string{"model"}}
	if fails := commonChecks(exp3, req, raw); len(fails) != 1 {
		t.Fatalf("mustomit: %v", fails)
	}
}

func TestCheckExpected_StreamAndLimits(t *testing.T) {
	t.Parallel()
	stream := true
	raw := []byte(`{"model":"m","stream":true,"input":[{"type":"message","role":"user","content":"hi"}]}`)
	req, err := parseCreateRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	bad := ExpectedRequest{Stream: &stream, MaxInputItems: 0, RequireTools: 1}
	streamFalse := false
	badStream := bad
	badStream.Stream = &streamFalse
	if fails := checkExpected(badStream, fakeRequest("POST", "/responses", "application/json", "Bearer x"), req, raw); len(fails) != 2 {
		t.Fatalf("stream+tools: %v", fails)
	}
	// compact with stream assertion is validated at script level.
	cr := &CompactRequest{Model: "m", Input: &InputValue{Items: []Item{NewMessageItem("user", "input_text", "x")}}}
	if fails := checkCompactExpected(ExpectedRequest{Model: "m", MaxInputItems: 0}, fakeRequest("POST", "/compact", "application/json", "Bearer x"), cr, raw); len(fails) != 0 {
		t.Fatalf("compact ok: %v", fails)
	}
	if fails := checkCompactExpected(ExpectedRequest{MinInputItems: 5}, fakeRequest("POST", "/compact", "application/json", "Bearer x"), cr, raw); len(fails) != 1 {
		t.Fatalf("compact min: %v", fails)
	}
}

func TestReadBounded_AndHijackFallback(t *testing.T) {
	t.Parallel()
	if _, err := readBounded(&failingReader{}, 16); err == nil {
		t.Fatal("expected read error")
	}
	// Non-hijacker response writer: hijackAndClose must no-op.
	hijackAndClose(httptest.NewRecorder())
}

func TestSSEWriter_ErrorBranches(t *testing.T) {
	t.Parallel()
	failing := &failingWriter{}
	sw := &sseWriter{w: failing}
	if err := sw.writeEvent(StreamEvent{Type: "x", Seq: 0, Fields: map[string]any{"k": 1}}, "x"); err == nil {
		t.Fatal("expected write error")
	}
	if err := sw.writeDone(); err == nil {
		t.Fatal("expected writeDone error")
	}
}

func TestRequestParser_MoreFields(t *testing.T) {
	t.Parallel()
	raw := `{"model":"m","input":"x","tool_choice":{"type":"function","name":"f"},"text":{"format":{"type":"text"}},"reasoning":{"effort":"medium"},"metadata":{"org":"t"},"background":false,"prompt_cache_retention":"5m","safety_identifier":"s","truncation":"auto","instructions":null}`
	req, err := parseCreateRequest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolChoice == nil || req.Text == nil || req.Reasoning == nil || req.Metadata == nil || req.Background == nil || req.PromptCacheRetention != "5m" || req.SafetyIdentifier != "s" || req.Truncation != "auto" || req.Instructions != nil {
		t.Fatalf("fields: %+v", req)
	}
	// Compact with instructions null tolerated.
	craw := `{"model":"m","input":"x","instructions":null,"acme:mode":1}`
	creq, err := parseCompactRequest([]byte(craw))
	if err != nil {
		t.Fatal(err)
	}
	if creq.Instructions != nil || creq.Extensions["acme:mode"] == nil {
		t.Fatalf("compact: %+v", creq)
	}
	// Extension top-level on create.
	raw2 := `{"model":"m","input":"x","acme:routing":{"region":"eu"}}`
	req2, err := parseCreateRequest([]byte(raw2))
	if err != nil || req2.Extensions["acme:routing"] == nil {
		t.Fatalf("extensions: %v", err)
	}
}

func TestNewCompactionItem_Branches(t *testing.T) {
	t.Parallel()
	withRaw := NewCompactionItem("cmp_1", `{"id":"cmp_1","k":1}`)
	if !strings.Contains(string(withRaw.Opaque), `"type":"compaction"`) || !strings.Contains(string(withRaw.Opaque), `"k":1`) {
		t.Fatalf("compaction with raw: %s", withRaw.Opaque)
	}
	badRaw := NewCompactionItem("cmp_2", `not-json`)
	if !strings.Contains(string(badRaw.Opaque), `"type":"compaction"`) {
		t.Fatalf("compaction bad raw: %s", badRaw.Opaque)
	}
}

// TestNewCompactionItem_NullRawKeepsDefaultOpaque covers JSON null, which
// unmarshals into a nil map without error: the merge must not write into it.
func TestNewCompactionItem_NullRawKeepsDefaultOpaque(t *testing.T) {
	t.Parallel()
	item := NewCompactionItem("cmp_null", `null`)
	if !strings.Contains(string(item.Opaque), `"type":"compaction"`) || !strings.Contains(string(item.Opaque), `"id":"cmp_null"`) {
		t.Fatalf("compaction null raw opaque = %s", item.Opaque)
	}
}

func TestNewExtensionItem_Branches(t *testing.T) {
	t.Parallel()
	noRaw := NewExtensionItem("acme:empty", "")
	if !strings.Contains(string(noRaw.Opaque), `"type":"acme:empty"`) {
		t.Fatalf("extension empty: %s", noRaw.Opaque)
	}
	badRaw := NewExtensionItem("acme:bad", "not-json")
	if string(badRaw.Opaque) != "not-json" {
		t.Fatalf("extension bad raw: %s", badRaw.Opaque)
	}
}

// TestNewExtensionItem_NullRawPreserved covers JSON null, which unmarshals
// into a nil map without error: the discriminator merge must not write into it.
func TestNewExtensionItem_NullRawPreserved(t *testing.T) {
	t.Parallel()
	item := NewExtensionItem("acme:null", `null`)
	if string(item.Opaque) != "null" {
		t.Fatalf("extension null raw opaque = %s", item.Opaque)
	}
	if item.Type != "acme:null" {
		t.Fatalf("extension null raw type = %q", item.Type)
	}
}

func TestErrorObject_FromStepDefaults(t *testing.T) {
	t.Parallel()
	eo := errorObjectFromStep(&ErrorStep{Code: "c", Message: "m"})
	if eo.Type != "invalid_request" {
		t.Fatalf("default type: %q", eo.Type)
	}
	if errorStatus(&ErrorStep{}) != http.StatusBadRequest {
		t.Fatal("default error status must be 400")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

var (
	_ io.Reader = failingReader{}
	_ io.Writer = failingWriter{}
)
