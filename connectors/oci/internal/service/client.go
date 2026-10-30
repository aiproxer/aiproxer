package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/ssestream"
)

type GenericChatResponseWrapper struct {
	ChatResponse *GenericChatResponse `json:"chatResponse"`
	Choices      []ChatChoice         `json:"choices"`
}

type GenericChatResponse struct {
	APIFormat string       `json:"apiFormat"`
	Choices   []ChatChoice `json:"choices"`
}

type ChatChoice struct {
	Index   int          `json:"index"`
	Message *ChatMessage `json:"message"`
	Text    string       `json:"text"`
}

type ChatMessage struct {
	Role    string        `json:"role"`
	Content []ChatContent `json:"content"`
}

type ChatContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func extractTextFromChatResponse(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", fmt.Errorf("oci-generative-ai: empty response body")
	}

	var parsed GenericChatResponseWrapper
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("oci-generative-ai: unmarshal chat response: %w", err)
	}

	choices := parsed.Choices
	if parsed.ChatResponse != nil && len(parsed.ChatResponse.Choices) > 0 {
		choices = parsed.ChatResponse.Choices
	}

	if len(choices) == 0 {
		return "", fmt.Errorf("oci-generative-ai: no choices in chat response")
	}

	var sb strings.Builder
	for _, choice := range choices {
		if choice.Message != nil && len(choice.Message.Content) > 0 {
			for _, c := range choice.Message.Content {
				sb.WriteString(c.Text)
			}
		} else if choice.Text != "" {
			sb.WriteString(choice.Text)
		}
	}
	return sb.String(), nil
}

type Client struct {
	Config     Config
	Signer     RequestSigner
	HTTPClient *http.Client
}

func (c *Client) Open(ctx context.Context, call aipapi.Call, model string) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("oci-generative-ai: tools are unsupported in this connector")
	}

	var messages []ChatMessage
	for _, m := range call.Messages {
		var role string
		switch m.Role {
		case aipapi.RoleUser:
			role = "USER"
		case aipapi.RoleAssistant:
			role = "ASSISTANT"
		case aipapi.RoleSystem:
			role = "SYSTEM"
		default:
			return nil, fmt.Errorf("oci-generative-ai: unsupported message role %q", m.Role)
		}

		var textBuilder strings.Builder
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText {
				return nil, fmt.Errorf("oci-generative-ai: unsupported part kind %q", p.Kind)
			}
			textBuilder.WriteString(p.Text)
		}
		messages = append(messages, ChatMessage{
			Role: role,
			Content: []ChatContent{
				{
					Type: "TEXT",
					Text: textBuilder.String(),
				},
			},
		})
	}

	isStreaming := call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != aipapi.TransportModeNonStreaming

	servingMode := map[string]string{
		"servingType": "ON_DEMAND",
		"modelId":     model,
	}
	if c.Config.DedicatedEndpointID != "" {
		servingMode = map[string]string{
			"servingType": "DEDICATED",
			"endpointId":  c.Config.DedicatedEndpointID,
		}
	}

	chatRequest := map[string]any{
		"apiFormat": "GENERIC",
		"messages":  messages,
		"isStream":  isStreaming,
	}
	if call.Options.MaxOutputTokens != nil {
		chatRequest["maxTokens"] = *call.Options.MaxOutputTokens
	}

	payload := map[string]any{
		"compartmentId": c.Config.CompartmentID,
		"servingMode":   servingMode,
		"chatRequest":   chatRequest,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: marshal payload: %w", err)
	}

	chatURL := c.Config.ChatEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if isStreaming {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Host = req.URL.Host

	if err := c.Signer.Sign(req); err != nil {
		return nil, fmt.Errorf("oci-generative-ai: sign request: %w", err)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: chat request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("oci-generative-ai: chat request status %d: %s", resp.StatusCode, string(body))
	}

	if isStreaming {
		return aipapi.CloseOnlyManagedStream{Stream: newSSEStream(resp.Body)}, nil
	}

	defer func() { _ = resp.Body.Close() }()
	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oci-generative-ai: read response: %w", err)
	}

	genText, err := extractTextFromChatResponse(rawResp)
	if err != nil {
		return nil, err
	}

	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: genText},
		{Kind: aipapi.EventResponseFinished},
	}
	return aipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	mgmtURL := c.Config.ManagementEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mgmtURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: new list-models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Host = req.URL.Host

	if err := c.Signer.Sign(req); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: sign list-models request: %w", err)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: list models status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: read list models body: %w", err)
	}

	var parsed struct {
		ModelCollection *struct {
			Items []struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"items"`
		} `json:"modelCollection"`
		Items []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("oci-generative-ai: unmarshal list models: %w", err)
	}

	rawItems := parsed.Items
	if parsed.ModelCollection != nil && len(parsed.ModelCollection.Items) > 0 {
		rawItems = parsed.ModelCollection.Items
	}

	out := make([]backendplugin.ModelDescriptor, 0, len(rawItems))
	for _, item := range rawItems {
		id := strings.TrimSpace(item.ID)
		name := strings.TrimSpace(item.DisplayName)
		if id == "" {
			id = name
		}
		if name == "" {
			name = id
		}
		if id == "" {
			continue
		}

		lowerID := strings.ToLower(id)
		lowerName := strings.ToLower(name)
		if strings.Contains(lowerID, "embed") || strings.Contains(lowerID, "rerank") || strings.Contains(lowerID, "image") ||
			strings.Contains(lowerName, "embed") || strings.Contains(lowerName, "rerank") || strings.Contains(lowerName, "image") {
			continue
		}

		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + name,
			NativeModelID:    id,
			DisplayName:      name,
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
		return aipapi.Event{}, fmt.Errorf("oci-generative-ai: stream closed")
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

// newSSEStream decodes an OCI chat stream. ssestream owns framing; OCI always
// brackets the text deltas with a response start and finish.
func newSSEStream(body io.ReadCloser) *ssestream.Stream {
	return ssestream.New(body, ssestream.Decoder{
		Start: func() []aipapi.Event { return []aipapi.Event{{Kind: aipapi.EventResponseStarted}} },
		Data: func(payload []byte) ([]aipapi.Event, error) {
			text, err := extractTextFromChatResponse(payload)
			if err != nil || text == "" {
				return nil, nil
			}
			return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: text}}, nil
		},
		End: func() []aipapi.Event { return []aipapi.Event{{Kind: aipapi.EventResponseFinished}} },
	}, ssestream.Options{Name: "oci-generative-ai"})
}
