package largebody

// Static pre-capture disposition (Task 3.6; Requirements 1, 5, 21;
// design section 4).
//
// Static disposition is an O(1), allocation-free, leaf-pure gate evaluated
// before spool or streaming scanner construction. It evaluates whether a
// request is definitely canonical or needs request-specific assessment.
//
// Invariants (Task 3.6, Requirements 1.9, 5.9, 5.10):
// - Static disposition NEVER says wire-eligible (only DefinitelyCanonical vs NeedsRequestAssessment).
// - DefinitelyCanonical directs the request to the unchanged canonical body-read path with zero spool/scanner allocation.
// - Pure function of WireEligibilitySummary + cheap request facts (known length, gzip flag, feature flag, custom hook flag).
// - Zero I/O, zero body reads, zero reflection, zero maps, zero heap allocations.

// StaticWireDisposition classifies a candidate request before spooling or scanning.
type StaticWireDisposition uint8

const (
	// StaticWireUnknown is the zero value and represents an uninitialized disposition.
	StaticWireUnknown StaticWireDisposition = iota
	// StaticWireDefinitelyCanonical directs the request immediately to the canonical path without spool/scanner setup.
	StaticWireDefinitelyCanonical
	// StaticWireNeedsRequestAssessment directs the request to capture and two-phase assessment.
	// NOTE: Static disposition NEVER says wire-eligible.
	StaticWireNeedsRequestAssessment
)

// Vocabulary aliases matching design section 4 and task requirements:
const (
	DefinitelyCanonical    = StaticWireDefinitelyCanonical
	NeedsRequestAssessment = StaticWireNeedsRequestAssessment
)

// String returns a bounded static label for metrics and diagnostics.
func (d StaticWireDisposition) String() string {
	switch d {
	case StaticWireDefinitelyCanonical:
		return "definitely_canonical"
	case StaticWireNeedsRequestAssessment:
		return "needs_request_assessment"
	default:
		return "unknown"
	}
}

// IsDefinitelyCanonical reports whether the disposition is definitely canonical.
func (d StaticWireDisposition) IsDefinitelyCanonical() bool {
	return d == StaticWireDefinitelyCanonical
}

// IsNeedsRequestAssessment reports whether the disposition requires request assessment.
func (d StaticWireDisposition) IsNeedsRequestAssessment() bool {
	return d == StaticWireNeedsRequestAssessment
}

// StaticWireReason is the bounded taxonomy of static pre-capture decline reasons.
// Labels are static enums; provider/backend/user strings never appear here (Requirement 22.2).
type StaticWireReason uint16

const (
	// StaticWireReasonNone accompanies StaticWireNeedsRequestAssessment (no static blocker).
	StaticWireReasonNone StaticWireReason = iota
	// StaticWireReasonFeatureDisabled indicates server.large_payload_fast_path.enabled is false.
	StaticWireReasonFeatureDisabled
	// StaticWireReasonStaticBlocker indicates a frozen generation authority or unsealed summary blocks wire mode.
	StaticWireReasonStaticBlocker
	// StaticWireReasonBelowThreshold indicates a known uncompressed body length is below the fast-path threshold.
	StaticWireReasonBelowThreshold
	// StaticWireReasonGzipCompressed indicates the body is gzip/compressed (unsupported in Wave 1).
	StaticWireReasonGzipCompressed
	// StaticWireReasonLegacyResolverConfigured indicates a custom route hook is configured without a wire contract (stable reason name for metrics).
	StaticWireReasonLegacyResolverConfigured
)

// String returns a bounded static label for metrics and diagnostics.
func (r StaticWireReason) String() string {
	switch r {
	case StaticWireReasonNone:
		return "none"
	case StaticWireReasonFeatureDisabled:
		return "feature_disabled"
	case StaticWireReasonStaticBlocker:
		return "static_blocker"
	case StaticWireReasonBelowThreshold:
		return "below_threshold"
	case StaticWireReasonGzipCompressed:
		return "gzip_compressed"
	case StaticWireReasonLegacyResolverConfigured:
		return "legacy_resolver_configured"
	default:
		return "unknown"
	}
}

// DefaultThresholdBytes is the fallback decoded-size gate (1 MiB) when
// StaticDispositionInput.ThresholdBytes is not configured (> 0).
const DefaultThresholdBytes int64 = 1 << 20

// StaticDispositionInput carries cheap request facts available before body capture.
// No body bytes, no reader, no maps, no heap-allocated structures.
type StaticDispositionInput struct {
	// FeatureEnabled reports whether the large payload fast path is enabled.
	FeatureEnabled bool
	// GenerationID optionally binds the request to an expected generation identity.
	GenerationID string
	// ThresholdBytes is the decoded-size consideration gate. When <= 0, DefaultThresholdBytes is used.
	ThresholdBytes int64
	// HasKnownLength reports whether ContentLength is a known parsed byte length.
	HasKnownLength bool
	// ContentLength is the parsed Content-Length in bytes when known. Negative indicates unknown/chunked.
	ContentLength int64
	// GzipCompressed reports whether Content-Encoding indicates gzip compression.
	GzipCompressed bool
	// LegacyResolverConfigured reports whether a custom route hook is configured (stable field name for metrics).
	LegacyResolverConfigured bool
}

// StaticDisposition evaluates whether a request is definitely canonical or needs request assessment.
// It is a constant-time, allocation-free pure function. It NEVER returns wire-eligible.
func StaticDisposition(summary WireEligibilitySummary, in StaticDispositionInput) (StaticWireDisposition, StaticWireReason) {
	if !in.FeatureEnabled {
		return StaticWireDefinitelyCanonical, StaticWireReasonFeatureDisabled
	}
	if in.GenerationID != "" && !summary.PinnedTo(in.GenerationID) {
		return StaticWireDefinitelyCanonical, StaticWireReasonStaticBlocker
	}
	if summary.HasStaticBlocker() {
		return StaticWireDefinitelyCanonical, StaticWireReasonStaticBlocker
	}
	if in.LegacyResolverConfigured {
		return StaticWireDefinitelyCanonical, StaticWireReasonLegacyResolverConfigured
	}
	if in.GzipCompressed {
		return StaticWireDefinitelyCanonical, StaticWireReasonGzipCompressed
	}
	if isKnownLength(in) {
		threshold := in.ThresholdBytes
		if threshold <= 0 {
			threshold = DefaultThresholdBytes
		}
		if in.ContentLength < threshold {
			return StaticWireDefinitelyCanonical, StaticWireReasonBelowThreshold
		}
	}
	return StaticWireNeedsRequestAssessment, StaticWireReasonNone
}

// isKnownLength determines if the input specifies a known non-negative content length.
func isKnownLength(in StaticDispositionInput) bool {
	if in.ContentLength > 0 {
		return true
	}
	return in.HasKnownLength && in.ContentLength >= 0
}

// ResolveStaticDisposition is an alias for StaticDisposition.
func ResolveStaticDisposition(summary WireEligibilitySummary, in StaticDispositionInput) (StaticWireDisposition, StaticWireReason) {
	return StaticDisposition(summary, in)
}

// StaticDisposition evaluates whether a request is definitely canonical on the summary receiver.
func (s WireEligibilitySummary) StaticDisposition(in StaticDispositionInput) (StaticWireDisposition, StaticWireReason) {
	return StaticDisposition(s, in)
}
