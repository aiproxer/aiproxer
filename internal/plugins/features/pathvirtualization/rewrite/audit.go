package rewrite

// This file adds the measurement mode of requirements 7.2, 7.3 and 9.5 to the one
// rewriter, and it is deliberately the smallest file in the package.
//
// The whole design is one sentence: audit and rewrite are the SAME pass with the
// publication step switched off. There is no second traversal, no second selector
// resolution, no second mapping decision, and no second opaque recognizer here, and
// there cannot be one, because this file contains no detection at all: it declares
// the two-mode toggle, the mode's read-back, and the one gate that consults it, and
// nothing that looks at a payload byte.
//
// That is what makes the parity property structural instead of aspirational. The
// counters a caller would otherwise trust drift exactly when the measuring code and
// the mutating code are separate implementations: the measuring copy misses a
// selector the mutating one honours, refuses a payload the other rewrites, or spells
// the byte basis differently. Requirement 9.5 asks for realized savings "per
// request/turn", which is only a claim about the rewrite if the two are literally
// the same code reaching the same numbers. Sharing the pass is therefore the
// requirement, not an optimization.
//
// Suppressing publication also has the properties requirement 7.3 asks for without
// any of them being re-implemented:
//
//   - audit returns the very call it was handed. It never builds the clone it would
//     publish into, so there is no second value to keep byte-identical and no way
//     for a mutation to escape into the caller's own request;
//   - audit performs the identical read of the identical bytes, so its eligible
//     count, rewritten count, byte totals and bounded skip reasons are the rewrite's
//     numbers, not an estimate of them. `Rewritten` counts what the rewrite WOULD
//     publish differently for THIS input, which is why an already virtualized
//     request measures zero in both modes rather than only in one;
//   - audit and rewrite therefore fail the same way, because a refusal is reached by
//     the same code. There is no measurement-only failure mode to invent.
//
// The measurement basis is shared for the same reason. BytesBefore and BytesAfter
// are the DECODED-VALUE lengths of the locations the mapping accepted, in both
// modes, because that is the length the model would otherwise see. An audit number
// computed on raw wire bytes would be a different measurement of the same rewrite
// and would disagree with it whenever a client spelled a path with escapes.
//
// The mode is a closed two-member set and it fails closed: only ModeRewrite
// publishes, so a value this build does not define measures rather than mutates.
// That direction is the safe one, because the failure it prevents is a request
// published with virtual paths on a misconfigured generation.

import "github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"

// Mode is the feature's rollout mode: whether a detected replacement is published
// or only measured.
//
// It is an enum because the value is a fixed configuration choice that reaches
// content-free observability dimensions and nothing else. It carries no path, tool
// name, payload byte, or workspace identity.
type Mode uint8

const (
	// ModeAudit performs candidate detection and savings estimation and publishes
	// nothing (requirement 7.3). The canonical request is returned exactly as it
	// arrived, so no canonical mutation happens in this mode at all.
	ModeAudit Mode = iota
	// ModeRewrite performs detection and publishes every replacement, returning a
	// new call and leaving the caller's own value untouched.
	ModeRewrite
)

// String returns the fixed, low-cardinality label of a mode.
//
// The labels are the operator-facing spellings of requirement 7.2's `audit` and
// `rewrite` modes, so the diagnostics surface of requirement 7.8 can report the
// active mode without parsing anything.
func (m Mode) String() string {
	switch m {
	case ModeAudit:
		return "audit"
	case ModeRewrite:
		return "rewrite"
	default:
		return "unknown"
	}
}

// NewWithMode binds one mapping to one compiled profile policy in one rollout mode.
//
// It cannot fail, for the same reason New cannot: both arguments are values with no
// failure mode. The mode is not a tuning knob either — it decides whether the
// outbound pass publishes replacements or only measures them, and an unrecognized
// mode value measures rather than mutates.
func NewWithMode(mapping pathvirtualization.Mapping, resolver *pathvirtualization.Resolver, mode Mode) *Rewriter {
	return &Rewriter{mapping: mapping, resolver: resolver, mode: mode}
}

// Mode reports the rollout mode this rewriter was bound with.
//
// The nil receiver answers audit, which is what a nil rewriter does: it publishes
// the call it was given unchanged and records nothing. Reporting it that way keeps
// the mode and the behavior consistent for a caller that has not finished building
// its rewriter yet.
func (r *Rewriter) Mode() Mode {
	if r == nil {
		return ModeAudit
	}
	return r.mode
}
