package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/reasoning"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// codexStream maps Codex app-server NDJSON lines to aipapi.EventStream. It
// embeds acp.NDJSONStreamBase for shared scanner/body/pending/framing/EOF
// mechanics and supplies the Codex-specific NDJSONStreamStrategy via receiver
// methods.
//
// Unlike the ACP promptStream, the Codex protocol uses:
//   - item/agentMessage/delta → text deltas
//   - item/reasoning/summaryTextDelta, item/reasoning/textDelta → reasoning deltas
//   - item/completed → tool completion summaries (text deltas)
//   - turn/completed → terminal event (ResponseFinished)
//   - server requests (approval methods) → auto-accept/decline responses
type codexStream struct {
	*acp.NDJSONStreamBase

	cli                       *codexClient
	turnRPCID                 int64
	srv                       *codexServerRequestHandler
	reasoningSummarySanitizer reasoning.SummarySanitizer
}

func newCodexStream(
	parent context.Context,
	body io.ReadCloser,
	cli *codexClient,
	turnRPCID int64,
	srv *codexServerRequestHandler,
	maxPending int,
) *codexStream {
	s := &codexStream{
		cli:       cli,
		turnRPCID: turnRPCID,
		srv:       srv,
	}
	s.NDJSONStreamBase = acp.NewNDJSONStreamBase(parent, body, maxPending, s)
	return s
}

// Label implements acp.NDJSONStreamStrategy.
func (s *codexStream) Label() string { return "codex" }

// IsServerRequest implements acp.NDJSONStreamStrategy: Codex has no method
// exclusions.
func (s *codexStream) IsServerRequest(probe map[string]any) bool {
	return acp.IsInboundServerRequest(probe, nil)
}

// HandleServerRequest implements acp.NDJSONStreamStrategy. Unlike ACP, a
// handler failure returns a wrapped error and terminates the stream.
func (s *codexStream) HandleServerRequest(ctx context.Context, probe map[string]any) error {
	if err := s.handleServerRequest(ctx, probe); err != nil {
		return fmt.Errorf("codex: handle server request: %w", err)
	}
	return nil
}

// MapLine implements acp.NDJSONStreamStrategy. Responses (id present) are
// turn/start responses (checked for error, otherwise skipped) or stray
// responses (ignored); notifications are mapped via mapNotification.
func (s *codexStream) MapLine(_ context.Context, _ string, probe map[string]any) ([]aipapi.Event, error) {
	if id, ok := probe["id"]; ok && id != nil {
		idBytes, _ := json.Marshal(id)
		idStr := strings.TrimSpace(string(idBytes))
		turnIDStr := strconv.FormatInt(s.turnRPCID, 10)
		if idStr == turnIDStr {
			if errMsg, ok := probe["error"]; ok && errMsg != nil {
				return nil, fmt.Errorf("codex: turn/start error: %v", errMsg)
			}
			return nil, nil
		}
		return nil, nil
	}
	evs, err := s.mapNotification(probe)
	if err != nil {
		return nil, fmt.Errorf("codex: map notification: %w", err)
	}
	return evs, nil
}

// OnCancel implements acp.NDJSONStreamStrategy: cancel the stream context.
// Best-effort turn/interrupt is not supported by the Codex app-server.
func (s *codexStream) OnCancel() {
	s.CancelStreamContext()
}

// mapNotification converts a Codex JSON-RPC notification to canonical events.
func (s *codexStream) mapNotification(probe map[string]any) ([]aipapi.Event, error) {
	method, _ := probe["method"].(string)
	params, _ := probe["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}

	switch method {
	case "item/agentMessage/delta":
		delta, _ := params["delta"].(string)
		if delta == "" {
			return nil, nil
		}
		return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: delta}}, nil

	case "item/reasoning/summaryTextDelta":
		delta, _ := params["delta"].(string)
		delta = s.reasoningSummarySanitizer.SanitizeDelta(delta)
		if delta == "" {
			return nil, nil
		}
		return []aipapi.Event{{Kind: aipapi.EventReasoningDelta, Delta: delta}}, nil

	case "item/reasoning/summaryPartAdded":
		s.reasoningSummarySanitizer.StartSummaryPart()
		return nil, nil

	case "item/reasoning/textDelta":
		delta, _ := params["delta"].(string)
		if delta == "" {
			return nil, nil
		}
		return []aipapi.Event{{Kind: aipapi.EventReasoningDelta, Delta: delta}}, nil

	case "item/completed":
		text := buildItemCompletionSummary(params)
		if text == "" {
			return nil, nil
		}
		return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: text}}, nil

	case "turn/started":
		s.reasoningSummarySanitizer.Reset()
		return nil, nil

	case "turn/completed":
		s.reasoningSummarySanitizer.Reset()
		return []aipapi.Event{{Kind: aipapi.EventResponseFinished}}, nil

	case "item/started", "thread/started",
		"item/commandExecution/outputDelta", "item/fileChange/outputDelta",
		"item/plan/delta", "turn/diff/updated", "thread/tokenUsage/updated",
		"serverRequest/resolved":
		return nil, nil

	default:
		return nil, nil
	}
}

// buildItemCompletionSummary generates a compact fenced summary for a completed
// Codex command or file change item.
func buildItemCompletionSummary(params map[string]any) string {
	item, _ := params["item"].(map[string]any)
	if item == nil {
		item = params
	}
	itemType, _ := item["type"].(string)
	switch itemType {
	case "commandExecution":
		return buildCommandSummary(item)
	case "fileChange":
		return buildFileChangeSummary(item)
	default:
		return ""
	}
}

func buildCommandSummary(item map[string]any) string {
	actualCommand := ""
	if actions, ok := item["commandActions"].([]any); ok && len(actions) > 0 {
		if first, ok := actions[0].(map[string]any); ok {
			if cmd, ok := first["command"].(string); ok && cmd != "" {
				actualCommand = cmd
			}
		}
	}
	if actualCommand == "" {
		if cmd, ok := item["command"].(string); ok {
			actualCommand = cmd
		}
	}
	name := commandBasename(actualCommand)
	if name == "" {
		name = "command"
	}
	durationMs, _ := item["durationMs"].(float64)
	elapsedS := durationMs / 1000.0
	aggregatedOutput, _ := item["aggregatedOutput"].(string)
	outputBytes := len(aggregatedOutput)
	now := time.Now().UTC()
	started := now.Add(-time.Duration(elapsedS * float64(time.Second)))
	// Input size is the command string length (codex commandExecution semantics);
	// output size is the aggregated output length. The shared formatter derives
	// elapsed from ended-started, which reproduces elapsedS exactly.
	return acp.FormatToolCompletionSummary(name, len(actualCommand), outputBytes, started, now)
}

func buildFileChangeSummary(item map[string]any) string {
	changes, _ := item["changes"].([]any)
	var paths []string
	for _, change := range changes {
		if m, ok := change.(map[string]any); ok {
			if path, ok := m["path"].(string); ok && path != "" {
				paths = append(paths, path)
			}
		}
	}
	now := time.Now().UTC()
	inputBytes := len(strings.Join(paths, ", "))
	// fileChange carries no aggregated output; elapsed is zero (codex semantics).
	return acp.FormatToolCompletionSummary("fileChange", inputBytes, 0, now, now)
}

func commandBasename(command string) string {
	if command == "" {
		return ""
	}
	stripped := strings.TrimSpace(command)
	if stripped == "" {
		return ""
	}
	first := strings.Fields(stripped)
	if len(first) == 0 {
		return ""
	}
	base := first[0]
	if idx := strings.LastIndexAny(base, "/\\"); idx >= 0 {
		base = base[idx+1:]
	}
	return base
}

// handleServerRequest processes a server-initiated JSON-RPC request by invoking
// the codexServerRequestHandler and writing the response back to stdin.
func (s *codexStream) handleServerRequest(ctx context.Context, probe map[string]any) error {
	method, idBytes, paramsRaw, dropped, err := acp.ExtractServerRequestProbe("codex", probe)
	if err != nil {
		return err
	}
	if dropped {
		return nil
	}
	res, err := s.srv.HandleServerRequest(ctx, method, idBytes, paramsRaw)
	if err != nil {
		// Codex policy: a handler failure terminates the stream (unlike ACP,
		// which sends a -32601 error response and continues).
		return fmt.Errorf("codex: handle server request method %s: %w", method, err)
	}
	respBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(idBytes),
		"result":  res,
	})
	if err != nil {
		return fmt.Errorf("codex: encode server response: %w", err)
	}
	if err := s.cli.t.SendJSONRPC(ctx, respBody); err != nil {
		return fmt.Errorf("codex: send server response: %w", err)
	}
	return nil
}
