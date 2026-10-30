package reasoningpreservation

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ReplaySemantics is the bounded typed classification for reasoning replay.
// It is derived from canonical dialect plus structure/presence, not provider strings.
type ReplaySemantics uint8

const (
	ReplayUnknown ReplaySemantics = iota
	ReplayExactRequired
	ReplaySemanticText
)

// SegmentSemantics describes the classification of a single placed reasoning segment.
type SegmentSemantics struct {
	PlacementIndex int
	Dialect        aipapi.ReasoningDialect
	Semantics      ReplaySemantics
	SourceBytes    int
}

// ClassifyReasoningPart returns the replay semantics for a single reasoning part.
// Pure function: provider-name free, no I/O, deterministic.
func ClassifyReasoningPart(part aipapi.Part) ReplaySemantics {
	if part.Kind != aipapi.PartReasoning || part.Reasoning == nil {
		return ReplayUnknown
	}
	rp := part.Reasoning
	normalized := aipapi.NormalizeReasoningDialect(rp.Dialect)
	if normalized == "" {
		return ReplayUnknown
	}
	switch normalized {
	case aipapi.ReasoningDialectOpenAIChatTextV1,
		aipapi.ReasoningDialectOpenAIResponsesItemV1,
		aipapi.ReasoningDialectAnthropicThinkingV1,
		aipapi.ReasoningDialectAnthropicRedactedThinkingV1:
	default:
		return ReplayUnknown
	}
	// Exact authority wins over readable text. Check exact fields after dialect
	// known/unknown decision so unknown dialects map to ReplayUnknown.
	if aipapi.ReasoningHasExactResponsesFields(rp) {
		return ReplayExactRequired
	}
	if rp.Signature != "" {
		return ReplayExactRequired
	}
	if len(rp.Opaque) > 0 {
		return ReplayExactRequired
	}
	switch normalized {
	case aipapi.ReasoningDialectOpenAIChatTextV1:
		if strings.TrimSpace(rp.Text) == "" {
			return ReplayUnknown
		}
		return ReplaySemanticText
	case aipapi.ReasoningDialectOpenAIResponsesItemV1:
		return ReplayExactRequired
	case aipapi.ReasoningDialectAnthropicThinkingV1:
		return ReplayExactRequired
	case aipapi.ReasoningDialectAnthropicRedactedThinkingV1:
		return ReplayExactRequired
	default:
		return ReplayUnknown
	}
}

// ClassifyPlacement returns per-placement semantics for a single placed reasoning.
func ClassifyPlacement(idx int, pr PlacedReasoning) SegmentSemantics {
	dialect := aipapi.ReasoningDialect("")
	srcBytes := 0
	if pr.Part.Kind == aipapi.PartReasoning && pr.Part.Reasoning != nil {
		dialect = aipapi.NormalizeReasoningDialect(pr.Part.Reasoning.Dialect)
		srcBytes = aipapi.ReasoningPayloadBytes(pr.Part.Reasoning)
	}
	return SegmentSemantics{
		PlacementIndex: idx,
		Dialect:        dialect,
		Semantics:      ClassifyReasoningPart(pr.Part),
		SourceBytes:    srcBytes,
	}
}

// ClassifyPlacements returns per-placement semantics for all placements.
// The PlacementIndex reflects input order, not BeforeNonReasoningPart.
func ClassifyPlacements(placements []PlacedReasoning) []SegmentSemantics {
	out := make([]SegmentSemantics, 0, len(placements))
	for i, pr := range placements {
		out = append(out, ClassifyPlacement(i, pr))
	}
	return out
}
