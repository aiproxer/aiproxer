package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type managedSSEStream struct {
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
	providerRequestID string
	providerUsage     aipapi.Event
	providerUsageSeen bool
	*backendplugin.UsageEvidenceBuffer
}

func newManagedSSEStream(resp *http.Response) *managedSSEStream {
	sc := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	return &managedSSEStream{resp: resp, sc: sc, toolCalls: make(map[int]string), UsageEvidenceBuffer: backendplugin.NewUsageEvidenceBuffer()}
}

type ssePayload struct {
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
		ID    string      `json:"id"`
		Usage usageFields `json:"usage"`
	} `json:"message"`
	Usage usageFields `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *managedSSEStream) Recv(ctx context.Context) (aipapi.Event, error) {
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
		var p ssePayload
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			continue
		}
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

func (s *managedSSEStream) ensureStarted() {
	if !s.started {
		s.started = true
		s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseStarted})
	}
	if !s.msgStart {
		s.msgStart = true
		s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventMessageStarted})
	}
}

func (s *managedSSEStream) emitFinishIfStarted(reason string) {
	s.flushUsage()
	if s.finished || !s.started {
		return
	}
	s.finished = true
	s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: reason})
}

func (s *managedSSEStream) handlePayload(p ssePayload) error {
	switch p.Type {
	case "message_start":
		if !s.started {
			s.started = true
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventResponseStarted})
		}
		s.providerRequestID = strings.TrimSpace(p.Message.ID)
		u := p.Message.Usage
		if ev := commandcodeUsageEvent(&u, s.providerRequestID); ev != nil {
			s.addUsage(*ev)
		}
	case "content_block_start":
		s.ensureStarted()
		if p.ContentBlock.Type == "tool_use" {
			toolID := p.ContentBlock.ID
			s.toolCalls[p.Index] = toolID
			s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: toolID, ToolName: p.ContentBlock.Name})
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
				s.pending = append(s.pending, aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: s.toolCalls[p.Index], Delta: p.Delta.PartialJSON})
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
		if ev := commandcodeUsageEvent(&p.Usage, s.providerRequestID); ev != nil {
			s.addUsage(*ev)
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

func (s *managedSSEStream) addUsage(ev aipapi.Event) {
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

func (s *managedSSEStream) canonicalUsageEvent(ev aipapi.Event) aipapi.Event {
	if ev.Kind == aipapi.EventUsageDelta && s.UsageEvidenceBuffer != nil && s.AccountingEvidenceEnabled() {
		ev.Accounting.DedupeKey = ""
	}
	return ev
}

func (s *managedSSEStream) flushUsage() {
	if s == nil || !s.providerUsageSeen || s.UsageEvidenceBuffer == nil {
		return
	}
	s.AddUsageEvent(s.providerUsage, "commandcode.anthropic.usage:stream")
}

func (s *managedSSEStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	err := s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly, Err: err}
}

func (s *managedSSEStream) Close() error {
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
