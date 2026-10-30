package openailegacy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// chatStream adapts the OpenAI chat-completions SSE stream to aipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on sdk.Next; Close closes the SDK stream to unblock Next.
// Context: sdk.Next does not observe ctx; cancel the request context alone may not
// return from Recv until Close runs (see [aipapi.EventStream] cancellation notes).
type chatStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *ssestream.Stream[openai.ChatCompletionChunk]

	pending         stream.PendingEventQueue
	sawResp         bool
	sawMsg          bool
	terminalEmitted bool
	closed          bool
	activeTools     map[int64]string
	activeToolOrder []int64
	// outputLimit records that the upstream ended the turn on the OpenAI
	// output-token-limit lexeme; it is applied to the terminal event.
	outputLimit bool
	// refusal records a content-filter refusal lexeme; it is carried verbatim
	// on the terminal with no incomplete classification (a refusal is a
	// complete upstream decision, never length taxonomy).
	refusal bool
	*coremetering.ProviderEvidenceBuffer
}

// terminalEvent builds the canonical terminal for this stream. An
// output-token-limit finish_reason carries incomplete/length; a
// content-filter refusal is carried verbatim with no incomplete
// classification. Every other stop leaves FinishReason empty so the
// continuation recorder and compaction detection keep treating it as a
// clean terminal.
func (s *chatStream) terminalEvent() aipapi.Event {
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
	switch {
	case s.outputLimit:
		ev.ResponseStatus = "incomplete"
		ev.FinishReason = "length"
	case s.refusal:
		ev.FinishReason = "content_filter"
	}
	return ev
}

func NewChatStream(s *ssestream.Stream[openai.ChatCompletionChunk], maxPending int) aipapi.ManagedEventStream {
	if s == nil {
		return aipapi.NewFixedEventStream(nil)
	}
	return &chatStream{
		sdk:                    s,
		pending:                stream.NewPendingEventQueue(maxPending),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
}

func (s *chatStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Event{}, err
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return aipapi.Event{}, io.EOF
		}
		if ev, ok := s.pending.PopFront(); ok {
			s.mu.Unlock()
			return ev, nil
		}
		s.mu.Unlock()

		if !s.sdk.Next() {
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				return aipapi.Event{}, io.EOF
			}
			if err := s.sdk.Err(); err != nil {
				s.mu.Unlock()
				return aipapi.Event{}, fmt.Errorf("openai-legacy: recv stream: %w", err)
			}
			if s.terminalEmitted {
				s.mu.Unlock()
				return aipapi.Event{}, io.EOF
			}
			if !s.sawResp {
				s.mu.Unlock()
				return aipapi.Event{}, io.EOF
			}
			s.terminalEmitted = true
			if err := s.pending.Push(s.terminalEvent()); err != nil {
				s.mu.Unlock()
				return aipapi.Event{}, err
			}
			s.mu.Unlock()
			continue
		}
		cur := s.sdk.Current()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		if err := s.handleChunk(cur); err != nil {
			s.mu.Unlock()
			return aipapi.Event{}, err
		}
		s.mu.Unlock()
	}
}

func (s *chatStream) handleChunk(ch openai.ChatCompletionChunk) error {
	if !s.sawResp {
		s.sawResp = true
		if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
			return err
		}
	}

	for _, choice := range ch.Choices {
		d := choice.Delta
		if d.Role != "" && !s.sawMsg {
			s.sawMsg = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
				return err
			}
		}

		if len(d.ToolCalls) > 0 {
			if !s.sawMsg {
				s.sawMsg = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			for _, tc := range d.ToolCalls {
				if s.activeTools == nil {
					s.activeTools = make(map[int64]string)
				}
				if tc.ID != "" {
					if _, seen := s.activeTools[tc.Index]; !seen {
						s.activeToolOrder = append(s.activeToolOrder, tc.Index)
					}
					s.activeTools[tc.Index] = tc.ID
					if err := s.pending.Push(aipapi.Event{
						Kind:       aipapi.EventToolCallStarted,
						ToolCallID: tc.ID,
						ToolName:   tc.Function.Name,
					}); err != nil {
						return err
					}
				}
				if tc.Function.Arguments != "" {
					id := s.activeTools[tc.Index]
					if err := s.pending.Push(aipapi.Event{
						Kind:       aipapi.EventToolCallArgsDelta,
						ToolCallID: id,
						Delta:      tc.Function.Arguments,
					}); err != nil {
						return err
					}
				}
			}
		}

		if reason := chatDeltaReasoningContent(d); reason != "" {
			if !s.sawMsg {
				s.sawMsg = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: reason}); err != nil {
				return err
			}
		}

		if d.Content != "" {
			if !s.sawMsg {
				s.sawMsg = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: d.Content}); err != nil {
				return err
			}
		}

		switch choice.FinishReason {
		case "length":
			s.outputLimit = true
			s.refusal = false
		case "content_filter":
			if !s.outputLimit {
				s.refusal = true
			}
		}

		if choice.FinishReason == "tool_calls" {
			for _, idx := range s.activeToolOrder {
				if id := s.activeTools[idx]; id != "" {
					if err := s.pending.Push(aipapi.Event{
						Kind:       aipapi.EventToolCallFinished,
						ToolCallID: id,
					}); err != nil {
						return err
					}
				}
			}
			s.activeTools = nil
			s.activeToolOrder = nil
		}
	}

	if ch.JSON.Usage.Valid() {
		usage := openaiusage.ChatUsageEvent(ch.Usage)
		openaiusage.AnnotateProviderContext(&usage, ch.ID, string(ch.ServiceTier))
		if err := s.pending.Push(usage); err != nil {
			return err
		}
		if s.ProviderEvidenceBuffer != nil {
			sourceKey := "openai.chat.usage:stream"
			if ch.ID != "" {
				sourceKey = "openai.chat.usage:" + ch.ID
			}
			s.Add(openaiusage.ProviderEvidenceDraft(usage, "openai.chat.v2", sourceKey))
		}
	}
	return nil
}

func (s *chatStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		if s.sdk != nil {
			err = s.sdk.Close()
		}
	})
	return err
}

func (s *chatStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}

// chatDeltaReasoningContent reads OpenAI-compatible reasoning_content / reasoning
// extras that the official SDK leaves in delta ExtraFields / RawJSON.
func chatDeltaReasoningContent(d openai.ChatCompletionChunkChoiceDelta) string {
	if f, ok := d.JSON.ExtraFields["reasoning_content"]; ok {
		if raw := f.Raw(); raw != "" && raw != "null" {
			var s string
			if err := json.Unmarshal([]byte(raw), &s); err == nil && s != "" {
				return s
			}
		}
	}
	if f, ok := d.JSON.ExtraFields["reasoning"]; ok {
		if raw := f.Raw(); raw != "" && raw != "null" {
			var s string
			if err := json.Unmarshal([]byte(raw), &s); err == nil && s != "" {
				return s
			}
		}
	}
	raw := d.RawJSON()
	if raw == "" {
		return ""
	}
	var probe struct {
		ReasoningContent string `json:"reasoning_content"`
		Reasoning        string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	if probe.ReasoningContent != "" {
		return probe.ReasoningContent
	}
	return probe.Reasoning
}
