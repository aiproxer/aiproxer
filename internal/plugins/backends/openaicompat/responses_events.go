package openaicompat

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	responsesbackend "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/protocols/openairesponsestream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/responses"
)

var _ aipapi.ManagedEventStream = (*responsesStream)(nil)

type responsesStream struct {
	noCopy noCopy

	mu        sync.Mutex
	closeOnce sync.Once

	provider string
	sdk      *ssestream.Stream[responses.ResponseStreamEventUnion]

	pending         stream.PendingEventQueue
	mapper          *openairesponsestream.Mapper
	terminalEmitted bool
	closed          bool
	*coremetering.ProviderEvidenceBuffer
}

func NewResponsesStream(provider string, s *ssestream.Stream[responses.ResponseStreamEventUnion], maxPending int) aipapi.ManagedEventStream {
	if s == nil {
		return aipapi.NewFixedEventStream(nil)
	}
	st := &responsesStream{
		provider:               provider,
		sdk:                    s,
		pending:                stream.NewPendingEventQueue(maxPending),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	st.mapper = openairesponsestream.New(&st.pending)
	return st
}

func newUnitResponsesStream() *responsesStream {
	st := &responsesStream{
		pending:                stream.NewPendingEventQueue(0),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	st.mapper = openairesponsestream.New(&st.pending)
	return st
}

func (s *responsesStream) Recv(ctx context.Context) (aipapi.Event, error) {
	pump := stream.EventPump[responses.ResponseStreamEventUnion]{
		Lock:     &s.mu,
		Pending:  &s.pending,
		IsClosed: func() bool { return s.closed },
		Read: func() (responses.ResponseStreamEventUnion, bool, error) {
			if !s.sdk.Next() {
				if err := s.sdk.Err(); err != nil {
					return responses.ResponseStreamEventUnion{}, false, fmt.Errorf("%s: recv responses stream: %w", s.provider, err)
				}
				return responses.ResponseStreamEventUnion{}, false, nil
			}
			return s.sdk.Current(), true, nil
		},
		Handle: s.handleUnion,
		OnEOF: func() (bool, error) {
			return true, s.finishOnEOF()
		},
	}
	return pump.Recv(ctx)
}

func (s *responsesStream) handleUnion(cur responses.ResponseStreamEventUnion) error {
	m := s.mapper
	switch cur.Type {
	case "response.created":
		return m.ResponseCreated()
	case "response.output_text.delta":
		return m.OutputTextDelta(cur.Delta)
	case "response.completed":
		resp := cur.Response
		if err := m.BeginCompleted(); err != nil {
			return err
		}
		if err := m.EmitCompletedOutputByIndex(resp); err != nil {
			return err
		}
		if usage := s.usageFromResponse(resp); usage != nil {
			openaiusage.AnnotateProviderContext(usage, resp.ID, string(resp.ServiceTier))
			if err := m.PushUsage(usage); err != nil {
				return err
			}
			if s.ProviderEvidenceBuffer != nil {
				sourceKey := "openai.responses.usage:stream"
				if resp.ID != "" {
					sourceKey = "openai.responses.usage:" + resp.ID
				}
				s.Add(openaiusage.ProviderEvidenceDraft(*usage, "openai.responses.v2", sourceKey))
			}
		}
		if err := m.ResponseFinished(); err != nil {
			return err
		}
		s.terminalEmitted = true
		return nil
	case "response.incomplete":
		// The upstream cut the turn off (usually the output-token limit). It
		// carries the same payload as response.completed and must terminate the
		// canonical stream with an authoritative incomplete status instead of
		// being dropped, which would leave the turn with no terminal at all.
		resp := cur.Response
		if err := m.BeginCompleted(); err != nil {
			return err
		}
		if err := m.EmitCompletedOutputByIndex(resp); err != nil {
			return err
		}
		if usage := s.usageFromResponse(resp); usage != nil {
			openaiusage.AnnotateProviderContext(usage, resp.ID, string(resp.ServiceTier))
			if err := m.PushUsage(usage); err != nil {
				return err
			}
			if s.ProviderEvidenceBuffer != nil {
				sourceKey := "openai.responses.usage:stream"
				if resp.ID != "" {
					sourceKey = "openai.responses.usage:" + resp.ID
				}
				s.Add(openaiusage.ProviderEvidenceDraft(*usage, "openai.responses.v2", sourceKey))
			}
		}
		if err := m.ResponseFinishedIncomplete(resp.IncompleteDetails.Reason); err != nil {
			return err
		}
		s.terminalEmitted = true
		return nil
	case "error":
		ev := cur.AsError()
		if err := m.StreamError(ev.Code, ev.Message, "stream error"); err != nil {
			return err
		}
		s.terminalEmitted = true
		return nil
	case "response.output_item.added":
		addEv := cur.AsResponseOutputItemAdded()
		item := addEv.Item
		switch item.Type {
		case "function_call":
			fc := item.AsFunctionCall()
			return m.ToolCallAdded(openairesponsestream.ToolCallID(fc.ID, fc.CallID), fc.Name)
		case "reasoning":
			return m.ReasoningOutputItemAdded(addEv.OutputIndex, item)
		default:
			return nil
		}
	case "response.reasoning_summary_part.added":
		ev := cur.AsResponseReasoningSummaryPartAdded()
		return m.ReasoningSummaryPartAdded(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Part.Text)
	case "response.reasoning_summary_part.done":
		ev := cur.AsResponseReasoningSummaryPartDone()
		return m.ReasoningSummaryPartDone(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Part.Text)
	case "response.reasoning_summary_text.delta":
		ev := cur.AsResponseReasoningSummaryTextDelta()
		return m.ReasoningSummaryTextDelta(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Delta)
	case "response.reasoning_summary_text.done":
		ev := cur.AsResponseReasoningSummaryTextDone()
		return m.ReasoningSummaryTextDone(ev.ItemID, ev.OutputIndex, ev.SummaryIndex, ev.Text)
	case "response.reasoning_text.delta":
		ev := cur.AsResponseReasoningTextDelta()
		return m.ReasoningTextDelta(ev.ItemID, ev.OutputIndex, ev.ContentIndex, ev.Delta)
	case "response.reasoning_text.done":
		ev := cur.AsResponseReasoningTextDone()
		return m.ReasoningTextDone(ev.ItemID, ev.OutputIndex, ev.ContentIndex, ev.Text)
	case "response.function_call_arguments.delta":
		d := cur.AsResponseFunctionCallArgumentsDelta()
		id := openairesponsestream.ToolCallIDFromRaw(d.ItemID, d.RawJSON())
		return m.ToolCallArgsDelta(id, d.Delta)
	case "response.function_call_arguments.done":
		d := cur.AsResponseFunctionCallArgumentsDone()
		raw := d.RawJSON()
		id := openairesponsestream.ToolCallIDFromRaw(d.ItemID, raw)
		return m.FinishToolCallArguments(id, openairesponsestream.FunctionNameFromRawJSON(raw), d.Arguments)
	case "response.output_item.done":
		doneEv := cur.AsResponseOutputItemDone()
		item := doneEv.Item
		switch item.Type {
		case "function_call":
			fc := item.AsFunctionCall()
			return m.FinishToolCallArguments(
				openairesponsestream.ToolCallID(fc.ID, fc.CallID),
				fc.Name,
				fc.Arguments,
			)
		case "reasoning":
			return m.ReasoningOutputItemDone(doneEv.OutputIndex, item)
		default:
			return nil
		}
	}
	return nil
}

func (s *responsesStream) finishOnEOF() error {
	if s.closed {
		return nil
	}
	if s.terminalEmitted {
		s.closed = true
		return nil
	}
	if err := s.mapper.FinalizeOnEOF(); err != nil {
		s.closed = true
		return err
	}
	if !s.mapper.SawResponseStarted() {
		s.closed = true
		return nil
	}
	s.terminalEmitted = true
	return s.mapper.ResponseFinished()
}

func (s *responsesStream) usageFromResponse(resp responses.Response) *aipapi.Event {
	if !resp.JSON.Usage.Valid() {
		return nil
	}
	ev := openaiusage.ResponsesUsageEvent(resp.Usage)
	return &ev
}

func ResponseEvents(resp responses.Response) ([]aipapi.Event, error) {
	return responsesbackend.CompletionEvents(resp)
}

func (s *responsesStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	if s.mapper != nil {
		s.mapper.AbortReasoningAssembly()
	}
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		if s.sdk != nil {
			err = s.sdk.Close()
		}
	})
	return err
}

func (s *responsesStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}
