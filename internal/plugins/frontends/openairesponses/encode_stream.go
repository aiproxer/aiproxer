package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	return writeStreamSSECore(ctx, w, model, opts.ResponseID, opts.MessageID, opts.CreatedAt, opts.ExposeAipUsageExtensions, opts.namespaceTools, func(w http.ResponseWriter) {
		sessionwire.WriteResponseCarriers(w, call)
	}, es, true)
}

// WireWriteStreamSSE emits responses SSE events incrementally from the canonical stream for wire fast-path execution.
// It is deterministic-only: creation time comes from the response context
// seeds and it never consults an executor clock. Clock-selected wire encoding
// goes through the handler closure into wireWriteStreamSSEWithOptions.
func WireWriteStreamSSE(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, exposeExt bool) error {
	return wireWriteStreamSSEWithOptions(ctx, w, rc, es, EncodeOptions{ExposeAipUsageExtensions: exposeExt})
}

// wireWriteStreamSSEWithOptions emits responses SSE events from the canonical
// stream using caller-selected encode options, sharing the canonical encoder
// core. Zero option fields fall back to the response context deterministic
// seeds, preserving canonical zero/default normalization.
func wireWriteStreamSSEWithOptions(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, opts EncodeOptions) error {
	model := rc.ClientModel()
	if model == "" {
		model = rc.EffectiveModel()
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	rid := opts.ResponseID
	if rid == "" {
		rid = rc.OpenAIResponseID()
	}
	mid := opts.MessageID
	if mid == "" {
		mid = rc.OpenAIMessageID()
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = rc.DeterministicTimestamp()
	}
	if ts == 0 {
		ts = time.Now().Unix()
	}
	return writeStreamSSECore(ctx, w, model, rid, mid, ts, opts.ExposeAipUsageExtensions, opts.namespaceTools, func(w http.ResponseWriter) {
		rc.WriteSessionHeaders(w)
	}, es, false)
}

func writeStreamSSECore(ctx context.Context, w http.ResponseWriter, model, rid, mid string, ts int64, exposeExt bool, namespaceTools *namespaceToolMapping, writeCarriers func(w http.ResponseWriter), es aipapi.EventStream, closeBeforePump bool) error {
	// Canonical encoding owns pre-pump cleanup; wire replay retains its caller's
	// raw-stream fallback close. Neither path adds a close after PumpSSE takes over.
	defer func() {
		if closeBeforePump && es != nil {
			_ = es.Close() // Preserve the initial framing/write failure.
		}
	}()
	if writeCarriers != nil {
		writeCarriers(w)
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("openairesponses: ResponseWriter is not a Flusher")
	}

	var seq int
	nextSeq := func() int { seq++; return seq }

	var usageCol aipapi.Collected
	var fullText strings.Builder
	var fullReasoning strings.Builder
	var assistantMedia []aipapi.Part

	type toolStream struct {
		CallID      string
		ItemID      string
		OutputIndex int64
		Name        string
		Namespace   string
		Args        strings.Builder
	}
	toolByCallID := make(map[string]*toolStream)
	var toolOrder []*toolStream
	nextOutIdx := int64(0)

	ensureToolStream := func(callID string) (*toolStream, error) {
		if st := toolByCallID[callID]; st != nil {
			return st, nil
		}
		st := &toolStream{
			CallID:      callID,
			ItemID:      fcItemID(callID),
			OutputIndex: nextOutIdx,
			Name:        "",
		}
		nextOutIdx++
		toolByCallID[callID] = st
		toolOrder = append(toolOrder, st)
		if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.added", streamOutputItemAddedFunc{
			Type:           "response.output_item.added",
			SequenceNumber: nextSeq(),
			OutputIndex:    st.OutputIndex,
			Item: streamFuncCallInProgress{
				Type:      "function_call",
				ID:        st.ItemID,
				CallID:    st.CallID,
				Name:      st.Name,
				Namespace: st.Namespace,
				Arguments: "",
				Status:    "in_progress",
			},
		}); err != nil {
			return nil, err
		}
		return st, nil
	}

	createdResponse := wireResponse{
		ID:        rid,
		Object:    "response",
		CreatedAt: ts,
		Status:    "in_progress",
		Model:     model,
		Output:    []any{},
	}
	if err := stream.FlushSSEEventJSON(w, fl, "response.created", wireStreamEnvelope{
		Type:           "response.created",
		SequenceNumber: nextSeq(),
		Response:       createdResponse,
	}); err != nil {
		return err
	}
	if err := stream.FlushSSEEventJSON(w, fl, "response.in_progress", wireStreamEnvelope{
		Type:           "response.in_progress",
		SequenceNumber: nextSeq(),
		Response:       createdResponse,
	}); err != nil {
		return err
	}
	reasoningItemID := "rs_" + rid
	var reasoningOutputIndex int64 = -1
	reasoningStarted := false
	reasoningClosed := false
	type exactReasoningSlot struct {
		outputIndex int64
		item        map[string]any
	}
	var exactReasoningSlots []exactReasoningSlot
	messageItemID := mid
	var messageOutputIndex int64 = -1
	messageStarted := false
	messageClosed := false
	var messageParts []streamMsgContent

	emitExactReasoningPart := func(part *aipapi.ReasoningPart) error {
		canon, err := canonExactResponsesReasoning(part)
		if err != nil {
			if errors.Is(err, errReasoningDialectSkip) {
				return nil
			}
			return err
		}
		id, err := reasoningIDFromCanon(canon)
		if err != nil {
			return err
		}
		summaries, err := summaryTextsFromCanon(canon)
		if err != nil {
			return err
		}
		addedItem, err := exactReasoningAddedShell(canon)
		if err != nil {
			return err
		}
		var doneObj map[string]any
		if err := json.Unmarshal(canon, &doneObj); err != nil {
			return fmt.Errorf("openairesponses: invalid reasoning item")
		}
		idx := nextOutIdx
		nextOutIdx++
		if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.added", streamOutputItemExactReasoning{
			Type:           "response.output_item.added",
			SequenceNumber: nextSeq(),
			OutputIndex:    idx,
			Item:           addedItem,
		}); err != nil {
			return err
		}
		for i, text := range summaries {
			if err := stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_part.added", streamReasoningSummaryPartAdded{
				Type:           "response.reasoning_summary_part.added",
				SequenceNumber: nextSeq(),
				ItemID:         id,
				OutputIndex:    idx,
				SummaryIndex:   i,
				Part:           streamReasoningSummaryPart{Type: "summary_text", Text: ""},
			}); err != nil {
				return err
			}
			if err := stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_text.done", streamReasoningSummaryTextDone{
				Type:           "response.reasoning_summary_text.done",
				SequenceNumber: nextSeq(),
				ItemID:         id,
				OutputIndex:    idx,
				SummaryIndex:   i,
				Text:           text,
			}); err != nil {
				return err
			}
			if err := stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_part.done", streamReasoningSummaryPartDone{
				Type:           "response.reasoning_summary_part.done",
				SequenceNumber: nextSeq(),
				ItemID:         id,
				OutputIndex:    idx,
				SummaryIndex:   i,
				Part:           streamReasoningSummaryPart{Type: "summary_text", Text: text},
			}); err != nil {
				return err
			}
		}
		if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.done", streamOutputItemExactReasoning{
			Type:           "response.output_item.done",
			SequenceNumber: nextSeq(),
			OutputIndex:    idx,
			Item:           append(json.RawMessage(nil), canon...),
		}); err != nil {
			return err
		}
		exactReasoningSlots = append(exactReasoningSlots, exactReasoningSlot{outputIndex: idx, item: doneObj})
		return nil
	}

	openReasoningItem := func() error {
		if reasoningStarted {
			return nil
		}
		reasoningStarted = true
		reasoningOutputIndex = nextOutIdx
		nextOutIdx++
		if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.added", streamOutputItemAddedReasoning{
			Type:           "response.output_item.added",
			SequenceNumber: nextSeq(),
			OutputIndex:    reasoningOutputIndex,
			Item: streamReasoningItem{
				Type:    "reasoning",
				ID:      reasoningItemID,
				Status:  "in_progress",
				Summary: []streamReasoningSummary{},
			},
		}); err != nil {
			return err
		}
		return stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_part.added", streamReasoningSummaryPartAdded{
			Type:           "response.reasoning_summary_part.added",
			SequenceNumber: nextSeq(),
			ItemID:         reasoningItemID,
			OutputIndex:    reasoningOutputIndex,
			SummaryIndex:   0,
			Part:           streamReasoningSummaryPart{Type: "summary_text", Text: ""},
		})
	}
	// closeReasoningItem finalizes the open reasoning output item, if any.
	// Invariant: any non-reasoning content event (text, tool call, assistant
	// media, or response completion) must close an open reasoning item first so
	// the reasoning item is finalized before the next item begins. Each such
	// handler calls this at its top; the guard makes it idempotent, so adding a
	// new content event kind only requires calling this once at its entry.
	closeReasoningItem := func() error {
		if !reasoningStarted || reasoningClosed {
			return nil
		}
		reasoningClosed = true
		text := fullReasoning.String()
		if err := stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_text.done", streamReasoningSummaryTextDone{
			Type:           "response.reasoning_summary_text.done",
			SequenceNumber: nextSeq(),
			ItemID:         reasoningItemID,
			OutputIndex:    reasoningOutputIndex,
			SummaryIndex:   0,
			Text:           text,
		}); err != nil {
			return err
		}
		if err := stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_part.done", streamReasoningSummaryPartDone{
			Type:           "response.reasoning_summary_part.done",
			SequenceNumber: nextSeq(),
			ItemID:         reasoningItemID,
			OutputIndex:    reasoningOutputIndex,
			SummaryIndex:   0,
			Part:           streamReasoningSummaryPart{Type: "summary_text", Text: text},
		}); err != nil {
			return err
		}
		return stream.FlushSSEEventJSON(w, fl, "response.output_item.done", streamOutputItemDoneReasoning{
			Type:           "response.output_item.done",
			SequenceNumber: nextSeq(),
			OutputIndex:    reasoningOutputIndex,
			Item: streamReasoningItem{
				Type:    "reasoning",
				ID:      reasoningItemID,
				Status:  "completed",
				Summary: []streamReasoningSummary{{Type: "summary_text", Text: text}},
			},
		})
	}
	openMessageItem := func() error {
		if messageStarted {
			return nil
		}
		messageStarted = true
		messageOutputIndex = nextOutIdx
		nextOutIdx++
		if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.added", streamOutputItemAddedMsg{
			Type:           "response.output_item.added",
			SequenceNumber: nextSeq(),
			OutputIndex:    messageOutputIndex,
			Item: streamMessageItem{
				Type:    "message",
				ID:      messageItemID,
				Status:  "in_progress",
				Role:    "assistant",
				Content: []streamMsgContent{},
			},
		}); err != nil {
			return err
		}
		var partAdded streamContentPartAdded
		partAdded.Type = "response.content_part.added"
		partAdded.SequenceNumber = nextSeq()
		partAdded.ItemID = messageItemID
		partAdded.OutputIndex = messageOutputIndex
		partAdded.Part.Type = "output_text"
		partAdded.Part.Text = ""
		return stream.FlushSSEEventJSON(w, fl, "response.content_part.added", partAdded)
	}
	closeMessageItem := func() error {
		if !messageStarted || messageClosed {
			return nil
		}
		messageClosed = true
		text := fullText.String()
		if err := stream.FlushSSEEventJSON(w, fl, "response.output_text.done", streamOutputTextDone{
			Type:           "response.output_text.done",
			SequenceNumber: nextSeq(),
			ItemID:         messageItemID,
			OutputIndex:    messageOutputIndex,
			Text:           text,
		}); err != nil {
			return err
		}
		messageParts = []streamMsgContent{{Type: "output_text", Text: text}}
		for _, p := range assistantMedia {
			switch p.Kind {
			case aipapi.PartImageRef:
				messageParts = append(messageParts, streamMsgContent{Type: "input_image", ImageURL: p.ImageRef})
			case aipapi.PartFileRef:
				messageParts = append(messageParts, streamMsgContent{Type: "input_file", FileID: p.FileRef, FileName: p.FileName})
			}
		}
		if err := stream.FlushSSEEventJSON(w, fl, "response.content_part.done", streamContentPartDone{
			Type:           "response.content_part.done",
			SequenceNumber: nextSeq(),
			ItemID:         messageItemID,
			OutputIndex:    messageOutputIndex,
			Part:           streamMsgContent{Type: "output_text", Text: text},
		}); err != nil {
			return err
		}
		return stream.FlushSSEEventJSON(w, fl, "response.output_item.done", streamOutputItemDoneMessage{
			Type:           "response.output_item.done",
			SequenceNumber: nextSeq(),
			OutputIndex:    messageOutputIndex,
			Item: streamMessageItem{
				Type:    "message",
				ID:      messageItemID,
				Status:  "completed",
				Role:    "assistant",
				Content: messageParts,
			},
		})
	}

	closeBeforePump = false
	return frontendpipe.PumpSSEWithFailure(ctx, w, es, fmt.Errorf("openairesponses: stream ended without response_finished"), func(ev aipapi.Event) (bool, error) {
		switch ev.Kind {
		case aipapi.EventResponseStarted, aipapi.EventMessageStarted:
		case aipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case aipapi.EventTextDelta:
			if err := closeReasoningItem(); err != nil {
				return false, err
			}
			if err := openMessageItem(); err != nil {
				return false, err
			}
			fullText.WriteString(ev.Delta)
			if err := stream.FlushSSEEventJSON(w, fl, "response.output_text.delta", streamOutputTextDelta{
				Type:           "response.output_text.delta",
				SequenceNumber: nextSeq(),
				ItemID:         messageItemID,
				OutputIndex:    messageOutputIndex,
				Delta:          ev.Delta,
			}); err != nil {
				return false, err
			}
		case aipapi.EventToolCallStarted:
			if err := closeReasoningItem(); err != nil {
				return false, err
			}
			name, namespace := restoreNamespaceToolName(namespaceTools, ev.ToolName)
			if st, ok := toolByCallID[ev.ToolCallID]; ok {
				if ev.ToolName != "" {
					st.Name = name
					st.Namespace = namespace
				}
				break
			}
			st := &toolStream{
				CallID:      ev.ToolCallID,
				ItemID:      fcItemID(ev.ToolCallID),
				OutputIndex: nextOutIdx,
				Name:        name,
				Namespace:   namespace,
			}
			nextOutIdx++
			toolByCallID[ev.ToolCallID] = st
			toolOrder = append(toolOrder, st)
			if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.added", streamOutputItemAddedFunc{
				Type:           "response.output_item.added",
				SequenceNumber: nextSeq(),
				OutputIndex:    st.OutputIndex,
				Item: streamFuncCallInProgress{
					Type:      "function_call",
					ID:        st.ItemID,
					CallID:    st.CallID,
					Name:      st.Name,
					Namespace: st.Namespace,
					Arguments: "",
					Status:    "in_progress",
				},
			}); err != nil {
				return false, err
			}
		case aipapi.EventToolCallArgsDelta:
			st, err := ensureToolStream(ev.ToolCallID)
			if err != nil {
				return false, err
			}
			st.Args.WriteString(ev.Delta)
			if err := stream.FlushSSEEventJSON(w, fl, "response.function_call_arguments.delta", streamFuncArgsDelta{
				Type:           "response.function_call_arguments.delta",
				SequenceNumber: nextSeq(),
				ItemID:         st.ItemID,
				OutputIndex:    st.OutputIndex,
				Delta:          ev.Delta,
			}); err != nil {
				return false, err
			}
		case aipapi.EventAssistantImageRef:
			if err := closeReasoningItem(); err != nil {
				return false, err
			}
			if err := openMessageItem(); err != nil {
				return false, err
			}
			assistantMedia = append(assistantMedia, aipapi.Part{
				Kind: aipapi.PartImageRef, ImageRef: ev.AssistantRef, ImageMIME: ev.AssistantMIME,
			})
		case aipapi.EventAssistantFileRef:
			if err := closeReasoningItem(); err != nil {
				return false, err
			}
			if err := openMessageItem(); err != nil {
				return false, err
			}
			assistantMedia = append(assistantMedia, aipapi.Part{
				Kind: aipapi.PartFileRef, FileRef: ev.AssistantRef, FileMIME: ev.AssistantMIME, FileName: ev.AssistantName,
			})
		case aipapi.EventToolCallFinished:
			st := toolByCallID[ev.ToolCallID]
			if st == nil {
				return false, nil
			}
			args := st.Args.String()
			if err := stream.FlushSSEEventJSON(w, fl, "response.function_call_arguments.done", streamFuncArgsDone{
				Type:           "response.function_call_arguments.done",
				SequenceNumber: nextSeq(),
				ItemID:         st.ItemID,
				Name:           st.Name,
				Namespace:      st.Namespace,
				Arguments:      args,
				OutputIndex:    st.OutputIndex,
			}); err != nil {
				return false, err
			}
			if err := stream.FlushSSEEventJSON(w, fl, "response.output_item.done", streamOutputItemDone{
				Type:           "response.output_item.done",
				SequenceNumber: nextSeq(),
				OutputIndex:    st.OutputIndex,
				Item: streamFuncItemDone{
					Type:      "function_call",
					ID:        st.ItemID,
					CallID:    st.CallID,
					Name:      st.Name,
					Namespace: st.Namespace,
					Arguments: args,
					Status:    "completed",
				},
			}); err != nil {
				return false, err
			}
		case aipapi.EventResponseFinished:
			if err := closeReasoningItem(); err != nil {
				return false, err
			}
			if err := closeMessageItem(); err != nil {
				return false, err
			}
			// Build completed.output in output_index (announcement) order. The
			// invariant is that every slot in [0, nextOutIdx) is populated exactly
			// once: each open* closure increments nextOutIdx exactly once when it
			// first opens an item, and every opened item (reasoning, message, tool)
			// is placed here at its assigned index. A future code path that
			// increments nextOutIdx without a matching assignment here would emit a
			// zero-value {"type":""} entry on the wire, so guard any such addition.
			out := make([]any, nextOutIdx)
			if reasoningStarted {
				out[reasoningOutputIndex] = streamCompletedOut{
					Type:    "reasoning",
					ID:      reasoningItemID,
					Status:  "completed",
					Summary: []streamReasoningSummary{{Type: "summary_text", Text: fullReasoning.String()}},
				}
			}
			for _, slot := range exactReasoningSlots {
				out[slot.outputIndex] = slot.item
			}
			if messageStarted {
				out[messageOutputIndex] = streamCompletedOut{
					Type:    "message",
					ID:      messageItemID,
					Status:  "completed",
					Role:    "assistant",
					Content: messageParts,
				}
			}
			for _, st := range toolOrder {
				out[st.OutputIndex] = streamCompletedOut{
					Type:      "function_call",
					ID:        st.ItemID,
					CallID:    st.CallID,
					Name:      st.Name,
					Namespace: st.Namespace,
					Arguments: st.Args.String(),
					Status:    "completed",
				}
			}

			var completed streamCompletedEvent
			completed.Type = "response.completed"
			completed.SequenceNumber = nextSeq()
			completed.Response.ID = rid
			completed.Response.Object = "response"
			completed.Response.CreatedAt = ts
			completed.Response.Status = "completed"
			completed.Response.Model = model
			completed.Response.Output = out
			completed.Response.Usage = wireResponsesUsage(usageCol, exposeExt)
			// An output-token-limit stop terminates the stream with
			// response.incomplete, never response.completed. A refusal
			// surfaces the same envelope with the content_filter reason.
			if openaiwire.IsOutputLimitStop(ev.ResponseStatus, ev.FinishReason) {
				completed.Type = "response.incomplete"
				completed.Response.Status = "incomplete"
				completed.Response.IncompleteDetails = &wireIncomplete{Reason: "max_output_tokens"}
			} else if ev.FinishReason == "content_filter" {
				completed.Type = "response.incomplete"
				completed.Response.Status = "incomplete"
				completed.Response.IncompleteDetails = &wireIncomplete{Reason: "content_filter"}
			}
			if err := stream.FlushSSEEventJSON(w, fl, completed.Type, completed); err != nil {
				return false, err
			}
			if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
				return false, err
			}
			fl.Flush()
			return true, nil
		case aipapi.EventReasoningDelta:
			if err := openReasoningItem(); err != nil {
				return false, err
			}
			fullReasoning.WriteString(ev.Delta)
			if err := stream.FlushSSEEventJSON(w, fl, "response.reasoning_summary_text.delta", streamReasoningSummaryTextDelta{
				Type:           "response.reasoning_summary_text.delta",
				SequenceNumber: nextSeq(),
				ItemID:         reasoningItemID,
				OutputIndex:    reasoningOutputIndex,
				SummaryIndex:   0,
				Delta:          ev.Delta,
			}); err != nil {
				return false, err
			}
		case aipapi.EventReasoningPart:
			if err := closeReasoningItem(); err != nil {
				return false, err
			}
			if err := emitExactReasoningPart(ev.Reasoning); err != nil {
				return false, err
			}
		default:
		}
		return false, nil
	}, func(err error) error {
		failure := responseStreamFailure(execerr.ClassifyExecute(err))
		failed := createdResponse
		failed.Status = "failed"
		failed.Error = &failure
		return stream.FlushSSEEventJSON(w, fl, "response.failed", wireStreamEnvelope{
			Type: "response.failed", SequenceNumber: nextSeq(), Response: failed,
		})
	})
}

// Only emit ResponseError.code values supported by the Responses resource
// schema. A generic capability/policy refusal has no exact resource-error code;
// keep its safe message and use server_error rather than inventing an enum.
func responseStreamFailure(out execerr.Outcome) wireResponseError {
	code := "server_error"
	if out.Status == http.StatusTooManyRequests {
		code = "rate_limit_exceeded"
	}
	return wireResponseError{Code: code, Message: out.Message}
}
