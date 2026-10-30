package reasoningpreservation

import (
	"context"
	"errors"
	"fmt"
)

// CompressorInputSegment is a local index + text for one semantic placement.
// It carries only the local placement index and sanitized text; no
// session/account/lineage/anchor/digest is ever stored here.
type CompressorInputSegment struct {
	Index int
	Text  string
}

// PreparationOutcome is the typed content-free outcome of bounded preparation.
type PreparationOutcome string

const (
	OutcomeIneligible          PreparationOutcome = "ineligible"
	OutcomeDenied              PreparationOutcome = "denied"
	OutcomeMissingPolicy       PreparationOutcome = "missing-policy"
	OutcomeSanitizerFailed     PreparationOutcome = "sanitizer_failed"
	OutcomeInputBytesExceeded  PreparationOutcome = "input_bytes_exceeded"
	OutcomeInputTokensExceeded PreparationOutcome = "input_tokens_exceeded"
	OutcomeInputOversize       PreparationOutcome = "input_oversize" // alias for bytes exceeded (compat)
	OutcomePrepared            PreparationOutcome = "prepared"
)

var (
	ErrPreparationIneligible          = errors.New(ID + ": ineligible")
	ErrPreparationDenied              = errors.New(ID + ": egress denied")
	ErrPreparationMissingPolicy       = errors.New(ID + ": egress denied missing-policy")
	ErrPreparationSanitizerFailed     = errors.New(ID + ": sanitizer failed")
	ErrPreparationInputBytesExceeded  = errors.New(ID + ": input exceeds max_input_bytes")
	ErrPreparationInputTokensExceeded = errors.New(ID + ": input exceeds max_input_tokens")
)

// EstimateInputTokens is the deterministic bounded token estimator for
// sanitized semantic text. It reuses the repository's conservative
// byte-as-token pattern (extractor's "each UTF-8 byte as one token-equivalent
// unit") and does not add a tokenizer dependency.
// One byte = one token-equivalent: an upper bound on real token count,
// deterministic, bounded, and provider-neutral.
func EstimateInputTokens(text string) int {
	return len(text)
}

// EstimatedTokensForSegments sums EstimateInputTokens over segments without mutation.
func EstimatedTokensForSegments(segments []CompressorInputSegment) int {
	total := 0
	for _, s := range segments {
		total += EstimateInputTokens(s.Text)
	}
	return total
}

// EstimatedBytesForSegments sums byte lengths without mutation.
func EstimatedBytesForSegments(segments []CompressorInputSegment) int {
	total := 0
	for _, s := range segments {
		total += len(s.Text)
	}
	return total
}

// ExtractSemanticSegments returns ONLY placements classified as ReplaySemanticText.
// It excludes ordinary answer/transcript/tools/media/signatures/opaque/native.
// Retains the local placement index (input order) only; never copies
// session/account/lineage/anchor/digest. Result is a new slice; inputs are not mutated.
func ExtractSemanticSegments(placements []PlacedReasoning) []CompressorInputSegment {
	if len(placements) == 0 {
		return nil
	}
	out := make([]CompressorInputSegment, 0, len(placements))
	for i, pr := range placements {
		if ClassifyReasoningPart(pr.Part) != ReplaySemanticText {
			continue
		}
		if pr.Part.Reasoning == nil {
			continue
		}
		txt := pr.Part.Reasoning.Text
		out = append(out, CompressorInputSegment{Index: i, Text: txt})
	}
	if len(out) == 0 {
		return nil
	}
	// defensive: return new slice already; caller cannot mutate inputs via returned Text (string is immutable)
	cp := make([]CompressorInputSegment, len(out))
	copy(cp, out)
	return cp
}

// ExtractSemanticSegmentsFromArtifact extracts eligible segments from an artifact's reasoning.
// Never includes artifact ID/anchor/backend/model/lineage.
func ExtractSemanticSegmentsFromArtifact(artifact TurnArtifact) []CompressorInputSegment {
	return ExtractSemanticSegments(artifact.Reasoning)
}

// PrepareSemanticSegments is the bounded semantic-segment preparation entry point.
// It extracts only ReplaySemanticText placements, applies egress-required sanitization
// BEFORE byte/token accounting, retains local placement index only, supports multiple
// eligible placements, uses the deterministic bounded token estimator, and returns
// typed outcomes for ineligible, denied, sanitizer failure, bytes/tokens exceeded.
// Inputs are preserved (defensive copies); no session/account/lineage/anchor/digest is emitted.
func PrepareSemanticSegments(ctx context.Context, placements []PlacedReasoning, decision CompressionEgressDecision, maxInputBytes, maxInputTokens int) ([]CompressorInputSegment, PreparationOutcome, error) {
	extracted := ExtractSemanticSegments(placements)
	if len(extracted) == 0 {
		return nil, OutcomeIneligible, fmt.Errorf("%w: no eligible semantic placements", ErrPreparationIneligible)
	}
	return prepareSanitizedSegments(ctx, extracted, decision, maxInputBytes, maxInputTokens)
}

// PrepareSemanticSegmentsFromArtifact is a convenience wrapper over PrepareSemanticSegments.
func PrepareSemanticSegmentsFromArtifact(ctx context.Context, artifact TurnArtifact, decision CompressionEgressDecision, maxInputBytes, maxInputTokens int) ([]CompressorInputSegment, PreparationOutcome, error) {
	return PrepareSemanticSegments(ctx, artifact.Reasoning, decision, maxInputBytes, maxInputTokens)
}

// PrepareCompressorInputWithLimits is the bounded sanitization+budget step that
// enforces redaction BEFORE byte/token accounting. It is used by PrepareSemanticSegments
// and is also exposed for direct segment callers. Typed outcomes distinguish bytes vs tokens.
func PrepareCompressorInputWithLimits(ctx context.Context, segments []CompressorInputSegment, decision CompressionEgressDecision, maxInputBytes, maxInputTokens int) ([]CompressorInputSegment, PreparationOutcome, error) {
	// defensive copy of input for non-mutation guarantee even on error paths we do not mutate input
	inputCopy := make([]CompressorInputSegment, len(segments))
	copy(inputCopy, segments)
	segments = inputCopy
	return prepareSanitizedSegments(ctx, segments, decision, maxInputBytes, maxInputTokens)
}

func prepareSanitizedSegments(ctx context.Context, segments []CompressorInputSegment, decision CompressionEgressDecision, maxInputBytes, maxInputTokens int) ([]CompressorInputSegment, PreparationOutcome, error) {
	if decision.Action == EgressDeny {
		vers := decision.PolicyVersion
		if vers == "" {
			vers = "denied"
		}
		if vers == "missing-policy" {
			return nil, OutcomeMissingPolicy, fmt.Errorf("%w: %s", ErrPreparationMissingPolicy, vers)
		}
		return nil, OutcomeDenied, fmt.Errorf("%w: %s", ErrPreparationDenied, vers)
	}
	var sanitized []CompressorInputSegment
	switch decision.Action {
	case EgressAllow:
		sanitized = make([]CompressorInputSegment, len(segments))
		copy(sanitized, segments)
	case EgressRedactThenAllow:
		if decision.Sanitizer == nil {
			return nil, OutcomeSanitizerFailed, fmt.Errorf("%w: redact requires sanitizer", ErrPreparationSanitizerFailed)
		}
		sanitized = make([]CompressorInputSegment, 0, len(segments))
		for _, seg := range segments {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return nil, OutcomeSanitizerFailed, fmt.Errorf("%w: %v", ErrPreparationSanitizerFailed, err)
				}
			}
			out, err := decision.Sanitizer.SanitizeText(ctx, seg.Text)
			if err != nil {
				return nil, OutcomeSanitizerFailed, fmt.Errorf("%w: %v", ErrPreparationSanitizerFailed, err)
			}
			sanitized = append(sanitized, CompressorInputSegment{Index: seg.Index, Text: out})
		}
	default:
		return nil, OutcomeDenied, fmt.Errorf("%w: unknown egress action %d", ErrPreparationDenied, decision.Action)
	}
	// budget after sanitization
	totalBytes := 0
	totalTokens := 0
	for _, s := range sanitized {
		totalBytes += len(s.Text)
		totalTokens += EstimateInputTokens(s.Text)
	}
	if maxInputBytes > 0 && totalBytes > maxInputBytes {
		return nil, OutcomeInputBytesExceeded, fmt.Errorf("%w %d > %d", ErrPreparationInputBytesExceeded, totalBytes, maxInputBytes)
	}
	if maxInputTokens > 0 && totalTokens > maxInputTokens {
		return nil, OutcomeInputTokensExceeded, fmt.Errorf("%w %d > %d", ErrPreparationInputTokensExceeded, totalTokens, maxInputTokens)
	}
	// defensive copy for caller
	cp := make([]CompressorInputSegment, len(sanitized))
	copy(cp, sanitized)
	return cp, OutcomePrepared, nil
}

// PrepareCompressorInput applies egress decision => sanitization if required => then budgets.
// It enforces that redaction occurs before input-size accounting.
// Returns sanitized segments, outcome, error.
// On deny, returns nil segments, outcome "denied" or "missing-policy", and an error.
// Compatibility wrapper: delegates to PrepareCompressorInputWithLimits with no token bound.
func PrepareCompressorInput(ctx context.Context, segments []CompressorInputSegment, decision CompressionEgressDecision, maxInputBytes int) ([]CompressorInputSegment, string, error) {
	segs, outcome, err := PrepareCompressorInputWithLimits(ctx, segments, decision, maxInputBytes, 0)
	if outcome == OutcomeInputBytesExceeded {
		return nil, string(OutcomeInputOversize), err
	}
	return segs, string(outcome), err
}
