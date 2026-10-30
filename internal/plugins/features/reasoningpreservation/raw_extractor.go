package reasoningpreservation

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// HardRawOutputCeiling is the hard implementation byte limit distinct from configured max_output_bytes.
// It is a defense-in-depth cap applied before JSON decode regardless of operator config.
const HardRawOutputCeiling = 512 * 1024 // 512 KiB

var (
	ErrRawOversize       = errors.New(ID + ": raw_oversize")
	ErrRawInvalidChannel = errors.New(ID + ": raw_invalid_channel")
	ErrRawInvalidLimit   = errors.New(ID + ": raw_invalid_limit")
)

// rawTextSource is the minimal fragment-source abstraction for bounded extraction.
// It is intentionally unexported: only trusted internal callers may supply a Len/String
// source, preventing attacker-controlled sources from injecting arbitrary Len/String.
// The public API accepts only trusted aipapi.Collected.
//
// Current aipapi.Collected aggregates text via a single strings.Builder. It does NOT
// expose a fragment iterator – text arrives as sequential TextDelta events collapsed
// into one buffer during Collect/CollectWithLimits. For this implementation, the
// allocation guard is therefore: check Builder.Len() (O(1), no allocation) BEFORE
// calling Builder.String() (which copies the buffer). Oversize paths return
// raw_oversize without ever invoking String() or JSON decode, so they allocate O(1)
// (only error formatting) and never a payload-sized copy.
//
// Scheduler MaxResultBytes (default 8 MiB) is an outer defense-in-depth ceiling that
// caps Collected at collection time. Feature max_output_bytes (validated ≤
// HardRawOutputCeiling = 512 KiB) is the stricter inner parser guard enforced here
// before any decode. Effective limit is min(configured, hardCeiling) to make clamping
// explicit. The two bounds are independent: scheduler protects the process from
// unbounded streams, this extractor protects the feature from unbounded parser
// allocation even if the scheduler bound is misconfigured larger.
type rawTextSource interface {
	TextLen() int
	TextString() string
}

// collectedTextSource adapts aipapi.Collected to rawTextSource without allocation.
type collectedTextSource struct {
	c *aipapi.Collected
}

func (s collectedTextSource) TextLen() int {
	if s.c == nil {
		return 0
	}
	return s.c.Text.Len()
}

func (s collectedTextSource) TextString() string {
	if s.c == nil {
		return ""
	}
	return s.c.Text.String()
}

// extractBoundedRawFromSource is the internal bounded extraction helper. It is
// unexported to prevent external injection of arbitrary Len/String implementations.
// Only ExtractBoundedRaw (trusted Collected) is public.
func extractBoundedRawFromSource(src rawTextSource, collected aipapi.Collected, maxOutputBytes int) ([]byte, error) {
	if maxOutputBytes <= 0 {
		return nil, fmt.Errorf("%w: max_output_bytes %d must be > 0", ErrRawInvalidLimit, maxOutputBytes)
	}
	// Reject non-text/terminal channels first per design §9 – must precede any byte accounting or materialization.
	if collected.TerminalError != nil {
		return nil, fmt.Errorf("%w: terminal error present", ErrRawInvalidChannel)
	}
	if !collected.FinishReceived {
		return nil, fmt.Errorf("%w: finish not received", ErrRawInvalidChannel)
	}
	if len(collected.ToolArgs) > 0 || len(collected.ToolNames) > 0 || len(collected.ToolCallOrder) > 0 {
		return nil, fmt.Errorf("%w: tool calls present", ErrRawInvalidChannel)
	}
	if collected.Reasoning.Len() > 0 || len(collected.ReasoningParts) > 0 {
		return nil, fmt.Errorf("%w: reasoning channel present", ErrRawInvalidChannel)
	}
	if len(collected.AssistantMedia) > 0 {
		return nil, fmt.Errorf("%w: assistant media present", ErrRawInvalidChannel)
	}
	// Warnings are permitted: collector aggregates warnings separately and they are
	// not part of raw compressor schema, but their presence does not imply a
	// non-text content channel. Bounded warning counts are enforced at collection
	// (CollectLimits.MaxWarnings) and scheduler level, not here.

	// Effective limit is min(configured, hard ceiling) – explicit clamp.
	effectiveLimit := min(maxOutputBytes, HardRawOutputCeiling)

	// Byte-counter guard: check Len() before String(). Oversize returns before materialization.
	n := src.TextLen()
	if n > effectiveLimit {
		// Report which bound was hit for observability, but effectiveLimit already encodes clamping.
		if n > HardRawOutputCeiling {
			return nil, fmt.Errorf("%w: collected %d > hard ceiling %d", ErrRawOversize, n, HardRawOutputCeiling)
		}
		return nil, fmt.Errorf("%w: collected %d > max_output_bytes %d", ErrRawOversize, n, maxOutputBytes)
	}

	// Materialize bounded bytes only now – bounded by effectiveLimit.
	rawStr := src.TextString()
	if len(rawStr) > effectiveLimit {
		if len(rawStr) > HardRawOutputCeiling {
			return nil, fmt.Errorf("%w: raw %d > hard ceiling %d", ErrRawOversize, len(rawStr), HardRawOutputCeiling)
		}
		return nil, fmt.Errorf("%w: raw %d > max_output_bytes %d", ErrRawOversize, len(rawStr), maxOutputBytes)
	}
	// Copy into []byte – caller owns the bytes; size already bounded ≤ effectiveLimit.
	raw := make([]byte, len(rawStr))
	copy(raw, rawStr)
	return raw, nil
}

// ExtractBoundedRaw extracts raw bytes from a collected canonical response with strict bounding.
// It rejects terminal errors, missing finish, tool calls / non-text channels first, then enforces
// max_output_bytes (clamped to hard ceiling) without invoking JSON decode on oversize and without
// constructing a string beyond the bound.
// Scheduler MaxResultBytes is outer defense-in-depth only; this function enforces the feature's
// stricter max_output_bytes before decode per requirements 3,10 and design §9.
func ExtractBoundedRaw(collected aipapi.Collected, maxOutputBytes int) ([]byte, error) {
	src := collectedTextSource{c: &collected}
	return extractBoundedRawFromSource(src, collected, maxOutputBytes)
}
