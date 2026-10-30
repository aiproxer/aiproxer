package openailegacy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// streamChunkState holds reusable wire state for chat.completion.chunk SSE frames.
type streamChunkState struct {
	chunk   wireChatCompletion
	choices [1]wireChatChoice
	delta   wireDelta
	fn      wireLegacyToolDeltaFn
	tools   [1]wireLegacyToolDelta
}

// EncodeOptions controls wire identifiers for encoded Chat Completions payloads.
type EncodeOptions struct {
	CompletionID             string
	CreatedAt                int64
	ExposeAipUsageExtensions bool
}

type wireChatCompletion struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []wireChatChoice `json:"choices"`
	Usage   *wireUsageLegacy `json:"usage,omitempty"`
}

type wireChatChoice struct {
	Index        int            `json:"index"`
	Message      *wireAssistant `json:"message,omitempty"`
	Delta        *wireDelta     `json:"delta,omitempty"`
	FinishReason *string        `json:"finish_reason"`
}

type wireAssistant struct {
	Role             string           `json:"role"`
	Content          json.RawMessage  `json:"content,omitempty"` // JSON string or multimodal part array
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ToolCalls        []wireToolCallNS `json:"tool_calls,omitempty"`
}

type wireToolCallNS struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireDelta struct {
	Role             string                `json:"role,omitempty"`
	Content          string                `json:"content,omitempty"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	Reasoning        string                `json:"reasoning,omitempty"`
	ToolCalls        []wireLegacyToolDelta `json:"tool_calls,omitempty"`
}

type wireLegacyToolDelta struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function *wireLegacyToolDeltaFn `json:"function,omitempty"`
}

type wireLegacyToolDeltaFn struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type wireUsageLegacy struct {
	PromptTokens            int                          `json:"prompt_tokens"`
	CompletionTokens        int                          `json:"completion_tokens"`
	TotalTokens             int                          `json:"total_tokens"`
	PromptTokensDetails     *wirePromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *wireCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	CostNanoUnits           int64                        `json:"x_aip_cost_nano_units,omitempty"`
	Currency                string                       `json:"x_aip_currency,omitempty"`
	CostSource              string                       `json:"x_aip_cost_source,omitempty"`
}

type wirePromptTokensDetails struct {
	CachedTokens   int `json:"cached_tokens,omitempty"`
	UncachedTokens int `json:"x_aip_uncached_tokens,omitempty"`
	CacheWrite     int `json:"x_aip_cache_write_tokens,omitempty"`
}

type wireCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func wireLegacyUsage(col aipapi.Collected, exposeExt bool) *wireUsageLegacy {
	if col.InputTokens == 0 && col.OutputTokens == 0 && col.CacheReadTokens == 0 && col.CacheWriteTokens == 0 && col.ReasoningTokens == 0 && col.TotalTokens == 0 && col.CostNanoUnits == 0 {
		return nil
	}
	// The OpenAI prompt count contains the cached and rewritten prompt subsets,
	// so a producer that reports them outside its input counter is translated
	// here. The OpenAI total covers the translated prompt and the completion:
	// a provider total that differs stays in the canonical evidence and never
	// leaks into the wire total.
	promptTokens := col.InclusiveInputTokens()
	u := &wireUsageLegacy{
		PromptTokens:     promptTokens,
		CompletionTokens: col.OutputTokens,
		TotalTokens:      promptTokens + col.OutputTokens,
	}
	if col.CacheReadTokens > 0 || col.CacheWriteTokens > 0 {
		u.PromptTokensDetails = &wirePromptTokensDetails{CachedTokens: col.CacheReadTokens}
		if exposeExt {
			u.PromptTokensDetails.UncachedTokens = col.UncachedInputTokens()
			u.PromptTokensDetails.CacheWrite = col.CacheWriteTokens
		}
	}
	if col.ReasoningTokens > 0 {
		u.CompletionTokensDetails = &wireCompletionTokensDetails{ReasoningTokens: col.ReasoningTokens}
	}
	if exposeExt {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
	}
	return u
}

// marshalChatAssistantContent returns JSON for Chat Completions assistant message content:
// a JSON string when there is text-only output, otherwise a parts array (text + image_url / file).
func marshalChatAssistantContent(col aipapi.Collected) (json.RawMessage, error) {
	if len(col.AssistantMedia) == 0 {
		return json.Marshal(col.Text.String())
	}
	parts := []map[string]any{}
	if col.Text.String() != "" {
		parts = append(parts, map[string]any{"type": "text", "text": col.Text.String()})
	}
	for _, p := range col.AssistantMedia {
		switch p.Kind {
		case aipapi.PartImageRef:
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": p.ImageRef},
			})
		case aipapi.PartFileRef:
			parts = append(parts, map[string]any{
				"type": "file",
				"file": map[string]any{"file_id": p.FileRef},
			})
		}
	}
	return json.Marshal(parts)
}

func defaultEncodeOptions(call *aipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.CompletionID == "" {
		opts.CompletionID = "chatcmpl_" + diag.StableCallToken(call)
	}
	if opts.CreatedAt == 0 {
		opts.CreatedAt = diag.StableUnix(call)
	}
	return opts
}

// WriteNonStreamJSON encodes a completed canonical stream as chat.completion JSON.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	return writeNonStreamJSONCore(ctx, w, model, opts.CompletionID, opts.CreatedAt, opts.ExposeAipUsageExtensions, func(w http.ResponseWriter) {
		sessionwire.WriteResponseCarriers(w, call)
	}, es)
}

// WireWriteNonStreamJSON encodes a completed canonical stream as chat.completion JSON for wire fast-path execution.
// It is deterministic-only: creation time comes from the response context
// seeds and it never consults an executor clock. Clock-selected wire encoding
// goes through the handler closure into wireWriteNonStreamJSONWithOptions.
func WireWriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, exposeExt bool) error {
	return wireWriteNonStreamJSONWithOptions(ctx, w, rc, es, EncodeOptions{ExposeAipUsageExtensions: exposeExt})
}

// wireWriteNonStreamJSONWithOptions encodes a completed canonical stream as
// chat.completion JSON using caller-selected encode options, sharing the
// existing encoder core. Zero option fields fall back to the response context
// deterministic seeds, preserving canonical zero/default normalization.
func wireWriteNonStreamJSONWithOptions(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, opts EncodeOptions) error {
	model := rc.ClientModel()
	if model == "" {
		model = rc.EffectiveModel()
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	cid := opts.CompletionID
	if cid == "" {
		cid = rc.OpenAIChatCompletionID()
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = rc.DeterministicTimestamp()
	}
	if ts == 0 {
		ts = time.Now().Unix()
	}
	return writeNonStreamJSONCore(ctx, w, model, cid, ts, opts.ExposeAipUsageExtensions, func(w http.ResponseWriter) {
		rc.WriteSessionHeaders(w)
	}, es)
}

func writeNonStreamJSONCore(ctx context.Context, w http.ResponseWriter, model string, cid string, ts int64, exposeExt bool, writeCarriers func(w http.ResponseWriter), es aipapi.EventStream) error {
	tee := openaiwire.TeeTerminal(es)
	col, err := aipapi.Collect(ctx, tee)
	if err != nil {
		return err
	}
	tools := col.OrderedToolCalls()
	stop := "stop"
	if len(tools) > 0 {
		stop = "tool_calls"
	}
	// An output-token-limit stop wins over tool_calls: the upstream may have cut
	// the turn off midway through tool arguments. A content-filter refusal is
	// surfaced verbatim (never length taxonomy, never clean-stop).
	if tee.IsOutputLimitTerminal(col) {
		stop = "length"
	} else if col.FinishReason == "content_filter" {
		stop = "content_filter"
	}
	contentJSON, err := marshalChatAssistantContent(col)
	if err != nil {
		return err
	}
	msg := &wireAssistant{Role: "assistant", Content: contentJSON}
	if reasoning := col.Reasoning.String(); reasoning != "" {
		msg.ReasoningContent = reasoning
		msg.Reasoning = reasoning
	}
	for _, tc := range tools {
		var wtc wireToolCallNS
		wtc.ID = tc.ID
		wtc.Type = "function"
		wtc.Function.Name = tc.Name
		wtc.Function.Arguments = tc.Arguments
		msg.ToolCalls = append(msg.ToolCalls, wtc)
	}
	out := wireChatCompletion{
		ID:      cid,
		Object:  "chat.completion",
		Created: ts,
		Model:   model,
		Choices: []wireChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: &stop,
		}},
	}
	out.Usage = wireLegacyUsage(col, exposeExt)
	if writeCarriers != nil {
		writeCarriers(w)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(out)
}

// WriteStreamSSE emits chat.completion.chunk SSE events incrementally from the canonical stream.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	model := ModelFromCall(call)
	if model == "" {
		model = "gpt-4o-mini"
	}
	opts = defaultEncodeOptions(call, opts)
	return writeStreamSSECore(ctx, w, model, opts.CompletionID, opts.CreatedAt, opts.ExposeAipUsageExtensions, func(w http.ResponseWriter) {
		sessionwire.WriteResponseCarriers(w, call)
	}, es, true)
}

// WireWriteStreamSSE emits chat.completion.chunk SSE events incrementally from the canonical stream for wire fast-path execution.
// It is deterministic-only: creation time comes from the response context
// seeds and it never consults an executor clock. Clock-selected wire encoding
// goes through the handler closure into wireWriteStreamSSEWithOptions.
func WireWriteStreamSSE(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, exposeExt bool) error {
	return wireWriteStreamSSEWithOptions(ctx, w, rc, es, EncodeOptions{ExposeAipUsageExtensions: exposeExt})
}

// wireWriteStreamSSEWithOptions emits chat.completion.chunk SSE events using
// caller-selected encode options, sharing the existing encoder core. Zero
// option fields fall back to the response context deterministic seeds,
// preserving canonical zero/default normalization.
func wireWriteStreamSSEWithOptions(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream, opts EncodeOptions) error {
	model := rc.ClientModel()
	if model == "" {
		model = rc.EffectiveModel()
	}
	if model == "" {
		model = "gpt-4o-mini"
	}
	cid := opts.CompletionID
	if cid == "" {
		cid = rc.OpenAIChatCompletionID()
	}
	ts := opts.CreatedAt
	if ts == 0 {
		ts = rc.DeterministicTimestamp()
	}
	if ts == 0 {
		ts = time.Now().Unix()
	}
	return writeStreamSSECore(ctx, w, model, cid, ts, opts.ExposeAipUsageExtensions, func(w http.ResponseWriter) {
		rc.WriteSessionHeaders(w)
	}, es, false)
}

func writeStreamSSECore(ctx context.Context, w http.ResponseWriter, model string, cid string, ts int64, exposeExt bool, writeCarriers func(w http.ResponseWriter), es aipapi.EventStream, closeBeforePump bool) error {
	// The canonical path owns pre-pump cleanup because frontendpipe never closes
	// the stream it hands over; the wire replay path retains its caller's
	// raw-stream fallback close. Neither path adds a close once PumpSSE owns it.
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
		return fmt.Errorf("openailegacy: ResponseWriter is not a Flusher")
	}

	var st streamChunkState
	st.chunk.ID = cid
	st.chunk.Object = "chat.completion.chunk"
	st.chunk.Created = ts
	st.chunk.Model = model
	st.choices[0].Index = 0
	st.choices[0].FinishReason = nil
	st.delta.Role = "assistant"
	st.choices[0].Delta = &st.delta
	st.chunk.Choices = st.choices[:]
	if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
		return err
	}

	var usageCol aipapi.Collected
	streamToolIndex := make(map[string]int)
	nextToolStreamIndex := 0
	sawTool := false

	closeBeforePump = false
	return frontendpipe.PumpSSEWithFailure(ctx, w, es, fmt.Errorf("openailegacy: stream ended without response_finished"), func(ev aipapi.Event) (bool, error) {
		switch ev.Kind {
		case aipapi.EventResponseStarted, aipapi.EventMessageStarted:
		case aipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case aipapi.EventTextDelta:
			st.choices[0].FinishReason = nil
			st.delta = wireDelta{Content: ev.Delta}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return false, err
			}
		case aipapi.EventToolCallStarted:
			sawTool = true
			idx := nextToolStreamIndex
			nextToolStreamIndex++
			streamToolIndex[ev.ToolCallID] = idx
			st.choices[0].FinishReason = nil
			st.fn = wireLegacyToolDeltaFn{Name: ev.ToolName}
			st.tools[0] = wireLegacyToolDelta{
				Index:    idx,
				ID:       ev.ToolCallID,
				Type:     "function",
				Function: &st.fn,
			}
			st.delta = wireDelta{ToolCalls: st.tools[:]}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return false, err
			}
		case aipapi.EventToolCallArgsDelta:
			sawTool = true
			idx, ok := streamToolIndex[ev.ToolCallID]
			if !ok {
				idx = nextToolStreamIndex
				nextToolStreamIndex++
				streamToolIndex[ev.ToolCallID] = idx
			}
			st.choices[0].FinishReason = nil
			st.fn = wireLegacyToolDeltaFn{Arguments: ev.Delta}
			st.tools[0] = wireLegacyToolDelta{
				Index:    idx,
				Function: &st.fn,
			}
			st.delta = wireDelta{ToolCalls: st.tools[:]}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return false, err
			}
		case aipapi.EventToolCallFinished:
			sawTool = true
		case aipapi.EventResponseFinished:
			stop := "stop"
			if sawTool {
				stop = "tool_calls"
			}
			// An output-token-limit stop wins over tool_calls: the upstream may
			// have cut the turn off midway through tool arguments. A
			// content-filter refusal is surfaced verbatim.
			if openaiwire.IsOutputLimitStop(ev.ResponseStatus, ev.FinishReason) {
				stop = "length"
			} else if ev.FinishReason == "content_filter" {
				stop = "content_filter"
			}
			st.delta = wireDelta{}
			st.choices[0].Delta = &st.delta
			st.choices[0].FinishReason = &stop
			st.chunk.Usage = wireLegacyUsage(usageCol, exposeExt)
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return false, err
			}
			if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
				return false, err
			}
			fl.Flush()
			return true, nil
		case aipapi.EventReasoningDelta:
			st.choices[0].FinishReason = nil
			st.delta = wireDelta{ReasoningContent: ev.Delta, Reasoning: ev.Delta}
			st.choices[0].Delta = &st.delta
			if err := stream.FlushSSEDataJSON(w, fl, st.chunk); err != nil {
				return false, err
			}
		default:
		}
		return false, nil
	}, func(err error) error {
		out := execerr.ClassifyExecute(err)
		_, message, errType, code := frontendpipe.MapOpenAIExecuteError(out)
		switch out.Status {
		case http.StatusForbidden:
			errType = "permission_error"
		case http.StatusTooManyRequests:
			if out.Kind != execerr.KindBillingDenied {
				errType, code = "rate_limit_error", "rate_limit_exceeded"
			}
		}
		if err := stream.FlushSSEDataJSON(w, fl, map[string]any{"error": map[string]string{
			"message": message, "type": errType, "code": code,
		}}); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
			return err
		}
		fl.Flush()
		return nil
	})
}
