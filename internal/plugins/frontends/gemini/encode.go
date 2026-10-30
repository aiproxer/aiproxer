package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// gemStreamWireScratch holds reusable wire buffers for WriteStreamSSE.
// It must not escape the WriteStreamSSE call stack; FlushSSEDataJSON returns before any other use.
type gemStreamWireScratch struct {
	frame     gemCandStreamWire
	cands     [1]gemCandItem
	textParts [1]gemCandPart
	toolParts [1]gemCandPart
	fc        gemFuncCallWire
}

func (s *gemStreamWireScratch) initFrame() {
	s.frame.Candidates = s.cands[:]
	s.cands[0].Content.Role = "model"
}

// flushFinish emits the terminal candidate carrying only the finish reason.
func (s *gemStreamWireScratch) flushFinish(w io.Writer, fl http.Flusher, reason string) error {
	s.cands[0].Content.Parts = s.textParts[:0]
	s.cands[0].FinishReason = reason
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

// geminiFinishReason maps a canonical terminal onto the Gemini finishReason
// vocabulary: MAX_TOKENS for an output-token-limit truncation, SAFETY for a
// refusal, STOP otherwise.
func geminiFinishReason(status, reason string) string {
	if openaiwire.IsOutputLimitStop(status, reason) {
		return "MAX_TOKENS"
	}
	if reason == "content_filter" {
		return "SAFETY"
	}
	return "STOP"
}

func (s *gemStreamWireScratch) flushTextDelta(w io.Writer, fl http.Flusher, delta string) error {
	s.textParts[0].Text = delta
	s.textParts[0].Thought = false
	s.textParts[0].FunctionCall = nil
	s.textParts[0].FileData = nil
	s.cands[0].Content.Parts = s.textParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

func (s *gemStreamWireScratch) flushThoughtDelta(w io.Writer, fl http.Flusher, delta string) error {
	s.textParts[0].Text = delta
	s.textParts[0].Thought = true
	s.textParts[0].FunctionCall = nil
	s.textParts[0].FileData = nil
	s.cands[0].Content.Parts = s.textParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

func (s *gemStreamWireScratch) flushToolCall(w io.Writer, fl http.Flusher, name, argsStr string) error {
	s.fc.Name = name
	s.fc.Args = openaiwire.ToolArgsRawJSON(argsStr)
	s.toolParts[0].Text = ""
	s.toolParts[0].Thought = false
	s.toolParts[0].FunctionCall = &s.fc
	s.toolParts[0].FileData = nil
	s.cands[0].Content.Parts = s.toolParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

func (s *gemStreamWireScratch) flushFileDataURI(w io.Writer, fl http.Flusher, uri, mime string) error {
	if mime == "" {
		mime = "application/octet-stream"
	}
	s.textParts[0].Text = ""
	s.textParts[0].Thought = false
	s.textParts[0].FunctionCall = nil
	s.textParts[0].FileData = &gemFileDataWire{MIMEType: mime, FileURI: uri}
	s.cands[0].Content.Parts = s.textParts[:1]
	return stream.FlushSSEDataJSON(w, fl, s.frame)
}

// EncodeOptions controls optional encoding tweaks.
type EncodeOptions struct {
	ExposeAipUsageExtensions bool
}

// gemCandStreamWire matches the streaming generateContent SSE JSON shape.
type gemCandStreamWire struct {
	Candidates []gemCandItem `json:"candidates"`
}

type gemCandItem struct {
	Content gemCandContent `json:"content"`
	// FinishReason is set on the final candidate only. Gemini reports it as an
	// enum lexeme; "STOP" for a clean turn and "MAX_TOKENS" for a truncation.
	FinishReason string `json:"finishReason,omitempty"`
}

type gemCandContent struct {
	Role  string        `json:"role"`
	Parts []gemCandPart `json:"parts"`
}

type gemFileDataWire struct {
	MIMEType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gemCandPart struct {
	Text         string           `json:"text,omitempty"`
	Thought      bool             `json:"thought,omitempty"`
	FunctionCall *gemFuncCallWire `json:"functionCall,omitempty"`
	FileData     *gemFileDataWire `json:"fileData,omitempty"`
}

type gemFuncCallWire struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gemUsageStreamWire struct {
	UsageMetadata gemUsageMeta `json:"usageMetadata"`
}

type gemUsageMeta struct {
	PromptTokenCount        int    `json:"promptTokenCount"`
	CandidatesTokenCount    int    `json:"candidatesTokenCount"`
	TotalTokenCount         int    `json:"totalTokenCount"`
	CachedContentTokenCount int    `json:"cachedContentTokenCount,omitempty"`
	CostNanoUnits           int64  `json:"xAipCostNanoUnits,omitempty"`
	Currency                string `json:"xAipCurrency,omitempty"`
	CostSource              string `json:"xAipCostSource,omitempty"`
	UncachedTokens          int    `json:"xAipUncachedTokens,omitempty"`
	CacheWriteTokens        int    `json:"xAipCacheWriteTokens,omitempty"`
}

type gemNonStreamWire struct {
	Candidates    []gemCandItem `json:"candidates"`
	UsageMetadata *gemUsageMeta `json:"usageMetadata,omitempty"`
}

func gemUsageMetaFromCollect(col aipapi.Collected, exposeAipExtensions bool) *gemUsageMeta {
	if col.InputTokens == 0 && col.OutputTokens == 0 && col.CacheReadTokens == 0 && col.CacheWriteTokens == 0 && col.TotalTokens == 0 && (!exposeAipExtensions || col.CostNanoUnits == 0) {
		return nil
	}
	// The Gemini prompt count contains the cached prompt subset, so a producer
	// that reports cache reads outside its input counter is translated here.
	// Gemini carries the provider's own total: a reported total is preserved
	// when it does not fall below the translated prompt plus the candidate
	// count, and that floor is reported otherwise. The Gemini usage metadata
	// defines the total as the prompt plus the candidates plus the thoughts
	// plus the tool-use prompt tokens, so a total below the prompt would be a
	// protocol-invalid payload. A larger provider total keeps its separately
	// represented reasoning tokens, and the OpenAI total equality rule is not
	// imported here.
	promptTokenCount := col.InclusiveInputTokens()
	totalTokenCount := col.TotalTokens
	if floor := promptTokenCount + col.OutputTokens; totalTokenCount < floor {
		totalTokenCount = floor
	}
	meta := &gemUsageMeta{
		PromptTokenCount:     promptTokenCount,
		CandidatesTokenCount: col.OutputTokens,
		TotalTokenCount:      totalTokenCount,
	}
	if col.CacheReadTokens > 0 {
		meta.CachedContentTokenCount = col.CacheReadTokens
	}
	if exposeAipExtensions {
		meta.CostNanoUnits = col.CostNanoUnits
		meta.Currency = col.Currency
		meta.CostSource = col.CostSource
		if uncached := col.UncachedInputTokens(); uncached > 0 {
			meta.UncachedTokens = uncached
		}
		if col.CacheWriteTokens > 0 {
			meta.CacheWriteTokens = col.CacheWriteTokens
		}
	}
	return meta
}

func buildGenerateContentWire(col aipapi.Collected) (gemCandStreamWire, error) {
	text := col.Text.String()
	tools := col.OrderedToolCalls()
	parts := make([]gemCandPart, 0, len(tools)+1+len(col.AssistantMedia))
	if text != "" {
		parts = append(parts, gemCandPart{Text: text})
	}
	for _, tc := range tools {
		parts = append(parts, gemCandPart{
			FunctionCall: &gemFuncCallWire{Name: tc.Name, Args: openaiwire.ToolArgsRawJSON(tc.Arguments)},
		})
	}
	for _, p := range col.AssistantMedia {
		switch p.Kind {
		case aipapi.PartImageRef, aipapi.PartFileRef:
			ref := p.ImageRef
			mime := p.ImageMIME
			if p.Kind == aipapi.PartFileRef {
				ref = p.FileRef
				mime = p.FileMIME
			}
			if mime == "" {
				mime = "application/octet-stream"
			}
			parts = append(parts, gemCandPart{FileData: &gemFileDataWire{MIMEType: mime, FileURI: ref}})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, gemCandPart{Text: ""})
	}
	return gemCandStreamWire{
		Candidates: []gemCandItem{{
			Content: gemCandContent{Role: "model", Parts: parts},
		}},
	}, nil
}

// WriteNonStreamJSON encodes a completed canonical stream as a generateContent JSON body.
func WriteNonStreamJSON(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
	tee := openaiwire.TeeTerminal(es)
	col, err := aipapi.Collect(ctx, tee)
	if err != nil {
		return err
	}
	cands, err := buildGenerateContentWire(col)
	if err != nil {
		return err
	}
	if len(cands.Candidates) > 0 {
		cands.Candidates[0].FinishReason = geminiFinishReason(tee.Status(), col.FinishReason)
	}
	resp := gemNonStreamWire{
		Candidates:    cands.Candidates,
		UsageMetadata: gemUsageMetaFromCollect(col, opts.ExposeAipUsageExtensions),
	}
	sessionwire.WriteResponseCarriers(w, call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(resp)
}

// WriteStreamSSE emits Gemini stream chunks incrementally from the canonical stream.
func WriteStreamSSE(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts EncodeOptions) error {
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	fl, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("gemini: ResponseWriter is not a Flusher")
	}
	fl.Flush()

	var usageCol aipapi.Collected
	toolArgs := make(map[string]*strings.Builder)
	toolNames := make(map[string]string)

	var scratch gemStreamWireScratch
	scratch.initFrame()

	closeBeforePump = false
	return frontendpipe.PumpSSEWithFailure(ctx, w, es, fmt.Errorf("gemini: stream ended without response_finished"), func(ev aipapi.Event) (bool, error) {
		switch ev.Kind {
		case aipapi.EventTextDelta:
			if err := scratch.flushTextDelta(w, fl, ev.Delta); err != nil {
				return false, err
			}
		case aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef:
			mime := ev.AssistantMIME
			if mime == "" {
				mime = "application/octet-stream"
			}
			if err := scratch.flushFileDataURI(w, fl, ev.AssistantRef, mime); err != nil {
				return false, err
			}
		case aipapi.EventToolCallStarted:
			if ev.ToolCallID != "" && ev.ToolName != "" {
				toolNames[ev.ToolCallID] = ev.ToolName
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "" {
				return false, nil
			}
			b := toolArgs[ev.ToolCallID]
			if b == nil {
				b = new(strings.Builder)
				toolArgs[ev.ToolCallID] = b
			}
			b.WriteString(ev.Delta)
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "" {
				return false, nil
			}
			name := toolNames[ev.ToolCallID]
			argsStr := ""
			if b := toolArgs[ev.ToolCallID]; b != nil {
				argsStr = b.String()
			}
			if err := scratch.flushToolCall(w, fl, name, argsStr); err != nil {
				return false, err
			}
		case aipapi.EventUsageDelta:
			usageCol.AccumulateUsage(ev)
		case aipapi.EventResponseFinished:
			if err := scratch.flushFinish(w, fl, geminiFinishReason(ev.ResponseStatus, ev.FinishReason)); err != nil {
				return false, err
			}
			if meta := gemUsageMetaFromCollect(usageCol, opts.ExposeAipUsageExtensions); meta != nil {
				var u gemUsageStreamWire
				u.UsageMetadata = *meta
				if err := stream.FlushSSEDataJSON(w, fl, u); err != nil {
					return false, err
				}
			}
			return true, nil
		case aipapi.EventResponseStarted, aipapi.EventMessageStarted:
		case aipapi.EventReasoningDelta:
			if err := scratch.flushThoughtDelta(w, fl, ev.Delta); err != nil {
				return false, err
			}
		default:
		}
		return false, nil
	}, func(err error) error {
		status, message, rpcStatus := frontendpipe.MapGeminiExecuteError(execerr.ClassifyExecute(err))
		var failure wireAPIError
		failure.Error.Code = status
		failure.Error.Message = message
		failure.Error.Status = rpcStatus
		return stream.FlushSSEDataJSON(w, fl, failure)
	})
}
