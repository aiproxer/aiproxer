package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	DefaultVersion = "2023-06-01"
)

type HTTPError struct {
	StatusCode int    `json:"status_code"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	Body       string `json:"body"`
	// RetryAfter carries the verbatim Retry-After response header for
	// quota-aware credential selection. It is response metadata, never secret
	// material, and is empty when the provider sent no hint.
	RetryAfter string `json:"-"`
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("commandcode anthropic HTTP %d (%s): %s", e.StatusCode, e.Type, e.Message)
	}
	return fmt.Sprintf("commandcode anthropic HTTP %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	BaseURL              string
	APIKey               string
	HTTPClient           *http.Client
	Version              string
	accountingEvidenceV1 bool
}

func NewClient(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	ver := DefaultVersion
	return &Client{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     strings.TrimSpace(apiKey),
		HTTPClient: hc,
		Version:    ver, accountingEvidenceV1: true,
	}
}

// SetAccountingEvidenceEnabled applies the negotiated V1 host-only sideband
// capability for this client generation. The default remains enabled for
// direct package users that do not construct an external ABI session.
func (c *Client) SetAccountingEvidenceEnabled(enabled bool) {
	if c != nil {
		c.accountingEvidenceV1 = enabled
	}
}

func (c *Client) ListModels(ctx context.Context, limit uint32) ([]Model, error) {
	endpoint := endpointFor(c.BaseURL, "/models")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("list models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	ver := c.Version
	if ver == "" {
		ver = DefaultVersion
	}
	req.Header.Set("anthropic-version", ver)
	applyAuth(req.Header, c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, parseHTTPError(resp.StatusCode, resp.Header, raw)
	}

	var list modelsListResponse
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	if limit > 0 && uint32(len(list.Data)) > limit {
		list.Data = list.Data[:limit]
	}
	return list.Data, nil
}

func (c *Client) Open(ctx context.Context, call aipapi.Call, model string) (aipapi.ManagedEventStream, error) {
	body, err := buildRequestBody(call, model)
	if err != nil {
		return nil, fmt.Errorf("build request body: %w", err)
	}

	endpoint := endpointFor(c.BaseURL, "/messages")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	applyAuth(req.Header, c.APIKey)
	ver := c.Version
	if ver == "" {
		ver = DefaultVersion
	}
	req.Header.Set("anthropic-version", ver)
	if isStreaming(call) {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, parseHTTPError(resp.StatusCode, resp.Header, raw)
	}

	if isStreaming(call) {
		stream := newManagedSSEStream(resp)
		stream.SetEnabled(c.accountingEvidenceV1)
		return stream, nil
	}

	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	events, err := parseNonStreamingResponse(raw)
	if err != nil {
		return nil, err
	}
	stream := newProviderSliceStream(events)
	stream.SetEnabled(c.accountingEvidenceV1)
	return stream, nil
}

func parseNonStreamingResponse(raw []byte) ([]aipapi.Event, error) {
	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
		} `json:"content"`
		StopReason string       `json:"stop_reason"`
		Usage      *usageFields `json:"usage"`
	}

	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode anthropic json: %w", err)
	}

	var events []aipapi.Event
	events = append(events,
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
	)

	// 1. Usage (including explicit zero values; absent/malformed fields remain
	// unavailable and do not turn into synthetic text-token evidence).
	if usage := commandcodeUsageEvent(resp.Usage, resp.ID); usage != nil {
		events = append(events, *usage)
	}

	// 2. Content blocks
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				events = append(events, aipapi.Event{
					Kind:  aipapi.EventTextDelta,
					Delta: block.Text,
				})
			}
		case "thinking":
			if block.Thinking != "" {
				events = append(events, aipapi.Event{
					Kind:  aipapi.EventReasoningDelta,
					Delta: block.Thinking,
				})
			}
			if block.Signature != "" {
				events = append(events, aipapi.Event{
					Kind:      aipapi.EventReasoningSignatureDelta,
					Signature: block.Signature,
				})
			}
		case "tool_use":
			events = append(events, aipapi.Event{
				Kind:       aipapi.EventToolCallStarted,
				ToolCallID: block.ID,
				ToolName:   block.Name,
			})
			if len(block.Input) > 0 {
				events = append(events, aipapi.Event{
					Kind:       aipapi.EventToolCallArgsDelta,
					ToolCallID: block.ID,
					Delta:      string(block.Input),
				})
			}
			events = append(events, aipapi.Event{
				Kind:       aipapi.EventToolCallFinished,
				ToolCallID: block.ID,
			})
		}
	}

	// 3. Finish reason
	if resp.StopReason != "" {
		events = append(events, aipapi.Event{
			Kind:         aipapi.EventResponseFinished,
			FinishReason: resp.StopReason,
		})
	}
	annotateProviderUsageEvents(events, resp.ID)
	return events, nil
}

func parseHTTPError(statusCode int, header http.Header, body []byte) error {
	var errPayload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &errPayload)
	errType := errPayload.Error.Type
	if errType == "" {
		errType = errPayload.Type
	}
	return &HTTPError{
		StatusCode: statusCode,
		Type:       errType,
		Message:    errPayload.Error.Message,
		Body:       strings.TrimSpace(string(body)),
		RetryAfter: strings.TrimSpace(header.Get("Retry-After")),
	}
}

func applyAuth(h http.Header, apiKey string) {
	if strings.TrimSpace(apiKey) == "" {
		return
	}
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("x-api-key", apiKey)
}

func endpointFor(baseURL, suffix string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, suffix) {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + suffix
	}
	return base + "/v1" + suffix
}
