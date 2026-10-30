package openresponses

import (
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func (sm *StateMachine) handleResponseStarted(ev aipapi.Event, events *[]StreamEvent) error {
	if sm.state != StateInit {
		return &SequenceError{
			Code:     "duplicate_start",
			Event:    string(ev.Kind),
			Sequence: sm.sequenceNum,
			Message:  "response already started",
			Err:      ErrInvalidLifecycleState,
		}
	}
	sm.state = StateStarted
	// Reserve the initial response envelope now, even though its wire event
	// is deferred until an output item can be announced.
	_, resourceData, snapshotErr := sm.snapshotResource("in_progress")
	if snapshotErr != nil {
		return snapshotErr
	}
	sm.resourceBytes = len(resourceData)
	if err := sm.validateResourceBudget(); err != nil {
		return err
	}
	// The response.created event is deliberately deferred until an output
	// item has been announced. This makes output_item.added the first event
	// while retaining the created lifecycle event for compatibility.

	return nil
}

func (sm *StateMachine) handleMessageStarted(ev aipapi.Event, events *[]StreamEvent) error {
	if err := sm.closeActiveContentPart(events); err != nil {
		return err
	}
	if err := sm.closeActiveItem(events); err != nil {
		return err
	}
	if err := sm.startMessageItem(events); err != nil {
		return err
	}

	return nil
}

func (sm *StateMachine) handleTextDelta(ev aipapi.Event, events *[]StreamEvent) error {
	if sm.activeItem == nil || sm.activeItem.Kind != aipapi.ItemKindMessage || sm.activeContentPart == nil {
		if err := sm.synthesizeMessageAfterReasoning(events); err != nil {
			return err
		}
	}

	if err := sm.reserveEncodedDelta(ev.Delta); err != nil {
		return err
	}
	builder := sm.textBuilders[sm.activeItemIdx]

	if builder == nil {
		builder = &deltaBuffer{}
		builder.WriteString(sm.activeContentPart.Text)
		sm.textBuilders[sm.activeItemIdx] = builder
		sm.textPartIndexes[sm.activeItemIdx] = sm.activeContentIdx
	}
	builder.WriteString(ev.Delta)
	*events = append(*events, StreamEvent{
		Type:           "response.output_text.delta",
		SequenceNumber: sm.nextSeq(),
		ItemID:         sm.activeItem.ID,
		OutputIndex:    new(sm.activeItemIdx),
		ContentIndex:   new(sm.activeContentIdx),
		Delta:          ev.Delta,
	})

	return nil
}

func (sm *StateMachine) handleReasoning(ev aipapi.Event, events *[]StreamEvent) error {
	if sm.activeItem == nil || sm.activeItem.Kind != aipapi.ItemKindReasoning {
		if err := sm.closeActiveContentPart(events); err != nil {
			return err
		}
		if err := sm.closeActiveItem(events); err != nil {
			return err
		}

		rsItem := aipapi.Item{
			ID:     "rs_" + strconv.Itoa(len(sm.trajectory)),
			Kind:   aipapi.ItemKindReasoning,
			Status: aipapi.ItemStatusInProgress,
			Reasoning: &aipapi.ReasoningItem{
				Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialect("openresponses.reasoning.v1"),
					Text:    "",
				},
			},
		}
		sm.trajectory = append(sm.trajectory, rsItem)
		if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
			return err
		}
		sm.activeItemIdx = len(sm.trajectory) - 1
		sm.activeItem = &sm.trajectory[sm.activeItemIdx]
		if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
			return err
		}

		wItem, err := EncodeItem(*sm.activeItem)
		if err != nil {
			return err
		}

		*events = append(*events, StreamEvent{
			Type:           "response.output_item.added",
			SequenceNumber: sm.nextSeq(),
			OutputIndex:    new(sm.activeItemIdx),
			Item:           &wItem,
		})
		if err := sm.appendResponseCreated(events); err != nil {
			return err
		}
	}

	if sm.activeItem.Reasoning == nil || sm.activeItem.Reasoning.Reasoning == nil {
		return &SequenceError{Code: "reasoning_item_missing", Event: string(ev.Kind), Sequence: sm.sequenceNum, Message: "reasoning item payload is unavailable", Err: ErrInvalidLifecycleState}
	}
	sm.touchItem(sm.activeItemIdx)
	// Rebind after touchItem: the snapshot stores a deep copy for rollback,
	// while the active pointer remains the live trajectory item.
	sm.activeItem = &sm.trajectory[sm.activeItemIdx]
	part := sm.activeItem.Reasoning.Reasoning
	switch ev.Kind {
	case aipapi.EventReasoningPart:
		if ev.Reasoning == nil {
			return &SequenceError{Code: "reasoning_part_missing", Event: string(ev.Kind), Sequence: sm.sequenceNum, Message: "reasoning part payload is unavailable", Err: ErrInvalidLifecycleState}
		}
		part.Dialect = ev.Reasoning.Dialect
		part.Text = ev.Reasoning.Text
		part.Signature = ev.Reasoning.Signature
		part.Opaque = append(part.Opaque[:0], ev.Reasoning.Opaque...)
		part.Summary = append(part.Summary[:0], ev.Reasoning.Summary...)
		part.SummaryPresent = ev.Reasoning.SummaryPresent
		part.Content = append(part.Content[:0], ev.Reasoning.Content...)
		part.ContentPresent = ev.Reasoning.ContentPresent
		part.EncryptedContent = append(part.EncryptedContent[:0], ev.Reasoning.EncryptedContent...)
		part.EncryptedContentPresent = ev.Reasoning.EncryptedContentPresent
		if part.Text != "" {
			*events = append(*events, StreamEvent{Type: "response.reasoning.delta", SequenceNumber: sm.nextSeq(), ItemID: sm.activeItem.ID, OutputIndex: new(sm.activeItemIdx), ContentIndex: new(0), Delta: part.Text})
		}
		if part.Signature != "" {
			*events = append(*events, StreamEvent{Type: "response.reasoning.signature.delta", SequenceNumber: sm.nextSeq(), ItemID: sm.activeItem.ID, OutputIndex: new(sm.activeItemIdx), ContentIndex: new(0), Signature: part.Signature})
		}
		if len(part.Opaque) > 0 {
			*events = append(*events, StreamEvent{Type: "response.reasoning.opaque.delta", SequenceNumber: sm.nextSeq(), ItemID: sm.activeItem.ID, OutputIndex: new(sm.activeItemIdx), ContentIndex: new(0), Opaque: append([]byte(nil), part.Opaque...)})
		}
	case aipapi.EventReasoningDelta:
		if err := sm.reserveEncodedDelta(ev.Delta); err != nil {
			return err
		}
		builder := sm.reasoningBuilders[sm.activeItemIdx]
		if builder == nil {
			builder = &deltaBuffer{}
			builder.WriteString(part.Text)
			sm.reasoningBuilders[sm.activeItemIdx] = builder
		}
		builder.WriteString(ev.Delta)
		*events = append(*events, StreamEvent{
			Type:           "response.reasoning.delta",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    new(sm.activeItemIdx),
			ContentIndex:   new(0),
			Delta:          ev.Delta,
		})
	case aipapi.EventReasoningSignatureDelta:
		part.Signature += ev.Signature
		*events = append(*events, StreamEvent{
			Type:           "response.reasoning.signature.delta",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    new(sm.activeItemIdx),
			ContentIndex:   new(0),
			Signature:      ev.Signature,
		})
	case aipapi.EventReasoningOpaqueDelta:
		part.Opaque = append(part.Opaque, ev.Opaque...)
		*events = append(*events, StreamEvent{
			Type:           "response.reasoning.opaque.delta",
			SequenceNumber: sm.nextSeq(),
			ItemID:         sm.activeItem.ID,
			OutputIndex:    new(sm.activeItemIdx),
			ContentIndex:   new(0),
			Opaque:         append([]byte(nil), ev.Opaque...),
		})
	}

	return nil
}

func (sm *StateMachine) handleToolCallStarted(ev aipapi.Event, events *[]StreamEvent) error {
	// A new parallel tool call may begin while another tool call remains
	// open. Close ordinary text/reasoning items, but keep existing tool
	// calls indexed so their later deltas can be interleaved safely.
	if sm.activeItem != nil && sm.activeItem.Kind != aipapi.ItemKindToolCall {
		if err := sm.closeActiveContentPart(events); err != nil {
			return err
		}
		if err := sm.closeActiveItem(events); err != nil {
			return err
		}
	}

	callID := ev.ToolCallID
	if callID == "" {
		callID = "call_" + strconv.Itoa(len(sm.trajectory))
	}

	tcItem := aipapi.Item{
		ID:     "fc_" + strconv.Itoa(len(sm.trajectory)),
		Kind:   aipapi.ItemKindToolCall,
		Status: aipapi.ItemStatusInProgress,
		ToolCall: &aipapi.ToolCallItem{
			CallID:    callID,
			Name:      ev.ToolName,
			Arguments: []byte{},
		},
	}
	sm.trajectory = append(sm.trajectory, tcItem)
	if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
		return err
	}
	sm.activeItemIdx = len(sm.trajectory) - 1
	sm.activeItem = &sm.trajectory[sm.activeItemIdx]
	if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
		return err
	}
	if sm.activeToolCalls == nil {
		sm.activeToolCalls = make(map[string]int)
	}
	if _, exists := sm.activeToolCalls[callID]; exists {
		return &SequenceError{Code: "duplicate_tool_call", Event: string(ev.Kind), ID: callID, Sequence: sm.sequenceNum, Message: "tool call ID already active", Err: ErrMismatchedID}
	}
	sm.touchToolCall(callID)
	sm.activeToolCalls[callID] = sm.activeItemIdx

	wItem, err := EncodeItem(*sm.activeItem)
	if err != nil {
		return err
	}

	*events = append(*events, StreamEvent{
		Type:           "response.output_item.added",
		SequenceNumber: sm.nextSeq(),
		OutputIndex:    new(sm.activeItemIdx),
		Item:           &wItem,
	})
	if err := sm.appendResponseCreated(events); err != nil {
		return err
	}

	return nil
}

func (sm *StateMachine) handleToolCallArgsDelta(ev aipapi.Event, events *[]StreamEvent) error {
	callID := ev.ToolCallID
	if callID == "" && sm.activeItem != nil && sm.activeItem.Kind == aipapi.ItemKindToolCall {
		callID = sm.activeItem.ToolCall.CallID
	}
	idx, ok := sm.activeToolCalls[callID]
	if !ok || idx < 0 || idx >= len(sm.trajectory) || sm.trajectory[idx].ToolCall == nil {
		return &SequenceError{Code: "tool_args_without_item", Event: string(ev.Kind), ID: callID, Sequence: sm.sequenceNum, Message: "tool call args delta without active tool call item", Err: ErrMismatchedID}
	}
	if err := sm.reserveEncodedDelta(ev.Delta); err != nil {
		return err
	}
	item := &sm.trajectory[idx]
	builder := sm.toolArgBuilders[idx]
	if builder == nil {
		builder = &deltaBuffer{}
		builder.Write(item.ToolCall.Arguments)
		sm.toolArgBuilders[idx] = builder
	}
	builder.WriteString(ev.Delta)
	*events = append(*events, StreamEvent{Type: "response.function_call_arguments.delta", SequenceNumber: sm.nextSeq(), ItemID: item.ID, OutputIndex: new(idx), CallID: item.ToolCall.CallID, Delta: ev.Delta})

	return nil
}

func (sm *StateMachine) handleToolCallFinished(ev aipapi.Event, events *[]StreamEvent) error {
	callID := ev.ToolCallID
	if callID == "" && sm.activeItem != nil && sm.activeItem.Kind == aipapi.ItemKindToolCall {
		callID = sm.activeItem.ToolCall.CallID
	}
	idx, ok := sm.activeToolCalls[callID]
	if !ok || idx < 0 || idx >= len(sm.trajectory) || sm.trajectory[idx].ToolCall == nil {
		return &SequenceError{Code: "tool_finished_without_item", Event: string(ev.Kind), ID: callID, Sequence: sm.sequenceNum, Message: "tool call finished without active tool call item", Err: ErrMismatchedID}
	}
	if err := sm.closeToolCallAt(idx, events); err != nil {
		return err
	}

	return nil
}

func (sm *StateMachine) handleCarriedItem(ev aipapi.Event, events *[]StreamEvent) error {
	// A standalone canonical item carrier (provider compaction item in a
	// compacted ordered window). It is not content-class output and never
	// synthesizes message/reasoning/tool semantics; legacy normalizers never
	// produce it. Close any active item and append the carried item in
	// trajectory order, marking an empty status completed.
	if ev.Item == nil {
		return &SequenceError{
			Code:     "item_event_without_item",
			Event:    string(ev.Kind),
			Sequence: sm.sequenceNum,
			Message:  "item event carried no canonical item",
			Err:      ErrInvalidLifecycleState,
		}
	}
	if err := sm.closeActiveContentPart(events); err != nil {
		return err
	}
	if err := sm.closeActiveItem(events); err != nil {
		return err
	}
	carried := cloneItem(*ev.Item)
	if carried.Status == "" {
		carried.Status = aipapi.ItemStatusCompleted
	}
	sm.trajectory = append(sm.trajectory, carried)
	if err := ValidateItemCount(len(sm.trajectory), sm.limits); err != nil {
		return err
	}
	sm.activeItemIdx = len(sm.trajectory) - 1
	sm.activeItem = &sm.trajectory[sm.activeItemIdx]
	if err := sm.reserveItemBytes(*sm.activeItem); err != nil {
		return err
	}

	wItem, err := EncodeItem(*sm.activeItem)
	if err != nil {
		return err
	}
	*events = append(*events, StreamEvent{
		Type:           "response.output_item.added",
		SequenceNumber: sm.nextSeq(),
		OutputIndex:    new(sm.activeItemIdx),
		Item:           &wItem,
	})
	if err := sm.appendResponseCreated(events); err != nil {
		return err
	}
	// Standalone items are complete when carried; emit the done marker.
	if err := sm.closeActiveItem(events); err != nil {
		return err
	}

	return nil
}

func (sm *StateMachine) handleUsageDelta(ev aipapi.Event, events *[]StreamEvent) error {
	sm.usage.InputTokens = ev.InputTokens
	sm.usage.OutputTokens = ev.OutputTokens
	sm.usage.TotalTokens = ev.TotalTokens
	sm.usage.CachedTokens = ev.CacheReadTokens
	sm.usage.ReasoningTokens = ev.ReasoningTokens

	return nil
}

func (sm *StateMachine) handleError(ev aipapi.Event, events *[]StreamEvent) error {
	if !sm.responseCreatedEmitted {
		if err := sm.startMessageItem(events); err != nil {
			return err
		}
	}
	if err := sm.reserveEncodedDelta(ev.ErrorMessage); err != nil {
		return err
	}
	if err := sm.closeActiveContentPart(events); err != nil {
		return err
	}
	if err := sm.closeActiveItem(events); err != nil {
		return err
	}
	for _, idx := range sm.activeToolCallIndexes() {
		if err := sm.closeToolCallAt(idx, events); err != nil {
			return err
		}
	}

	sm.status = "failed"
	sm.streamErr = &aipapi.StreamError{
		Code:    ev.ErrorCode,
		Message: ev.ErrorMessage,
	}
	sm.state = StateTerminal

	wireRes, _, err := sm.snapshotResource("failed")
	if err != nil {
		return err
	}

	*events = append(*events, StreamEvent{
		Type:           "response.failed",
		SequenceNumber: sm.nextSeq(),
		Response:       wireRes,
	})

	return nil
}

func (sm *StateMachine) handleResponseFinished(ev aipapi.Event, events *[]StreamEvent) error {
	if !sm.responseCreatedEmitted {
		if err := sm.startMessageItem(events); err != nil {
			return err
		}
	}
	if err := sm.closeActiveContentPart(events); err != nil {
		return err
	}
	if err := sm.closeActiveItem(events); err != nil {
		return err
	}
	for _, idx := range sm.activeToolCallIndexes() {
		if err := sm.closeToolCallAt(idx, events); err != nil {
			return err
		}
	}

	finalStatus := "completed"
	switch {
	case ev.ResponseStatus == "incomplete":
		// Explicit upstream incomplete semantics win regardless of the
		// finish reason so non-length reasons (content_filter,
		// interruption, unknown, ...) are never rewritten to completed.
		finalStatus = "incomplete"
	case ev.ResponseStatus == "completed":
		// Explicit upstream completed semantics win too; a completed
		// response may legitimately carry finish_reason content_filter.
		finalStatus = "completed"
	case ev.FinishReason == "length" || ev.FinishReason == "max_tokens":
		// Legacy providers do not carry explicit response status; keep the
		// token-limit inference for their terminal event.
		finalStatus = "incomplete"
	}
	if ev.FinishReason == "content_filter" {
		// A refusal surfaces its lexeme in incomplete_details on every
		// transport while the status contract above stays untouched: it is
		// a complete decision, never length taxonomy, never clean-stop.
		sm.envelope.IncompleteReason = "content_filter"
	}
	sm.status = finalStatus
	sm.state = StateTerminal

	wireRes, _, err := sm.snapshotResource(finalStatus)
	if err != nil {
		return err
	}

	eventType := "response.completed"
	if finalStatus == "incomplete" {
		eventType = "response.incomplete"
	}

	*events = append(*events, StreamEvent{
		Type:           eventType,
		SequenceNumber: sm.nextSeq(),
		Response:       wireRes,
	})

	return nil
}

func (sm *StateMachine) handleUnknown(ev aipapi.Event, events *[]StreamEvent) error {
	if strings.Contains(string(ev.Kind), ":") {
		*events = append(*events, StreamEvent{
			Type:           string(ev.Kind),
			SequenceNumber: sm.nextSeq(),
		})
	} else {
		return &SequenceError{
			Code:     "unknown_event_type",
			Event:    string(ev.Kind),
			Sequence: sm.sequenceNum,
			Message:  "unknown unprefixed event type",
			Err:      ErrUnknownDiscriminator,
		}
	}
	return nil
}
