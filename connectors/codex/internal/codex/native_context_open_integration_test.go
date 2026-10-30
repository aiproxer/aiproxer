package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/gorilla/websocket"
)

type nativeOpenRecord struct {
	Path          string
	Authorization string
	Body          []byte
	WebSocket     bool
}

type nativeOpenEmulator struct {
	mu                   sync.Mutex
	records              []nativeOpenRecord
	compactions          int
	compactionTailChecks []string
	server               *httptest.Server
}

func newNativeOpenEmulator(t *testing.T) *nativeOpenEmulator {
	t.Helper()
	e := &nativeOpenEmulator{compactionTailChecks: []string{"LIVE_TAIL_SECOND", "LIVE_TAIL_THIRD"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			e.handleWS(w, r)
			return
		}
		// The streamed trigger path is deliberately invalid for native compaction.
		if r.Method == http.MethodPost && requestIsCompactionBody(r) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_request_error","type":"invalid_request_error"}}`)
			return
		}
		e.handleHTTP(w, r)
	})
	mux.HandleFunc("/responses/compact", func(w http.ResponseWriter, r *http.Request) {
		e.handleCompactionHTTP(w, r)
	})
	e.server = httptest.NewServer(mux)
	t.Cleanup(e.server.Close)
	return e
}

func (e *nativeOpenEmulator) handleCompactionHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	e.mu.Lock()
	compactionIndex := e.compactions
	e.records = append(e.records, nativeOpenRecord{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: append([]byte(nil), body...)})
	e.mu.Unlock()
	var payload struct {
		Model              string           `json:"model"`
		Input              []map[string]any `json:"input"`
		Instructions       string           `json:"instructions"`
		Store              *bool            `json:"store"`
		Reasoning          json.RawMessage  `json:"reasoning"`
		Include            []string         `json:"include"`
		PromptCacheKey     string           `json:"prompt_cache_key"`
		PreviousResponseID string           `json:"previous_response_id"`
	}
	currentTailIncluded := compactionIndex < len(e.compactionTailChecks) && strings.Contains(string(body), e.compactionTailChecks[compactionIndex])
	if json.Unmarshal(body, &payload) != nil || payload.Model == "" || payload.Instructions == "" || len(payload.Input) == 0 || payload.Store != nil || len(payload.Include) != 0 || payload.PromptCacheKey != "" || payload.PreviousResponseID != "" || currentTailIncluded || payload.Input[len(payload.Input)-1]["type"] == "compaction" {
		// This branch is retained only for malformed legacy trigger payloads.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_request_error","type":"invalid_request_error"}}`)
		return
	}
	e.mu.Lock()
	e.compactions++
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	// This mirrors the authoritative unary resource: output is a list with one
	// compaction item, not the retained prompt history.
	_, _ = io.WriteString(w, `{"id":"compact-response","object":"response.compaction","created_at":1710000000,"output":[{"type":"message","id":"retained-message","role":"user","status":"completed","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction_summary","id":"cmp-1","encrypted_content":"compact-state"}],"usage":{"input_tokens":21,"output_tokens":4,"total_tokens":25}}`)
}

func requestIsCompactionBody(r *http.Request) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	return requestIsCompaction(body)
}

func (e *nativeOpenEmulator) handleHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	e.mu.Lock()
	e.records = append(e.records, nativeOpenRecord{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: append([]byte(nil), body...)})
	e.mu.Unlock()
	if r.Header.Get("Authorization") == "Bearer account-a" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	e.writeSSE(w, body)
}

func (e *nativeOpenEmulator) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "Bearer account-a" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, body, err := conn.ReadMessage()
	if err != nil {
		return
	}
	e.mu.Lock()
	e.records = append(e.records, nativeOpenRecord{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: append([]byte(nil), body...), WebSocket: true})
	if requestIsCompaction(body) {
		e.compactions++
	}
	e.mu.Unlock()
	for _, frame := range responseFrames(body) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			return
		}
	}
}

func (e *nativeOpenEmulator) writeSSE(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	e.mu.Lock()
	if requestIsCompaction(body) {
		e.compactions++
	}
	e.mu.Unlock()
	for _, frame := range responseFrames(body) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (e *nativeOpenEmulator) compactionCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compactions
}

func (e *nativeOpenEmulator) recordsSnapshot() []nativeOpenRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]nativeOpenRecord, len(e.records))
	copy(out, e.records)
	return out
}

func responseFrames(body []byte) []string {
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	_ = json.Unmarshal(body, &payload)
	isCompaction := len(payload.Input) > 0 && payload.Input[len(payload.Input)-1]["type"] == "compaction"
	if isCompaction {
		item := `{"type":"compaction_summary","id":"cmp-1","encrypted_content":"compact-state"}`
		return []string{
			`{"type":"response.created","response":{"id":"compact-response"}}`,
			`{"type":"response.output_item.done","item":` + item + `}`,
			`{"type":"response.completed","response":{"id":"compact-response","status":"completed","output":[` + item + `],"usage":{"input_tokens":21,"output_tokens":4,"total_tokens":25}}}`,
		}
	}
	return []string{
		`{"type":"response.created","response":{"id":"normal-response"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"reasoning-1","summary":[],"encrypted_content":"normal-reasoning","status":"completed"}}`,
		`{"type":"response.output_text.delta","delta":"deterministic-ok"}`,
		`{"type":"response.completed","response":{"id":"normal-response","status":"completed","output":[{"type":"reasoning","id":"reasoning-1","summary":[],"encrypted_content":"normal-reasoning","status":"completed"},{"type":"message","id":"message-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"deterministic-ok"}]}],"usage":{"input_tokens":31,"output_tokens":5,"total_tokens":36}}}`,
	}
}

func requestIsCompaction(body []byte) bool {
	var payload struct {
		Input []map[string]any `json:"input"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Input) == 0 {
		return false
	}
	return payload.Input[len(payload.Input)-1]["type"] == "compaction"
}

func nativeScenarioConfig(baseURL, transport string) Config {
	return Config{
		BaseURL:               baseURL,
		AccessToken:           "scenario-token",
		Transport:             transport,
		ExperimentalWebSocket: transport == TransportWebSocket,
		NativeContext: &NativeContextConfig{
			Enabled: true, RequestEncryptedReasoning: true, ReasoningContinuity: ContinuityRequired,
			Compaction: NativeCompactionConfig{
				// The fixture deliberately crosses 256 before compaction, while the
				// compact replacement plus the next tiny tail remains below it.
				Enabled: true, TriggerTokens: 256, RetainedMessageTokens: 1, MinSavingsTokens: 1,
				StateTTL: timeHour, MaxEntries: 8, MaxEntryBytes: 1 << 20, FailureCooldown: timeMinute,
			},
		},
	}
}

// Fixed durations keep the scenario independent of host locale/time settings.
const (
	timeHour   = 3600 * 1e9
	timeMinute = 60 * 1e9
)

func nativeScenarioCall(session string, messages []aipapi.Message) aipapi.Call {
	return aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: session},
		Extensions: map[string]json.RawMessage{
			// This is the proxy-injected marker at the connector boundary, not a
			// client hint; reasoning-preservation tests cover client spoof removal.
			nativeContinuityMarkerKey: json.RawMessage(nativeContinuityMarkerValue),
		},
		Messages: messages,
	}
}

func nativeText(role aipapi.Role, text string) aipapi.Message {
	return aipapi.Message{Role: role, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}}}
}

func nativeReasoningMessage(opaque string) aipapi.Message {
	return aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
		Kind:      aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: []byte(opaque)},
	}}}
}

func nativeActionMessages(reasoning string, live string) []aipapi.Message {
	return []aipapi.Message{
		nativeText(aipapi.RoleUser, strings.Repeat("older context ", 72)),
		nativeReasoningMessage(reasoning),
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"q\":\"fixed\"}"}`)}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "call-1", Content: json.RawMessage(`{"value":"fixed-result"}`)}}},
		nativeReasoningMessage(`{"type":"reasoning","id":"reasoning-2","summary":[],"encrypted_content":"post-action-reasoning","status":"completed"}`),
		nativeText(aipapi.RoleUser, live),
	}
}

func collectNativeEvents(t *testing.T, engine *Engine, call aipapi.Call, model string) []aipapi.Event {
	t.Helper()
	stream, err := engine.Open(context.Background(), &call, routingstub.AttemptCandidate{Primary: routingstub.Primary{Model: model}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
}

func assertNormalReasoning(t *testing.T, events []aipapi.Event) {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == aipapi.EventReasoningPart && ev.Reasoning != nil && strings.Contains(string(ev.Reasoning.Opaque), "normal-reasoning") {
			return
		}
	}
	t.Fatalf("normal encrypted reasoning event was not surfaced: %#v", events)
}

func assertBodyContains(t *testing.T, body []byte, values ...string) {
	t.Helper()
	text := string(body)
	for _, value := range values {
		if !strings.Contains(text, value) {
			t.Fatalf("request body omitted %q: %s", value, text)
		}
	}
}

func runNativeOpenScenario(t *testing.T, transport string) {
	t.Helper()
	emu := newNativeOpenEmulator(t)
	engine, err := New(nativeScenarioConfig(emu.server.URL, transport))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	model := "gpt-5.3-codex-spark"
	first := nativeScenarioCall("session-open", []aipapi.Message{nativeText(aipapi.RoleUser, "first request")})
	firstEvents := collectNativeEvents(t, engine, first, model)
	assertNormalReasoning(t, firstEvents)
	second := nativeScenarioCall("session-open", nativeActionMessages(`{"type":"reasoning","id":"reasoning-1","summary":[],"encrypted_content":"normal-reasoning","status":"completed"}`, "LIVE_TAIL_SECOND"))
	second.Tools = []aipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
	secondEvents := collectNativeEvents(t, engine, second, model)
	assertNormalReasoning(t, secondEvents)
	thirdMessages := append(nativeActionMessages(`{"type":"reasoning","id":"reasoning-1","summary":[],"encrypted_content":"normal-reasoning","status":"completed"}`, "LIVE_TAIL_SECOND"), nativeText(aipapi.RoleUser, "LIVE_TAIL_THIRD"))
	thirdCall := nativeScenarioCall("session-open", thirdMessages)
	thirdCall.Tools = []aipapi.ToolDef{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
	thirdEvents := collectNativeEvents(t, engine, thirdCall, model)
	assertNormalReasoning(t, thirdEvents)

	records := emu.recordsSnapshot()
	if len(records) != 4 {
		t.Fatalf("request count=%d, want first normal + compaction + second normal + checkpoint reuse", len(records))
	}
	compactionSeen := false
	for _, record := range records {
		if record.Path == "/responses/compact" {
			compactionSeen = true
			assertBodyContains(t, record.Body, "normal-reasoning", "function_call", "function_call_output", "call-1")
		}
		if strings.Contains(string(record.Body), nativeContinuityMarkerKey) {
			t.Fatal("internal continuity marker entered provider payload")
		}
	}
	if !compactionSeen || emu.compactionCount() != 1 {
		t.Fatalf("no dedicated compaction request was observed: count=%d records=%+v", emu.compactionCount(), records)
	}
	var rewrittenNormal bool
	for _, record := range records {
		if record.Path == "/responses" && strings.Contains(string(record.Body), `"type":"compaction_summary"`) {
			rewrittenNormal = true
			break
		}
	}
	if !rewrittenNormal {
		t.Fatalf("normal request did not install the completed compaction item: %+v", records)
	}
	assertBodyContains(t, records[len(records)-1].Body, "LIVE_TAIL_THIRD")
}

func TestNativeContextConnectorOpen_HTTPFullScenario(t *testing.T) {
	t.Parallel()
	runNativeOpenScenario(t, TransportHTTPS)
}

func TestNativeContextConnectorOpen_WebSocketFullScenario(t *testing.T) {
	t.Parallel()
	runNativeOpenScenario(t, TransportWebSocket)
}

func TestNativeContextConnectorOpen_DisabledHasZeroExtraRequests(t *testing.T) {
	t.Parallel()
	emu := newNativeOpenEmulator(t)
	engine, err := New(Config{BaseURL: emu.server.URL, AccessToken: "scenario-token", Transport: TransportHTTPS})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Close() }()
	call := nativeScenarioCall("disabled-session", []aipapi.Message{nativeText(aipapi.RoleUser, "disabled")})
	_ = collectNativeEvents(t, engine, call, "gpt-5.3-codex-spark")
	if got := len(emu.recordsSnapshot()); got != 1 {
		t.Fatalf("disabled request count=%d, want 1", got)
	}
}

func TestNativeContextConnectorOpen_ManagedAccountRotationNoLeakage(t *testing.T) {
	t.Parallel()
	emu := newNativeOpenEmulator(t)
	dir := t.TempDir()
	for name, account := range map[string][2]string{"a.json": {"account-a", "account-a"}, "b.json": {"account-b", "account-b"}} {
		body, _ := json.Marshal(map[string]string{"account_id": account[0], "access_token": account[1]})
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	engine, err := New(Config{
		BaseURL: emu.server.URL, ManagedOAuthEnabled: true, ManagedOAuthStoragePath: dir,
		Transport: TransportHTTPS, ManagedOAuthSelectionStrategy: "round-robin",
		NativeContext: &NativeContextConfig{
			Enabled: true, RequestEncryptedReasoning: true, ReasoningContinuity: ContinuityRequired,
			Compaction: NativeCompactionConfig{Enabled: true, TriggerTokens: 2, RetainedMessageTokens: 1, MinSavingsTokens: 1, StateTTL: timeHour, MaxEntries: 8, MaxEntryBytes: 1 << 20, FailureCooldown: timeMinute},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Close() }()
	call := nativeScenarioCall("managed-session", []aipapi.Message{
		nativeText(aipapi.RoleUser, strings.Repeat("managed older context ", 12)),
		nativeText(aipapi.RoleUser, "managed live"),
	})
	_ = collectNativeEvents(t, engine, call, "gpt-5.3-codex-spark")
	records := emu.recordsSnapshot()
	if len(records) != 4 || records[0].Authorization != "Bearer account-a" || records[1].Authorization != "Bearer account-a" || records[2].Authorization != "Bearer account-b" || records[3].Authorization != "Bearer account-b" {
		t.Fatalf("managed rotation records=%+v", records)
	}
	for _, record := range records {
		if strings.Contains(string(record.Body), "account-a") || strings.Contains(string(record.Body), "account-b") {
			t.Fatalf("account credential leaked into provider payload")
		}
	}
}
