package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type Client struct {
	Config        Config
	TokenProvider TokenProvider
	HTTPClient    *http.Client
}

// HTTPError is a bounded Cohere HTTP failure. StatusCode drives provider
// quota classification; RetryAfter carries the verbatim Retry-After response
// header for threshold-gated rotation (empty when the provider sent no
// hint). The message text is secret-sanitized at construction, matching the
// historical plain-error strings, so errors never carry key material.
type HTTPError struct {
	StatusCode int
	RetryAfter string
	Message    string
}

func (e *HTTPError) Error() string { return e.Message }

type cohereMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []cohereToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolPlan   string           `json:"tool_plan,omitempty"`
}

type cohereChatRequest struct {
	Model      string          `json:"model"`
	Messages   []cohereMessage `json:"messages"`
	Stream     bool            `json:"stream"`
	MaxTokens  *uint32         `json:"max_tokens,omitempty"`
	Tools      []cohereToolDef `json:"tools,omitempty"`
	ToolChoice *string         `json:"tool_choice,omitempty"`
}

type cohereMessageContent struct {
	Text string
}

func (mc *cohereMessageContent) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		mc.Text = str
		return nil
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &arr); err == nil {
		var sb strings.Builder
		for _, p := range arr {
			sb.WriteString(p.Text)
		}
		mc.Text = sb.String()
		return nil
	}
	return nil
}

type cohereChatResponse struct {
	ID      string `json:"id"`
	Message struct {
		Role      string               `json:"role"`
		Content   cohereMessageContent `json:"content"`
		ToolCalls []cohereToolCall     `json:"tool_calls"`
		ToolPlan  string               `json:"tool_plan"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

func sanitizeSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}

func (c *Client) Open(ctx context.Context, call aipapi.Call, targetModel string) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses ||
		call.Invocation.Operation == aipapi.OperationOpenResponsesCreate {
		return nil, fmt.Errorf("cohere: responses operations are not supported")
	}

	targetModel = strings.TrimSpace(targetModel)
	if targetModel == "" {
		return nil, fmt.Errorf("cohere: model is required")
	}

	var messages []cohereMessage
	for _, inst := range call.Instructions {
		var sb strings.Builder
		for _, p := range inst.Parts {
			if p.Kind != aipapi.PartText {
				return nil, fmt.Errorf("cohere: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
			}
			sb.WriteString(p.Text)
		}
		messages = append(messages, cohereMessage{Role: "system", Content: sb.String()})
	}

	for _, msg := range call.Messages {
		switch msg.Role {
		case aipapi.RoleSystem:
			var sb strings.Builder
			for _, p := range msg.Parts {
				if p.Kind != aipapi.PartText {
					return nil, fmt.Errorf("cohere: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
				}
				sb.WriteString(p.Text)
			}
			messages = append(messages, cohereMessage{Role: "system", Content: sb.String()})
		case aipapi.RoleUser:
			var sb strings.Builder
			for _, p := range msg.Parts {
				if p.Kind != aipapi.PartText {
					return nil, fmt.Errorf("cohere: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
				}
				sb.WriteString(p.Text)
			}
			messages = append(messages, cohereMessage{Role: "user", Content: sb.String()})
		case aipapi.RoleAssistant:
			assistant, err := buildAssistantMessage(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, assistant)
		case aipapi.RoleTool:
			toolMsgs, err := buildToolMessages(msg)
			if err != nil {
				return nil, err
			}
			messages = append(messages, toolMsgs...)
		default:
			return nil, fmt.Errorf("cohere: unsupported message role %q", msg.Role)
		}
	}

	isStreaming := call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != aipapi.TransportModeNonStreaming

	chatReq := cohereChatRequest{
		Model:    targetModel,
		Messages: messages,
		Stream:   isStreaming,
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		mt := uint32(*call.Options.MaxOutputTokens)
		chatReq.MaxTokens = &mt
	}
	tools, err := buildCohereTools(call.Tools)
	if err != nil {
		return nil, err
	}
	chatReq.Tools = tools
	toolChoice, err := cohereToolChoiceValue(call.ToolChoice, len(call.Tools))
	if err != nil {
		return nil, err
	}
	chatReq.ToolChoice = toolChoice

	bodyBytes, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("cohere: marshal chat request: %w", err)
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("cohere: get token: %w", err)
	}

	reqURL := fmt.Sprintf("%s/v2/chat", c.Config.Origin())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("cohere: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if isStreaming {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	httpReq.Header.Set("Authorization", "Bearer "+tok)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("cohere: do chat request: %s", sanitizeSecret(err.Error(), tok))
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			RetryAfter: resp.Header.Get("Retry-After"),
			Message:    fmt.Sprintf("cohere: chat request failed (status %d): %s", resp.StatusCode, sanitizeSecret(string(respBody), tok)),
		}
	}

	if isStreaming {
		return aipapi.CloseOnlyManagedStream{Stream: newSSEChatStream(ctx, resp.Body)}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cohere: read chat response: %w", err)
	}

	var cr cohereChatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("cohere: unmarshal chat response: %w", err)
	}

	text := cr.Message.Content.Text
	events, err := cohereResponseEvents(text, cr.Message.ToolPlan, cr.Message.ToolCalls, cr.FinishReason)
	if err != nil {
		return nil, err
	}

	return aipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

type sliceStream struct {
	mu     sync.Mutex
	events []aipapi.Event
	idx    int
	closed bool
}

func (s *sliceStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("cohere: stream closed")
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type sseChatStream struct {
	ctx      context.Context
	body     io.ReadCloser
	scanner  *bufio.Scanner
	mu       sync.Mutex
	started  bool
	msgStart bool
	finished bool
	closed   bool
	pending  []aipapi.Event
	// toolIDs correlates stream indices to tool call IDs announced by
	// tool-call-start events; toolOrder preserves first-seen order so an
	// unterminated stream can still close open calls deterministically.
	toolIDs map[int]string
	// pendingArgs buffers arguments deltas that arrive before their
	// tool-call-start event, flushed in order when the start arrives.
	pendingArgs map[int][]string
	toolOrder   []int
}

func newSSEChatStream(ctx context.Context, body io.ReadCloser) *sseChatStream {
	return &sseChatStream{
		ctx:     ctx,
		body:    body,
		scanner: bufio.NewScanner(body),
	}
}

// ensureMsgStarted queues the message frame ahead of the first content-class
// event so streamed tool calls stay Collect-compatible like text deltas.
func (s *sseChatStream) ensureMsgStarted() {
	if !s.msgStart {
		s.msgStart = true
		s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventMessageStarted})
	}
}

func (s *sseChatStream) finishOpenToolCalls() {
	for _, idx := range s.toolOrder {
		if id := s.toolIDs[idx]; id != "" {
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: id})
		}
	}
	s.toolIDs = nil
	s.pendingArgs = nil
	s.toolOrder = nil
}

func (s *sseChatStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return aipapi.Event{}, fmt.Errorf("cohere: stream closed")
	}

	if !s.started {
		s.started = true
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
		if !s.scanner.Scan() {
			break
		}
		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		line := strings.TrimSpace(s.scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var ev struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Message struct {
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
					ToolPlan  string          `json:"tool_plan"`
					ToolCalls json.RawMessage `json:"tool_calls"`
				} `json:"message"`
				Text         string `json:"text"`
				FinishReason string `json:"finish_reason"`
			} `json:"delta"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		s.handleStreamEvent(ev.Type, ev.Index, ev.Delta.Message.Content.Text, ev.Delta.Message.ToolPlan,
			ev.Delta.Message.ToolCalls, ev.Delta.Text, ev.Text, ev.Delta.FinishReason)
	}

	if err := s.scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return aipapi.Event{}, err
	}

	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}

	if len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		return ev, nil
	}

	if !s.finished {
		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		s.finished = true
		s.finishOpenToolCalls()
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			// Keep the terminal event for the next Recv: requeue it behind
			// the tool finishes so lifecycles close before the response ends.
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished})
			return ev, nil
		}
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	}

	return aipapi.Event{}, io.EOF
}

// handleStreamEvent routes one Cohere v2 SSE event onto canonical pending
// events: text deltas, tool-plan deltas (reasoning channel), ID-correlated
// tool-call start/args/finish lifecycles, and the terminal message-end.
func (s *sseChatStream) handleStreamEvent(
	eventType string,
	index int,
	contentText, toolPlan string,
	toolCalls json.RawMessage,
	deltaText, text, finishReason string,
) {
	switch normalizeCohereStreamType(eventType) {
	case "tool-plan-delta":
		if toolPlan != "" {
			s.ensureMsgStarted()
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: toolPlan})
		}
		return
	case "tool-call-start":
		for _, tc := range parseCohereStreamToolCalls(toolCalls) {
			id := strings.TrimSpace(tc.ID)
			if id == "" {
				continue
			}
			s.ensureMsgStarted()
			if s.toolIDs == nil {
				s.toolIDs = make(map[int]string)
			}
			if _, seen := s.toolIDs[index]; !seen {
				s.toolOrder = append(s.toolOrder, index)
			}
			s.toolIDs[index] = id
			s.pending = append(s.pending, aipapi.Event{
				Kind: aipapi.EventToolCallStarted, ToolCallID: id, ToolName: tc.Function.Name,
			})
			for _, args := range s.pendingArgs[index] {
				s.pending = append(s.pending, aipapi.Event{
					Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: args,
				})
			}
			delete(s.pendingArgs, index)
		}
		return
	case "tool-call-delta":
		for _, tc := range parseCohereStreamToolCalls(toolCalls) {
			if tc.Function.Arguments == "" {
				continue
			}
			s.ensureMsgStarted()
			if id := s.toolIDs[index]; id != "" {
				s.pending = append(s.pending, aipapi.Event{
					Kind: aipapi.EventToolCallArgsDelta, ToolCallID: id, Delta: tc.Function.Arguments,
				})
				continue
			}
			if s.pendingArgs == nil {
				s.pendingArgs = make(map[int][]string)
			}
			s.pendingArgs[index] = append(s.pendingArgs[index], tc.Function.Arguments)
		}
		return
	case "tool-call-end":
		if id := s.toolIDs[index]; id != "" {
			delete(s.toolIDs, index)
			s.pending = append(s.pending, aipapi.Event{
				Kind: aipapi.EventToolCallFinished, ToolCallID: id,
			})
		}
		return
	case "message-end":
		if !s.finished {
			s.finished = true
			s.finishOpenToolCalls()
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: finishReason})
		}
		return
	}

	delta := contentText
	if delta == "" {
		delta = deltaText
	}
	if delta == "" {
		delta = text
	}
	if delta != "" {
		s.ensureMsgStarted()
		s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: delta})
	}
}

func (s *sseChatStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

type cohereModelItem struct {
	Name          string   `json:"name"`
	Endpoints     []string `json:"endpoints"`
	ContextLength int      `json:"context_length"`
}

type cohereListModelsResponse struct {
	Models []cohereModelItem `json:"models"`
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: get token: %w", err)
	}

	reqURL := fmt.Sprintf("%s/v1/models?endpoint=chat", c.Config.Origin())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: create list models request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: do list models request: %s", sanitizeSecret(err.Error(), tok))
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: read list models response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return backendplugin.ListModelsResponse{}, &HTTPError{
			StatusCode: resp.StatusCode,
			RetryAfter: resp.Header.Get("Retry-After"),
			Message:    fmt.Sprintf("cohere: list models failed (status %d): %s", resp.StatusCode, sanitizeSecret(string(bodyBytes), tok)),
		}
	}

	var lmResp cohereListModelsResponse
	if err := json.Unmarshal(bodyBytes, &lmResp); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("cohere: unmarshal list models response: %w", err)
	}

	dropPatterns := []string{"embed", "rerank", "image", "audio"}
	var out []backendplugin.ModelDescriptor

	for _, m := range lmResp.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		drop := false
		for _, p := range dropPatterns {
			if strings.Contains(lower, p) {
				drop = true
				break
			}
		}
		if drop {
			continue
		}

		if len(m.Endpoints) > 0 {
			hasChat := false
			for _, ep := range m.Endpoints {
				if strings.EqualFold(strings.TrimSpace(ep), "chat") {
					hasChat = true
					break
				}
			}
			if !hasChat {
				continue
			}
		}

		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + name,
			NativeModelID:    name,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}

	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}
