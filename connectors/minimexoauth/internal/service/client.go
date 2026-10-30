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

	"github.com/aiproxer/aiproxer/connector-support/oauthcred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Client handles MiniMax OAuth communication and Anthropic Messages inference.
type Client struct {
	Config               Config
	TokenProvider        TokenProvider
	OAuthSession         *oauthcred.Session
	HTTPClient           *http.Client
	accountingEvidenceV1 bool
}

func (c *Client) getHTTPClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// ListModels enumerates models from the MiniMax models inventory API.
// Successful enumeration is authoritative: the returned list contains exactly
// the discovered models. Empty success returns empty (no resurrection of
// previously seen models). All failures are explicit errors so the host
// runtime can apply its cached-enumeration fallback; this connector keeps no
// static model list.
func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if ctx == nil {
		return backendplugin.ListModelsResponse{}, errors.New("minimax-oauth: nil context")
	}
	if err := ctx.Err(); err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	if c == nil || c.TokenProvider == nil {
		return backendplugin.ListModelsResponse{}, errors.New("minimax-oauth: token provider not configured")
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return backendplugin.ListModelsResponse{}, ctx.Err()
		}
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: retrieve token for models inventory: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/models", strings.TrimRight(c.Config.InferenceBaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return backendplugin.ListModelsResponse{}, ctx.Err()
		}
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: list models request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errText := readBoundedError(resp, ErrorBodyLimit)
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: list models API failed (status %d): %s", resp.StatusCode, errText)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("minimax-oauth: decode list models JSON: %w", err)
	}

	var candidates []string
	for _, m := range payload.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			candidates = append(candidates, id)
		}
	}
	for _, m := range payload.Models {
		if id := strings.TrimSpace(m.ID); id != "" {
			candidates = append(candidates, id)
		}
	}

	fetchedMS := time.Now().UnixMilli()
	if len(candidates) == 0 {
		return backendplugin.ListModelsResponse{
			Models:          []backendplugin.ModelDescriptor{},
			InventorySource: FactoryKind,
			FetchedUnixMS:   fetchedMS,
		}, nil
	}

	seen := make(map[string]struct{})
	result := make([]backendplugin.ModelDescriptor, 0, len(candidates))
	for _, cID := range candidates {
		native := strings.TrimSpace(cID)
		if after, ok := strings.CutPrefix(native, FactoryKind+"/"); ok {
			native = strings.TrimSpace(after)
		}
		if native == "" {
			continue
		}
		canonical := FactoryKind + "/" + native
		if _, ok := seen[canonical]; !ok {
			seen[canonical] = struct{}{}
			result = append(result, backendplugin.ModelDescriptor{
				CanonicalModelID: canonical,
				NativeModelID:    native,
				DisplayName:      "MiniMax (" + native + ")",
				FactoryKind:      FactoryKind,
				Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
			})
		}
		if limit > 0 && uint32(len(result)) >= limit {
			break
		}
	}

	return backendplugin.ListModelsResponse{
		Models:          result,
		InventorySource: FactoryKind,
		FetchedUnixMS:   fetchedMS,
	}, nil
}

type messageItem struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// Execute opens an Anthropic Messages inference stream towards MiniMax.
func (c *Client) Execute(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call, requestedModel string) (aipapi.ManagedEventStream, error) {
	// Hard negatives:
	if inv.Operation == string(aipapi.OperationOpenAIResponses) ||
		inv.Operation == string(aipapi.OperationOpenResponsesCreate) {
		return nil, fmt.Errorf("minimax-oauth: responses operations are not supported; anthropic messages transport only")
	}
	if inv.Operation == "agent_control" || strings.Contains(strings.ToLower(inv.Operation), "acp") {
		return nil, errors.New("minimax-oauth: ACP is not supported; inference connector only")
	}

	model := strings.TrimSpace(requestedModel)
	if model == "" || model == FactoryKind {
		model = ModelM27
	}
	if after, ok := strings.CutPrefix(model, FactoryKind+"/"); ok {
		model = after
	}

	isStreaming := inv.DeliveryMode != string(aipapi.DeliveryModeNonStreaming)

	msgs, systemPrompt := extractAnthropicMessages(call, inv)

	reqPayload := map[string]any{
		"model":      model,
		"messages":   msgs,
		"max_tokens": 8192,
		"stream":     isStreaming,
	}
	if systemPrompt != "" {
		reqPayload["system"] = systemPrompt
	}

	// Tools support (standard Anthropic wire format)
	tools := extractTools(call, inv)
	if len(tools) > 0 {
		reqPayload["tools"] = tools
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: marshal payload: %w", err)
	}

	if c.TokenProvider == nil {
		return nil, errors.New("minimax-oauth: token provider not configured")
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: acquire token: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/messages", strings.TrimRight(c.Config.InferenceBaseURL, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("anthropic-version", "2023-06-01")
	if isStreaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("minimax-oauth: inference request: %w", err)
	}

	// Transient 401 retry once with refreshed token
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		newTok, rerr := c.TokenProvider.ForceRefresh(ctx)
		if rerr != nil {
			return nil, fmt.Errorf("minimax-oauth: token refresh after 401 failed: %w", rerr)
		}

		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+newTok)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", DefaultUserAgent)
		req2.Header.Set("anthropic-version", "2023-06-01")
		if isStreaming {
			req2.Header.Set("Accept", "text/event-stream")
		} else {
			req2.Header.Set("Accept", "application/json")
		}

		resp, err = c.getHTTPClient().Do(req2)
		if err != nil {
			return nil, fmt.Errorf("minimax-oauth: retry inference request: %w", err)
		}
	}

	// Entitlement 403 fails closed immediately without refresh
	if resp.StatusCode == http.StatusForbidden {
		defer func() { _ = resp.Body.Close() }()
		errText := readBoundedError(resp, ErrorBodyLimit)
		return nil, fmt.Errorf("minimax-oauth: entitlement error (status 403): %s", errText)
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		errText := readBoundedError(resp, ErrorBodyLimit)
		return nil, fmt.Errorf("minimax-oauth: inference failed (status %d): %s", resp.StatusCode, errText)
	}

	if isStreaming {
		stream := newAnthropicManagedSSEStream(resp)
		stream.SetEnabled(c.accountingEvidenceV1)
		return stream, nil
	}

	stream, err := newUnaryAnthropicStream(resp)
	if err != nil {
		return nil, err
	}
	stream.SetEnabled(c.accountingEvidenceV1)
	return stream, nil
}

func extractAnthropicMessages(call aipapi.Call, inv backendplugin.Invocation) ([]messageItem, string) {
	var items []messageItem
	var systemPrompt string

	if len(call.Messages) > 0 {
		for _, m := range call.Messages {
			if m.Role == aipapi.RoleSystem {
				for _, p := range m.Parts {
					if p.Kind == aipapi.PartText && p.Text != "" {
						if systemPrompt != "" {
							systemPrompt += "\n\n"
						}
						systemPrompt += p.Text
					}
				}
				continue
			}
			role := "user"
			if m.Role == aipapi.RoleAssistant {
				role = "assistant"
			}
			var sb strings.Builder
			for _, p := range m.Parts {
				if p.Kind == aipapi.PartText && p.Text != "" {
					sb.WriteString(p.Text)
				}
			}
			items = append(items, messageItem{Role: role, Content: sb.String()})
		}
	} else {
		for _, m := range inv.Messages {
			if m.Role == backendplugin.RoleSystem {
				for _, p := range m.Parts {
					if p.Kind == backendplugin.PartKindText && p.Text != nil {
						if systemPrompt != "" {
							systemPrompt += "\n\n"
						}
						systemPrompt += *p.Text
					}
				}
				continue
			}
			role := "user"
			if m.Role == backendplugin.RoleAssistant {
				role = "assistant"
			}
			var sb strings.Builder
			for _, p := range m.Parts {
				if p.Kind == backendplugin.PartKindText && p.Text != nil {
					sb.WriteString(*p.Text)
				}
			}
			items = append(items, messageItem{Role: role, Content: sb.String()})
		}
	}

	if len(items) == 0 {
		items = append(items, messageItem{Role: "user", Content: "Hello"})
	}
	return items, systemPrompt
}

func extractTools(call aipapi.Call, inv backendplugin.Invocation) []map[string]any {
	var tools []map[string]any
	for _, t := range call.Tools {
		if t.Name == "" {
			continue
		}
		tools = append(tools, map[string]any{
			"name":         t.Name,
			"description":  t.Description,
			"input_schema": t.Parameters,
		})
	}
	if len(tools) == 0 {
		for _, t := range inv.Tools {
			if t.Name == "" {
				continue
			}
			var schema any
			if b := t.ParametersJSON.Bytes(); len(b) > 0 {
				_ = json.Unmarshal(b, &schema)
			}
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": schema,
			})
		}
	}
	return tools
}

// anthropicManagedSSEStream processes SSE chunks from Anthropic proxy.
type anthropicManagedSSEStream struct {
	resp              *http.Response
	sc                *bufio.Scanner
	mu                sync.Mutex
	closed            bool
	started           bool
	msgStart          bool
	finished          bool
	done              bool
	pending           []aipapi.Event
	toolCalls         map[int]string
	providerUsage     aipapi.Event
	providerUsageSeen bool
	*backendplugin.UsageEvidenceBuffer
}

func newAnthropicManagedSSEStream(resp *http.Response) *anthropicManagedSSEStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &anthropicManagedSSEStream{
		resp: resp, sc: sc, toolCalls: make(map[int]string),
		UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer(),
	}
}

type anthropicUsageFields struct {
	InputTokens              *int                          `json:"input_tokens"`
	OutputTokens             *int                          `json:"output_tokens"`
	CacheCreationInputTokens *int                          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int                          `json:"cache_read_input_tokens"`
	ReasoningTokens          *int                          `json:"reasoning_tokens"`
	ThinkingTokens           *int                          `json:"thinking_tokens"`
	TotalTokens              *int                          `json:"total_tokens"`
	CacheCreation            *anthropicCacheCreationFields `json:"cache_creation"`
	ServerToolUse            *anthropicServerToolUseFields `json:"server_tool_use"`
	ServiceTier              string                        `json:"service_tier"`
}

type anthropicSSEPayload struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		ID    string               `json:"id"`
		Usage anthropicUsageFields `json:"usage"`
	} `json:"message"`
	Usage anthropicUsageFields `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	usagePresent bool
}

func (s *anthropicManagedSSEStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return s.canonicalUsageEvent(ev), nil
		}
		if s.done {
			return aipapi.Event{}, io.EOF
		}
		if !s.sc.Scan() {
			if err := s.sc.Err(); err != nil {
				s.flushUsage()
				return aipapi.Event{}, err
			}
			s.done = true
			s.emitFinishIfStarted("")
			if len(s.pending) > 0 {
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return s.canonicalUsageEvent(ev), nil
			}
			return aipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			s.done = true
			s.emitFinishIfStarted("")
			if len(s.pending) > 0 {
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return s.canonicalUsageEvent(ev), nil
			}
			continue
		}
		var p anthropicSSEPayload
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			continue
		}
		p.usagePresent = strings.Contains(data, `"usage"`)
		if err := s.handlePayload(p); err != nil {
			return aipapi.Event{}, err
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return s.canonicalUsageEvent(ev), nil
		}
	}
}

func (s *anthropicManagedSSEStream) ensureStarted() {
	if !s.started {
		s.started = true
		s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseStarted})
	}
	if !s.msgStart {
		s.msgStart = true
		s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventMessageStarted})
	}
}

func (s *anthropicManagedSSEStream) emitFinishIfStarted(reason string) {
	s.flushUsage()
	if s.finished || !s.started {
		return
	}
	s.finished = true
	s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: reason})
}

func (s *anthropicManagedSSEStream) handlePayload(p anthropicSSEPayload) error {
	switch p.Type {
	case "message_start":
		if !s.started {
			s.started = true
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		if ev := minimaxAnthropicUsageEvent(p.Message.Usage, p.usagePresent, p.Message.ID); ev != nil {
			s.addAnthropicUsage(*ev)
		}
	case "content_block_start":
		s.ensureStarted()
		if p.ContentBlock.Type == "tool_use" {
			toolID := p.ContentBlock.ID
			s.toolCalls[p.Index] = toolID
			s.pending = append(s.pending, aipapi.Event{
				Kind:       aipapi.EventToolCallStarted,
				ToolCallID: toolID,
				ToolName:   p.ContentBlock.Name,
			})
		}
	case "content_block_delta":
		s.ensureStarted()
		switch p.Delta.Type {
		case "text_delta":
			if p.Delta.Text != "" {
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: p.Delta.Text})
			}
		case "input_json_delta":
			if p.Delta.PartialJSON != "" {
				s.pending = append(s.pending, aipapi.Event{
					Kind:       aipapi.EventToolCallArgsDelta,
					ToolCallID: s.toolCalls[p.Index],
					Delta:      p.Delta.PartialJSON,
				})
			}
		case "thinking_delta":
			if p.Delta.Thinking != "" {
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: p.Delta.Thinking})
			}
		case "signature_delta":
			if p.Delta.Signature != "" {
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: p.Delta.Signature})
			}
		}
	case "content_block_stop":
		if toolID, ok := s.toolCalls[p.Index]; ok {
			delete(s.toolCalls, p.Index)
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: toolID})
		}
	case "message_delta":
		if ev := minimaxAnthropicUsageEvent(p.Usage, p.usagePresent, p.Message.ID); ev != nil {
			s.addAnthropicUsage(*ev)
		}
		if p.Delta.StopReason != "" {
			s.flushUsage()
			if s.finished {
				break
			}
			s.finished = true
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: p.Delta.StopReason})
		}
	case "message_stop":
		s.done = true
		s.emitFinishIfStarted("")
	case "error":
		s.flushUsage()
		return fmt.Errorf("anthropic stream error: %s (%s)", p.Error.Message, p.Error.Type)
	}
	return nil
}

func (s *anthropicManagedSSEStream) addAnthropicUsage(ev aipapi.Event) {
	// Keep the provider key until Recv. A negotiated sideband then projects the
	// canonical event as observer-only; an unnegotiated legacy host keeps the
	// original V1 durable path.
	s.pending = append(s.pending, ev)
	if s.providerUsageSeen {
		s.providerUsage = mergeAnthropicUsageEvent(s.providerUsage, ev)
	} else {
		s.providerUsage = ev
		s.providerUsageSeen = true
	}
	if strings.TrimSpace(ev.RawUsageJSON) != "" {
		s.providerUsage.RawUsageJSON = ev.RawUsageJSON
	}
}

func (s *anthropicManagedSSEStream) canonicalUsageEvent(ev aipapi.Event) aipapi.Event {
	if ev.Kind == aipapi.EventUsageDelta && s.UsageEvidenceBuffer != nil && s.AccountingEvidenceEnabled() {
		ev.Accounting.DedupeKey = ""
	}
	return ev
}

func (s *anthropicManagedSSEStream) flushUsage() {
	if s == nil || !s.providerUsageSeen || s.UsageEvidenceBuffer == nil {
		return
	}
	s.AddUsageEvent(s.providerUsage, "minimexoauth.anthropic:stream")
}

func (s *anthropicManagedSSEStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	err := s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly, Err: err}
}

func (s *anthropicManagedSSEStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.flushUsage()
	if s.resp != nil && s.resp.Body != nil {
		return s.resp.Body.Close()
	}
	return nil
}

// newUnaryAnthropicStream converts a unary JSON response into a managed event stream.
func newUnaryAnthropicStream(resp *http.Response) (*unaryAnthropicStream, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string               `json:"stop_reason"`
		Usage      anthropicUsageFields `json:"usage"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("minimax-oauth: decode unary anthropic response: %w", err)
	}

	var events []aipapi.Event
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseStarted})
	events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
	if ev := minimaxAnthropicUsageEvent(res.Usage, bytes.Contains(body, []byte(`"usage"`)), res.ID); ev != nil {
		events = append(events, *ev)
	}
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: c.Text})
		}
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: res.StopReason})

	return newUnaryAnthropicEventStream(events), nil
}

type unaryAnthropicStream struct {
	events []aipapi.Event
	idx    int
	mu     sync.Mutex
	closed bool
	*backendplugin.UsageEvidenceBuffer
}

func newUnaryAnthropicEventStream(events []aipapi.Event) *unaryAnthropicStream {
	u := &unaryAnthropicStream{events: append([]aipapi.Event(nil), events...), UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer()}
	var cumulative aipapi.Event
	seenUsage := false
	for _, ev := range events {
		if ev.Kind != aipapi.EventUsageDelta {
			continue
		}
		if seenUsage {
			cumulative = mergeAnthropicUsageEvent(cumulative, ev)
		} else {
			cumulative = ev
			seenUsage = true
		}
	}
	if seenUsage {
		u.AddUsageEvent(cumulative, "minimexoauth.anthropic:stream")
	}
	return u
}

func (u *unaryAnthropicStream) Recv(ctx context.Context) (aipapi.Event, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return aipapi.Event{}, io.EOF
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	if u.idx >= len(u.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := u.events[u.idx]
	u.idx++
	if ev.Kind == aipapi.EventUsageDelta && u.AccountingEvidenceEnabled() {
		ev.Accounting.DedupeKey = ""
	}
	return ev, nil
}

func (u *unaryAnthropicStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	_ = u.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (u *unaryAnthropicStream) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	return nil
}
