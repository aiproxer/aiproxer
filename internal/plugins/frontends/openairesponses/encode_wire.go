package openairesponses

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type wireResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	CreatedAt         int64              `json:"created_at"`
	Status            string             `json:"status"`
	Model             string             `json:"model"`
	Output            []any              `json:"output"`
	Usage             *wireUsage         `json:"usage,omitempty"`
	IncompleteDetails *wireIncomplete    `json:"incomplete_details,omitempty"`
	Error             *wireResponseError `json:"error,omitempty"`
}

// Response resource errors have a different code enum from API error envelopes.
type wireResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wireIncomplete carries the OpenAI Responses incomplete_details discriminator.
type wireIncomplete struct {
	Reason string `json:"reason"`
}

type wireUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// The Responses usage object carries its total alongside the input and
	// output counters, so a zero total is reported rather than dropped.
	TotalTokens         int                      `json:"total_tokens"`
	InputTokensDetails  *wireInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *wireOutputTokensDetails `json:"output_tokens_details,omitempty"`
	CostNanoUnits       int64                    `json:"x_aip_cost_nano_units,omitempty"`
	Currency            string                   `json:"x_aip_currency,omitempty"`
	CostSource          string                   `json:"x_aip_cost_source,omitempty"`
}

type wireInputTokensDetails struct {
	CachedTokens   int `json:"cached_tokens,omitempty"`
	UncachedTokens int `json:"x_aip_uncached_tokens,omitempty"`
	CacheWrite     int `json:"x_aip_cache_write_tokens,omitempty"`
}

type wireOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

func wireResponsesUsage(col aipapi.Collected, exposeExt bool) *wireUsage {
	if col.InputTokens == 0 && col.OutputTokens == 0 && col.CacheReadTokens == 0 && col.CacheWriteTokens == 0 && col.ReasoningTokens == 0 && col.TotalTokens == 0 && col.CostNanoUnits == 0 {
		return nil
	}
	// The Responses input count contains the cached and rewritten prompt
	// subsets, so a producer that reports them outside its input counter is
	// translated here. The OpenAI total covers the translated input and the
	// output: a provider total that differs stays in the canonical evidence
	// and never leaks into the wire total.
	inputTokens := col.InclusiveInputTokens()
	u := &wireUsage{
		InputTokens:  inputTokens,
		OutputTokens: col.OutputTokens,
		TotalTokens:  inputTokens + col.OutputTokens,
	}
	if col.CacheReadTokens > 0 || col.CacheWriteTokens > 0 {
		u.InputTokensDetails = &wireInputTokensDetails{CachedTokens: col.CacheReadTokens}
		if exposeExt {
			u.InputTokensDetails.UncachedTokens = col.UncachedInputTokens()
			u.InputTokensDetails.CacheWrite = col.CacheWriteTokens
		}
	}
	if col.ReasoningTokens > 0 {
		u.OutputTokensDetails = &wireOutputTokensDetails{ReasoningTokens: col.ReasoningTokens}
	}
	if exposeExt {
		u.CostNanoUnits = col.CostNanoUnits
		u.Currency = col.Currency
		u.CostSource = col.CostSource
	}
	return u
}

func fcItemID(callID string) string {
	return "fc_" + strings.ReplaceAll(callID, ":", "_")
}

// wireMessageContentParts builds Responses API message.content items (output_text plus optional input_image / input_file refs).
func wireMessageContentParts(text string, media []aipapi.Part) []any {
	out := []any{map[string]any{"type": "output_text", "text": text}}
	for _, p := range media {
		switch p.Kind {
		case aipapi.PartImageRef:
			out = append(out, map[string]any{"type": "input_image", "image_url": p.ImageRef})
		case aipapi.PartFileRef:
			m := map[string]any{"type": "input_file", "file_id": p.FileRef}
			if p.FileName != "" {
				m["filename"] = p.FileName
			}
			out = append(out, m)
		}
	}
	return out
}

type wireStreamEnvelope struct {
	Type           string       `json:"type"`
	SequenceNumber int          `json:"sequence_number"`
	Response       wireResponse `json:"response"`
}
