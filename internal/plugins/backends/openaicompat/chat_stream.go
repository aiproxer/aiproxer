package openaicompat

import (
	"context"
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

var _ aipapi.ManagedEventStream = (*chatStream)(nil)

// noCopy lets go vet's copylocks analyzer catch accidental copies of stream
// state after first use.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

type chatStream struct {
	noCopy noCopy

	mu        sync.Mutex
	closeOnce sync.Once

	provider string
	sdk      *ssestream.Stream[openai.ChatCompletionChunk]

	pending         stream.PendingEventQueue
	sawResp         bool
	sawMsg          bool
	terminalEmitted bool
	closed          bool
	activeTools     map[int64]string
	pendingToolArgs map[int64][]string
	activeToolOrder []int64
	// outputLimit records that the upstream ended the turn on the OpenAI
	// output-token-limit lexeme; it is applied to the terminal event.
	outputLimit bool
	*coremetering.ProviderEvidenceBuffer
}

// NewChatStream maps an openai-go chat-completions stream into canonical events.
func NewChatStream(provider string, s *ssestream.Stream[openai.ChatCompletionChunk], maxPending int) aipapi.ManagedEventStream {
	if s == nil {
		return aipapi.NewFixedEventStream(nil)
	}
	return &chatStream{
		provider:               provider,
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
				return aipapi.Event{}, fmt.Errorf("%s: recv chat stream: %w", s.provider, err)
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

func (s *chatStream) ensureMsgStarted() error {
	if !s.sawMsg {
		s.sawMsg = true
		return s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted})
	}
	return nil
}

// terminalEvent builds the canonical terminal for this stream. Only an
// output-token-limit finish_reason is classified; every other stop leaves
// FinishReason empty so the continuation recorder and compaction detection keep
// treating it as a clean terminal.
func (s *chatStream) terminalEvent() aipapi.Event {
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
	if s.outputLimit {
		ev.ResponseStatus = "incomplete"
		ev.FinishReason = "length"
	}
	return ev
}

func (s *chatStream) handleToolCall(tc openai.ChatCompletionChunkChoiceDeltaToolCall) error {
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
		for _, args := range s.pendingToolArgs[tc.Index] {
			if err := s.pending.Push(aipapi.Event{
				Kind:       aipapi.EventToolCallArgsDelta,
				ToolCallID: tc.ID,
				Delta:      args,
			}); err != nil {
				return err
			}
		}
		delete(s.pendingToolArgs, tc.Index)
	}
	if tc.Function.Arguments != "" {
		id := s.activeTools[tc.Index]
		if id == "" {
			if s.pendingToolArgs == nil {
				s.pendingToolArgs = make(map[int64][]string)
			}
			s.pendingToolArgs[tc.Index] = append(s.pendingToolArgs[tc.Index], tc.Function.Arguments)
			return nil
		}
		if err := s.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: id,
			Delta:      tc.Function.Arguments,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *chatStream) handleChoice(choice openai.ChatCompletionChunkChoice) error {
	d := choice.Delta
	if d.Role != "" {
		if err := s.ensureMsgStarted(); err != nil {
			return err
		}
	}

	if reasoning := ReasoningTextFromChunkDelta(d); reasoning != "" {
		if err := s.ensureMsgStarted(); err != nil {
			return err
		}
		if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: reasoning}); err != nil {
			return err
		}
	}

	if len(d.ToolCalls) > 0 {
		if err := s.ensureMsgStarted(); err != nil {
			return err
		}
		for _, tc := range d.ToolCalls {
			if err := s.handleToolCall(tc); err != nil {
				return err
			}
		}
	}

	if d.Content != "" {
		if err := s.ensureMsgStarted(); err != nil {
			return err
		}
		if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: d.Content}); err != nil {
			return err
		}
	}

	if choice.FinishReason == "length" {
		s.outputLimit = true
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
		s.pendingToolArgs = nil
		s.activeToolOrder = nil
	}
	return nil
}

func (s *chatStream) handleChunk(ch openai.ChatCompletionChunk) error {
	if !s.sawResp {
		s.sawResp = true
		if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
			return err
		}
	}

	for _, choice := range ch.Choices {
		if err := s.handleChoice(choice); err != nil {
			return err
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
