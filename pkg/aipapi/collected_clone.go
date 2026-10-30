package aipapi

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// CloneCollected returns a deep copy of a collected event aggregation.
//
// The copy is heap-allocated and owned by the returned pointer, so its
// Text/Reasoning builders remain valid and writable after the call. Nil stays
// nil. Every mutable interior is independent of the source: tool-argument
// builders, name/order/warning maps and slices, assistant media parts,
// reasoning parts, and the terminal error event graph.
//
// Use CloneCollectedInto when the result must be stored by value at a
// caller-owned address (the builders of that value host stay valid as long as
// the host object does).
func CloneCollected(c *Collected) *Collected {
	if c == nil {
		return nil
	}
	out := new(Collected)
	CloneCollectedInto(out, c)
	return out
}

// CloneCollectedInto copies the interior of src into dst. dst's Text/Reasoning
// builders are reset first, so writing into them is safe even if dst carried
// prior content. dst must outlive any use of its builders.
func CloneCollectedInto(dst, src *Collected) {
	if dst == nil || src == nil {
		return
	}
	dst.Text.Reset()
	dst.Reasoning.Reset()
	_, _ = dst.Text.WriteString(src.Text.String())
	_, _ = dst.Reasoning.WriteString(src.Reasoning.String())
	if src.ToolArgs != nil {
		dst.ToolArgs = make(map[string]*strings.Builder, len(src.ToolArgs))
		for id, b := range src.ToolArgs {
			if b == nil {
				dst.ToolArgs[id] = nil
				continue
			}
			nb := &strings.Builder{}
			_, _ = nb.WriteString(b.String())
			dst.ToolArgs[id] = nb
		}
	} else {
		dst.ToolArgs = nil
	}
	if src.ToolNames != nil {
		dst.ToolNames = maps.Clone(src.ToolNames)
	} else {
		dst.ToolNames = nil
	}
	dst.ToolCallOrder = slices.Clone(src.ToolCallOrder)
	dst.Warnings = slices.Clone(src.Warnings)
	dst.InputTokens = src.InputTokens
	dst.OutputTokens = src.OutputTokens
	dst.CacheReadTokens = src.CacheReadTokens
	dst.cacheReadExcludedFromInput = src.cacheReadExcludedFromInput
	dst.cacheWriteExcludedFromInput = src.cacheWriteExcludedFromInput
	dst.CacheWriteTokens = src.CacheWriteTokens
	dst.ReasoningTokens = src.ReasoningTokens
	dst.TotalTokens = src.TotalTokens
	dst.CostNanoUnits = src.CostNanoUnits
	dst.Currency = src.Currency
	dst.CostSource = src.CostSource
	dst.clientVisibleUsageSelected = src.clientVisibleUsageSelected
	dst.FinishReceived = src.FinishReceived
	dst.FinishReason = src.FinishReason
	if src.TerminalError != nil {
		ev := cloneEvent(*src.TerminalError)
		dst.TerminalError = &ev
	} else {
		dst.TerminalError = nil
	}
	if src.AssistantMedia != nil {
		dst.AssistantMedia = make([]Part, len(src.AssistantMedia))
		for i := range src.AssistantMedia {
			dst.AssistantMedia[i] = src.AssistantMedia[i]
			if src.AssistantMedia[i].Content != nil {
				dst.AssistantMedia[i].Content = append(json.RawMessage(nil), src.AssistantMedia[i].Content...)
			}
			dst.AssistantMedia[i].Reasoning = cloneReasoningPart(src.AssistantMedia[i].Reasoning)
		}
	} else {
		dst.AssistantMedia = nil
	}
	if src.ReasoningParts != nil {
		dst.ReasoningParts = make([]ReasoningPart, len(src.ReasoningParts))
		for i := range src.ReasoningParts {
			if rp := cloneReasoningPart(&src.ReasoningParts[i]); rp != nil {
				dst.ReasoningParts[i] = *rp
			}
		}
	} else {
		dst.ReasoningParts = nil
	}
}
