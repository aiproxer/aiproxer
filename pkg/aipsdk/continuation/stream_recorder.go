package continuation

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// StreamRecorder observes canonical events and attempts one terminal write.
// Storage failures are deliberately retained as diagnostics only; they never
// become stream errors and therefore cannot trigger retry after commitment.
type StreamRecorder struct {
	mu         sync.Mutex
	recorder   Recorder
	record     ContinuationRecord
	events     []aipapi.Event
	eventBytes int64
	isClosed   bool
	stored     bool
	overflow   bool
	storeErr   error
	cleanup    func()
}

// NewStreamRecorder creates an observer for one reserved response record.
func NewStreamRecorder(recorder Recorder, record ContinuationRecord, cleanup func()) *StreamRecorder {
	if cleanup == nil {
		panic("continuation: stream recorder cleanup callback is required")
	}
	return &StreamRecorder{
		recorder:   recorder,
		record:     CloneRecord(record),
		cleanup:    cleanup,
		eventBytes: EstimateItemsBytes(record.InputItems),
	}
}

// Observe records a defensive copy of each event and stores only after the
// canonical terminal event. Non-terminal failures and cancellation are never
// eligible for persistence.
func (r *StreamRecorder) Observe(ctx context.Context, event aipapi.Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.isClosed || r.stored || r.overflow {
		r.mu.Unlock()
		return
	}
	size := recorderEventSize(event)
	overflow := size < 0 || r.eventBytes > math.MaxInt64-size
	if maxBytes := r.record.Policy.Limits.MaxRecordBytes; !overflow && maxBytes > 0 && r.eventBytes+size > maxBytes {
		overflow = true
	}
	if overflow {
		r.overflow = true
		r.stored = true
		release := r.cleanup
		r.cleanup = nil
		r.mu.Unlock()
		safeRunCleanup(release)
		return
	}

	r.events = append(r.events, event.Clone())
	r.eventBytes += size
	if event.Kind != aipapi.EventResponseFinished && event.Kind != aipapi.EventError {
		r.mu.Unlock()
		return
	}
	record := CloneRecord(r.record)
	record.Terminal = true
	if event.Kind == aipapi.EventError {
		record.Status = RecordStatusFailed
	} else {
		record.Status = terminalStatusFromEvent(event)
	}
	record.OutputItems = outputItems(r.events)
	record.MaterializedBytes = EstimateItemsBytes(record.InputItems) + EstimateItemsBytes(record.OutputItems)
	recorder := r.recorder
	release := r.cleanup
	r.cleanup = nil
	r.stored = true
	r.mu.Unlock()
	if recorder == nil {
		safeRunCleanup(release)
		return
	}
	if err := safeRecordTerminal(ctx, recorder, record); err != nil {
		r.mu.Lock()
		r.storeErr = err
		r.mu.Unlock()
		safeRunCleanup(release)
	}
}

// terminalStatusFromEvent maps one canonical terminal event to the record
// status, using the same precedence as the OpenResponses state machine and the
// compaction detector: an explicit ResponseStatus is authoritative; otherwise
// only the output-limit finish taxonomy marks the record incomplete. Refusal
// lexemes (refusal, content_filter, SAFETY, guardrail_intervened, ...) are
// complete upstream decisions: recording them as incomplete would make the
// stores reject the record under the default policy while the wire already
// reported the turn as completed, breaking previous_response_id continuity.
func terminalStatusFromEvent(event aipapi.Event) RecordStatus {
	if status := strings.ToLower(strings.TrimSpace(event.ResponseStatus)); status != "" {
		if status == "incomplete" {
			return RecordStatusIncomplete
		}
		return RecordStatusCompleted
	}
	switch strings.ToLower(strings.TrimSpace(event.FinishReason)) {
	case "length", "max_tokens", "max_output_tokens":
		return RecordStatusIncomplete
	default:
		return RecordStatusCompleted
	}
}

func safeRecordTerminal(ctx context.Context, recorder Recorder, record ContinuationRecord) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("continuation: terminal recorder panicked: %v", recovered)
		}
	}()
	return recorder.RecordTerminal(ctx, record)
}

func safeRunCleanup(release func()) {
	if release == nil {
		return
	}
	defer func() { _ = recover() }()
	release()
}

// OwnsContinuationReservation identifies the recorder as the owner of the
// reservation cleanup callback passed at construction time.
func (r *StreamRecorder) OwnsContinuationReservation() bool { return r != nil }

// ContinuationReservationCleanupConsumed reports whether the recorder detached
// its cleanup callback.
func (r *StreamRecorder) ContinuationReservationCleanupConsumed() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cleanup == nil
}

// ReleaseContinuationReservation consumes the reservation cleanup callback
// without attempting to persist a terminal record.
func (r *StreamRecorder) ReleaseContinuationReservation() {
	if r == nil {
		return
	}
	r.mu.Lock()
	release := r.cleanup
	r.cleanup = nil
	r.stored = true
	r.mu.Unlock()
	safeRunCleanup(release)
}

// FinalizeIncomplete persists the events observed so far as an incomplete
// terminal record. It is idempotent.
func (r *StreamRecorder) FinalizeIncomplete(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.stored || r.overflow {
		err := r.storeErr
		r.mu.Unlock()
		return err
	}
	record := CloneRecord(r.record)
	record.Terminal = true
	record.Status = RecordStatusIncomplete
	record.OutputItems = outputItems(r.events)
	record.MaterializedBytes = EstimateItemsBytes(record.InputItems) + EstimateItemsBytes(record.OutputItems)
	recorder := r.recorder
	release := r.cleanup
	r.stored = true
	r.cleanup = nil
	r.mu.Unlock()
	if recorder == nil {
		safeRunCleanup(release)
		return nil
	}
	var recordErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				recordErr = fmt.Errorf("continuation: terminal recorder panicked: %v", recovered)
			}
		}()
		recordErr = recorder.RecordTerminal(ctx, record)
	}()
	if recordErr != nil {
		r.mu.Lock()
		r.storeErr = recordErr
		r.mu.Unlock()
		safeRunCleanup(release)
		return recordErr
	}
	return nil
}

func recorderEventSize(event aipapi.Event) int64 {
	return int64(event.PayloadBytes())
}

// Close is idempotent and does not turn a partial/cancelled stream into a
// stored continuation record.
func (r *StreamRecorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.isClosed = true
	stored := r.stored
	release := r.cleanup
	if !stored {
		r.cleanup = nil
	}
	r.mu.Unlock()
	if !stored {
		safeRunCleanup(release)
	}
	return nil
}

// StorageError exposes a best-effort persistence failure for diagnostics/tests.
func (r *StreamRecorder) StorageError() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storeErr
}

func outputItems(events []aipapi.Event) []aipapi.Item {
	items := make([]aipapi.Item, 0, 2)
	var text strings.Builder
	var reasoningActive bool
	var reasoningText strings.Builder
	var reasoningSig strings.Builder
	var reasoningOpaque []byte
	toolCalls := make(map[string]*aipapi.ToolCallItem)
	toolOrder := make([]string, 0)
	var activeToolID string

	flushText := func() {
		if text.Len() == 0 {
			return
		}
		items = append(items, aipapi.Item{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Status: aipapi.ItemStatusCompleted, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text.String()}}})
		text.Reset()
	}
	flushReasoning := func() {
		if !reasoningActive {
			return
		}
		if reasoningText.Len() == 0 && reasoningSig.Len() == 0 && len(reasoningOpaque) == 0 {
			reasoningActive = false
			return
		}
		items = append(items, aipapi.Item{Kind: aipapi.ItemKindReasoning, Status: aipapi.ItemStatusCompleted, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{Text: reasoningText.String(), Signature: reasoningSig.String(), Opaque: append([]byte(nil), reasoningOpaque...)}}})
		reasoningText.Reset()
		reasoningSig.Reset()
		reasoningOpaque = nil
		reasoningActive = false
	}
	for _, event := range events {
		switch event.Kind {
		case aipapi.EventTextDelta:
			flushReasoning()
			text.WriteString(event.Delta)
		case aipapi.EventReasoningDelta:
			flushText()
			reasoningActive = true
			reasoningText.WriteString(event.Delta)
		case aipapi.EventReasoningSignatureDelta, aipapi.EventReasoningOpaqueDelta:
			flushText()
			reasoningActive = true
			if event.Kind == aipapi.EventReasoningSignatureDelta {
				reasoningSig.WriteString(event.Signature)
			} else {
				reasoningOpaque = append(reasoningOpaque, event.Opaque...)
			}
		case aipapi.EventReasoningPart:
			flushText()
			flushReasoning()
			if event.Reasoning != nil {
				reasoningActive = true
				reasoningText.WriteString(event.Reasoning.Text)
				reasoningSig.WriteString(event.Reasoning.Signature)
				reasoningOpaque = append([]byte(nil), event.Reasoning.Opaque...)
			}
		case aipapi.EventToolCallStarted:
			flushText()
			flushReasoning()
			id := event.ToolCallID
			if id != "" {
				activeToolID = id
				if _, exists := toolCalls[id]; !exists {
					toolCalls[id] = &aipapi.ToolCallItem{CallID: id, Name: event.ToolName}
					toolOrder = append(toolOrder, id)
				} else if event.ToolName != "" {
					toolCalls[id].Name = event.ToolName
				}
			}
		case aipapi.EventToolCallArgsDelta:
			id := event.ToolCallID
			if id == "" {
				id = activeToolID
			}
			if id != "" {
				tc, exists := toolCalls[id]
				if !exists {
					tc = &aipapi.ToolCallItem{CallID: id}
					toolCalls[id] = tc
					toolOrder = append(toolOrder, id)
				}
				tc.Arguments = append(tc.Arguments, []byte(event.Delta)...)
			}
		case aipapi.EventToolCallFinished:
			flushText()
			flushReasoning()
			id := event.ToolCallID
			if id == "" {
				id = activeToolID
			}
			if id != "" {
				if tc, ok := toolCalls[id]; ok {
					items = append(items, aipapi.Item{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: tc})
					delete(toolCalls, id)
				}
			}
		}
	}
	flushText()
	flushReasoning()
	for _, id := range toolOrder {
		if tc, ok := toolCalls[id]; ok {
			items = append(items, aipapi.Item{Kind: aipapi.ItemKindToolCall, Status: aipapi.ItemStatusCompleted, ToolCall: tc})
		}
	}
	return items
}
