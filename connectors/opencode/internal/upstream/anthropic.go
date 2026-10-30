package upstream

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

func openAnthropic(ctx context.Context, hc *http.Client, baseURL, apiKey string, call aipapi.Call, model string) (aipapi.ManagedEventStream, error) {
	body, err := anthropicRequestBody(call, model)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasSuffix(endpoint, "/v1/messages") {
		endpoint += "/v1/messages"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if sid := resolveSessionID(call); sid != "" {
		req.Header.Set(HeaderOpenCodeSession, sid)
	}
	if streaming(call) {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("opencode anthropic HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if streaming(call) {
		return aipapi.CloseOnlyManagedStream{Stream: newAnthropicSSEStream(resp)}, nil
	}
	s, err := newAnthropicJSONStream(resp)
	if err != nil {
		return nil, err
	}
	return aipapi.CloseOnlyManagedStream{Stream: s}, nil
}

func anthropicRequestBody(call aipapi.Call, model string) ([]byte, error) {
	if err := validateTextOnlyCall(call); err != nil {
		return nil, err
	}
	text := firstUserText(call)
	payload := map[string]any{
		"model":      model,
		"max_tokens": 4096,
		"messages":   []map[string]any{{"role": "user", "content": text}},
	}
	if streaming(call) {
		payload["stream"] = true
	}
	return json.Marshal(payload)
}

func newAnthropicJSONStream(resp *http.Response) (aipapi.EventStream, error) {
	defer func() { _ = resp.Body.Close() }()
	raw, err := readNonStreamResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("opencode anthropic decode: %w", err)
	}
	var events []aipapi.Event
	for _, block := range payload.Content {
		if block.Type == "text" && block.Text != "" {
			events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: block.Text})
		}
	}
	return newSliceStream(events), nil
}

func streaming(call aipapi.Call) bool {
	return call.Invocation.DeliveryMode == aipapi.DeliveryModeStreaming ||
		call.Invocation.TransportMode == aipapi.TransportModeStreaming
}

func firstUserText(call aipapi.Call) string {
	for _, msg := range call.Messages {
		if msg.Role != aipapi.RoleUser {
			continue
		}
		var b strings.Builder
		for _, part := range msg.Parts {
			if part.Kind == aipapi.PartText {
				b.WriteString(part.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s
		}
	}
	return "hi"
}

func validateTextOnlyCall(call aipapi.Call) error {
	return aipapi.WalkCallContentParts(call, func(_ aipapi.Item, part aipapi.ContentPart) error {
		switch part.Kind {
		case aipapi.ContentPartImageRef, aipapi.ContentPartFileRef, aipapi.ContentPartVideoRef:
			return fmt.Errorf("opencode: unsupported canonical media content part %q; text-only route", part.Kind)
		}
		return nil
	})
}
