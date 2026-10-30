package openaicodex

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

const maxBodyBytes = 10 << 20

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// WSFailureMode selects a one-shot WebSocket failure behavior for the emulator.
type WSFailureMode int

const (
	WSFailureNone WSFailureMode = iota
	// WSFailurePolicyCloseBeforeEvent closes immediately after upgrade before any event frame.
	WSFailurePolicyCloseBeforeEvent
	// WSFailureNormalCloseBeforeEvent sends CloseNormalClosure immediately after upgrade.
	WSFailureNormalCloseBeforeEvent
	// WSFailureNoCanonicalFirstFrame sends one mappable but non-canonical event, then closes normally.
	WSFailureNoCanonicalFirstFrame
	// WSFailureMalformedFirstFrame sends invalid JSON as the first event frame.
	WSFailureMalformedFirstFrame
	// WSFailureStall consumes response.create and never sends an event frame.
	WSFailureStall
	// WSFailureAfterFirstEvent sends the first canonical event frame, then drops the connection.
	WSFailureAfterFirstEvent
	// WSFailureStallAfterFirstEvent sends the first canonical event frame, then stalls.
	WSFailureStallAfterFirstEvent
)

// Config tunes the Codex Responses emulator.
type Config struct {
	Token            string
	OutputText       string
	PlanType         string
	UsagePercent     string
	ForcedHTTPStatus int
	ForcedRetryAfter string
	ForcedErrorJSON  string
	// ForcedWSFailure applies one one-shot WebSocket failure mode to the first upgrade.
	ForcedWSFailure WSFailureMode
	// ForcedWSRejectModel, when set, makes the WebSocket handler send a pre-content
	// error event frame and close when the response.create payload names this model.
	// Used to exercise reactive gpt-5.5 downgrade on the WebSocket path.
	ForcedWSRejectModel string
}

// CapturedRequest is a snapshot of the latest handled POST /responses request.
type CapturedRequest struct {
	Path             string
	Transport        string
	Authorization    string
	OpenAIBeta       string
	Originator       string
	CodexTaskType    string
	ConversationID   string
	SessionID        string
	ChatGPTAccountID string
	Body             map[string]any
}

// Server is a thread-safe Codex Responses emulator with request capture.
type Server struct {
	cfg Config

	mu                  sync.Mutex
	latest              CapturedRequest
	nextForcedStatus    int
	nextForcedRetry     string
	nextForcedErrorJSON string
	nextWSFailure       WSFailureMode
}

// New returns an emulator configured from cfg.
func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	if cfg.ForcedHTTPStatus != 0 {
		s.nextForcedStatus = cfg.ForcedHTTPStatus
		s.nextForcedRetry = cfg.ForcedRetryAfter
		s.nextForcedErrorJSON = cfg.ForcedErrorJSON
	}
	s.nextWSFailure = cfg.ForcedWSFailure
	return s
}

// ForceNextWSFailure applies mode to the next WebSocket upgrade.
func (s *Server) ForceNextWSFailure(mode WSFailureMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextWSFailure = mode
}

// Handler returns the emulator HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

// LatestRequest returns a copy of the most recently captured request.
func (s *Server) LatestRequest() CapturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.latest
	if s.latest.Body != nil {
		out.Body = maps.Clone(s.latest.Body)
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		s.serveWebSocket(w, r)
		return
	}
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
		http.NotFound(w, r)
		return
	}

	if s.cfg.Token != "" {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+s.cfg.Token {
			http.Error(w, "missing or invalid bearer", http.StatusUnauthorized)
			return
		}
	}

	if err := validateCodexHeaders(r); err != nil {
		http.Error(w, "invalid headers", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var payload map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}

	s.mu.Lock()
	s.latest = captureRequest(r, "https", payload)
	forcedStatus := s.nextForcedStatus
	forcedRetry := s.nextForcedRetry
	forcedJSON := s.nextForcedErrorJSON
	if forcedStatus != 0 {
		s.nextForcedStatus = 0
		s.nextForcedRetry = ""
		s.nextForcedErrorJSON = ""
	}
	s.mu.Unlock()

	if forcedStatus != 0 {
		writeForcedError(w, forcedStatus, forcedRetry, forcedJSON)
		return
	}

	if s.cfg.PlanType != "" {
		w.Header().Set("x-codex-plan-type", s.cfg.PlanType)
	}
	if s.cfg.UsagePercent != "" {
		w.Header().Set("x-codex-primary-used-percent", s.cfg.UsagePercent)
	}

	writeStream(w, s.outputText())
}

func (s *Server) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Token != "" {
		if r.Header.Get("Authorization") != "Bearer "+s.cfg.Token {
			http.Error(w, "missing or invalid bearer", http.StatusUnauthorized)
			return
		}
	}
	if err := validateCodexHeaders(r); err != nil {
		http.Error(w, "invalid headers", http.StatusBadRequest)
		return
	}
	respHeader := http.Header{}
	if s.cfg.PlanType != "" {
		respHeader.Set("x-codex-plan-type", s.cfg.PlanType)
	}
	if s.cfg.UsagePercent != "" {
		respHeader.Set("x-codex-primary-used-percent", s.cfg.UsagePercent)
	}
	conn, err := wsUpgrader.Upgrade(w, r, respHeader)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	s.mu.Lock()
	forcedWSFailure := s.nextWSFailure
	s.nextWSFailure = WSFailureNone
	s.mu.Unlock()
	switch forcedWSFailure {
	case WSFailurePolicyCloseBeforeEvent:
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "forced ws fail"))
		return
	case WSFailureNormalCloseBeforeEvent:
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		return
	}

	_, frame, err := conn.ReadMessage()
	if err != nil {
		return
	}
	var payload map[string]any
	if len(frame) > 0 {
		_ = json.Unmarshal(frame, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if rejectModel := strings.TrimSpace(s.cfg.ForcedWSRejectModel); rejectModel != "" {
		if model, _ := payload["model"].(string); model == rejectModel {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(
				`{"type":"error","error":{"message":"gpt-5.5 is not available on free plan"}}`,
			))
			return
		}
	}
	s.mu.Lock()
	s.latest = captureRequest(r, "websocket", payload)
	s.mu.Unlock()

	switch forcedWSFailure {
	case WSFailureStall:
		// Upgrade succeeded and the request frame was consumed, but no event is
		// ever sent. Block until the client abandons the read and closes, which
		// is how a server that upgrades but never produces a first event looks.
		_, _, _ = conn.ReadMessage()
		return
	case WSFailureNoCanonicalFirstFrame:
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.unknown_ack"}`))
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		return
	case WSFailureMalformedFirstFrame:
		_ = conn.WriteMessage(websocket.TextMessage, []byte("{not json"))
		return
	case WSFailureAfterFirstEvent:
		// Send only the first canonical event, then drop mid-stream.
		_ = conn.WriteMessage(websocket.TextMessage, []byte(codexEventFrames(s.outputText())[0]))
		return
	case WSFailureStallAfterFirstEvent:
		_ = conn.WriteMessage(websocket.TextMessage, []byte(codexEventFrames(s.outputText())[0]))
		_, _, _ = conn.ReadMessage()
		return
	}

	for _, raw := range codexEventFrames(s.outputText()) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
			return
		}
	}
	_ = conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}

func codexEventFrames(text string) []string {
	created := `{"type":"response.created","sequence_number":0,"response":{"id":"resp_codex_ref","object":"response","created_at":1715620000,"status":"in_progress","model":"gpt-5.3-codex-spark"}}`
	delta := fmt.Sprintf(`{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_codex_ref","output_index":0,"content_index":0,"delta":%q}`, text)
	completed := fmt.Sprintf(`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_codex_ref","object":"response","created_at":1715620000,"status":"completed","model":"gpt-5.3-codex-spark","output":[{"type":"message","id":"msg_codex_ref","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}`, text)
	return []string{created, delta, completed}
}

func validateCodexHeaders(r *http.Request) error {
	required := []struct {
		name string
		val  string
	}{
		{"OpenAI-Beta", r.Header.Get("OpenAI-Beta")},
		{"originator", r.Header.Get("originator")},
		{"Codex-Task-Type", r.Header.Get("Codex-Task-Type")},
		{"conversation_id", r.Header.Get("conversation_id")},
		{"session_id", r.Header.Get("session_id")},
	}
	for _, h := range required {
		if strings.TrimSpace(h.val) == "" {
			return fmt.Errorf("missing header %s", h.name)
		}
	}
	return nil
}

func captureRequest(r *http.Request, transport string, body map[string]any) CapturedRequest {
	return CapturedRequest{
		Path:             r.URL.Path,
		Transport:        transport,
		Authorization:    r.Header.Get("Authorization"),
		OpenAIBeta:       r.Header.Get("OpenAI-Beta"),
		Originator:       r.Header.Get("originator"),
		CodexTaskType:    r.Header.Get("Codex-Task-Type"),
		ConversationID:   r.Header.Get("conversation_id"),
		SessionID:        r.Header.Get("session_id"),
		ChatGPTAccountID: r.Header.Get("chatgpt-account-id"),
		Body:             maps.Clone(body),
	}
}

func (s *Server) outputText() string {
	if s.cfg.OutputText != "" {
		return s.cfg.OutputText
	}
	return "ok"
}

func writeForcedError(w http.ResponseWriter, status int, retryAfter, body string) {
	if retryAfter != "" && status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == "" {
		body = defaultForcedErrorJSON(status)
	}
	_, _ = io.WriteString(w, body)
}

func defaultForcedErrorJSON(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return `{"error":{"message":"incorrect api key","type":"invalid_request_error","code":"invalid_api_key"}}`
	case http.StatusTooManyRequests:
		return `{"error":{"message":"rate limit exceeded","type":"requests","code":"rate_limit_exceeded"}}`
	default:
		return `{"error":{"message":"error","type":"invalid_request_error"}}`
	}
}

func writeStream(w http.ResponseWriter, text string) {
	frames := codexEventFrames(text)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for _, raw := range frames {
		_, _ = io.WriteString(w, "event: "+sseEventNameFromFrame(raw)+"\n")
		_, _ = io.WriteString(w, "data: "+raw+"\n\n")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// sseEventNameFromFrame extracts the Codex Responses event "type" from a raw
// JSON frame so the SSE event name always matches the payload instead of
// relying on a parallel hardcoded list that can drift from codexEventFrames.
func sseEventNameFromFrame(raw string) string {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || probe.Type == "" {
		return "response.created"
	}
	return probe.Type
}
