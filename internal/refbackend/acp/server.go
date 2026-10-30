package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/jsonpresence"
)

const maxBodyBytes = 10 << 20

// Config tunes the emulator handler.
type Config struct {
	// OnRequestBody is invoked with the raw HTTP body after a successful POST /v1/acp
	// route match and before dispatch.
	OnRequestBody func(body []byte)
	// OnPromptUpdate is called after prompt-turn progress notifications are written.
	OnPromptUpdate func(stage string, sessionID string)
	// PromptUpdateDelay is slept between streaming session/update notifications during
	// session/prompt so tests can interleave session/cancel. Zero means no delay.
	PromptUpdateDelay time.Duration
}

// NewHandler returns an http.Handler that emulates the ACP subset described in package doc.
func NewHandler(cfg Config) http.Handler {
	h := &handler{cfg: cfg, sessions: make(map[string]*emuSession)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/acp" {
			http.NotFound(w, r)
			return
		}
		h.serve(w, r)
	})
}

type handler struct {
	cfg Config

	mu            sync.Mutex
	initialized   bool
	nextSessionID int
	sessions      map[string]*emuSession
}

type emuSession struct {
	mu         sync.Mutex
	cancelled  bool // set by session/cancel; cleared at start of each session/prompt
	cancelCh   chan struct{}
	cancelOnce sync.Once
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if h.cfg.OnRequestBody != nil {
		h.cfg.OnRequestBody(body)
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeRPCError(w, req.ID, -32700, "parse error")
		return
	}

	// JSON-RPC notifications omit "id" (or use null in broken clients). session/cancel is always notification-shaped.
	isNotification := len(bytes.TrimSpace(req.ID)) == 0 || jsonpresence.IsJSONNull(req.ID) || req.Method == "session/cancel"

	if isNotification {
		switch req.Method {
		case "session/cancel":
			if err := h.handleCancel(w, req.Params); err != nil {
				slog.Default().LogAttrs(r.Context(), slog.LevelError, "acp session/cancel failed",
					slog.Any("error", err))
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			// Notifications other than session/cancel are ignored in this subset emulator.
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	switch req.Method {
	case "initialize":
		h.handleInitialize(w, req)
	case "authenticate":
		h.handleAuthenticate(w, req)
	case "session/new":
		h.handleSessionNew(w, req)
	case "session/load":
		h.handleSessionLoad(w, req)
	case "session/prompt":
		h.handleSessionPrompt(w, r, req)
	default:
		writeRPCError(w, req.ID, -32601, "method not found")
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (h *handler) handleInitialize(w http.ResponseWriter, req rpcRequest) {
	h.mu.Lock()
	h.initialized = true
	h.mu.Unlock()

	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(req.ID),
		"result": map[string]any{
			"protocolVersion": 1,
			"agentCapabilities": map[string]any{
				"loadSession": false,
				"promptCapabilities": map[string]any{
					"image":             true,
					"audio":             false,
					"embeddedContext":   true,
					"resource":          true,
					"resourceReference": true,
				},
			},
			"agentInfo": map[string]any{
				"name":    "refbackend-acp",
				"title":   "ACP reference emulator",
				"version": "0.0.1",
			},
			"authMethods": []any{},
		},
	}
	writeJSON(w, res)
}

func (h *handler) handleAuthenticate(w http.ResponseWriter, req rpcRequest) {
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(req.ID),
		"result":  map[string]any{},
	}
	writeJSON(w, res)
}

func (h *handler) handleSessionNew(w http.ResponseWriter, req rpcRequest) {
	if !h.isInitialized() {
		writeRPCError(w, req.ID, -32002, "not initialized")
		return
	}
	var p struct {
		Cwd        string `json:"cwd"`
		McpServers []any  `json:"mcpServers"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeRPCError(w, req.ID, -32602, "invalid params")
		return
	}

	h.mu.Lock()
	h.nextSessionID++
	sid := "sess_ref_" + strconv.Itoa(h.nextSessionID)
	h.sessions[sid] = &emuSession{}
	h.mu.Unlock()

	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(req.ID),
		"result": map[string]any{
			"sessionId": sid,
		},
	}
	writeJSON(w, res)
}

func (h *handler) handleSessionLoad(w http.ResponseWriter, req rpcRequest) {
	writeRPCError(w, req.ID, -32601, "session/load not supported (loadSession is false)")
}

func (h *handler) handleSessionPrompt(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	if !h.isInitialized() {
		writeRPCError(w, req.ID, -32002, "not initialized")
		return
	}

	var p struct {
		SessionID string           `json:"sessionId"`
		Prompt    []map[string]any `json:"prompt"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.SessionID == "" {
		writeRPCError(w, req.ID, -32602, "invalid params")
		return
	}

	sess := h.getSession(p.SessionID)
	if sess == nil {
		writeRPCError(w, req.ID, -32602, "unknown session")
		return
	}

	sess.resetForPrompt()

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	var fl http.Flusher
	if f, ok := w.(http.Flusher); ok {
		fl = f
	}

	resourceEcho := extractResourceEcho(p.Prompt)

	// Progress: plan update (ACP prompt-turn example shape).
	plan := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": p.SessionID,
			"update": map[string]any{
				"sessionUpdate": "plan",
				"entries": []any{
					map[string]any{"content": "refbackend tick", "priority": "low", "status": "in_progress"},
				},
			},
		},
	}
	if !writeNDJSONLine(w, fl, plan) {
		return
	}
	if h.cfg.OnPromptUpdate != nil {
		h.cfg.OnPromptUpdate("plan", p.SessionID)
	}

	if d := h.cfg.PromptUpdateDelay; d > 0 {
		if !sleepOrDone(r.Context(), d, sess) {
			h.writePromptCancelled(w, fl, req.ID, p.SessionID)
			return
		}
	}

	if sess.isCancelled() {
		h.writePromptCancelled(w, fl, req.ID, p.SessionID)
		return
	}

	chunkText := "ok"
	if resourceEcho != "" {
		chunkText = "saw resource: " + resourceEcho
	}
	chunk := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": p.SessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": chunkText,
				},
			},
		},
	}
	if !writeNDJSONLine(w, fl, chunk) {
		return
	}
	if h.cfg.OnPromptUpdate != nil {
		h.cfg.OnPromptUpdate("chunk", p.SessionID)
	}

	if d := h.cfg.PromptUpdateDelay; d > 0 {
		if !sleepOrDone(r.Context(), d, sess) {
			h.writePromptCancelled(w, fl, req.ID, p.SessionID)
			return
		}
	}

	if sess.isCancelled() {
		h.writePromptCancelled(w, fl, req.ID, p.SessionID)
		return
	}

	final := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(req.ID),
		"result": map[string]any{
			"stopReason": "end_turn",
		},
	}
	if !writeNDJSONLine(w, fl, final) {
		return
	}
	if h.cfg.OnPromptUpdate != nil {
		h.cfg.OnPromptUpdate("end_turn", p.SessionID)
	}
}

func (h *handler) writePromptCancelled(w http.ResponseWriter, fl http.Flusher, id json.RawMessage, sessionID string) {
	tail := map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{
					"type": "text",
					"text": "stopped",
				},
			},
		},
	}
	if !writeNDJSONLine(w, fl, tail) {
		return
	}

	final := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result": map[string]any{
			"stopReason": "cancelled",
		},
	}
	if !writeNDJSONLine(w, fl, final) {
		return
	}
	if h.cfg.OnPromptUpdate != nil {
		h.cfg.OnPromptUpdate("cancelled", sessionID)
	}
}

func (h *handler) handleCancel(w http.ResponseWriter, params json.RawMessage) error {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.SessionID == "" {
		return errors.New("invalid cancel params")
	}
	sess := h.getSession(p.SessionID)
	if sess == nil {
		return errors.New("unknown session")
	}
	sess.setCancelled()
	return nil
}

func (h *handler) isInitialized() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.initialized
}

func (h *handler) getSession(id string) *emuSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

func (s *emuSession) resetForPrompt() {
	s.mu.Lock()
	s.cancelled = false
	s.cancelCh = make(chan struct{})
	s.cancelOnce = sync.Once{}
	s.mu.Unlock()
}

func (s *emuSession) setCancelled() {
	s.mu.Lock()
	s.cancelled = true
	ch := s.cancelCh
	s.mu.Unlock()
	if ch != nil {
		s.cancelOnce.Do(func() {
			close(ch)
		})
	}
}

func (s *emuSession) isCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelled
}

func extractResourceEcho(prompt []map[string]any) string {
	var b strings.Builder
	for _, block := range prompt {
		typ, ok := block["type"].(string)
		if !ok || typ != "resource" {
			continue
		}
		res, ok := block["resource"].(map[string]any)
		if !ok || res == nil {
			continue
		}
		if uri, ok := res["uri"].(string); ok && uri != "" {
			if b.Len() > 0 {
				b.WriteString(";")
			}
			b.WriteString(uri)
		}
		if txt, ok := res["text"].(string); ok && txt != "" {
			if b.Len() > 0 {
				b.WriteString("|")
			}
			b.WriteString(txt)
		}
	}
	return b.String()
}

func sleepOrDone(ctx context.Context, d time.Duration, sess *emuSession) bool {
	if d <= 0 {
		return ctx.Err() == nil && !sess.isCancelled()
	}
	timer := time.NewTimer(d)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return false
	case <-sess.cancelCh:
		return false
	case <-timer.C:
		return ctx.Err() == nil && !sess.isCancelled()
	}
}

func writeNDJSONLine(w io.Writer, fl http.Flusher, v any) bool {
	buf, err := json.Marshal(v)
	if err != nil {
		slog.Default().Warn("refbackend/acp: marshal ndjson line", "error", err)
		return false
	}
	if _, err := w.Write(buf); err != nil {
		return false
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		return false
	}
	if fl != nil {
		fl.Flush()
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Default().Debug("refbackend/acp: encode json response", "error", err)
	}
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(bytes.TrimSpace(id)) == 0 {
		id = []byte("null")
	}
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	writeJSON(w, res)
}
