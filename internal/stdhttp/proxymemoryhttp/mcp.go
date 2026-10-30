// MCP endpoint of the proxy-memory HTTP adapter (design §11.3): the Streamable
// HTTP transport in JSON response mode only, over the mounted POST /mcp route.
package proxymemoryhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
)

// maxMCPBodyBytes caps one MCP request body at 1 MiB (design §11.2).
const maxMCPBodyBytes = 1 << 20

// MCP protocol versions of design §11.3: the prior version is echoed, the
// current one is the default answer for anything else.
const (
	mcpProtocolVersionCurrent = "2025-06-18"
	mcpProtocolVersionPrior   = "2025-03-26"

	jsonRPCVersion = "2.0"

	rpcCodeInvalidRequest = -32600
	rpcCodeMethodNotFound = -32601
	rpcCodeInvalidParams  = -32602
)

// MCP methods of design §11.3.
const (
	mcpMethodInitialize = "initialize"
	mcpMethodPing       = "ping"
	mcpMethodToolsList  = "tools/list"
	mcpMethodToolsCall  = "tools/call"
)

// Tool names of design §11.3.
const (
	toolMemoryList    = "memory_list"
	toolMemoryRead    = "memory_read"
	toolMemorySearch  = "memory_search"
	toolMemoryAddNote = "memory_add_note"
)

// rpcFault is one JSON-RPC error object.
type rpcFault struct {
	Code    int
	Message string
}

// toolResult is the tools/call result shape of design §11.3.
type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// mcpTool describes one tool of the closed catalog.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// mcpTools is the closed tool catalog of design §11.3. Every input schema is a
// JSON Schema object requiring scope.
var mcpTools = []mcpTool{
	{
		Name:        toolMemoryList,
		Description: "List the bank's memory document paths, one per line.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string"}},"required":["scope"]}`),
	},
	{
		Name:        toolMemoryRead,
		Description: "Read one memory document, limited to the requested 1-based inclusive line range.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string"},"path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"}},"required":["scope","path"]}`),
	},
	{
		Name:        toolMemorySearch,
		Description: "Search the bank's memory documents; returns path:line: text lines, or no matches.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string"},"query":{"type":"string"}},"required":["scope","query"]}`),
	},
	{
		Name:        toolMemoryAddNote,
		Description: "Save one explicit note for the bank's next consolidation.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"scope":{"type":"string"},"note":{"type":"string"}},"required":["scope","note"]}`),
	},
}

// errUnknownTool and errToolArguments mark the two protocol-level tool errors;
// they are invalid params, not service errors.
var (
	errUnknownTool   = errors.New("tools/call: unknown tool")
	errToolArguments = errors.New("tools/call: invalid arguments")
)

// handleMCP serves one MCP request: a single JSON-RPC 2.0 object on POST.
// Batches, malformed documents and method-less objects are invalid requests;
// notifications are acknowledged with an empty 202; requests get a 200 JSON-RPC
// response. No session identifier is issued.
func (h *handler) handleMCP(w http.ResponseWriter, r *http.Request, p proxymemory.Principal) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if r.ContentLength > maxMCPBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBodyBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		writeRPCError(w, nil, rpcCodeInvalidRequest, "invalid request")
		return
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		// A batch array, a scalar or malformed JSON is not one request object.
		writeRPCError(w, nil, rpcCodeInvalidRequest, "invalid request")
		return
	}
	// Exact-case member names only (JSON-RPC §2): encoding/json resolves struct
	// tags case-insensitively, so JSONRPC/METHOD/ID/Id/Params aliases are unknown
	// extension members that supply and override nothing. An alias-only id is no
	// id: for every method such an envelope stays notification-shaped (202, no
	// dispatch, no side effect) and is never answered as a proxy request.
	id, params := members["id"], members["params"]
	var method, version string
	_, idLegal := rpcRequestID(id)
	if json.Unmarshal(members["jsonrpc"], &version) != nil || version != jsonRPCVersion ||
		json.Unmarshal(members["method"], &method) != nil || strings.TrimSpace(method) == "" ||
		(len(id) > 0 && !idLegal) {
		// A rejected envelope runs no method; only its exact-case id is echoed.
		writeRPCError(w, id, rpcCodeInvalidRequest, "invalid request")
		return
	}
	if len(id) == 0 {
		// A notification is acknowledged without a response body.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	result, fault := h.dispatchMCP(r.Context(), p, method, params)
	if fault != nil {
		writeRPCError(w, id, fault.Code, fault.Message)
		return
	}
	writeRPCResult(w, id, result)
}

// dispatchMCP runs one request method (design §11.3).
func (h *handler) dispatchMCP(ctx context.Context, p proxymemory.Principal, method string, params json.RawMessage) (any, *rpcFault) {
	switch method {
	case mcpMethodInitialize:
		return mcpInitialize(params), nil
	case mcpMethodPing:
		return map[string]any{}, nil
	case mcpMethodToolsList:
		return map[string]any{"tools": mcpTools}, nil
	case mcpMethodToolsCall:
		return h.callTool(ctx, p, params)
	default:
		return nil, &rpcFault{Code: rpcCodeMethodNotFound, Message: "method not found"}
	}
}

// mcpInitialize negotiates the protocol version: a version the server
// implements is echoed, anything else gets the current default.
func mcpInitialize(params json.RawMessage) map[string]any {
	var in struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &in)
	return map[string]any{
		"protocolVersion": negotiateProtocolVersion(in.ProtocolVersion),
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "aiproxer-memory", "version": "1"},
	}
}

func negotiateProtocolVersion(requested string) string {
	switch requested {
	case mcpProtocolVersionCurrent, mcpProtocolVersionPrior:
		return requested
	default:
		return mcpProtocolVersionCurrent
	}
}

// callTool decodes, validates and runs one tools/call request. A successful
// call counts tool_call exactly once (design §11.3).
func (h *handler) callTool(ctx context.Context, p proxymemory.Principal, params json.RawMessage) (any, *rpcFault) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcFault{Code: rpcCodeInvalidParams, Message: "invalid params"}
	}
	text, err := h.runTool(ctx, p, call.Name, call.Arguments)
	if err != nil {
		switch {
		case errors.Is(err, errUnknownTool):
			return nil, &rpcFault{Code: rpcCodeInvalidParams, Message: "unknown tool"}
		case errors.Is(err, errToolArguments):
			return nil, &rpcFault{Code: rpcCodeInvalidParams, Message: "invalid params"}
		default:
			return h.toolError(err), nil
		}
	}
	h.metrics.Inc(proxymemory.EventToolCall)
	return textToolResult(text), nil
}

// runTool dispatches one tool by name and returns its text payload. The tool
// calls map onto the frozen service surface of design §11.1 verbatim; the
// adapter never post-processes a service result.
func (h *handler) runTool(ctx context.Context, p proxymemory.Principal, name string, raw json.RawMessage) (string, error) {
	switch name {
	case toolMemoryList:
		var args struct {
			Scope string `json:"scope"`
		}
		if err := decodeToolArguments(raw, &args); err != nil {
			return "", err
		}
		paths, err := h.svc.Paths(ctx, p, args.Scope)
		if err != nil {
			return "", err
		}
		return strings.Join(paths, "\n"), nil
	case toolMemoryRead:
		var args struct {
			Scope     string `json:"scope"`
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := decodeToolArguments(raw, &args); err != nil {
			return "", err
		}
		return h.svc.Read(ctx, p, args.Scope, args.Path, args.StartLine, args.EndLine)
	case toolMemorySearch:
		var args struct {
			Scope string `json:"scope"`
			Query string `json:"query"`
		}
		if err := decodeToolArguments(raw, &args); err != nil {
			return "", err
		}
		hits, err := h.svc.Search(ctx, p, args.Scope, args.Query)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "no matches", nil
		}
		var b strings.Builder
		for i, hit := range hits {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "%s:%d: %s", hit.Path, hit.Line, hit.Text)
		}
		return b.String(), nil
	case toolMemoryAddNote:
		var args struct {
			Scope string `json:"scope"`
			Note  string `json:"note"`
		}
		if err := decodeToolArguments(raw, &args); err != nil {
			return "", err
		}
		if err := h.svc.AddNote(ctx, p, args.Scope, args.Note); err != nil {
			return "", err
		}
		return "saved", nil
	default:
		return "", errUnknownTool
	}
}

// decodeToolArguments decodes one tool's arguments document. An absent
// arguments value decodes as the zero value; a malformed document is the
// invalid-params protocol error, not a service error.
func decodeToolArguments(raw json.RawMessage, dest any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return errToolArguments
	}
	return nil
}

// toolError maps one service failure onto the bounded tool result of design
// §11.3. The two enumerated classes get their fixed texts; every other store
// failure is the MCP equivalent of the REST 500, counted as store_unavailable,
// and carries no error text.
func (h *handler) toolError(err error) toolResult {
	switch {
	case errors.Is(err, proxymemory.ErrNotFound):
		return textErrorResult("not found")
	case errors.Is(err, proxymemory.ErrInvalid):
		return textErrorResult("invalid input")
	default:
		h.metrics.Inc(proxymemory.EventStoreUnavailable)
		return textErrorResult("unavailable")
	}
}

func textToolResult(text string) toolResult {
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}, IsError: false}
}

func textErrorResult(text string) toolResult {
	return toolResult{Content: []toolContent{{Type: "text", Text: text}}, IsError: true}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": jsonRPCVersion,
		"id":      rpcResponseID(id),
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": jsonRPCVersion,
		"id":      rpcResponseID(id),
		"error":   map[string]any{"code": code, "message": message},
	})
}

// rpcRequestID returns a present JSON string or number request id verbatim;
// any other value answers nil, and the bool reports the legal id case.
func rpcRequestID(id json.RawMessage) (json.RawMessage, bool) {
	if len(id) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(id))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	_, isString := v.(string)
	_, isNumber := v.(json.Number)
	return id, isString || isNumber
}

// rpcResponseID renders one response id: a legal request id (a JSON string or
// number), echoed even for a rejected envelope, or null for an absent or
// illegal one, which is never echoed back.
func rpcResponseID(id json.RawMessage) json.RawMessage {
	if id, ok := rpcRequestID(id); ok {
		return id
	}
	return json.RawMessage("null")
}
