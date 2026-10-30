package rewrite

// This file owns the bounded, content-free outcome of one rewrite. Everything a
// caller may learn about what the rewriter did is a count, a byte total, or one
// reason from a closed vocabulary: no path, suffix, tool name, call ID, pointer
// text, or digest can reach a log line or a metric attribute through these types
// (design.md 252).
//
// The vocabulary is closed and total on purpose. A condition this step can observe
// has exactly one reason, so an accounting stage never has to guess why a surface
// contributed nothing, and an unobserved condition cannot invent a new label. The
// four selector reasons are the canonical selector layer's own reasons, projected
// one to one, so the same refusal reads identically wherever it is reported.

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// SkipReason is the bounded, content-free reason one surface contributed no
// rewrite.
//
// It is an enum because the value only ever reaches fixed-count observability
// dimensions. The whole set is closed: SkipReasonNone is the no-skip value and never
// appears in a published statistics value, and every other member is a condition
// this step can actually observe.
type SkipReason uint8

const (
	// SkipReasonNone marks a surface that was not skipped. It is the zero value and
	// is never recorded.
	SkipReasonNone SkipReason = iota
	// SkipReasonMappingInactive marks a usable project root whose alias is not
	// strictly shorter than the root, which leaves outbound virtualization inactive
	// for every surface of the call (requirement 1.4). It is recorded once per call
	// rather than once per surface, because the whole call shares that one state and
	// reporting it per surface would imply a per-surface decision that never
	// happened.
	SkipReasonMappingInactive
	// SkipReasonNoSelectors marks a tool-call argument surface or a structured
	// result surface that no exact profile claimed and the optional inference step
	// did not prove path-bearing, so nothing was selected (requirements 3.5, 3.8).
	SkipReasonNoSelectors
	// SkipReasonPayloadAbsent marks a surface that carries no payload: the field is
	// empty, or it is the JSON null literal. Absent and null are the same answer
	// here because a null payload names no location at all, and the field's own
	// absence is preserved rather than materialized.
	SkipReasonPayloadAbsent
	// SkipReasonPayloadInvalid marks a payload that is not one complete JSON value.
	// A payload that cannot be read whole is never read partially.
	SkipReasonPayloadInvalid
	// SkipReasonPayloadNotObject marks a payload that is present and readable but
	// whose root is not an object. A JSON Pointer can only name a member of an
	// object, so an array or scalar root has no selected location.
	SkipReasonPayloadNotObject
	// SkipReasonSelectorUnresolved marks a pointer that names no location in this
	// payload: an absent member or array position, a token below a scalar, or a
	// position outside the array. The canonical selector layer refuses it, and the
	// rewriter repeats the refusal without guessing at a nearby location.
	SkipReasonSelectorUnresolved
	// SkipReasonSelectorNotString marks a selected value that is neither a string
	// nor an array of strings: a number, a boolean, or null.
	SkipReasonSelectorNotString
	// SkipReasonSelectorObject marks a selected object. Its own nested strings are
	// not selected, because descending into arbitrary structure is the recursive
	// rewrite requirement 2.3 forbids.
	SkipReasonSelectorObject
	// SkipReasonSelectorNotStringArray marks an array holding at least one element
	// that is not a string. The whole array is refused rather than partially
	// selected, so a rewritten payload can never keep an unrewritten path element
	// beside a rewritten one.
	SkipReasonSelectorNotStringArray
	// SkipReasonOpaqueResultUnchanged marks an opaque result payload that no exact
	// profile marked path-oriented, which therefore stays byte-for-byte unchanged
	// (requirement 2.5). It covers the item-authoritative Output, a legacy
	// PartToolResult text payload, and a text result content part.
	SkipReasonOpaqueResultUnchanged
	// SkipReasonOpaqueResultBounded marks an opaque result payload that an exact
	// profile DID mark path-oriented and that the bounded recognizers still left
	// byte-for-byte unchanged. That is the honest answer whenever nothing in the
	// payload is unambiguously a location: the declared mode ran, found no line it
	// could prove, and reported the refusal rather than guessing at a text rewrite
	// (requirement 2.6, design.md 246-250). A payload the recognizers accepted
	// records no skip at all, and that includes a payload accepted on some of its
	// lines and refused on others: the surface, not the line, is this step's unit,
	// and a refused line contributes no counters either, so a partially accepted
	// payload is reported through its counts alone rather than as a refusal.
	SkipReasonOpaqueResultBounded
	// skipReasonCount is the size of the closed reason vocabulary. It is the bound
	// on the accounting structure: a hostile payload can raise a recorded count but
	// can never widen the vocabulary.
	skipReasonCount
)

// String returns the fixed, low-cardinality label of a reason. It is safe for
// content-free observability dimensions: it never contains path, tool-name,
// pointer, or payload bytes.
func (r SkipReason) String() string {
	switch r {
	case SkipReasonNone:
		return ""
	case SkipReasonMappingInactive:
		return "mapping_inactive"
	case SkipReasonNoSelectors:
		return "no_selectors"
	case SkipReasonPayloadAbsent:
		return "payload_absent"
	case SkipReasonPayloadInvalid:
		return "payload_invalid"
	case SkipReasonPayloadNotObject:
		return "payload_not_object"
	case SkipReasonSelectorUnresolved:
		return "selector_unresolved"
	case SkipReasonSelectorNotString:
		return "selector_not_string"
	case SkipReasonSelectorObject:
		return "selector_object"
	case SkipReasonSelectorNotStringArray:
		return "selector_not_string_array"
	case SkipReasonOpaqueResultUnchanged:
		return "opaque_result_unchanged"
	case SkipReasonOpaqueResultBounded:
		return "opaque_result_bounded"
	default:
		return "unknown"
	}
}

// MarshalText implements [encoding.TextMarshaler] so a skip reason reaches any
// exporter as the bounded label rather than as its ordinal.
//
// It exists because requirements.md 7.7 constrains OBSERVABLE output, and the most
// likely exporter of a report is a JSON or log encoder rather than this package's own
// String method. An integer-coded enum encodes as a bare number, which is unbounded in
// the sense that matters: a value outside the closed vocabulary - a future member, or
// an ordinal a caller supplied - leaves the process as whatever number it is, with no
// label a reader can act on and nothing that degrades safely. The default branch of
// String already answers that case, so marshalling delegates to it rather than
// restating the vocabulary a second time.
func (r SkipReason) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// selectorSkipReason projects one canonical selector refusal into this step's
// vocabulary.
//
// The projection is total and one to one over the canonical vocabulary, so a
// refused location reads the same here as it does in the selector layer. A value
// outside that closed set is not representable, so it projects to the no-skip value
// rather than to a mislabeled reason.
func selectorSkipReason(skip pathvirtualization.SelectorSkip) SkipReason {
	switch skip {
	case pathvirtualization.SelectorSkipUnresolved:
		return SkipReasonSelectorUnresolved
	case pathvirtualization.SelectorSkipNotString:
		return SkipReasonSelectorNotString
	case pathvirtualization.SelectorSkipObject:
		return SkipReasonSelectorObject
	case pathvirtualization.SelectorSkipNotStringArray:
		return SkipReasonSelectorNotStringArray
	default:
		return SkipReasonNone
	}
}

// Skip records that one bounded reason occurred, and how often.
//
// A reason outside the closed vocabulary is dropped rather than indexed, so a caller
// cannot make the accounting structure grow by passing an undefined value.
type Skip struct {
	// Reason is the bounded, content-free reason the surface was left unchanged.
	Reason SkipReason
	// Count is how many surfaces reported that reason in this rewrite.
	Count int
}

// Stats is the content-free outcome of one rewrite.
//
// Every field is a count or a byte total, so the whole value is safe to log, export,
// or use as a metric dimension. The type is shared with the accounting mode of the
// feature: measuring what a rewrite would have done runs the same detection and
// mapping logic and reports the same numbers, which is what keeps measured savings
// from drifting from the rewrite they claim to describe.
type Stats struct {
	// Eligible counts the selected leaves this mapping accepted: a leaf whose value
	// carried a real-root prefix ending on a segment boundary. A leaf that simply
	// held no workspace path is neither eligible nor skipped; it is ordinary content
	// for this step.
	Eligible int
	// Rewritten counts the eligible leaves whose published value differs from the
	// value the client sent. In rewrite mode this equals Eligible, because the alias
	// is strictly shorter than the prefix it replaces; it is a separate counter so
	// the measurement mode can report eligibility without a mutation.
	Rewritten int
	// BytesBefore is the total length of the eligible values as the client spelled
	// them, which is the length the model would otherwise see.
	BytesBefore int
	// BytesAfter is the total length of the values this rewrite published.
	BytesAfter int
	// Skips holds one entry per reason that occurred, in ascending reason order. It
	// holds no entry for a reason that never occurred, so its length is at most the
	// size of the closed reason vocabulary.
	Skips []Skip
}

// BytesSaved returns the byte difference this rewrite is expected to save.
//
// It is derived rather than stored so the reported saving cannot disagree with the
// two totals it is defined from, in either direction.
func (s Stats) BytesSaved() int { return s.BytesBefore - s.BytesAfter }

// account accumulates one rewrite's counters and reason tallies.
//
// The tally is a fixed-size array indexed by the closed reason vocabulary, so
// recording is allocation-free and its memory is bounded no matter what a hostile
// payload contains. Converting it to a published Stats emits only the reasons that
// actually occurred, in ascending order, so two identical rewrites always produce
// identical statistics.
type account struct {
	eligible    int
	rewritten   int
	bytesBefore int
	bytesAfter  int
	skips       [skipReasonCount]int
}

// skip records one occurrence of a bounded reason. The no-skip value and any value
// outside the closed vocabulary are dropped: neither is a condition this step can
// observe, and indexing them would either invent a label or grow the structure.
func (a *account) skip(reason SkipReason) {
	if reason == SkipReasonNone || reason >= skipReasonCount {
		return
	}
	a.skips[reason]++
}

// record folds one shared payload pass into this walk's accounting.
//
// The pass reports its outcome, the canonical selector layer's own refusals, and its
// counters; this is the single place those become the rewriter's closed vocabulary
// and the walk's totals. Folding them here rather than at each call site is what keeps
// the OUTBOUND statistics and the INBOUND statistics measured on one engine, so a
// number reported by either direction describes the same pass (requirement 7.3).
func (a *account) record(pass DocumentPass) {
	switch pass.Outcome {
	case PayloadOutcomeAbsent:
		a.skip(SkipReasonPayloadAbsent)
	case PayloadOutcomeInvalid:
		a.skip(SkipReasonPayloadInvalid)
	case PayloadOutcomeNotObject:
		a.skip(SkipReasonPayloadNotObject)
	case PayloadOutcomeReplaced, PayloadOutcomeNoSelectors, PayloadOutcomeNoLeaves:
		// A pass that reached its leaves without replacing one is ordinary content
		// for this step, and a pass with nothing to select is accounted for by the
		// caller that knew it had no selectors. Neither is a surface skip.
	}
	for _, skipped := range pass.Selection.Skipped {
		a.skip(selectorSkipReason(skipped.Reason))
	}
	a.eligible += pass.Eligible
	a.rewritten += pass.Replaced
	a.bytesBefore += pass.BytesBefore
	a.bytesAfter += pass.BytesAfter
}

// Stats projects one payload pass onto the content-free statistics value this feature
// reports.
//
// It exists for callers outside the package, so the two directions fold one pass into
// one vocabulary: an external caller gets exactly the skip tallies and counters the
// outbound walker would have recorded for the identical pass, rather than a parallel
// set it maintains itself.
func (p DocumentPass) Stats() Stats {
	var acc account
	acc.record(p)
	return acc.stats()
}

// stats publishes the accumulated counters as the content-free rewrite outcome.
func (a *account) stats() Stats {
	stats := Stats{
		Eligible:    a.eligible,
		Rewritten:   a.rewritten,
		BytesBefore: a.bytesBefore,
		BytesAfter:  a.bytesAfter,
	}
	for reason := SkipReason(1); reason < skipReasonCount; reason++ {
		if count := a.skips[reason]; count > 0 {
			stats.Skips = append(stats.Skips, Skip{Reason: reason, Count: count})
		}
	}
	return stats
}
