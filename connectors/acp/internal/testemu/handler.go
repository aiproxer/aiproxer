package testemu

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
)

// NewHandler returns a deterministic ACP HTTP JSON-RPC/NDJSON emulator for
// module-local tests (named testemu; not a live product agent).
func NewHandler() http.Handler {
	h := &handler{sessions: map[string]*session{}}
	return http.HandlerFunc(h.serve)
}

type handler struct {
	mu       sync.Mutex
	next     int
	sessions map[string]*session
}

type session struct {
	mu        sync.Mutex
	cancelled bool
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/acp" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
		return
	}
	switch req.Method {
	case "initialize":
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"protocolVersion": 1}})
	case "authenticate":
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{}})
	case "session/new":
		h.mu.Lock()
		h.next++
		id := "s" + strconv.Itoa(h.next)
		h.sessions[id] = &session{}
		h.mu.Unlock()
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"sessionId": id}})
	case "session/prompt":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		h.mu.Lock()
		s := h.sessions[p.SessionID]
		h.mu.Unlock()
		if s == nil {
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32602, "message": "unknown session"}})
			return
		}
		s.mu.Lock()
		cancelled := s.cancelled
		s.cancelled = false
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if cancelled {
			writeND(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{"stopReason": "cancelled"}})
			return
		}
		writeND(w, map[string]any{
			"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{
				"sessionId": p.SessionID,
				"update": map[string]any{
					"sessionUpdate": "agent_thought_chunk",
					"content":       map[string]any{"type": "text", "text": "think"},
				},
			},
		})
		writeND(w, map[string]any{
			"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{
				"sessionId": p.SessionID,
				"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": "ok"},
				},
			},
		})
		writeND(w, map[string]any{
			"jsonrpc": "2.0", "id": json.RawMessage(req.ID),
			"result": map[string]any{
				"stopReason": "end_turn",
				// The usage shape of the Gemini CLI ACP agent: its input
				// counter is usageMetadata.promptTokenCount, which already
				// contains the cached content tokens, and its total is the
				// input plus the output.
				"usage": map[string]any{
					"inputTokens": 120, "outputTokens": 45,
					"cachedReadTokens": 80,
					"totalTokens":      165,
				},
			},
		})
	case "session/cancel":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		h.mu.Lock()
		s := h.sessions[p.SessionID]
		h.mu.Unlock()
		if s != nil {
			s.mu.Lock()
			s.cancelled = true
			s.mu.Unlock()
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": map[string]any{}})
	default:
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": -32601, "message": "method not found"}})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeND(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(append(b, '\n'))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
