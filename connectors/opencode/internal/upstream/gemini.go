package upstream

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

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type anthropicSSEStream struct {
	resp    *http.Response
	sc      *bufio.Scanner
	pending []aipapi.Event
	closed  bool
	mu      sync.Mutex
}

func newAnthropicSSEStream(resp *http.Response) aipapi.EventStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxSSEFrameLineBytes)
	return &anthropicSSEStream{resp: resp, sc: sc}
}

func (s *anthropicSSEStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("opencode: stream closed")
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
		data, err := nextSSEDataFrame(s.sc)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return aipapi.Event{}, io.EOF
			}
			return aipapi.Event{}, err
		}
		if data == "" {
			continue
		}
		var payload struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := decodeSSEFrame([]byte(data), &payload); err != nil {
			if errors.Is(err, errSSEFrameTooLarge) {
				return aipapi.Event{}, err
			}
			continue
		}
		if payload.Type == "content_block_delta" && payload.Delta.Text != "" {
			return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: payload.Delta.Text}, nil
		}
	}
}

func (s *anthropicSSEStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}

func openGemini(ctx context.Context, hc *http.Client, baseURL, apiKey string, call aipapi.Call, model string) (aipapi.ManagedEventStream, error) {
	endpoint := geminiEndpoint(baseURL, model, streaming(call))
	body, err := geminiRequestBody(call)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
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
		return nil, fmt.Errorf("opencode gemini HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if streaming(call) {
		return aipapi.CloseOnlyManagedStream{Stream: newGeminiSSEStream(resp)}, nil
	}
	s, err := newGeminiJSONStream(resp)
	if err != nil {
		return nil, err
	}
	return aipapi.CloseOnlyManagedStream{Stream: s}, nil
}

func geminiEndpoint(baseURL, model string, stream bool) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.Contains(base, "/models/") {
		if stream {
			return base + ":streamGenerateContent?alt=sse"
		}
		return base + ":generateContent"
	}
	path := fmt.Sprintf("/v1beta/models/%s", model)
	if stream {
		return base + path + ":streamGenerateContent?alt=sse"
	}
	return base + path + ":generateContent"
}

func geminiRequestBody(call aipapi.Call) ([]byte, error) {
	if err := validateTextOnlyCall(call); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": firstUserText(call)}},
		}},
	}
	return json.Marshal(payload)
}

func newGeminiJSONStream(resp *http.Response) (aipapi.EventStream, error) {
	defer func() { _ = resp.Body.Close() }()
	raw, err := readNonStreamResponse(resp.Body)
	if err != nil {
		return nil, err
	}
	text, err := geminiTextFromJSON(raw)
	if err != nil {
		return nil, err
	}
	return newSliceStream([]aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: text}}), nil
}

type geminiPayload struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func geminiTextFromJSON(raw []byte) (string, error) {
	var payload geminiPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("opencode gemini decode: %w", err)
	}
	return geminiText(payload)
}

// geminiText extracts the first non-empty text part; the non-stream and SSE
// paths share it so both materialize through the same payload shape.
func geminiText(payload geminiPayload) (string, error) {
	for _, cand := range payload.Candidates {
		for _, part := range cand.Content.Parts {
			if part.Text != "" {
				return part.Text, nil
			}
		}
	}
	return "", fmt.Errorf("opencode gemini: empty response")
}

type geminiSSEStream struct {
	resp   *http.Response
	sc     *bufio.Scanner
	closed bool
	mu     sync.Mutex
}

func newGeminiSSEStream(resp *http.Response) aipapi.EventStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, maxSSEFrameLineBytes)
	return &geminiSSEStream{resp: resp, sc: sc}
}

func (s *geminiSSEStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("opencode: stream closed")
	}
	for {
		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		data, err := nextSSEDataFrame(s.sc)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return aipapi.Event{}, io.EOF
			}
			return aipapi.Event{}, err
		}
		if data == "" {
			continue
		}
		var payload geminiPayload
		if err := decodeSSEFrame([]byte(data), &payload); err != nil {
			if errors.Is(err, errSSEFrameTooLarge) {
				return aipapi.Event{}, err
			}
			continue
		}
		text, err := geminiText(payload)
		if err != nil || text == "" {
			continue
		}
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text}, nil
	}
}

func (s *geminiSSEStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}

type sliceStream struct {
	mu     sync.Mutex
	events []aipapi.Event
	idx    int
	closed bool
}

func newSliceStream(events []aipapi.Event) aipapi.EventStream {
	return &sliceStream{events: events}
}

func (s *sliceStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("opencode: stream closed")
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
