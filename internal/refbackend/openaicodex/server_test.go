package openaicodex_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/openaicodex"
	gorillawebsocket "github.com/gorilla/websocket"
)

const streamBody = `{"model":"gpt-5.3-codex-spark","stream":true,"input":[{"type":"message","role":"user","content":"hi"}]}`

func setCodexHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "aip-test")
	req.Header.Set("Codex-Task-Type", "code")
	req.Header.Set("conversation_id", "conv-1")
	req.Header.Set("session_id", "sess-1")
}

func TestServer_happyPath_streamsSSEAndCapturesRequest(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:        "sk-codex",
		OutputText:   "codex-ok",
		PlanType:     "pro",
		UsagePercent: "42",
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/backend-api/codex/responses", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	setCodexHeaders(req, "sk-codex")
	req.Header.Set("chatgpt-account-id", "acct-9")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("x-codex-plan-type"); got != "pro" {
		t.Fatalf("x-codex-plan-type: %q", got)
	}
	if got := resp.Header.Get("x-codex-primary-used-percent"); got != "42" {
		t.Fatalf("x-codex-primary-used-percent: %q", got)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type: %q", ct)
	}

	events := readSSEEvents(t, resp.Body)
	if !containsEventType(events, "response.created") {
		t.Fatalf("missing response.created in %v", events)
	}
	if !containsEventDelta(events, "codex-ok") {
		t.Fatalf("missing output_text.delta in %v", events)
	}
	if !containsEventType(events, "response.completed") {
		t.Fatalf("missing response.completed in %v", events)
	}
	if !containsSubstring(events, `"usage"`) {
		t.Fatalf("missing usage in %v", events)
	}

	got := srv.LatestRequest()
	if got.Path != "/backend-api/codex/responses" {
		t.Fatalf("path: %q", got.Path)
	}
	if got.Authorization != "Bearer sk-codex" {
		t.Fatalf("authorization: %q", got.Authorization)
	}
	if got.OpenAIBeta != "responses=experimental" || got.Originator != "aip-test" {
		t.Fatalf("codex headers: beta=%q originator=%q", got.OpenAIBeta, got.Originator)
	}
	if got.CodexTaskType != "code" || got.ConversationID != "conv-1" || got.SessionID != "sess-1" {
		t.Fatalf("ids: task=%q conv=%q sess=%q", got.CodexTaskType, got.ConversationID, got.SessionID)
	}
	if got.ChatGPTAccountID != "acct-9" {
		t.Fatalf("chatgpt-account-id: %q", got.ChatGPTAccountID)
	}
	model, ok := got.Body["model"].(string)
	if !ok || model != "gpt-5.3-codex-spark" {
		t.Fatalf("body model: %#v", got.Body["model"])
	}
}

func TestServer_responsesPathAlias(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	setCodexHeaders(req, "sk-codex")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := srv.LatestRequest().Path; got != "/responses" {
		t.Fatalf("path: %q", got)
	}
}

func TestServer_missingAuthorization_401WhenTokenRequired(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/backend-api/codex/responses", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	setCodexHeaders(req, "sk-codex")
	req.Header.Del("Authorization")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestServer_missingCodexHeaders_400(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/backend-api/codex/responses", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-codex")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "aip-test")
	req.Header.Set("conversation_id", "conv-1")
	req.Header.Set("session_id", "sess-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "invalid headers" {
		t.Fatalf("body: %q, want %q", got, "invalid headers")
	}
}

func TestServer_forced429_includesRetryAfter(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{
		Token:            "sk-codex",
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "17",
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/backend-api/codex/responses", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	setCodexHeaders(req, "sk-codex")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "17" {
		t.Fatalf("Retry-After: %q", got)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "rate_limit") {
		t.Fatalf("body: %s", b)
	}

	req2, err := http.NewRequest(http.MethodPost, ts.URL+"/backend-api/codex/responses", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	setCodexHeaders(req2, "sk-codex")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second status: %d", resp2.StatusCode)
	}
}

func readSSEEvents(t *testing.T, r io.Reader) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			out = append(out, data)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func containsEventType(events []string, typ string) bool {
	needle := `"type":"` + typ + `"`
	for _, e := range events {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

func containsEventDelta(events []string, text string) bool {
	for _, e := range events {
		if strings.Contains(e, `"type":"response.output_text.delta"`) && strings.Contains(e, `"delta":"`+text+`"`) {
			return true
		}
	}
	return false
}

func containsSubstring(events []string, sub string) bool {
	for _, e := range events {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestServer_webSocketUpgrade_streamsEventFramesAndCaptures(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", OutputText: "ws-ok"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	dialer := gorillawebsocket.Dialer{HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer sk-codex")
	hdr.Set("OpenAI-Beta", "responses=experimental")
	hdr.Set("originator", "aip-test")
	hdr.Set("Codex-Task-Type", "code")
	hdr.Set("conversation_id", "conv-ws")
	hdr.Set("session_id", "sess-ws")

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/backend-api/codex/responses"
	conn, resp, err := dialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer func() { _ = conn.Close() }()

	frame := `{"type":"response.create","model":"gpt-5.3-codex-spark","store":false,"input":[{"type":"message","role":"user","content":"hi"}]}`
	if err := conn.WriteMessage(gorillawebsocket.TextMessage, []byte(frame)); err != nil {
		t.Fatalf("write: %v", err)
	}

	var types []string
	for i := range 3 {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, rerr := conn.ReadMessage()
		if rerr != nil {
			t.Fatalf("read[%d]: %v", i, rerr)
		}
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("decode[%d]: %v: %s", i, err, data)
		}
		types = append(types, ev.Type)
	}
	if !slices.Contains(types, "response.created") || !slices.Contains(types, "response.completed") {
		t.Fatalf("event types: %v", types)
	}

	got := srv.LatestRequest()
	if got.Transport != "websocket" {
		t.Fatalf("captured transport = %q, want websocket", got.Transport)
	}
	if got.ConversationID != "conv-ws" {
		t.Fatalf("conversation id: %q", got.ConversationID)
	}
	if m, _ := got.Body["model"].(string); m != "gpt-5.3-codex-spark" {
		t.Fatalf("frame model: %#v", got.Body["model"])
	}
	if _, hasStream := got.Body["stream"]; hasStream {
		t.Fatalf("WS frame must not carry stream field: %#v", got.Body)
	}
}

func TestServer_webSocketForcedFail_closesBeforeEvents(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex", ForcedWSFailure: refbackend.WSFailurePolicyCloseBeforeEvent})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	dialer := gorillawebsocket.Dialer{HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer sk-codex")
	hdr.Set("OpenAI-Beta", "responses=experimental")
	hdr.Set("originator", "aip-test")
	hdr.Set("Codex-Task-Type", "code")
	hdr.Set("conversation_id", "conv-ws")
	hdr.Set("session_id", "sess-ws")

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/backend-api/codex/responses"
	conn, _, err := dialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = conn.WriteMessage(gorillawebsocket.TextMessage, []byte(`{"type":"response.create"}`))
	if _, _, rerr := conn.ReadMessage(); rerr == nil {
		t.Fatal("expected read failure before first event")
	}
}

func TestServer_webSocketUpgrade_missingCodexHeaders_400(t *testing.T) {
	t.Parallel()
	srv := refbackend.New(refbackend.Config{Token: "sk-codex"})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	dialer := gorillawebsocket.Dialer{HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer sk-codex")
	hdr.Set("OpenAI-Beta", "responses=experimental")
	hdr.Set("originator", "aip-test")
	hdr.Set("conversation_id", "conv-ws")
	hdr.Set("session_id", "sess-ws")

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/backend-api/codex/responses"
	_, resp, err := dialer.Dial(wsURL, hdr)
	if err == nil {
		t.Fatal("expected dial to fail due to missing headers")
	}
	if resp == nil {
		t.Fatal("expected HTTP response from failed upgrade")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != "invalid headers" {
		t.Fatalf("body: %q, want %q", got, "invalid headers")
	}
}
