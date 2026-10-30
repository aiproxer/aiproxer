package reasoninge2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
)

const (
	DialectOpenAIChatTextV1            = "openai.chat.reasoning_text.v1"
	DialectAnthropicThinkingV1         = "anthropic.thinking.v1"
	DialectAnthropicRedactedThinkingV1 = "anthropic.redacted_thinking.v1"
)

// ChatSessionCarriers holds authoritative session resume material from proxy responses.
type ChatSessionCarriers struct {
	SessionID   string
	ResumeToken string
}

// ChatWireClient is a stateful raw-wire OpenAI Chat Completions client for E2E drivers.
type ChatWireClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Model      string
	// Route, when non-empty, sets X-AIP-Route on each request (overrides proxy default_route).
	Route    string
	Carriers ChatSessionCarriers
}

// ChatTurnResponse is the parsed proxy response for one chat turn.
type ChatTurnResponse struct {
	Status      int
	RawBody     []byte
	ContentType string
	Stream      bool
	VisibleText string
	Reasoning   []ReasoningBlock
	Tool        *ToolExchange
	Carriers    ChatSessionCarriers
}

// ChatCompletionRequest is a minimal wire request body.
type ChatCompletionRequest struct {
	Model    string           `json:"model"`
	Stream   bool             `json:"stream,omitempty"`
	Messages []map[string]any `json:"messages"`
	Tools    []map[string]any `json:"tools,omitempty"`
}

// PostChatCompletion sends one raw chat/completions request and updates carriers.
func (c *ChatWireClient) PostChatCompletion(ctx context.Context, stream bool, messages []map[string]any, tools []map[string]any) (ChatTurnResponse, error) {
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	body := ChatCompletionRequest{Model: c.Model, Stream: stream, Messages: messages, Tools: tools}
	raw, err := json.Marshal(body)
	if err != nil {
		return ChatTurnResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return ChatTurnResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if c.Route != "" {
		req.Header.Set("X-AIP-Route", c.Route)
	}
	if c.Carriers.SessionID != "" {
		req.Header.Set(sessionwire.HeaderAuthoritativeSessionID, c.Carriers.SessionID)
	}
	if c.Carriers.ResumeToken != "" {
		req.Header.Set(sessionwire.HeaderResumeToken, c.Carriers.ResumeToken)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ChatTurnResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatTurnResponse{}, err
	}
	out := ChatTurnResponse{
		Status:      resp.StatusCode,
		RawBody:     respBody,
		ContentType: resp.Header.Get("Content-Type"),
		Stream:      stream,
		Carriers: ChatSessionCarriers{
			SessionID:   strings.TrimSpace(resp.Header.Get(sessionwire.HeaderAuthoritativeSessionID)),
			ResumeToken: strings.TrimSpace(resp.Header.Get(sessionwire.HeaderResumeToken)),
		},
	}
	if out.Carriers.SessionID != "" {
		c.Carriers.SessionID = out.Carriers.SessionID
	}
	if out.Carriers.ResumeToken != "" {
		c.Carriers.ResumeToken = out.Carriers.ResumeToken
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("chat wire: status=%d body_bytes=%d", resp.StatusCode, len(respBody))
	}
	if stream {
		parsed, err := ParseChatSSEAssistant(respBody)
		if err != nil {
			return out, err
		}
		out.VisibleText = parsed.VisibleText
		out.Reasoning = parsed.Reasoning
		out.Tool = parsed.Tool
		return out, nil
	}
	parsed, err := ParseChatJSONAssistant(respBody)
	if err != nil {
		return out, err
	}
	out.VisibleText = parsed.VisibleText
	out.Reasoning = parsed.Reasoning
	out.Tool = parsed.Tool
	return out, nil
}

// AssistantTurnToChatMessage converts a planned/submitted assistant turn to wire JSON.
func AssistantTurnToChatMessage(turn AssistantTurn) map[string]any {
	msg := map[string]any{"role": "assistant", "content": turn.VisibleText}
	if len(turn.Reasoning) > 0 {
		var b strings.Builder
		for _, r := range turn.Reasoning {
			b.WriteString(r.Text)
		}
		msg["reasoning_content"] = b.String()
	}
	if turn.Tool != nil {
		msg["tool_calls"] = []map[string]any{{
			"id":   turn.Tool.ID,
			"type": "function",
			"function": map[string]any{
				"name":      turn.Tool.Name,
				"arguments": turn.Tool.Arguments,
			},
		}}
	}
	return msg
}

// ToolResultMessage builds a role=tool wire message.
func ToolResultMessage(tool ToolExchange) map[string]any {
	return map[string]any{
		"role":         "tool",
		"tool_call_id": tool.ID,
		"content":      tool.Result,
	}
}

// UserMessage builds a role=user wire message.
func UserMessage(text string) map[string]any {
	return map[string]any{"role": "user", "content": text}
}

// ParseChatJSONAssistant extracts assistant fields from a non-stream chat.completion body.
func ParseChatJSONAssistant(body []byte) (AssistantTurn, error) {
	var wrap struct {
		Choices []struct {
			Message struct {
				Content          any             `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return AssistantTurn{}, fmt.Errorf("chat json: %w", err)
	}
	if len(wrap.Choices) == 0 {
		return AssistantTurn{}, fmt.Errorf("chat json: no choices")
	}
	m := wrap.Choices[0].Message
	text, err := contentToString(m.Content)
	if err != nil {
		return AssistantTurn{}, err
	}
	out := AssistantTurn{VisibleText: text}
	reason := m.ReasoningContent
	if reason == "" {
		reason = m.Reasoning
	}
	if reason != "" {
		out.Reasoning = []ReasoningBlock{{Dialect: DialectOpenAIChatTextV1, Text: reason}}
	}
	tool, err := parseToolCalls(m.ToolCalls)
	if err != nil {
		return AssistantTurn{}, err
	}
	out.Tool = tool
	return out, nil
}

// ParseChatSSEAssistant aggregates assistant fields from a chat.completion.chunk SSE body.
func ParseChatSSEAssistant(body []byte) (AssistantTurn, error) {
	var text, reason strings.Builder
	toolID, toolName := "", ""
	var args strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return AssistantTurn{}, fmt.Errorf("chat sse: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		text.WriteString(d.Content)
		if d.ReasoningContent != "" {
			reason.WriteString(d.ReasoningContent)
		} else if d.Reasoning != "" {
			reason.WriteString(d.Reasoning)
		}
		for _, tc := range d.ToolCalls {
			if tc.ID != "" {
				toolID = tc.ID
			}
			if tc.Function.Name != "" {
				toolName = tc.Function.Name
			}
			args.WriteString(tc.Function.Arguments)
		}
	}
	if err := sc.Err(); err != nil {
		return AssistantTurn{}, err
	}
	out := AssistantTurn{VisibleText: text.String(), Streaming: true}
	if reason.Len() > 0 {
		out.Reasoning = []ReasoningBlock{{Dialect: DialectOpenAIChatTextV1, Text: reason.String()}}
	}
	if toolID != "" || toolName != "" || args.Len() > 0 {
		out.Tool = &ToolExchange{ID: toolID, Name: toolName, Arguments: args.String()}
	}
	return out, nil
}

// ChatResponseFromTurn maps a wire client response into a ClientEmulator ChatResponse.
func ChatResponseFromTurn(resp ChatTurnResponse) ChatResponse {
	return ChatResponse{
		VisibleText: resp.VisibleText,
		Reasoning:   cloneBlocks(resp.Reasoning),
		Tool:        cloneTool(resp.Tool),
		Streaming:   resp.Stream,
	}
}

// ObserveChatBackendRequest parses a backend-bound chat/completions body into oracle input.
// turnIDs must align with assistant history order (may be shorter than plan).
func ObserveChatBackendRequest(body []byte, turnIDs []string) (BackendRequestObservation, error) {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return BackendRequestObservation{}, fmt.Errorf("backend body: %w", err)
	}
	var out BackendRequestObservation
	idx := 0
	for i := 0; i < len(req.Messages); i++ {
		var probe struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(req.Messages[i], &probe); err != nil {
			return BackendRequestObservation{}, err
		}
		if probe.Role != "assistant" {
			continue
		}
		var msg struct {
			Content          any             `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
			ToolCalls        json.RawMessage `json:"tool_calls"`
		}
		if err := json.Unmarshal(req.Messages[i], &msg); err != nil {
			return BackendRequestObservation{}, err
		}
		text, err := contentToString(msg.Content)
		if err != nil {
			return BackendRequestObservation{}, err
		}
		turnID := ""
		if idx < len(turnIDs) {
			turnID = turnIDs[idx]
		}
		obs := BackendTurnObservation{TurnID: turnID, VisibleText: text}
		reason := msg.ReasoningContent
		if reason == "" {
			reason = msg.Reasoning
		}
		if reason != "" {
			obs.Reasoning = []ReasoningBlock{{Dialect: DialectOpenAIChatTextV1, Text: reason}}
		}
		tool, err := parseToolCalls(msg.ToolCalls)
		if err != nil {
			return BackendRequestObservation{}, err
		}
		if tool != nil && i+1 < len(req.Messages) {
			var next struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
				Content    any    `json:"content"`
			}
			if err := json.Unmarshal(req.Messages[i+1], &next); err == nil && next.Role == "tool" && next.ToolCallID == tool.ID {
				res, _ := contentToString(next.Content)
				tool.Result = res
			}
		}
		obs.Tool = tool
		out.AssistantTurns = append(out.AssistantTurns, obs)
		idx++
	}
	return out, nil
}

func contentToString(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

func parseToolCalls(raw json.RawMessage) (*ToolExchange, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var calls []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil, fmt.Errorf("tool_calls: %w", err)
	}
	if len(calls) == 0 {
		return nil, nil
	}
	return &ToolExchange{
		ID:        calls[0].ID,
		Name:      calls[0].Function.Name,
		Arguments: calls[0].Function.Arguments,
	}, nil
}
