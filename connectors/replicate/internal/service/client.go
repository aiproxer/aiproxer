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

// HTTPError is a bounded Replicate HTTP failure. StatusCode drives provider
// quota classification; RetryAfter carries the verbatim Retry-After response
// header for threshold-gated rotation (empty when the provider sent no
// hint). The message text is secret-sanitized at construction, matching the
// historical plain-error strings, so errors never carry token material.
type HTTPError struct {
	StatusCode int
	RetryAfter string
	Message    string
}

func (e *HTTPError) Error() string { return e.Message }

type replicateURLs struct {
	Get    string `json:"get"`
	Cancel string `json:"cancel"`
	Stream string `json:"stream"`
}

type replicatePrediction struct {
	ID     string        `json:"id"`
	Status string        `json:"status"`
	Output any           `json:"output"`
	URLs   replicateURLs `json:"urls"`
	Error  string        `json:"error"`
}

type replicateInput struct {
	Prompt    string  `json:"prompt"`
	MaxTokens *uint32 `json:"max_tokens,omitempty"`
}

type replicateCreateRequest struct {
	Version string         `json:"version,omitempty"`
	Input   replicateInput `json:"input"`
	Stream  bool           `json:"stream,omitempty"`
}

func sanitizeSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}

func resolveURL(origin, u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if strings.HasPrefix(u, "/") {
		return origin + u
	}
	return origin + "/" + u
}

func cancelPrediction(httpClient *http.Client, cancelURL, token string) {
	if cancelURL == "" || token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cancelURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

func isTerminal(status string) bool {
	switch strings.ToLower(status) {
	case "succeeded", "failed", "canceled", "aborted":
		return true
	default:
		return false
	}
}

func mapPredictionOutput(raw any) (string, error) {
	if raw == nil {
		return "", nil
	}
	switch v := raw.(type) {
	case string:
		return v, nil
	case []any:
		var sb strings.Builder
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("replicate: output array element at index %d is not a string (type %T)", i, item)
			}
			sb.WriteString(s)
		}
		return sb.String(), nil
	case []string:
		return strings.Join(v, ""), nil
	default:
		return "", fmt.Errorf("replicate: unexpected output type %T (expected string or array of strings)", raw)
	}
}

func (c *Client) Open(ctx context.Context, call aipapi.Call, targetModel string) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses ||
		call.Invocation.Operation == aipapi.OperationOpenResponsesCreate {
		return nil, fmt.Errorf("replicate: responses operations are not supported")
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("replicate: tools are not supported")
	}
	if len(call.Instructions) > 0 {
		return nil, fmt.Errorf("replicate: instructions/system prompts are not supported for prompt-text contract; only user text is supported")
	}

	var promptBuilder strings.Builder
	for _, msg := range call.Messages {
		if msg.Role != aipapi.RoleUser {
			return nil, fmt.Errorf("replicate: non-user role %q is not supported (contract prompt-text only supports user prompt)", msg.Role)
		}
		for _, p := range msg.Parts {
			if p.Kind != aipapi.PartText {
				return nil, fmt.Errorf("replicate: unsupported part kind %q (vision and non-text are not supported)", p.Kind)
			}
			promptBuilder.WriteString(p.Text)
		}
	}

	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("replicate: get token: %w", err)
	}

	owner, name := c.Config.ModelOwnerAndName()
	if owner == "" || name == "" {
		return nil, fmt.Errorf("replicate: invalid model configuration: %q", c.Config.Model)
	}

	isStreaming := call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming &&
		call.Invocation.TransportMode != aipapi.TransportModeNonStreaming

	createReq := replicateCreateRequest{
		Version: c.Config.Version,
		Input: replicateInput{
			Prompt: promptBuilder.String(),
		},
		Stream: isStreaming,
	}
	if call.Options.MaxOutputTokens != nil && *call.Options.MaxOutputTokens > 0 {
		mt := uint32(*call.Options.MaxOutputTokens)
		createReq.Input.MaxTokens = &mt
	}

	bodyBytes, err := json.Marshal(createReq)
	if err != nil {
		return nil, fmt.Errorf("replicate: marshal prediction request: %w", err)
	}

	createURL := fmt.Sprintf("%s/v1/models/%s/%s/predictions", c.Config.Origin(), owner, name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("replicate: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)
	httpReq.Header.Set("Prefer", "wait")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("replicate: create prediction: %s", sanitizeSecret(err.Error(), tok))
	}

	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("replicate: read prediction response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			RetryAfter: resp.Header.Get("Retry-After"),
			Message:    fmt.Sprintf("replicate: create prediction failed (status %d): %s", resp.StatusCode, sanitizeSecret(string(respBody), tok)),
		}
	}

	var pred replicatePrediction
	if err := json.Unmarshal(respBody, &pred); err != nil {
		return nil, fmt.Errorf("replicate: unmarshal prediction response: %w", err)
	}

	cancelURL := pred.URLs.Cancel
	if cancelURL == "" && pred.ID != "" {
		cancelURL = fmt.Sprintf("%s/v1/predictions/%s/cancel", c.Config.Origin(), pred.ID)
	}
	cancelURL = resolveURL(c.Config.Origin(), cancelURL)

	getURL := pred.URLs.Get
	if getURL == "" && pred.ID != "" {
		getURL = fmt.Sprintf("%s/v1/predictions/%s", c.Config.Origin(), pred.ID)
	}
	getURL = resolveURL(c.Config.Origin(), getURL)

	// Stream path: if streaming is requested and urls.stream is present
	if isStreaming && pred.URLs.Stream != "" {
		streamURL := resolveURL(c.Config.Origin(), pred.URLs.Stream)
		sReq, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
		if err != nil {
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, fmt.Errorf("replicate: stream request: %w", err)
		}
		sReq.Header.Set("Accept", "text/event-stream")
		sReq.Header.Set("Authorization", "Bearer "+tok)

		sResp, err := httpClient.Do(sReq)
		if err != nil {
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, fmt.Errorf("replicate: open stream: %s", sanitizeSecret(err.Error(), tok))
		}
		if sResp.StatusCode != http.StatusOK {
			sBody, _ := io.ReadAll(sResp.Body)
			_ = sResp.Body.Close()
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, &HTTPError{
				StatusCode: sResp.StatusCode,
				RetryAfter: sResp.Header.Get("Retry-After"),
				Message:    fmt.Sprintf("replicate: stream request failed (status %d): %s", sResp.StatusCode, sanitizeSecret(string(sBody), tok)),
			}
		}

		return aipapi.CloseOnlyManagedStream{
			Stream: newSSEPredictionStream(ctx, sResp.Body, cancelURL, tok, httpClient),
		}, nil
	}

	// Non-stream path, or stream without urls.stream (poll until terminal)
	pollInterval := c.Config.PollInterval()
	for !isTerminal(pred.Status) {
		select {
		case <-ctx.Done():
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}

		pReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
		if err != nil {
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, fmt.Errorf("replicate: create poll request: %w", err)
		}
		pReq.Header.Set("Accept", "application/json")
		pReq.Header.Set("Authorization", "Bearer "+tok)

		pResp, err := httpClient.Do(pReq)
		if err != nil {
			if ctx.Err() != nil {
				cancelPrediction(httpClient, cancelURL, tok)
				return nil, ctx.Err()
			}
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, fmt.Errorf("replicate: poll prediction: %s", sanitizeSecret(err.Error(), tok))
		}

		pBody, err := io.ReadAll(pResp.Body)
		_ = pResp.Body.Close()
		if err != nil {
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, fmt.Errorf("replicate: read poll response: %w", err)
		}

		if pResp.StatusCode != http.StatusOK {
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, &HTTPError{
				StatusCode: pResp.StatusCode,
				RetryAfter: pResp.Header.Get("Retry-After"),
				Message:    fmt.Sprintf("replicate: poll prediction failed (status %d): %s", pResp.StatusCode, sanitizeSecret(string(pBody), tok)),
			}
		}

		if err := json.Unmarshal(pBody, &pred); err != nil {
			cancelPrediction(httpClient, cancelURL, tok)
			return nil, fmt.Errorf("replicate: unmarshal poll response: %w", err)
		}
	}

	switch strings.ToLower(pred.Status) {
	case "succeeded":
		text, err := mapPredictionOutput(pred.Output)
		if err != nil {
			return nil, err
		}
		events := []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
		}
		if !isStreaming {
			events = append(events, aipapi.Event{Kind: aipapi.EventMessageStarted})
		}
		if text != "" {
			events = append(events, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text})
		}
		events = append(events, aipapi.Event{Kind: aipapi.EventResponseFinished})
		return aipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil

	case "failed":
		return nil, fmt.Errorf("replicate: prediction failed: %s", pred.Error)

	case "canceled", "aborted":
		return nil, fmt.Errorf("replicate: prediction %s", pred.Status)

	default:
		return nil, fmt.Errorf("replicate: unexpected terminal status %q", pred.Status)
	}
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
		return aipapi.Event{}, fmt.Errorf("replicate: stream closed")
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

type ssePredictionStream struct {
	ctx          context.Context
	body         io.ReadCloser
	cancelURL    string
	token        string
	httpClient   *http.Client
	scanner      *bufio.Scanner
	mu           sync.Mutex
	started      bool
	finished     bool
	closed       bool
	currentEvent string
	currentData  strings.Builder
}

func newSSEPredictionStream(ctx context.Context, body io.ReadCloser, cancelURL, token string, httpClient *http.Client) *ssePredictionStream {
	return &ssePredictionStream{
		ctx:        ctx,
		body:       body,
		cancelURL:  cancelURL,
		token:      token,
		httpClient: httpClient,
		scanner:    bufio.NewScanner(body),
	}
}

func (s *ssePredictionStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return aipapi.Event{}, fmt.Errorf("replicate: stream closed")
	}

	if !s.started {
		s.started = true
		return aipapi.Event{Kind: aipapi.EventResponseStarted}, nil
	}

	if s.finished {
		return aipapi.Event{}, io.EOF
	}

	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			if !s.finished {
				cancelPrediction(s.httpClient, s.cancelURL, s.token)
			}
			return aipapi.Event{}, err
		}
		line := s.scanner.Text()
		if strings.HasPrefix(line, ":") {
			continue
		}
		if line == "" {
			if s.currentData.Len() == 0 && s.currentEvent == "" {
				continue
			}
			evType := s.currentEvent
			evData := s.currentData.String()
			s.currentEvent = ""
			s.currentData.Reset()

			if evType == "done" || evData == "[DONE]" {
				s.finished = true
				return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
			}
			if evType == "error" {
				return aipapi.Event{}, fmt.Errorf("replicate: stream error: %s", evData)
			}
			if evType == "output" || evType == "" || evType == "message" {
				delta := evData
				if strings.HasPrefix(delta, "\"") && strings.HasSuffix(delta, "\"") {
					var unquoted string
					if err := json.Unmarshal([]byte(delta), &unquoted); err == nil {
						delta = unquoted
					}
				}
				if delta != "" {
					return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: delta}, nil
				}
			}
			continue
		}

		if after, ok := strings.CutPrefix(line, "event:"); ok {
			s.currentEvent = strings.TrimSpace(after)
		} else if after, ok := strings.CutPrefix(line, "data:"); ok {
			chunk := after
			chunk = strings.TrimPrefix(chunk, " ")
			if s.currentData.Len() > 0 {
				s.currentData.WriteString("\n")
			}
			s.currentData.WriteString(chunk)
		}
	}

	// Handle trailing un-delimited data if any before EOF
	if s.currentData.Len() > 0 || s.currentEvent != "" {
		evType := s.currentEvent
		evData := s.currentData.String()
		s.currentEvent = ""
		s.currentData.Reset()
		if evType == "done" || evData == "[DONE]" {
			s.finished = true
			return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
		}
		if evType == "error" {
			return aipapi.Event{}, fmt.Errorf("replicate: stream error: %s", evData)
		}
		if evType == "output" || evType == "" || evType == "message" {
			delta := evData
			if strings.HasPrefix(delta, "\"") && strings.HasSuffix(delta, "\"") {
				var unquoted string
				if err := json.Unmarshal([]byte(delta), &unquoted); err == nil {
					delta = unquoted
				}
			}
			if delta != "" {
				return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: delta}, nil
			}
		}
	}

	if err := s.scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return aipapi.Event{}, err
	}

	if !s.finished {
		s.finished = true
		return aipapi.Event{Kind: aipapi.EventResponseFinished}, nil
	}

	return aipapi.Event{}, io.EOF
}

func (s *ssePredictionStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if !s.finished {
		cancelPrediction(s.httpClient, s.cancelURL, s.token)
	}
	if s.body != nil {
		return s.body.Close()
	}
	return nil
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	tok, err := c.TokenProvider.Token(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("replicate: get token: %w", err)
	}

	owner, name := c.Config.ModelOwnerAndName()
	if owner == "" || name == "" {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("replicate: invalid model configuration: %q", c.Config.Model)
	}

	reqURL := fmt.Sprintf("%s/v1/models/%s/%s", c.Config.Origin(), owner, name)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("replicate: create get model request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("replicate: get model: %s", sanitizeSecret(err.Error(), tok))
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("replicate: read get model response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return backendplugin.ListModelsResponse{}, &HTTPError{
			StatusCode: resp.StatusCode,
			RetryAfter: resp.Header.Get("Retry-After"),
			Message:    fmt.Sprintf("replicate: get model failed (status %d): %s", resp.StatusCode, sanitizeSecret(string(bodyBytes), tok)),
		}
	}

	canonicalID := FactoryKind + "/" + c.Config.Model
	return backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{
			{
				CanonicalModelID: canonicalID,
				NativeModelID:    c.Config.Model,
				FactoryKind:      FactoryKind,
				Capabilities:     backendplugin.CapabilitySummary{Streaming: true, Tools: false, Vision: false},
			},
		},
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}
