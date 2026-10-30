package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	frontendlimits "github.com/aiproxer/aiproxer/internal/plugins/frontends/limits"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// EncodeOptions controls wire identifiers for encoded Messages payloads.
type EncodeOptions struct {
	MessageID                string
	ExposeAipUsageExtensions bool
}

type wireAPIError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type wireMessage struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Model        string             `json:"model"`
	Content      []wireContentBlock `json:"content"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        wireUsage          `json:"usage"`
}

type wireImageSource struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	MediaType string `json:"media_type,omitempty"`
}

type wireContentBlock struct {
	Type   string           `json:"type"`
	Text   string           `json:"text,omitempty"`
	ID     string           `json:"id,omitempty"`
	Name   string           `json:"name,omitempty"`
	Input  json.RawMessage  `json:"input,omitempty"`
	Source *wireImageSource `json:"source,omitempty"`
}

type wireUsage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens,omitempty"`
	CostNanoUnits            int64  `json:"x_aip_cost_nano_units,omitempty"`
	Currency                 string `json:"x_aip_currency,omitempty"`
	CostSource               string `json:"x_aip_cost_source,omitempty"`
	UncachedInputTokens      int    `json:"x_aip_uncached_tokens,omitempty"`
}

func wireAnthropicUsage(col aipapi.Collected, exposeAipExtensions bool) wireUsage {
	u := wireUsage{
		InputTokens:  col.InputTokens,
		OutputTokens: col.OutputTokens,
	}
	if col.CacheReadTokens > 0 {
		u.CacheReadInputTokens = col.CacheReadTokens
	}
	if col.CacheWriteTokens > 0 {
		u.CacheCreationInputTokens = col.CacheWriteTokens
	}
	if exposeAipExtensions {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
		if uncached := col.UncachedInputTokens(); uncached > 0 {
			u.UncachedInputTokens = uncached
		}
	}
	return u
}

// Streaming SSE wire shapes (typed JSON; avoids map[string]any in the hot loop).
var (
	anthropicJSONNull     = json.RawMessage("null")
	anthropicJSONEmptyArr = json.RawMessage("[]")
	anthropicJSONEmptyObj = json.RawMessage("{}")
)

type anthropicSSEMessageStart struct {
	Type    string                       `json:"type"`
	Message anthropicSSEMessageStartBody `json:"message"`
}

type anthropicSSEMessageStartBody struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	Model        string          `json:"model"`
	StopReason   json.RawMessage `json:"stop_reason"`
	StopSequence json.RawMessage `json:"stop_sequence"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicSSEContentBlockStartText struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	ContentBlock anthropicSSETextBlock `json:"content_block"`
}

type anthropicSSETextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicSSEContentBlockStartThinking struct {
	Type         string                    `json:"type"`
	Index        int                       `json:"index"`
	ContentBlock anthropicSSEThinkingBlock `json:"content_block"`
}

type anthropicSSEThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type anthropicSSEContentBlockStartRedacted struct {
	Type         string                    `json:"type"`
	Index        int                       `json:"index"`
	ContentBlock anthropicSSERedactedBlock `json:"content_block"`
}

type anthropicSSERedactedBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type anthropicSSEContentBlockStartTool struct {
	Type         string                `json:"type"`
	Index        int                   `json:"index"`
	ContentBlock anthropicSSEToolBlock `json:"content_block"`
}

type anthropicSSEContentBlockStartMedia struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type   string           `json:"type"`
		Source *wireImageSource `json:"source"`
	} `json:"content_block"`
}

type anthropicSSEToolBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicSSEDeltaJSON struct {
	Type  string                       `json:"type"`
	Index int                          `json:"index"`
	Delta anthropicSSEPartialJSONDelta `json:"delta"`
}

type anthropicSSEPartialJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

type anthropicSSEDeltaText struct {
	Type  string                     `json:"type"`
	Index int                        `json:"index"`
	Delta anthropicSSETextDeltaInner `json:"delta"`
}

type anthropicSSETextDeltaInner struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicSSEDeltaThinking struct {
	Type  string                         `json:"type"`
	Index int                            `json:"index"`
	Delta anthropicSSEThinkingDeltaInner `json:"delta"`
}

type anthropicSSEThinkingDeltaInner struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}

type anthropicSSEDeltaSignature struct {
	Type  string                          `json:"type"`
	Index int                             `json:"index"`
	Delta anthropicSSESignatureDeltaInner `json:"delta"`
}

type anthropicSSESignatureDeltaInner struct {
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

type anthropicSSEContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type anthropicSSEMessageDelta struct {
	Type  string                        `json:"type"`
	Delta anthropicSSEMessageDeltaInner `json:"delta"`
	Usage wireUsage                     `json:"usage"`
}

type anthropicSSEMessageDeltaInner struct {
	StopReason   string          `json:"stop_reason"`
	StopSequence json.RawMessage `json:"stop_sequence"`
}

type anthropicSSEMessageStop struct {
	Type string `json:"type"`
}

func defaultEncodeOptions(call *aipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.MessageID == "" {
		opts.MessageID = "msg_" + diag.StableCallToken(call)
	}
	return opts
}

// WriteErrorJSON writes an Anthropic-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var we wireAPIError
	we.Type = "error"
	we.Error.Type = errType
	if we.Error.Type == "" {
		we.Error.Type = "invalid_request_error"
	}
	we.Error.Message = message
	return json.NewEncoder(w).Encode(we)
}

// WriteNonStreamJSON encodes a completed canonical stream as a Messages API JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	tee := openaiwire.TeeTerminal(es)
	col, err := aipapi.Collect(ctx, tee)
	if err != nil {
		return err
	}
	text := col.Text.String()
	model := ModelFromCall(call)
	if model == "" {
		model = "claude-3-5-haiku-20241022"
	}
	opts = defaultEncodeOptions(call, opts)
	mid := opts.MessageID
	tools := col.OrderedToolCalls()
	stop := "end_turn"
	if len(tools) > 0 {
		stop = "tool_use"
	}
	// An output-token-limit stop wins over tool_use: real Anthropic reports
	// max_tokens when it truncates inside a tool_use block. A refusal is
	// surfaced verbatim.
	if tee.IsOutputLimitTerminal(col) {
		stop = "max_tokens"
	} else if col.FinishReason == "content_filter" {
		stop = "refusal"
	}
	blocksCap := len(tools) + len(col.AssistantMedia)
	if text != "" {
		blocksCap++
	}
	blocks := make([]wireContentBlock, 0, blocksCap)
	if text != "" {
		blocks = append(blocks, wireContentBlock{Type: "text", Text: text})
	}
	for _, tc := range tools {
		// Argument JSON cut off by the output-token limit is not parseable.
		// ToolArgsRawJSON preserves the partial text instead of failing the
		// whole response, and stop_reason already reports the truncation.
		blocks = append(blocks, wireContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: openaiwire.ToolArgsRawJSON(tc.Arguments),
		})
	}
	for _, p := range col.AssistantMedia {
		switch p.Kind {
		case aipapi.PartImageRef:
			src := &wireImageSource{Type: "url", URL: p.ImageRef}
			if strings.TrimSpace(p.ImageMIME) != "" {
				src.MediaType = p.ImageMIME
			}
			blocks = append(blocks, wireContentBlock{Type: "image", Source: src})
		case aipapi.PartFileRef:
			// Non-stream assistant document output (URL-style ref only in v1 subset).
			src := &wireImageSource{Type: "url", URL: p.FileRef}
			if strings.TrimSpace(p.FileMIME) != "" {
				src.MediaType = p.FileMIME
			}
			blocks = append(blocks, wireContentBlock{Type: "document", Source: src})
		}
	}
	if len(blocks) == 0 {
		blocks = []wireContentBlock{{Type: "text", Text: ""}}
	}
	out := wireMessage{
		ID:         mid,
		Type:       "message",
		Role:       "assistant",
		Model:      model,
		StopReason: stop,
		Content:    blocks,
		Usage:      wireAnthropicUsage(col, opts.ExposeAipUsageExtensions),
	}
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	model := ModelFromCall(call)
	if model == "" {
		model = "claude-3-5-haiku-20241022"
	}
	opts = defaultEncodeOptions(call, opts)
	mid := opts.MessageID

	// frontendpipe's canonical path hands the encoder stream ownership and never
	// closes the stream itself, so every pre-pump return must close it. PumpSSE
	// owns the close once it is called.
	closeBeforePump := true
	defer func() {
		if closeBeforePump && es != nil {
			_ = es.Close() // Preserve the initial framing/write failure.
		}
	}()

	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("anthropic: ResponseWriter is not a Flusher")
	}
	fl.Flush()

	var usageCol aipapi.Collected
	var msgStarted bool
	nextBlockIdx := 0
	textBlockIdx := -1
	thinkingBlockIdx := -1
	thinkingSignature := ""
	toolBlockIdx := make(map[string]int)
	sawTool := false

	flushMessageStart := func() error {
		if msgStarted {
			return nil
		}
		msgStarted = true
		var p anthropicSSEMessageStart
		p.Type = "message_start"
		p.Message.ID = mid
		p.Message.Type = "message"
		p.Message.Role = "assistant"
		p.Message.Content = anthropicJSONEmptyArr
		p.Message.Model = model
		p.Message.StopReason = anthropicJSONNull
		p.Message.StopSequence = anthropicJSONNull
		p.Message.Usage.InputTokens = usageCol.InputTokens
		p.Message.Usage.OutputTokens = 0
		return stream.FlushSSEEventJSON(w, fl, "message_start", &p)
	}

	openTextBlock := func() error {
		if textBlockIdx >= 0 {
			return nil
		}
		if err := flushMessageStart(); err != nil {
			return err
		}
		textBlockIdx = nextBlockIdx
		nextBlockIdx++
		cb := anthropicSSEContentBlockStartText{
			Type:         "content_block_start",
			Index:        textBlockIdx,
			ContentBlock: anthropicSSETextBlock{Type: "text", Text: ""},
		}
		return stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb)
	}

	openThinkingBlock := func() error {
		if thinkingBlockIdx >= 0 {
			return nil
		}
		if err := flushMessageStart(); err != nil {
			return err
		}
		thinkingBlockIdx = nextBlockIdx
		nextBlockIdx++
		cb := anthropicSSEContentBlockStartThinking{
			Type:         "content_block_start",
			Index:        thinkingBlockIdx,
			ContentBlock: anthropicSSEThinkingBlock{Type: "thinking"},
		}
		return stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb)
	}
	closeThinkingBlock := func() error {
		if thinkingBlockIdx < 0 {
			return nil
		}
		idx := thinkingBlockIdx
		if thinkingSignature != "" {
			d := anthropicSSEDeltaSignature{
				Type:  "content_block_delta",
				Index: idx,
				Delta: anthropicSSESignatureDeltaInner{Type: "signature_delta", Signature: thinkingSignature},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_delta", &d); err != nil {
				return err
			}
			thinkingSignature = ""
		}
		cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: idx}
		thinkingBlockIdx = -1
		return stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop)
	}

	openToolBlock := func(callID, name string) error {
		if _, ok := toolBlockIdx[callID]; ok {
			return nil
		}
		if err := closeThinkingBlock(); err != nil {
			return err
		}
		if err := flushMessageStart(); err != nil {
			return err
		}
		idx := nextBlockIdx
		nextBlockIdx++
		toolBlockIdx[callID] = idx
		sawTool = true
		cb := anthropicSSEContentBlockStartTool{
			Type:  "content_block_start",
			Index: idx,
			ContentBlock: anthropicSSEToolBlock{
				Type:  "tool_use",
				ID:    callID,
				Name:  name,
				Input: anthropicJSONEmptyObj,
			},
		}
		return stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb)
	}

	closeBeforePump = false
	return frontendpipe.PumpSSEWithFailure(ctx, w, es, fmt.Errorf("anthropic: stream ended without response_finished"), func(ev aipapi.Event) (bool, error) {
		switch ev.Kind {
		case aipapi.EventResponseStarted, aipapi.EventMessageStarted:
		case aipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case aipapi.EventToolCallStarted:
			if err := openToolBlock(ev.ToolCallID, ev.ToolName); err != nil {
				return false, err
			}
		case aipapi.EventToolCallArgsDelta:
			if err := openToolBlock(ev.ToolCallID, ""); err != nil {
				return false, err
			}
			idx := toolBlockIdx[ev.ToolCallID]
			d := anthropicSSEDeltaJSON{
				Type:  "content_block_delta",
				Index: idx,
				Delta: anthropicSSEPartialJSONDelta{
					Type:        "input_json_delta",
					PartialJSON: ev.Delta,
				},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_delta", &d); err != nil {
				return false, err
			}
		case aipapi.EventToolCallFinished:
			idx, ok := toolBlockIdx[ev.ToolCallID]
			if !ok {
				return false, nil
			}
			cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: idx}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
				return false, err
			}
		case aipapi.EventTextDelta:
			if err := closeThinkingBlock(); err != nil {
				return false, err
			}
			if err := openTextBlock(); err != nil {
				return false, err
			}
			d := anthropicSSEDeltaText{
				Type:  "content_block_delta",
				Index: textBlockIdx,
				Delta: anthropicSSETextDeltaInner{Type: "text_delta", Text: ev.Delta},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_delta", &d); err != nil {
				return false, err
			}
		case aipapi.EventReasoningDelta:
			if err := openThinkingBlock(); err != nil {
				return false, err
			}
			d := anthropicSSEDeltaThinking{
				Type:  "content_block_delta",
				Index: thinkingBlockIdx,
				Delta: anthropicSSEThinkingDeltaInner{Type: "thinking_delta", Thinking: ev.Delta},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_delta", &d); err != nil {
				return false, err
			}
		case aipapi.EventReasoningSignatureDelta:
			if err := openThinkingBlock(); err != nil {
				return false, err
			}
			if len(thinkingSignature)+len(ev.Signature) > aipapi.MaxRefStringBytes {
				return false, fmt.Errorf("anthropic: thinking signature exceeds %d byte limit", aipapi.MaxRefStringBytes)
			}
			thinkingSignature += ev.Signature
		case aipapi.EventReasoningOpaqueDelta:
			if err := closeThinkingBlock(); err != nil {
				return false, err
			}
			if err := flushMessageStart(); err != nil {
				return false, err
			}
			data := ""
			if len(ev.Opaque) > 0 {
				var envelope struct {
					Type string `json:"type"`
					Data string `json:"data"`
				}
				if err := json.Unmarshal(ev.Opaque, &envelope); err != nil {
					return false, fmt.Errorf("anthropic: redacted_thinking opaque: %w", err)
				}
				data = envelope.Data
			}
			if err := frontendlimits.StringBytes("redacted_thinking.data", data, aipapi.MaxReasoningOpaqueBytes); err != nil {
				return false, err
			}
			idx := nextBlockIdx
			nextBlockIdx++
			cb := anthropicSSEContentBlockStartRedacted{
				Type:         "content_block_start",
				Index:        idx,
				ContentBlock: anthropicSSERedactedBlock{Type: "redacted_thinking", Data: data},
			}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb); err != nil {
				return false, err
			}
			cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: idx}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
				return false, err
			}
		case aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef:
			if err := closeThinkingBlock(); err != nil {
				return false, err
			}
			idx := nextBlockIdx
			nextBlockIdx++
			src := &wireImageSource{Type: "url", URL: ev.AssistantRef}
			if mt := strings.TrimSpace(ev.AssistantMIME); mt != "" {
				src.MediaType = mt
			}
			var cb anthropicSSEContentBlockStartMedia
			cb.Type = "content_block_start"
			cb.Index = idx
			if ev.Kind == aipapi.EventAssistantImageRef {
				cb.ContentBlock.Type = "image"
			} else {
				cb.ContentBlock.Type = "document"
			}
			cb.ContentBlock.Source = src
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_start", &cb); err != nil {
				return false, err
			}
			cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: idx}
			if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
				return false, err
			}
		case aipapi.EventResponseFinished:
			if err := closeThinkingBlock(); err != nil {
				return false, err
			}
			if !msgStarted {
				if err := openTextBlock(); err != nil {
					return false, err
				}
			}
			if textBlockIdx >= 0 {
				cbStop := anthropicSSEContentBlockStop{Type: "content_block_stop", Index: textBlockIdx}
				if err := stream.FlushSSEEventJSON(w, fl, "content_block_stop", &cbStop); err != nil {
					return false, err
				}
			}
			stop := "end_turn"
			if sawTool {
				stop = "tool_use"
			}
			// An output-token-limit stop wins over tool_use: real Anthropic
			// reports max_tokens when it truncates inside a tool_use block.
			// A refusal is surfaced verbatim.
			if openaiwire.IsOutputLimitStop(ev.ResponseStatus, ev.FinishReason) {
				stop = "max_tokens"
			} else if ev.FinishReason == "content_filter" {
				stop = "refusal"
			}
			var msgDelta anthropicSSEMessageDelta
			msgDelta.Type = "message_delta"
			msgDelta.Delta.StopReason = stop
			msgDelta.Delta.StopSequence = anthropicJSONNull
			msgDelta.Usage = wireAnthropicUsage(usageCol, opts.ExposeAipUsageExtensions)
			if err := stream.FlushSSEEventJSON(w, fl, "message_delta", &msgDelta); err != nil {
				return false, err
			}
			stopPayload := anthropicSSEMessageStop{Type: "message_stop"}
			if err := stream.FlushSSEEventJSON(w, fl, "message_stop", &stopPayload); err != nil {
				return false, err
			}
			return true, nil
		default:
		}
		return false, nil
	}, func(err error) error {
		status, message, errType := frontendpipe.MapAnthropicExecuteError(execerr.ClassifyExecute(err))
		switch status {
		case http.StatusForbidden:
			errType = "permission_error"
		case http.StatusTooManyRequests:
			errType = "rate_limit_error"
		}
		return stream.FlushSSEEventJSON(w, fl, "error", map[string]any{
			"type": "error", "error": map[string]string{"type": errType, "message": message},
		})
	})
}
