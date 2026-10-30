package stdhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	refanth "github.com/aiproxer/aiproxer/internal/refbackend/anthropicmessages"
)

const (
	anthThinkText   = "plan-SECRET-THINK"
	anthThinkSig    = "sig-SECRET-PLAN"
	anthOpaqueData  = "opaque-SECRET-DATA"
	anthVisibleText = "checking"
	anthToolID      = "toolu_lookup_1"
	anthToolName    = "lookup"
	anthToolArgs    = `{"q":"nyc"}`
	anthToolResult  = `{"ok":true,"city":"nyc"}`
)

func TestReasoningPreservationHTTP_Anthropic(t *testing.T) {
	t.Run("drop_restore_thinking_redacted_tools", func(t *testing.T) {
		t.Parallel()
		runAnthropicDropRestoreThinkingRedactedTools(t)
	})
	t.Run("preserve_thinking_redacted_tools_no_duplicate", func(t *testing.T) {
		t.Parallel()
		runAnthropicPreserveThinkingRedactedTools(t)
	})
	t.Run("failure_messages_omit_sentinels", func(t *testing.T) {
		t.Parallel()
		runAnthropicFailureMessagesOmitSentinels(t)
	})
}

func runAnthropicDropRestoreThinkingRedactedTools(t *testing.T) {
	t.Helper()
	stack := startReasoningPreservationAnthropicStack(t, "restore", []refanth.ThinkingTurn{
		{
			VisibleText:     anthVisibleText,
			Thinking:        anthThinkText,
			Signature:       anthThinkSig,
			IncludeRedacted: true,
			RedactedData:    anthOpaqueData,
			ToolID:          anthToolID,
			ToolName:        anthToolName,
			ToolInputJSON:   anthToolArgs,
		},
		{VisibleText: "done"},
	})
	ctx := context.Background()
	sid, tok, body1 := postAnthropic(ctx, t, stack, "", "", anthropicBody(true, []any{
		map[string]any{"role": "user", "content": "hi"},
	}, anthropicLookupTool()))
	assertAnthropicStreamComplete(t, body1)
	assertAnthropicStreamHasTypes(t, body1, "thinking", "redacted_thinking", "text", "tool_use")
	_ = drainOracleBodies(t, stack.oracleCh, 1)

	// Drop thinking + redacted; keep visible text + tool_use; then client tool_result.
	_, _, _ = postAnthropic(ctx, t, stack, sid, tok, anthropicBody(true, []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": anthVisibleText},
			map[string]any{"type": "tool_use", "id": anthToolID, "name": anthToolName, "input": map[string]any{"q": "nyc"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": anthToolID, "content": anthToolResult},
		}},
	}, anthropicLookupTool()))
	bodies := drainOracleBodies(t, stack.oracleCh, 1)
	assertAnthropicBackendAssistantOrder(t, bodies[0], true)
	assertAnthropicBackendToolResult(t, bodies[0], anthToolID)
}

func runAnthropicPreserveThinkingRedactedTools(t *testing.T) {
	t.Helper()
	stack := startReasoningPreservationAnthropicStack(t, "restore", []refanth.ThinkingTurn{
		{
			VisibleText:     anthVisibleText,
			Thinking:        anthThinkText,
			Signature:       anthThinkSig,
			IncludeRedacted: true,
			RedactedData:    anthOpaqueData,
			ToolID:          anthToolID,
			ToolName:        anthToolName,
			ToolInputJSON:   anthToolArgs,
		},
		{VisibleText: "next"},
	})
	ctx := context.Background()
	sid, tok, body1 := postAnthropic(ctx, t, stack, "", "", anthropicBody(true, []any{
		map[string]any{"role": "user", "content": "hi"},
	}, anthropicLookupTool()))
	assertAnthropicStreamComplete(t, body1)
	_ = drainOracleBodies(t, stack.oracleCh, 1)

	content := []any{
		map[string]any{"type": "thinking", "thinking": anthThinkText, "signature": anthThinkSig},
		map[string]any{"type": "redacted_thinking", "data": anthOpaqueData},
		map[string]any{"type": "text", "text": anthVisibleText},
		map[string]any{"type": "tool_use", "id": anthToolID, "name": anthToolName, "input": map[string]any{"q": "nyc"}},
	}
	_, _, _ = postAnthropic(ctx, t, stack, sid, tok, anthropicBody(true, []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": content},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": anthToolID, "content": anthToolResult},
		}},
	}, anthropicLookupTool()))
	bodies := drainOracleBodies(t, stack.oracleCh, 1)
	asst := assistantContentBlocks(t, bodies[0])
	thinkN := countBlockType(asst, "thinking")
	redactedN := countBlockType(asst, "redacted_thinking")
	toolN := countBlockType(asst, "tool_use")
	if thinkN != 1 || redactedN != 1 || toolN != 1 {
		t.Fatalf("preserve must not duplicate; field=block_counts thinking=%d redacted=%d tool_use=%d", thinkN, redactedN, toolN)
	}
	assertAnthropicBackendAssistantOrder(t, bodies[0], true)
	assertAnthropicBackendToolResult(t, bodies[0], anthToolID)
}

func runAnthropicFailureMessagesOmitSentinels(t *testing.T) {
	t.Helper()
	sentinels := []string{anthThinkText, anthThinkSig, anthOpaqueData, anthToolArgs, anthToolResult, anthVisibleText}
	msgs := []string{
		anthFail("thinking_block", "present=false"),
		anthFail("redacted_thinking_block", "present=false"),
		anthFail("tool_use", "id_mismatch"),
		anthFail("block_order", "thinking_idx=-1"),
		anthFail("http_status", "status=500 body_bytes=120"),
		anthFail("stream_incomplete", "len=0"),
		fmt.Sprintf("preserve must not duplicate; field=block_counts thinking=%d redacted=%d tool_use=%d", 2, 2, 2),
	}
	for _, msg := range msgs {
		for _, needle := range sentinels {
			if strings.Contains(msg, needle) {
				t.Fatalf("validator failure leaked sentinel; field=fail_message")
			}
		}
	}
}

func anthFail(field, detail string) string {
	return fmt.Sprintf("anthropic_e2e field=%s detail=%s", field, detail)
}

func anthropicErrorMeta(raw []byte) (string, int, string) {
	var w struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return "unmarshal_error", 0, "unmarshal"
	}
	typ := w.Error.Type
	if typ == "" {
		typ = w.Type
	}
	msg := w.Error.Message
	class := "other"
	switch {
	case strings.Contains(msg, "script exhausted"):
		class = "script_exhausted"
	case strings.Contains(msg, "thinking"):
		class = "thinking"
	case strings.Contains(msg, "tool"):
		class = "tool"
	case strings.Contains(msg, "API key"), strings.Contains(msg, "api_key"), strings.Contains(msg, "authentication"):
		class = "auth"
	case strings.Contains(msg, "model"):
		class = "model"
	case strings.Contains(msg, "route"), strings.Contains(msg, "backend"):
		class = "routing"
	case strings.Contains(msg, "capability"), strings.Contains(msg, "unsupported"):
		class = "capability"
	case strings.Contains(msg, "marshal"):
		class = "marshal"
	case strings.Contains(msg, "decode"), strings.Contains(msg, "parse"):
		class = "decode"
	case strings.Contains(msg, "opaque"), strings.Contains(msg, "redacted"):
		class = "opaque"
	case strings.Contains(msg, "signature"):
		class = "signature"
	case strings.Contains(msg, "internal"):
		class = "internal"
	}
	return typ, len(msg), class
}

func anthropicLookupTool() []any {
	return []any{
		map[string]any{
			"name":        anthToolName,
			"description": "lookup",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"q": map[string]any{"type": "string"}},
			},
		},
	}
}

func assertAnthropicStreamComplete(t *testing.T, body []byte) {
	t.Helper()
	if !strings.Contains(string(body), "message_stop") {
		t.Fatalf("%s", anthFail("stream_incomplete", fmt.Sprintf("len=%d", len(body))))
	}
}

func assertAnthropicStreamHasTypes(t *testing.T, body []byte, types ...string) {
	t.Helper()
	s := string(body)
	for _, typ := range types {
		if !strings.Contains(s, `"type":"`+typ+`"`) {
			t.Fatalf("%s", anthFail("stream_type_"+typ, "present=false"))
		}
	}
}

func assertAnthropicBackendAssistantOrder(t *testing.T, body []byte, requireExactPayload bool) {
	t.Helper()
	blocks := assistantContentBlocks(t, body)
	ti := firstBlockIndex(blocks, "thinking")
	ri := firstBlockIndex(blocks, "redacted_thinking")
	xi := firstBlockIndex(blocks, "text")
	ui := firstBlockIndex(blocks, "tool_use")
	if ti < 0 || ri < 0 || xi < 0 || ui < 0 {
		t.Fatalf("%s", anthFail("block_presence", fmt.Sprintf("thinking=%v redacted=%v text=%v tool_use=%v", ti >= 0, ri >= 0, xi >= 0, ui >= 0)))
	}
	if ti >= ri || ri >= xi || xi >= ui {
		t.Fatalf("%s", anthFail("block_order", fmt.Sprintf("thinking_idx=%d redacted_idx=%d text_idx=%d tool_idx=%d", ti, ri, xi, ui)))
	}
	think := blocks[ti]
	if requireExactPayload {
		if strField(think, "thinking") != anthThinkText || strField(think, "signature") != anthThinkSig {
			t.Fatalf("%s", anthFail("thinking_payload", "mismatch"))
		}
		if strField(blocks[ri], "data") != anthOpaqueData {
			t.Fatalf("%s", anthFail("redacted_payload", "mismatch"))
		}
		if strField(blocks[xi], "text") != anthVisibleText {
			t.Fatalf("%s", anthFail("visible_text", "mismatch"))
		}
	}
	tool := blocks[ui]
	if strField(tool, "id") != anthToolID || strField(tool, "name") != anthToolName {
		t.Fatalf("%s", anthFail("tool_use", "id_or_name_mismatch"))
	}
	rawInput, _ := json.Marshal(tool["input"])
	var want any
	_ = json.Unmarshal([]byte(anthToolArgs), &want)
	wantRaw, _ := json.Marshal(want)
	if !bytes.Equal(rawInput, wantRaw) {
		t.Fatalf("%s", anthFail("tool_use", "args_mismatch"))
	}
}

func assertAnthropicBackendToolResult(t *testing.T, body []byte, toolUseID string) {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("%s", anthFail("backend_body", "unmarshal_error"))
	}
	found := false
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		var blocks []map[string]any
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if strField(b, "type") != "tool_result" {
				continue
			}
			if strField(b, "tool_use_id") != toolUseID {
				t.Fatalf("%s", anthFail("tool_result", "id_mismatch"))
			}
			got := normalizeToolResultContent(b["content"])
			if got != anthToolResult {
				t.Fatalf("%s", anthFail("tool_result", "result_mismatch"))
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("%s", anthFail("tool_result", "present=false"))
	}
}

func normalizeToolResultContent(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, item := range x {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strField(m, "type") == "text" {
				b.WriteString(strField(m, "text"))
			}
		}
		return b.String()
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		var blocks []map[string]any
		if err := json.Unmarshal(raw, &blocks); err == nil {
			var b strings.Builder
			for _, m := range blocks {
				if strField(m, "type") == "text" {
					b.WriteString(strField(m, "text"))
				}
			}
			return b.String()
		}
		return ""
	}
}

func assistantContentBlocks(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("%s", anthFail("backend_body", "unmarshal_error"))
	}
	for _, m := range req.Messages {
		if m.Role != "assistant" {
			continue
		}
		var blocks []map[string]any
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			t.Fatalf("%s", anthFail("assistant_content", "unmarshal_error"))
		}
		return blocks
	}
	t.Fatalf("%s", anthFail("assistant_message", "present=false"))
	return nil
}

func countBlockType(blocks []map[string]any, typ string) int {
	n := 0
	for _, b := range blocks {
		if strField(b, "type") == typ {
			n++
		}
	}
	return n
}

func firstBlockIndex(blocks []map[string]any, typ string) int {
	for i, b := range blocks {
		if strField(b, "type") == typ {
			return i
		}
	}
	return -1
}

func strField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func anthropicBody(stream bool, messages []any, tools []any) []byte {
	m := map[string]any{
		"model":      rpE2EAnthModel,
		"max_tokens": 64,
		"messages":   messages,
		"stream":     stream,
	}
	if len(tools) > 0 {
		m["tools"] = tools
	}
	b, _ := json.Marshal(m)
	return b
}

func postAnthropic(ctx context.Context, t *testing.T, stack *rpHTTPStack, sid, tok string, body []byte) (string, string, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stack.proxyURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", rpE2EAnthKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if sid != "" {
		req.Header.Set(sessionwire.HeaderAuthoritativeSessionID, sid)
	}
	if tok != "" {
		req.Header.Set(sessionwire.HeaderResumeToken, tok)
	}
	resp, err := stack.proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		errType, errMsgLen, errClass := anthropicErrorMeta(raw)
		t.Fatalf("%s", anthFail("http_status", fmt.Sprintf("status=%d body_bytes=%d err_type=%s err_msg_len=%d err_class=%s", resp.StatusCode, len(raw), errType, errMsgLen, errClass)))
	}
	outSID := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderAuthoritativeSessionID))
	outTok := strings.TrimSpace(resp.Header.Get(sessionwire.HeaderResumeToken))
	if outSID == "" {
		outSID = sid
	}
	if outTok == "" {
		outTok = tok
	}
	return outSID, outTok, raw
}
