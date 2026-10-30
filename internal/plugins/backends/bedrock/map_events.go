package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/safecast"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// recvSelectEntryHook is set from bedrock package tests to observe Recv reaching the
// blocking select on the SDK event channel (must be cleared via t.Cleanup).
var (
	recvSelectHookMu    sync.Mutex
	recvSelectEntryHook func()
)

func callRecvSelectEntryHook() {
	recvSelectHookMu.Lock()
	h := recvSelectEntryHook
	recvSelectHookMu.Unlock()
	if h != nil {
		h()
	}
}

// converseStream adapts the Bedrock ConverseStream SDK channel to aipapi.EventStream.
//
// Concurrency: one goroutine calls Recv at a time. Close may run concurrently with
// Recv blocked on the SDK event channel; Close closes the SDK stream to unblock Recv.
type converseStream struct {
	mu        sync.Mutex
	closeOnce sync.Once

	sdk *bedrockruntime.ConverseStreamEventStream
	ch  <-chan types.ConverseStreamOutput

	pending stream.PendingEventQueue

	sawResponse bool
	sawMessage  bool
	closed      bool
	afterFinish bool

	activeToolID string
	// stopReason holds the messageStop stopReason until the channel closes and
	// the terminal is emitted.
	stopReason string
	*coremetering.ProviderEvidenceBuffer
}

// terminalEvent builds the canonical terminal for this stream. An
// output-token-limit stopReason carries incomplete/max_tokens; a
// guardrail_intervened refusal is carried verbatim with no incomplete
// classification (a refusal is a complete upstream decision, never length
// taxonomy). Every other stop leaves FinishReason empty so the continuation
// recorder and compaction detection keep treating it as a clean terminal.
func (s *converseStream) terminalEvent() aipapi.Event {
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished}
	switch s.stopReason {
	case string(types.StopReasonMaxTokens):
		ev.ResponseStatus = "incomplete"
		ev.FinishReason = string(types.StopReasonMaxTokens)
	case string(types.StopReasonGuardrailIntervened):
		ev.FinishReason = string(types.StopReasonGuardrailIntervened)
	}
	return ev
}

func newConverseStream(sdk *bedrockruntime.ConverseStreamEventStream, maxPending int) aipapi.ManagedEventStream {
	if sdk == nil {
		return aipapi.NewFixedEventStream(nil)
	}
	return &converseStream{
		sdk:                    sdk,
		ch:                     sdk.Events(),
		pending:                stream.NewPendingEventQueue(maxPending),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
}

func (s *converseStream) Close() error {
	if s == nil || s.sdk == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	var err error
	s.closeOnce.Do(func() {
		err = s.sdk.Close()
	})
	return err
}

func (s *converseStream) Cancel(_ context.Context, _ leglifecycle.CancelCause) leglifecycle.CancelResult {
	err := s.Close()
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeTransport, Err: err}
}

func (s *converseStream) Recv(ctx context.Context) (aipapi.Event, error) {
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
			if ev.Kind == aipapi.EventResponseFinished {
				s.afterFinish = true
			}
			s.mu.Unlock()
			return ev, nil
		}
		if s.afterFinish {
			s.mu.Unlock()
			return aipapi.Event{}, io.EOF
		}
		s.mu.Unlock()

		callRecvSelectEntryHook()
		select {
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case out, ok := <-s.ch:
			if !ok {
				s.mu.Lock()
				if s.closed {
					s.mu.Unlock()
					return aipapi.Event{}, io.EOF
				}
				if err := s.sdk.Err(); err != nil {
					s.mu.Unlock()
					return aipapi.Event{}, fmt.Errorf("bedrock: recv stream: %w", err)
				}
				if !s.sawResponse {
					s.sawResponse = true
					if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
						s.mu.Unlock()
						return aipapi.Event{}, err
					}
				}
				if err := s.pending.Push(s.terminalEvent()); err != nil {
					s.mu.Unlock()
					return aipapi.Event{}, err
				}
				s.mu.Unlock()
				continue
			}
			s.mu.Lock()
			if s.closed {
				s.mu.Unlock()
				continue
			}
			if err := s.handleOutput(out); err != nil {
				s.mu.Unlock()
				return aipapi.Event{}, err
			}
			s.mu.Unlock()
		}
	}
}

func (s *converseStream) handleOutput(out types.ConverseStreamOutput) error {
	switch v := out.(type) {
	case *types.ConverseStreamOutputMemberMessageStart:
		if v.Value.Role == types.ConversationRoleAssistant {
			if !s.sawResponse {
				s.sawResponse = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
					return err
				}
			}
			if !s.sawMessage {
				s.sawMessage = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
		}
	case *types.ConverseStreamOutputMemberContentBlockStart:
		return s.handleBlockStart(v.Value)
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		return s.handleBlockDelta(v.Value)
	case *types.ConverseStreamOutputMemberContentBlockStop:
		return s.handleBlockStop(v.Value)
	case *types.ConverseStreamOutputMemberMessageStop:
		// The Converse channel closes after messageStop, so the stopReason is
		// captured here and applied to the terminal event emitted at close.
		s.stopReason = strings.TrimSpace(string(v.Value.StopReason))
	case *types.ConverseStreamOutputMemberMetadata:
		if u := v.Value.Usage; u != nil {
			// AWS ConverseStream usage uses *int32 token fields; preserve
			// explicit presence while rejecting malformed negative values.
			inT, inputPresent := bedrockUsageCount(u.InputTokens)
			outT, outputPresent := bedrockUsageCount(u.OutputTokens)
			totalT, totalPresent := bedrockUsageCount(u.TotalTokens)
			cacheReadT, cacheReadPresent := bedrockUsageCount(u.CacheReadInputTokens)
			cacheWriteT, cacheWritePresent := bedrockUsageCount(u.CacheWriteInputTokens)
			presence := aipapi.UsagePresence{
				InputTokens: inputPresent, OutputTokens: outputPresent,
				CacheReadTokens: cacheReadPresent, CacheWriteTokens: cacheWritePresent,
				TotalTokens: totalPresent,
			}
			if presence.Any() {
				ev := aipapi.Event{
					Kind:             aipapi.EventUsageDelta,
					InputTokens:      inT,
					OutputTokens:     outT,
					CacheReadTokens:  cacheReadT,
					CacheWriteTokens: cacheWriteT,
					TotalTokens:      totalT,
					UsagePresence:    presence,
					RawUsageJSON:     rawUsageJSON(u),
					Accounting: aipapi.UsageAccountingMetadata{
						Plane:     aipapi.UsagePlaneProviderBillable,
						Source:    aipapi.UsageSourceProviderReported,
						Authority: aipapi.UsageAuthorityAuthoritative,
						DedupeKey: "bedrock.converse:stream",
					},
				}
				if err := s.pending.Push(ev); err != nil {
					return err
				}
				s.Add(bedrockEvidenceDraft(ev, u))
			}
		}
	default:
		// ignore unknown union members
	}
	return nil
}

func bedrockUsageCount(value *int32) (int, bool) {
	if value == nil || *value < 0 {
		return 0, false
	}
	return safecast.IntFromInt64Clamp(int64(*value)), true
}

func (s *converseStream) handleBlockStart(ev types.ContentBlockStartEvent) error {
	switch st := ev.Start.(type) {
	case *types.ContentBlockStartMemberToolUse:
		tu := st.Value
		id := aws.ToString(tu.ToolUseId)
		name := aws.ToString(tu.Name)
		if id == "" {
			return nil
		}
		s.activeToolID = id
		if !s.sawResponse {
			s.sawResponse = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		return s.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallStarted,
			ToolCallID: id,
			ToolName:   name,
		})
	default:
		return nil
	}
}

func (s *converseStream) handleBlockDelta(ev types.ContentBlockDeltaEvent) error {
	switch d := ev.Delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		if d.Value == "" {
			return nil
		}
		if !s.sawResponse {
			s.sawResponse = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		return s.pending.Push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: d.Value})
	case *types.ContentBlockDeltaMemberToolUse:
		if d.Value.Input == nil || *d.Value.Input == "" {
			return nil
		}
		if s.activeToolID == "" {
			return nil
		}
		if !s.sawResponse {
			s.sawResponse = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
				return err
			}
		}
		if !s.sawMessage {
			s.sawMessage = true
			if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
				return err
			}
		}
		return s.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallArgsDelta,
			ToolCallID: s.activeToolID,
			Delta:      *d.Value.Input,
		})
	case *types.ContentBlockDeltaMemberReasoningContent:
		if txt := reasoningDeltaTextFromUnion(d.Value); txt != "" {
			if !s.sawResponse {
				s.sawResponse = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventResponseStarted}); err != nil {
					return err
				}
			}
			if !s.sawMessage {
				s.sawMessage = true
				if err := s.pending.Push(aipapi.Event{Kind: aipapi.EventMessageStarted}); err != nil {
					return err
				}
			}
			return s.pending.Push(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: txt})
		}
		return nil
	default:
		return nil
	}
}

func reasoningDeltaTextFromUnion(delta types.ReasoningContentBlockDelta) string {
	switch x := delta.(type) {
	case *types.ReasoningContentBlockDeltaMemberText:
		return x.Value
	default:
		return ""
	}
}

func (s *converseStream) handleBlockStop(ev types.ContentBlockStopEvent) error {
	if s.activeToolID != "" {
		if err := s.pending.Push(aipapi.Event{
			Kind:       aipapi.EventToolCallFinished,
			ToolCallID: s.activeToolID,
		}); err != nil {
			return err
		}
		s.activeToolID = ""
	}
	_ = ev
	return nil
}

func rawUsageJSON(usage any) string {
	b, err := json.Marshal(usage)
	if err != nil {
		return ""
	}
	return string(b)
}
