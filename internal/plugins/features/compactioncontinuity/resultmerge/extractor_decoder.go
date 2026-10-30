package resultmerge

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/extractor"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ExtractorDecoderConfig captures only immutable parser policy for one
// generation/job. Source references are bounded sanitized-source handles,
// never transcript or branch identifiers.
type ExtractorDecoderConfig struct {
	AllowedSourceRefs []string
	Limits            extractor.Limits
}

// ExtractorDecoder adapts the strict feature extractor parser to the late
// result seam. It deliberately parses against the verified parent supplied by
// Service rather than trusting child-provided branch or revision metadata.
type ExtractorDecoder struct {
	allowedSourceRefs []string
	limits            extractor.Limits
}

func NewExtractorDecoder(cfg ExtractorDecoderConfig) *ExtractorDecoder {
	return &ExtractorDecoder{
		allowedSourceRefs: append([]string(nil), cfg.AllowedSourceRefs...),
		limits:            cfg.Limits,
	}
}

func (d *ExtractorDecoder) Decode(collected aipapi.Collected, input DecodeInput) (capsule.Delta, error) {
	if d == nil {
		return capsule.Delta{}, fmt.Errorf("%w: nil extractor decoder", ErrInvalidResult)
	}
	if calls := collected.OrderedToolCalls(); len(calls) != 0 {
		return capsule.Delta{}, fmt.Errorf("%w: extractor returned tool calls", ErrInvalidResult)
	}
	parsed, err := extractor.ParseResult([]byte(collected.Text.String()), extractor.ParseOptions{
		Previous:            input.Previous,
		ExpectedBranch:      input.ExpectedBranch,
		AllowedSourceRefs:   append([]string(nil), d.allowedSourceRefs...),
		SourceHighWatermark: input.SourceHighWatermark,
		Limits:              d.limits,
	})
	if err != nil {
		return capsule.Delta{}, fmt.Errorf("%w: %w", ErrInvalidResult, err)
	}
	// Result.Delta carries parser-validated DecisionTransitions. The extractor
	// parser has already rejected unknown, duplicate, protected, and
	// non-semantic transition targets; retain the validated transitions here.
	return parsed.Delta(input.ExpectedBranch, input.SourceHighWatermark), nil
}

var _ DeltaDecoder = (*ExtractorDecoder)(nil)
