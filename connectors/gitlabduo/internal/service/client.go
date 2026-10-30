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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Client handles GitLab Duo communication for a single configured generation.
type Client struct {
	Config               Config
	TokenProvider        TokenProvider
	DirectAccessManager  DirectAccessManager
	HTTPClient           *http.Client
	accountingEvidenceV1 bool
}

func (c *Client) getHTTPClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// StaticModels returns the agentic chat models documented at the pin.
func StaticModels() []backendplugin.ModelDescriptor {
	return []backendplugin.ModelDescriptor{
		{
			CanonicalModelID: FactoryKind + "/" + ModelHaiku45,
			NativeModelID:    ModelHaiku45,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
		{
			CanonicalModelID: FactoryKind + "/" + ModelSonnet45,
			NativeModelID:    ModelSonnet45,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
		{
			CanonicalModelID: FactoryKind + "/" + ModelOpus45,
			NativeModelID:    ModelOpus45,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		},
	}
}

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLModelRef struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type graphQLResponse struct {
	Data *struct {
		AiChatAvailableModels *struct {
			DefaultModel     *graphQLModelRef  `json:"defaultModel"`
			SelectableModels []graphQLModelRef `json:"selectableModels"`
			PinnedModel      *graphQLModelRef  `json:"pinnedModel"`
		} `json:"aiChatAvailableModels"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

const aiChatAvailableModelsQuery = `query aiChatAvailableModels($rootNamespaceId: GroupID!) {
  aiChatAvailableModels(rootNamespaceId: $rootNamespaceId) {
    defaultModel { name ref }
    selectableModels { name ref }
    pinnedModel { name ref }
  }
}`

// ListModels enumerates models via GraphQL discovery when a namespace source
// (root_namespace_id or project_path) is configured. Successful discovery is
// authoritative: exactly the discovered models are advertised, with no static
// union, and every call re-discovers (no lifetime in-memory cache) so removals
// propagate on the next refresh. Failures are explicit errors so the host
// runtime can apply its cached-enumeration fallback. When no namespace source
// is configured, GraphQL discovery is unavailable and the documented static
// agentic chat models are served as the explicit no-discovery fallback.
func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if ctx == nil {
		return backendplugin.ListModelsResponse{}, errors.New("gitlab-duo: nil context")
	}
	if err := ctx.Err(); err != nil {
		return backendplugin.ListModelsResponse{}, err
	}

	if !discoveryConfigured(c.Config) {
		staticList := StaticModels()
		if limit > 0 && uint32(len(staticList)) > limit {
			staticList = staticList[:limit]
		}
		return backendplugin.ListModelsResponse{
			Models:          staticList,
			InventorySource: FactoryKind,
			FetchedUnixMS:   time.Now().UnixMilli(),
		}, nil
	}

	discovered, err := c.discoverModels(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return backendplugin.ListModelsResponse{}, ctx.Err()
		}
		return backendplugin.ListModelsResponse{}, err
	}
	if discovered == nil {
		discovered = []backendplugin.ModelDescriptor{}
	}
	if limit > 0 && uint32(len(discovered)) > limit {
		discovered = discovered[:limit]
	}
	return backendplugin.ListModelsResponse{
		Models:          discovered,
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

// discoveryConfigured reports whether a GraphQL discovery namespace source is
// present. Without root_namespace_id or project_path, discovery is skipped and
// the static fallback applies.
func discoveryConfigured(cfg Config) bool {
	return strings.TrimSpace(cfg.RootNamespaceID) != "" || strings.TrimSpace(cfg.ProjectPath) != ""
}

func (c *Client) resolveRootNamespaceID(ctx context.Context, token string) (string, bool, error) {
	if c.Config.RootNamespaceID != "" {
		id := c.Config.RootNamespaceID
		if !strings.HasPrefix(id, "gid://") {
			return fmt.Sprintf("gid://gitlab/Group/%s", id), true, nil
		}
		return id, true, nil
	}

	if c.Config.ProjectPath != "" {
		projURL := fmt.Sprintf("%s/api/v4/projects/%s", c.Config.GetInstanceURL(), url.QueryEscape(c.Config.ProjectPath))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, projURL, nil)
		if err != nil {
			return "", false, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", DefaultUserAgent)
		resp, err := c.getHTTPClient().Do(req)
		if err != nil {
			return "", false, fmt.Errorf("gitlab-duo: project lookup failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return "", false, fmt.Errorf("gitlab-duo: project lookup failed with status %d: %s", resp.StatusCode, string(body))
		}
		var proj struct {
			Namespace struct {
				ID int64 `json:"id"`
			} `json:"namespace"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&proj); err != nil {
			return "", false, fmt.Errorf("gitlab-duo: decode project response: %w", err)
		}
		if proj.Namespace.ID == 0 {
			return "", false, fmt.Errorf("gitlab-duo: project %s has no namespace id", c.Config.ProjectPath)
		}
		return fmt.Sprintf("gid://gitlab/Group/%d", proj.Namespace.ID), true, nil
	}

	// Neither root_namespace_id nor project_path configured.
	// Do not probe namespaces, do not invent Group/1. Return false to indicate no GraphQL discovery.
	return "", false, nil
}

func (c *Client) discoverModels(ctx context.Context) ([]backendplugin.ModelDescriptor, error) {
	if c.TokenProvider == nil {
		return nil, errors.New("gitlab-duo: token provider required for discovery")
	}

	token, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: auth token for discovery: %w", err)
	}

	rootNs, hasNs, err := c.resolveRootNamespaceID(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: resolve root namespace: %w", err)
	}
	if !hasNs {
		// Neither root_namespace_id nor project_path configured.
		// Skip GraphQL call completely.
		return nil, nil
	}

	endpoint := fmt.Sprintf("%s/api/graphql", c.Config.GetInstanceURL())
	gqlReq := graphQLRequest{
		Query: aiChatAvailableModelsQuery,
		Variables: map[string]any{
			"rootNamespaceId": rootNs,
		},
	}
	reqBytes, err := json.Marshal(gqlReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: discovery request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("gitlab-duo: discovery failed with status %d: %s", resp.StatusCode, string(body))
	}

	var gqlResp graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("gitlab-duo: decode discovery response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return nil, fmt.Errorf("gitlab-duo: discovery graphql error: %s", gqlResp.Errors[0].Message)
	}

	if gqlResp.Data == nil || gqlResp.Data.AiChatAvailableModels == nil {
		return nil, nil
	}

	aim := gqlResp.Data.AiChatAvailableModels
	var candidates []graphQLModelRef
	if aim.PinnedModel != nil && aim.PinnedModel.Ref != "" {
		candidates = append(candidates, *aim.PinnedModel)
	}
	for _, m := range aim.SelectableModels {
		if m.Ref != "" {
			candidates = append(candidates, m)
		}
	}
	if aim.DefaultModel != nil && aim.DefaultModel.Ref != "" {
		candidates = append(candidates, *aim.DefaultModel)
	}

	seen := make(map[string]struct{})
	var result []backendplugin.ModelDescriptor
	for _, m := range candidates {
		ref := strings.TrimSpace(m.Ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}

		dispName := m.Name
		if dispName == "" {
			dispName = "GitLab Duo (" + ref + ")"
		}

		result = append(result, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + ref,
			NativeModelID:    ref,
			DisplayName:      dispName,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
	}

	return result, nil
}

func (c *Client) resolveGatewayBase(da DirectAccessToken) string {
	if c.Config.AIGatewayURL != "" {
		return strings.TrimRight(c.Config.AIGatewayURL, "/")
	}
	if da.BaseURL != "" {
		return strings.TrimRight(da.BaseURL, "/")
	}
	return DefaultAIGatewayURL
}

func mapToBackendModel(modelID string) (backendModel string, isOpenAI bool) {
	m := strings.TrimSpace(modelID)
	if after, ok := strings.CutPrefix(m, FactoryKind+"/"); ok {
		m = after
	}

	switch m {
	case ModelHaiku45:
		return BackendClaudeHaiku45, false
	case ModelSonnet45:
		return BackendClaudeSonnet45, false
	case ModelOpus45:
		return BackendClaudeOpus45, false
	default:
		lower := strings.ToLower(m)
		if strings.Contains(lower, "gpt") || strings.Contains(lower, "openai") {
			return m, true
		}
		return m, false
	}
}

// Execute opens an inference stream towards GitLab's AI Gateway.
func (c *Client) Execute(ctx context.Context, inv backendplugin.Invocation, call aipapi.Call, requestedModel string) (aipapi.ManagedEventStream, error) {
	// Hard negatives: reject ACP requests or non-inference repository tool calls.
	if inv.Operation == "agent_control" || strings.Contains(strings.ToLower(inv.Operation), "acp") {
		return nil, errors.New("gitlab-duo: ACP is not supported; inference connector only")
	}
	for _, t := range call.Tools {
		name := strings.ToLower(t.Name)
		if strings.Contains(name, "gitlab_mr") ||
			strings.Contains(name, "gitlab_issue") ||
			strings.Contains(name, "gitlab_pipeline") ||
			strings.Contains(name, "repository") {
			return nil, fmt.Errorf("gitlab-duo: repository tools are out of scope; inference connector only: %s", t.Name)
		}
	}
	for _, t := range inv.Tools {
		name := strings.ToLower(t.Name)
		if strings.Contains(name, "gitlab_mr") ||
			strings.Contains(name, "gitlab_issue") ||
			strings.Contains(name, "gitlab_pipeline") ||
			strings.Contains(name, "repository") {
			return nil, fmt.Errorf("gitlab-duo: repository tools are out of scope; inference connector only: %s", t.Name)
		}
	}

	if c.DirectAccessManager == nil {
		return nil, errors.New("gitlab-duo: direct access manager not configured")
	}

	da, err := c.DirectAccessManager.GetDirectAccessToken(ctx, false)
	if err != nil {
		return nil, err
	}

	modelID := requestedModel
	if modelID == "" {
		modelID = c.Config.DefaultModel()
	}

	backendModel, isOpenAI := mapToBackendModel(modelID)
	if isOpenAI {
		return c.executeOpenAI(ctx, da, backendModel, inv, call)
	}

	return c.executeAnthropic(ctx, da, backendModel, inv, call)
}

type messageItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func extractMessages(call aipapi.Call, inv backendplugin.Invocation) ([]messageItem, string) {
	var items []messageItem
	var systemPrompt string

	if len(call.Messages) > 0 {
		for _, m := range call.Messages {
			if m.Role == aipapi.RoleSystem {
				for _, p := range m.Parts {
					if p.Kind == aipapi.PartText {
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
				if p.Kind == aipapi.PartText {
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

func (c *Client) executeAnthropic(ctx context.Context, da DirectAccessToken, backendModel string, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
	gatewayBase := c.resolveGatewayBase(da)
	endpoint := fmt.Sprintf("%s/ai/v1/proxy/anthropic/v1/messages", gatewayBase)

	isStreaming := inv.DeliveryMode != string(aipapi.DeliveryModeNonStreaming)

	msgs, systemPrompt := extractMessages(call, inv)

	reqPayload := map[string]any{
		"model":      backendModel,
		"messages":   msgs,
		"max_tokens": 8192,
		"stream":     isStreaming,
	}
	if systemPrompt != "" {
		reqPayload["system"] = systemPrompt
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+da.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("anthropic-beta", "context-1m-2025-08-07")

	for k, v := range da.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: messages request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		// Try refreshing direct access token once
		da2, rerr := c.DirectAccessManager.GetDirectAccessToken(ctx, true)
		if rerr != nil {
			return nil, fmt.Errorf("gitlab-duo: token refresh after 401 failed: %w", rerr)
		}
		endpoint2 := fmt.Sprintf("%s/ai/v1/proxy/anthropic/v1/messages", c.resolveGatewayBase(da2))
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint2, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+da2.Token)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", DefaultUserAgent)
		req2.Header.Set("anthropic-beta", "context-1m-2025-08-07")
		for k, v := range da2.Headers {
			req2.Header.Set(k, v)
		}
		resp, err = c.getHTTPClient().Do(req2)
		if err != nil {
			return nil, fmt.Errorf("gitlab-duo: retry messages request: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("gitlab-duo: messages error (status %d): %s", resp.StatusCode, string(body))
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
	if memory, ok := stream.(*memoryEventStream); ok {
		memory.SetEnabled(c.accountingEvidenceV1)
	}
	return stream, nil
}

func (c *Client) executeOpenAI(ctx context.Context, da DirectAccessToken, backendModel string, inv backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
	gatewayBase := c.resolveGatewayBase(da)
	endpoint := fmt.Sprintf("%s/ai/v1/proxy/openai/v1/chat/completions", gatewayBase)

	isStreaming := inv.DeliveryMode != string(aipapi.DeliveryModeNonStreaming)

	msgs, systemPrompt := extractMessages(call, inv)
	if systemPrompt != "" {
		msgs = append([]messageItem{{Role: "system", Content: systemPrompt}}, msgs...)
	}

	reqPayload := map[string]any{
		"model":      backendModel,
		"messages":   msgs,
		"max_tokens": 4096,
		"stream":     isStreaming,
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+da.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	for k, v := range da.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.getHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab-duo: openai proxy request: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		// Try refreshing direct access token once
		da2, rerr := c.DirectAccessManager.GetDirectAccessToken(ctx, true)
		if rerr != nil {
			return nil, fmt.Errorf("gitlab-duo: token refresh after 401 failed: %w", rerr)
		}
		endpoint2 := fmt.Sprintf("%s/ai/v1/proxy/openai/v1/chat/completions", c.resolveGatewayBase(da2))
		req2, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint2, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Authorization", "Bearer "+da2.Token)
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("User-Agent", DefaultUserAgent)
		for k, v := range da2.Headers {
			req2.Header.Set(k, v)
		}
		resp, err = c.getHTTPClient().Do(req2)
		if err != nil {
			return nil, fmt.Errorf("gitlab-duo: retry openai proxy request: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("gitlab-duo: openai proxy error (status %d): %s", resp.StatusCode, string(body))
	}

	if isStreaming {
		stream := newOpenAIManagedSSEStream(resp)
		stream.SetEnabled(c.accountingEvidenceV1)
		return stream, nil
	}

	stream, err := newUnaryOpenAIStream(resp)
	if err != nil {
		return nil, err
	}
	if memory, ok := stream.(*memoryEventStream); ok {
		memory.SetEnabled(c.accountingEvidenceV1)
	}
	return stream, nil
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
		if ev := gitlabAnthropicUsageEvent(p.Message.Usage, p.usagePresent, p.Message.ID); ev != nil {
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
		if ev := gitlabAnthropicUsageEvent(p.Usage, p.usagePresent, p.Message.ID); ev != nil {
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
	s.AddUsageEvent(s.providerUsage, "gitlabduo.anthropic:stream")
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

// Unary Anthropic Response handling
func newUnaryAnthropicStream(resp *http.Response) (aipapi.ManagedEventStream, error) {
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
		return nil, fmt.Errorf("gitlab-duo: decode unary anthropic response: %w", err)
	}

	var events []aipapi.Event
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseStarted})
	events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
	if ev := gitlabAnthropicUsageEvent(res.Usage, bytes.Contains(body, []byte(`"usage"`)), res.ID); ev != nil {
		events = append(events, *ev)
	}
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: c.Text})
		}
	}
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: res.StopReason})

	return newMemoryEventStream(events), nil
}

// OpenAI SSE stream processing
type openAIManagedSSEStream struct {
	resp     *http.Response
	sc       *bufio.Scanner
	mu       sync.Mutex
	closed   bool
	started  bool
	msgStart bool
	finished bool
	done     bool
	pending  []aipapi.Event
	*backendplugin.UsageEvidenceBuffer
}

func newOpenAIManagedSSEStream(resp *http.Response) *openAIManagedSSEStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &openAIManagedSSEStream{resp: resp, sc: sc, UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer()}
}

func (s *openAIManagedSSEStream) Recv(ctx context.Context) (aipapi.Event, error) {
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
				return aipapi.Event{}, err
			}
			s.done = true
			if !s.finished && s.started {
				s.finished = true
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished})
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return s.canonicalUsageEvent(ev), nil
			}
			return aipapi.Event{}, io.EOF
		}
		line := strings.TrimSpace(s.sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			s.done = true
			if !s.finished && s.started {
				s.finished = true
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished})
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return s.canonicalUsageEvent(ev), nil
			}
			continue
		}
		var p struct {
			ID          string `json:"id"`
			ServiceTier string `json:"service_tier"`
			Choices     []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *openAIUsageFields `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			continue
		}
		if !s.started {
			s.started = true
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		if !s.msgStart {
			s.msgStart = true
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventMessageStarted})
		}
		if p.Usage != nil {
			if ev := gitlabOpenAIUsageEvent(*p.Usage, p.ID, p.ServiceTier); ev != nil {
				s.pending = append(s.pending, *ev)
				s.AddUsageEvent(*ev, "gitlabduo.openai:stream")
			}
		}
		for _, ch := range p.Choices {
			if ch.Delta.Content != "" {
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: ch.Delta.Content})
			}
			if ch.FinishReason != "" && !s.finished {
				s.finished = true
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: ch.FinishReason})
			}
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return s.canonicalUsageEvent(ev), nil
		}
	}
}

func (s *openAIManagedSSEStream) canonicalUsageEvent(ev aipapi.Event) aipapi.Event {
	if ev.Kind == aipapi.EventUsageDelta && s.UsageEvidenceBuffer != nil && s.AccountingEvidenceEnabled() {
		ev.Accounting.DedupeKey = ""
	}
	return ev
}

func (s *openAIManagedSSEStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	err := s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly, Err: err}
}

func (s *openAIManagedSSEStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.resp != nil && s.resp.Body != nil {
		return s.resp.Body.Close()
	}
	return nil
}

// Unary OpenAI Response handling
func newUnaryOpenAIStream(resp *http.Response) (aipapi.ManagedEventStream, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res struct {
		ID          string `json:"id"`
		ServiceTier string `json:"service_tier"`
		Choices     []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *openAIUsageFields `json:"usage"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("gitlab-duo: decode unary openai response: %w", err)
	}

	var events []aipapi.Event
	events = append(events, aipapi.Event{Kind: aipapi.EventResponseStarted})
	events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
	if res.Usage != nil {
		if ev := gitlabOpenAIUsageEvent(*res.Usage, res.ID, res.ServiceTier); ev != nil {
			events = append(events, *ev)
		}
	}
	if len(res.Choices) > 0 {
		ch := res.Choices[0]
		if ch.Message.Content != "" {
			events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: ch.Message.Content})
		}
		events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: ch.FinishReason})
	} else {
		events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})
	}

	return newMemoryEventStream(events), nil
}

type memoryEventStream struct {
	events []aipapi.Event
	idx    int
	*backendplugin.UsageEvidenceBuffer
}

func newMemoryEventStream(events []aipapi.Event) *memoryEventStream {
	m := &memoryEventStream{events: append([]aipapi.Event(nil), events...), UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer()}
	var anthropicUsage aipapi.Event
	anthropicUsageSeen := false
	for _, ev := range events {
		if strings.HasPrefix(ev.Accounting.DedupeKey, "gitlabduo.anthropic:") {
			if ev.Kind != aipapi.EventUsageDelta {
				continue
			}
			if anthropicUsageSeen {
				anthropicUsage = mergeAnthropicUsageEvent(anthropicUsage, ev)
			} else {
				anthropicUsage = ev
				anthropicUsageSeen = true
			}
		} else {
			m.AddUsageEvent(ev, "gitlabduo.provider:stream")
		}
	}
	if anthropicUsageSeen {
		m.AddUsageEvent(anthropicUsage, "gitlabduo.anthropic:stream")
	}
	return m
}

func (m *memoryEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	if m.idx >= len(m.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := m.events[m.idx]
	m.idx++
	if ev.Kind == aipapi.EventUsageDelta && m.AccountingEvidenceEnabled() {
		ev.Accounting.DedupeKey = ""
	}
	return ev, nil
}

func (m *memoryEventStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (m *memoryEventStream) Close() error {
	m.idx = len(m.events)
	return nil
}
