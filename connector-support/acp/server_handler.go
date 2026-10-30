package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ServerRequestHandler answers inbound JSON-RPC requests from the agent (e.g. permissions,
// vendor extensions). Stdio transports interleave these with session/update lines; HTTP
// test transports typically do not emit them.
type ServerRequestHandler interface {
	HandleServerRequest(ctx context.Context, method string, id json.RawMessage, params json.RawMessage) (result any, err error)
}

// headlessServerRequestHandler responds with empty objects for unknown methods so a
// headless proxy can keep streaming; vendor-specific connectors replace this.
type headlessServerRequestHandler struct{}

func (headlessServerRequestHandler) HandleServerRequest(_ context.Context, _ string, _ json.RawMessage, _ json.RawMessage) (any, error) {
	return map[string]any{}, nil
}

func serverHandlerOrDefault(h ServerRequestHandler) ServerRequestHandler {
	if h != nil {
		return h
	}
	return headlessServerRequestHandler{}
}

// AutoApprovePermissionRequest selects an offered allow_once option, never a
// durable allow_always grant. Malformed or ambiguous IDs and offers without a
// one-time option produce a cancelled outcome. This is pure JSON selection: no connector
// callbacks, tool execution or I/O occur on the transport reader.
func AutoApprovePermissionRequest(params json.RawMessage) any {
	cancelled := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	var request struct {
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(params, &request) != nil {
		return cancelled
	}
	seen := make(map[string]bool, len(request.Options))
	for _, option := range request.Options {
		if option.ID == "" || seen[option.ID] {
			return cancelled
		}
		seen[option.ID] = true
	}
	for _, option := range request.Options {
		if option.Kind == "allow_once" {
			return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": option.ID}}
		}
	}
	return cancelled
}

func isInboundServerRequest(probe map[string]any) bool {
	return IsInboundServerRequest(probe, acpServerRequestExclusions)
}

// acpServerRequestExclusions are ACP methods that carry an id but must be
// treated as notifications/responses, not inbound server requests.
var acpServerRequestExclusions = []string{"session/update"}

func replyServerRequestJSON(id json.RawMessage, result any) ([]byte, error) {
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	return json.Marshal(res)
}

// replyServerRequestErrorJSON builds a JSON-RPC error response (code -32601)
// for an unhandled server-initiated request so the agent receives a
// method-not-found response without terminating the stream.
func replyServerRequestErrorJSON(id json.RawMessage, code int, message string) ([]byte, error) {
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	return json.Marshal(res)
}

// ExtractServerRequestProbe extracts the method, id, and params from an inbound
// JSON-RPC server-request probe, preparing them for a ServerRequestHandler
// invocation. It is the shared extraction preamble used by both the ACP
// promptStream and the Codex codexStream server-request handlers, which
// previously duplicated this logic. The error-handling policy (send a -32601
// error response and continue vs. terminate the stream) stays with each caller.
//
// Returns:
//   - method: the JSON-RPC method string.
//   - id: the marshaled id bytes (json.RawMessage), or nil when dropped.
//   - params: the marshaled params bytes, or nil when absent.
//   - dropped: true when the probe carries no id (a notification to drop, not a
//     request to answer).
//   - err: non-nil when the method is missing or id/params marshaling fails.
//
// The label prefixes error messages (e.g. "acp", "codex").
func ExtractServerRequestProbe(label string, probe map[string]any) (method string, id, params json.RawMessage, dropped bool, err error) {
	method, _ = probe["method"].(string)
	if strings.TrimSpace(method) == "" {
		return "", nil, nil, false, fmt.Errorf("%s: inbound JSON-RPC missing method", label)
	}
	idRaw, ok := probe["id"]
	if !ok || idRaw == nil {
		return method, nil, nil, true, nil
	}
	idBytes, mErr := json.Marshal(idRaw)
	if mErr != nil {
		return method, nil, nil, false, fmt.Errorf("%s: marshal inbound request id: %w", label, mErr)
	}
	if p, ok := probe["params"]; ok {
		b, pErr := json.Marshal(p)
		if pErr != nil {
			return method, idBytes, nil, false, fmt.Errorf("%s: marshal inbound request params: %w", label, pErr)
		}
		params = b
	}
	return method, idBytes, params, false, nil
}
