package reasoningpreservation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

// SurrogateDecodeOutcome is the typed content-free outcome of strict decoding.
// It is an alias to SafeOutcome so telemetry and decoder share the same string taxonomy.
type SurrogateDecodeOutcome = SafeOutcome

const (
	OutcomeSurrogateDecoded SurrogateDecodeOutcome = "decoded"
)

var (
	ErrSurrogateDecodeInvalid       = errors.New(ID + ": decode_invalid")
	ErrSurrogateSchemaInvalid       = errors.New(ID + ": schema_invalid")
	ErrSurrogateControlInvalid      = errors.New(ID + ": control_invalid")
	ErrSurrogateOversize            = errors.New(ID + ": surrogate_oversize")
	ErrSurrogateInsufficientSavings = errors.New(ID + ": insufficient_savings")
)

// SurrogateDecodeParams correlates the decoder result with the authoritative
// original, policy, and sanitization. SourceBytes is the authoritative source
// size used for savings comparison. Caller must provide raw bytes already
// bounded by ExtractBoundedRaw (max_output_bytes / hard ceiling).
type SurrogateDecodeParams struct {
	ExpectedIndexes     []int
	SourceBytes         int
	MaxSurrogateBytes   int
	MinSavedBytes       int
	MinSavingsRatio     float64
	OriginalDigest      [32]byte
	PolicyRevision      string
	Sanitization        string
	SemanticDigest      [32]byte
	EgressPolicyHash    [32]byte
	AuthorizedRouteHash [32]byte
}

// DecodeSurrogate implements the strict versioned JSON decoder for raw bounded
// bytes. It enforces DisallowUnknownFields, exactly schema_version=1, segments
// expected indexes exactly once (reject duplicate/missing/unexpected), text
// non-empty/non-whitespace, valid UTF-8, reject disallowed controls, raw bytes
// already bounded but decoded aggregate and each retained surrogate enforce
// MaxSurrogateBytes/hard ceiling, reject trailing JSON/tokens, and builds a
// ReasoningSurrogate correlated with original/policy/sanitization using caller
// params. Savings are validated (source bytes, decoded bytes, strictly smaller,
// MinSavedBytes, MinSavingsRatio) with overflow-safe math. Typed content-free
// outcome/error taxonomy is returned. This function never claims semantic
// equivalence; active replay is an operator-approved lossy mode.
func DecodeSurrogate(raw []byte, params SurrogateDecodeParams) (ReasoningSurrogate, SurrogateDecodeOutcome, error) {
	// Defensive param validation: expected indexes must be provided.
	if len(params.ExpectedIndexes) == 0 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: expected indexes must not be empty", ErrSurrogateSchemaInvalid)
	}
	// Validate expected indexes are non-negative and distinct (caller contract).
	seenExp := make(map[int]struct{}, len(params.ExpectedIndexes))
	for _, idx := range params.ExpectedIndexes {
		if idx < 0 {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: expected index %d must be >=0", ErrSurrogateSchemaInvalid, idx)
		}
		if _, dup := seenExp[idx]; dup {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: duplicate expected index %d", ErrSurrogateSchemaInvalid, idx)
		}
		seenExp[idx] = struct{}{}
	}
	if params.PolicyRevision == "" {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: policy revision required", ErrSurrogateSchemaInvalid)
	}
	if params.Sanitization == "" {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: sanitization required", ErrSurrogateSchemaInvalid)
	}
	if !isValidSanitization(params.Sanitization) {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: invalid sanitization %q", ErrSurrogateSchemaInvalid, params.Sanitization)
	}
	var zeroRouteHash [32]byte
	if params.AuthorizedRouteHash == zeroRouteHash {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: route hash required", ErrSurrogateSchemaInvalid)
	}
	if len(raw) == 0 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeDecodeInvalid, fmt.Errorf("%w: empty raw bytes", ErrSurrogateDecodeInvalid)
	}
	// Hard ceiling before UTF-8 scan: ensures raw cap before decode avoids O(n) scan on huge malformed.
	if len(raw) > HardRawOutputCeiling {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSurrogateOversize, fmt.Errorf("%w: raw %d > hard ceiling %d", ErrSurrogateOversize, len(raw), HardRawOutputCeiling)
	}
	if !utf8.Valid(raw) {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeControlInvalid, fmt.Errorf("%w: raw bytes must be valid UTF-8", ErrSurrogateControlInvalid)
	}
	if params.MaxSurrogateBytes <= 0 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSurrogateOversize, fmt.Errorf("%w: max_surrogate_bytes %d must be >0", ErrSurrogateOversize, params.MaxSurrogateBytes)
	}
	effectiveMax := min(params.MaxSurrogateBytes, HardCompressionMaxSurrogateBytes)
	// Explicit caller-params validation for SourceBytes: must be >0, schema_invalid for invalid caller params.
	if params.SourceBytes <= 0 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: source_bytes %d must be >0", ErrSurrogateSchemaInvalid, params.SourceBytes)
	}
	// Savings ratio defensive NaN/Inf check before use.
	if math.IsNaN(params.MinSavingsRatio) || math.IsInf(params.MinSavingsRatio, 0) {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: min_savings_ratio is NaN/Inf", ErrSurrogateInsufficientSavings)
	}
	// Strict JSON decode with DisallowUnknownFields and trailing-token rejection.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeDecodeInvalid, fmt.Errorf("%w: empty JSON", ErrSurrogateDecodeInvalid)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	type wireSeg struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	}
	type wireRes struct {
		SchemaVersion int       `json:"schema_version"`
		Segments      []wireSeg `json:"segments"`
	}
	var wire wireRes
	if err := dec.Decode(&wire); err != nil {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeDecodeInvalid, fmt.Errorf("%w: %v", ErrSurrogateDecodeInvalid, err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeDecodeInvalid, fmt.Errorf("%w: trailing JSON", ErrSurrogateDecodeInvalid)
	}
	if wire.SchemaVersion != 1 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: schema_version %d want 1", ErrSurrogateSchemaInvalid, wire.SchemaVersion)
	}
	if wire.Segments == nil {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: segments must be present", ErrSurrogateSchemaInvalid)
	}
	// Validate expected count and indexes exactly once.
	if len(wire.Segments) != len(params.ExpectedIndexes) {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: segments count %d want %d", ErrSurrogateSchemaInvalid, len(wire.Segments), len(params.ExpectedIndexes))
	}
	seen := make(map[int]struct{}, len(wire.Segments))
	totalBytes := 0
	for i, s := range wire.Segments {
		if s.Index < 0 {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: segments[%d].index %d must be >=0", ErrSurrogateSchemaInvalid, i, s.Index)
		}
		if _, dup := seen[s.Index]; dup {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: duplicate segment index %d", ErrSurrogateSchemaInvalid, s.Index)
		}
		seen[s.Index] = struct{}{}
		if _, ok := seenExp[s.Index]; !ok {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: unexpected index %d", ErrSurrogateSchemaInvalid, s.Index)
		}
		if !utf8.ValidString(s.Text) {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeControlInvalid, fmt.Errorf("%w: segments[%d].text must be valid UTF-8", ErrSurrogateControlInvalid, i)
		}
		if strings.TrimSpace(s.Text) == "" {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeControlInvalid, fmt.Errorf("%w: segments[%d].text must be non-empty/non-whitespace", ErrSurrogateControlInvalid, i)
		}
		if containsDisallowedControl(s.Text) {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeControlInvalid, fmt.Errorf("%w: segments[%d].text contains disallowed control", ErrSurrogateControlInvalid, i)
		}
		segLen := len(s.Text)
		if segLen > HardCompressionMaxSurrogateBytes {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSurrogateOversize, fmt.Errorf("%w: segments[%d] %d > hard ceiling %d", ErrSurrogateOversize, i, segLen, HardCompressionMaxSurrogateBytes)
		}
		if segLen > effectiveMax {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSurrogateOversize, fmt.Errorf("%w: segments[%d] %d > max_surrogate_bytes %d", ErrSurrogateOversize, i, segLen, effectiveMax)
		}
		// Overflow-safe aggregate: use int64 for total to avoid int overflow on 32-bit.
		total64 := int64(totalBytes) + int64(segLen)
		if total64 > int64(HardCompressionMaxSurrogateBytes) {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSurrogateOversize, fmt.Errorf("%w: decoded aggregate %d > hard ceiling %d", ErrSurrogateOversize, total64, HardCompressionMaxSurrogateBytes)
		}
		if total64 > int64(effectiveMax) {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSurrogateOversize, fmt.Errorf("%w: decoded aggregate %d > max_surrogate_bytes %d", ErrSurrogateOversize, total64, effectiveMax)
		}
		totalBytes = int(total64)
	}
	// Check missing expected indexes (duplicate already checked, size equal, unexpected checked; but keep explicit).
	for idx := range seenExp {
		if _, ok := seen[idx]; !ok {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeSchemaInvalid, fmt.Errorf("%w: missing expected index %d", ErrSurrogateSchemaInvalid, idx)
		}
	}
	// Savings validation: strictly smaller, MinSavedBytes, MinSavingsRatio with overflow-safe math.
	source64 := int64(params.SourceBytes)
	decoded64 := int64(totalBytes)
	if decoded64 >= source64 {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: decoded %d not strictly smaller than source %d", ErrSurrogateInsufficientSavings, decoded64, source64)
	}
	saved64 := source64 - decoded64
	if saved64 < 0 {
		// Defensive overflow: should not happen due to previous check, but guard.
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: negative savings", ErrSurrogateInsufficientSavings)
	}
	if int64(params.MinSavedBytes) > 0 && saved64 < int64(params.MinSavedBytes) {
		return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: saved %d < min_saved_bytes %d", ErrSurrogateInsufficientSavings, saved64, params.MinSavedBytes)
	}
	if params.MinSavingsRatio > 0 {
		// Ratio defensive already handled NaN/Inf above; compute ratio safely.
		if source64 <= 0 {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: source_bytes %d invalid for ratio", ErrSurrogateInsufficientSavings, source64)
		}
		ratio := float64(saved64) / float64(source64)
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: ratio NaN/Inf", ErrSurrogateInsufficientSavings)
		}
		// Defensive: if ratio is not in (0,1) due to misconfig, treat as insufficient.
		if ratio < params.MinSavingsRatio {
			return ReasoningSurrogate{Sanitization: "none"}, OutcomeInsufficientSavings, fmt.Errorf("%w: ratio %f < min %f", ErrSurrogateInsufficientSavings, ratio, params.MinSavingsRatio)
		}
	}
	// Build correlated ReasoningSurrogate (defensive copies, sorted by expected order for determinism).
	segments := make([]SurrogateSegment, 0, len(wire.Segments))
	for _, ws := range wire.Segments {
		segments = append(segments, SurrogateSegment{
			PlacementIndex: ws.Index,
			Text:           ws.Text,
			Bytes:          len(ws.Text),
		})
	}
	// Sort by PlacementIndex for stable surrogate (optional but deterministic).
	// Simple insertion sort tiny n; use stable deterministic order matching expected sorted.
	// Use map to order by params.ExpectedIndexes sorted order if needed; keep as decoded order sorted ascending.
	for i := 0; i < len(segments); i++ {
		for j := i + 1; j < len(segments); j++ {
			if segments[j].PlacementIndex < segments[i].PlacementIndex {
				segments[i], segments[j] = segments[j], segments[i]
			}
		}
	}
	sur := ReasoningSurrogate{
		OriginalDigest:      params.OriginalDigest,
		PolicyRevision:      params.PolicyRevision,
		Sanitization:        params.Sanitization,
		Segments:            segments,
		Bytes:               totalBytes,
		SemanticDigest:      params.SemanticDigest,
		EgressPolicyHash:    params.EgressPolicyHash,
		AuthorizedRouteHash: params.AuthorizedRouteHash,
	}
	return sur, OutcomeSurrogateDecoded, nil
}
