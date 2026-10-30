package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// codexSpec provides vendor-specific config for the Codex App Server protocol.
// Unlike the ACP CLI connectors, the Codex app-server does NOT use session/prompt
// — it uses initialize → initialized → thread/start → turn/start → item/* → turn/completed.
// So it cannot use acp.SubprocessConnectorSpec; instead codexProtocol (protocol.go)
// adapts it to acp.SubprocessProtocol so the shared subprocessBackend orchestrator
// can drive it.
type codexSpec struct {
	cfg Config
	// exe is the resolved Codex CLI executable path used to build the spawn
	// command. It is resolved once in newBackend (production) or set to a
	// placeholder when a ProcessStarter is injected (tests), so
	// BuildSpawnCommand never re-resolves and never fails on a CI runner that
	// lacks the real codex binary.
	exe string
	// index is the concurrent request-time allowlist populated by core
	// AcceptInventory after registry publish. Open rejects unknown models before spawn.
	index *acp.ModelIndex
}

// codexManagedStream wraps a codexStream to release the runtime pool on Close.
type codexManagedStream struct {
	inner  *codexStream
	pool   *acp.RuntimePool
	key    acp.RuntimeKey
	closed bool
	mu     sync.Mutex
}

var _ aipapi.ManagedEventStream = (*codexManagedStream)(nil)

func (s *codexManagedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	return s.inner.Recv(ctx)
}

func (s *codexManagedStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.Release(s.key)
	return s.inner.Close()
}

func (s *codexManagedStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	return s.inner.Cancel(ctx, cause)
}

// validateCall validates the canonical call for the Codex App Server backend.
func validateCall(call *aipapi.Call) error {
	if call == nil {
		return fmt.Errorf("codex app-server: nil call")
	}
	if err := call.Validate(); err != nil {
		return fmt.Errorf("codex app-server: invalid call: %w", err)
	}
	return nil
}

// extractReasoningEffort extracts reasoning_effort from call extensions.
func extractReasoningEffort(call *aipapi.Call) string {
	if call == nil {
		return ""
	}
	if effort := strings.TrimSpace(call.Options.ReasoningEffort); effort != "" {
		return effort
	}
	if call.Extensions == nil {
		return ""
	}
	for _, key := range []string{"reasoning_effort", "codex.reasoning_effort"} {
		if raw, ok := call.Extensions[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// buildTurnStartRequest builds the JSON-RPC turn/start request body. Unlike
// the in-handshake mustMarshalJSON call sites (which marshal guaranteed-safe
// literals and panic on error), this function takes caller-supplied params
// and therefore propagates the json.Marshal error normally.
func buildTurnStartRequest(params map[string]any, rpcID int64) ([]byte, error) {
	pb, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"method":  "turn/start",
		"params":  json.RawMessage(pb),
	}
	return json.Marshal(req)
}

// mustMarshalJSON encodes v as JSON and panics on error. Reserved for
// construction-time call sites in this file (handshake body builders) whose
// inputs are guaranteed-marshalable literal maps — any non-nil error here
// indicates a programmer mistake (unsupported type, cyclic structure), not
// a runtime condition, so panicking surfaces it immediately instead of
// silently producing a malformed wire request (per golang-error-handling:
// "Returned errors MUST always be checked — NEVER discard with `_`"). The
// %T in the panic message names the offending value type for faster triage.
func mustMarshalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("codex app-server: mustMarshalJSON(%T): %v", v, err))
	}
	return b
}

// runCodexHandshake performs the Codex-specific handshake:
// 1. initialize (with experimentalApi capability)
// 2. initialized notification
// 3. thread/start (returns thread ID)
//
// The thread/start result is accepted in two shapes for cross-version
// compatibility with the Codex CLI: the flat {"id": "thread-xxx"} shape and
// the nested {"thread": {"id": "thread-xxx"}} shape. The flat shape is
// preferred; the nested shape is a fallback. See
// docs/codex-app-server-backend.md.
func runCodexHandshake(ctx context.Context, cli *codexClient, transport acp.Transport, workspace, model string) (string, error) {
	// 1. initialize
	initID := cli.rpcID()
	initParams := mustMarshalJSON(map[string]any{
		"clientInfo": map[string]any{
			"name":    handshakeClientName,
			"version": "1",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
	})
	initReq := mustMarshalJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      initID,
		"method":  "initialize",
		"params":  json.RawMessage(initParams),
	})
	initResp, err := transport.CallUnary(ctx, initReq, 0)
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	if err := checkRPCError(initResp, "initialize"); err != nil {
		return "", err
	}

	// 2. initialized notification (no id → notification)
	initializedBody := mustMarshalJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	})
	if _, err := transport.CallUnary(ctx, initializedBody, 0); err != nil {
		return "", fmt.Errorf("initialized notification: %w", err)
	}

	// 3. thread/start
	threadID := cli.rpcID()
	threadParams := map[string]any{
		"cwd":                   workspace,
		"runtimeWorkspaceRoots": []string{workspace},
	}
	if !isAutoModel(model) {
		threadParams["model"] = model
	}
	tpb := mustMarshalJSON(threadParams)
	threadReq := mustMarshalJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      threadID,
		"method":  "thread/start",
		"params":  json.RawMessage(tpb),
	})
	threadResp, err := transport.CallUnary(ctx, threadReq, 0)
	if err != nil {
		return "", fmt.Errorf("thread/start: %w", err)
	}
	if err := checkRPCError(threadResp, "thread/start"); err != nil {
		return "", err
	}

	// Extract thread ID from response.
	var resp struct {
		Result struct {
			ID     string `json:"id"`
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		} `json:"result"`
	}
	if err := json.Unmarshal(threadResp, &resp); err != nil {
		return "", fmt.Errorf("thread/start: decode result: %w", err)
	}
	tid := resp.Result.ID
	if tid == "" {
		tid = resp.Result.Thread.ID
	}
	if strings.TrimSpace(tid) == "" {
		return "", fmt.Errorf("thread/start: empty threadId")
	}
	return tid, nil
}

// checkRPCError checks if a JSON-RPC response contains an error.
func checkRPCError(raw []byte, method string) error {
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("%s: decode response: %w", method, err)
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: json-rpc error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	return nil
}

// codexClient wraps an acp.Transport for Codex-specific JSON-RPC calls.
type codexClient struct {
	t      acp.Transport
	nextID atomic.Int64
}

func newCodexClient(t acp.Transport) *codexClient {
	return &codexClient{t: t}
}

func (c *codexClient) rpcID() int64 {
	return c.nextID.Add(1)
}
