package expansion

// This file owns the bounded, content-free vocabulary of one expansion decision.
//
// There are three vocabularies here and none of them may carry a path, an alias, a
// workspace tag, a tool name, a pointer, or argument bytes, because every one of them
// can reach a log line, a metric label, or the client-facing refusal the assembler
// builds from a finalizer's ReasonCode (requirements.md 7.7, 7.8):
//
//   - Reason is this package's own decision vocabulary. It is a NEW closed set rather
//     than a reuse of two existing ones, because no existing axis describes this pass.
//     The outbound rewriter's SkipReason describes PER-SURFACE refusals inside one
//     successful walk, and the lexical core's ExpandResult describes what a single path
//     means. What a caller needs here is what happened to one completed tool call, and
//     that is a third axis with a third answer set.
//   - Outcome is the pass-level shape of that answer, and it is three members because
//     requirement 7.6 asks for eligible/rewritten/skipped/rejected-style accounting and
//     nothing more. Every member is a condition this pass can actually observe.
//   - Report carries those two plus the shared engine's content-free statistics and the
//     lexical core's own bounded root refusal, reusing that closed vocabulary unchanged
//     so requirement 1.8's enumeration stays the single authority on why a root is
//     unusable.
//
// The labels are design.md "Observability"'s own vocabulary wherever one exists:
// workspace_mismatch, malformed_reserved_alias, selector skip, and mandatory overflow
// all appear there verbatim. Report deliberately has NO error field, for the same
// reason the outbound report has none: a Go error's text is the one thing in this
// feature that could carry a payload byte, and there is no reason a caller needs it
// when a closed reason says the same thing.
//
// Nothing in this file is ever formatted from a payload. Every label is a compile-time
// literal, and every counter comes from the shared engine, whose accounting is bounded
// by its own closed reason vocabulary and by the canonical payload limits
// (requirement 7.9).

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization/rewrite"
)

// Outcome is the bounded, content-free shape of one expansion decision.
//
// It is an enum because the value only ever reaches fixed-count observability
// dimensions. The set is closed: every member is a condition this pass can actually
// observe, and an unobserved condition cannot invent a new label.
type Outcome uint8

const (
	// OutcomeExpanded marks a decision that published an expanded argument document,
	// so at least one selected leaf carried this mapping's alias.
	OutcomeExpanded Outcome = iota
	// OutcomeNoop marks a decision that published nothing and refused nothing: the
	// pass reached a verdict and the call continues unchanged. Which verdict is in
	// Report.Reason, because "nothing to do" and "nothing proved" are different
	// operational facts and requirement 7.6 accounts for them separately.
	OutcomeNoop
	// OutcomeRejected marks a decision that failed the whole tool call closed, so no
	// argument byte reaches the client. This is requirements.md 4.4 and 8.3's only
	// safe answer once a reserved alias is model-visible and cannot be mapped to the
	// current workspace unambiguously.
	OutcomeRejected
)

// String returns the fixed, low-cardinality label of an outcome. It is safe for
// content-free observability dimensions: it never contains path, alias, tool-name,
// pointer, payload, or workspace-identity bytes.
func (o Outcome) String() string {
	switch o {
	case OutcomeExpanded:
		return "expanded"
	case OutcomeNoop:
		return "noop"
	case OutcomeRejected:
		return "rejected"
	default:
		return "unknown"
	}
}

// MarshalText implements [encoding.TextMarshaler] so an outcome reaches any exporter as
// the bounded label rather than as its ordinal.
//
// The argument is the one [outbound.Outcome] gives, and the reason it applies here
// unchanged is that requirements.md 7.7 constrains OBSERVABLE output: the most likely
// exporter of a report is a JSON or log encoder rather than this package's own String
// method, and an integer-coded enum encodes as a bare number. A value outside the
// closed vocabulary then leaves the process as whatever ordinal it is, with no label a
// reader can act on. The default branch of String already answers that case, so
// marshalling delegates to it rather than restating the vocabulary a second time.
func (o Outcome) MarshalText() ([]byte, error) { return []byte(o.String()), nil }

// Reason is the bounded, content-free reason one completed tool call decided the way
// it did.
//
// It doubles as the string this pass publishes as a toolcall.Result.ReasonCode, so
// every label here is safe to hand to a client-facing refusal: the assembler wraps it
// in toolcall.RejectError, whose text is the label and nothing else.
type Reason uint8

const (
	// ReasonNone is the zero value and is never reported: every decision carries
	// exactly one of the reasons below.
	ReasonNone Reason = iota
	// ReasonExpanded marks a decision that replaced at least one selected alias with
	// the current real root and published the expanded document (requirements.md 4.1).
	ReasonExpanded
	// ReasonAuditMode marks a decision that ran the identical detection and published
	// nothing because the generation was compiled in audit mode. The statistics still
	// report what the rewrite would have done (requirement 7.3).
	ReasonAuditMode
	// ReasonNoAlias marks a decision that read the whole document and visited its
	// selected leaves and found no alias of the current mapping among them, so the
	// call needs no expansion. This is requirements.md 4.7's ordinary case.
	ReasonNoAlias
	// ReasonNoSelectors marks a decision that inspected nothing, because no exact
	// profile claimed the tool's exact name and the optional inference step proved no
	// location from the declared schema (requirements.md 3.5, 3.8). It also marks the
	// case where the tool has no such location and the argument document could not be
	// read, which is why the unparseable-document refusal below is conditional on the
	// tool having at least one argument selector.
	ReasonNoSelectors
	// ReasonArgsAbsent marks a completed call whose arguments carry no value at all:
	// an empty field or the JSON null literal. Neither spelling is materialized,
	// because a document with no member holds no location.
	ReasonArgsAbsent
	// ReasonPayloadNotObject marks arguments that are readable but whose root is not a
	// JSON object. A JSON Pointer can only name a member of an object, so there is no
	// selected location to visit whatever was compiled.
	ReasonPayloadNotObject
	// ReasonRootUnusable marks a decision made against no mapping, because the
	// authoritative project root is not one of the five supported absolute forms or is
	// spelled inside the fixed V1 reserved alias namespace. Expansion is disabled
	// rather than guessed at, and the specific bounded code is in Report.RootReason
	// (requirement 1.8, design.md "Error Handling").
	ReasonRootUnusable
	// ReasonMappingInactive marks a decision against a usable project root whose alias
	// is not strictly shorter than the root itself, which leaves virtualization
	// inactive for that root (requirement 1.4). It is reported separately from
	// ReasonNoAlias because the two have different operational meanings: one is a
	// property of the deployment, the other of this call.
	ReasonMappingInactive
	// ReasonArgsUnparseable marks a decision that failed the call closed because the
	// completed argument document is not one complete JSON value AND those bytes carry
	// the fixed V1 reserved alias namespace, for a tool whose policy names at least one
	// argument location. design.md "Error Handling" requires exactly this: existing
	// repair policy may run first, but a recognized applicable alias must never bypass
	// required expansion and reach the client (requirements.md 4.4, 8.3).
	ReasonArgsUnparseable
	// ReasonMalformedReservedAlias marks a decision that failed the call closed because
	// a selected value spelled the reserved marker at an alias-root position and was not
	// followed by the frozen tag segment. Such a value is never expanded and never
	// handed back as an ordinary client path (requirement 4.4).
	ReasonMalformedReservedAlias
	// ReasonWorkspaceMismatch marks a decision that failed the call closed because a
	// selected value spelled a well-formed reserved alias that does not name the current
	// mapping: another project root's tag, an incompatible flavor, another drive, or a
	// mapping with no active alias. It is requirements.md 6.5's stale-workspace
	// outcome, and it is never expanded against the current root.
	ReasonWorkspaceMismatch
	// ReasonInvalidRewrite marks a decision that failed the call closed because the
	// spliced document did not survive the JSON validation design.md section 7 step 9
	// requires. A splice of a decoded document cannot break the grammar, so this is a
	// defensive invariant rather than a reachable state; it exists because requirement
	// 8.5 asks for canonical validation to hold after every mutation, and a rule that
	// is only stated is a rule nobody can point at when it stops holding.
	// ReasonExpandedTooLarge marks a decision that failed the call closed because the
	// expanded document would not fit the canonical tool-call args delta the assembler
	// publishes it in.
	//
	// Expansion substitutes the real root for the alias in every selected leaf at once, so
	// a readable document well inside the declared argument bound can still expand past a
	// canonical bound: the alias is short by construction and the real root is not. The
	// expanded output is therefore bounded BEFORE it is published, and a document that does
	// not fit is refused rather than handed on as a lifecycle the runtime's own canonical
	// validation would reject (requirements.md 8.5). Publishing only part of the expansion
	// is not an alternative either: it would release one decided path beside one undecided
	// alias (requirements.md 4.4).
	ReasonExpandedTooLarge
	// ReasonInvalidRewrite marks a decision that failed the call closed because the
	// spliced document did not survive the JSON validation design.md section 7 step 9
	// requires. A splice of a decoded document cannot break the grammar, so this is a
	// defensive invariant rather than a reachable state; it exists because requirement
	// 8.5 asks for canonical validation to hold after every mutation, and a rule that is
	// only stated is a rule nobody can point at when it stops holding.
	ReasonInvalidRewrite
)

// String returns the fixed, low-cardinality label of a reason. It is safe as a
// toolcall.Result.ReasonCode, as a metric dimension, and as a log field: it never
// contains path, alias, tool-name, pointer, payload, or workspace-identity bytes.
func (r Reason) String() string {
	switch r {
	case ReasonNone:
		return ""
	case ReasonExpanded:
		return "expanded"
	case ReasonAuditMode:
		return "audit_mode"
	case ReasonNoAlias:
		return "no_alias"
	case ReasonNoSelectors:
		return "no_selectors"
	case ReasonArgsAbsent:
		return "args_absent"
	case ReasonPayloadNotObject:
		return "payload_not_object"
	case ReasonRootUnusable:
		return "root_unusable"
	case ReasonMappingInactive:
		return "mapping_inactive"
	case ReasonArgsUnparseable:
		return "args_unparseable"
	case ReasonMalformedReservedAlias:
		return "malformed_reserved_alias"
	case ReasonWorkspaceMismatch:
		return "workspace_mismatch"
	case ReasonExpandedTooLarge:
		return "expanded_too_large"
	case ReasonInvalidRewrite:
		return "invalid_rewrite"
	default:
		return "unknown"
	}
}

// MarshalText implements [encoding.TextMarshaler] so a reason reaches any exporter as
// the bounded label rather than as its ordinal.
//
// This reason already doubles as the string published as a toolcall.Result.ReasonCode,
// so the label IS this feature's wire form on one path; rendering the VALUE the same
// way on every other path is what keeps a report and a client-facing refusal from
// describing the same condition with two different renderings. See
// [Outcome.MarshalText] for why the ordinal form is the risk.
func (r Reason) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// rejects reports whether a reason failed the tool call closed.
//
// The mapping is total over the closed vocabulary and it is the only place a reason
// becomes an action, so the two cannot drift apart: no reason can be published with
// the wrong effect, and no rejection can be published under a label that reads as a
// pass-through.
func (r Reason) rejects() bool {
	switch r {
	case ReasonArgsUnparseable, ReasonMalformedReservedAlias, ReasonWorkspaceMismatch,
		ReasonExpandedTooLarge, ReasonInvalidRewrite:
		return true
	case ReasonNone, ReasonExpanded, ReasonAuditMode, ReasonNoAlias, ReasonNoSelectors,
		ReasonArgsAbsent, ReasonPayloadNotObject, ReasonRootUnusable, ReasonMappingInactive:
		return false
	default:
		// An undefined reason is treated as the safe direction: a caller must never be
		// able to make this pass release a possibly alias-bearing argument by passing
		// a value outside the closed vocabulary.
		return true
	}
}

// ParseReason decodes one bounded reason label back into its [Reason].
//
// It exists because the label IS this feature's wire form: the same string travels as
// a toolcall.Result.ReasonCode and out through the assembler's client-facing refusal,
// so a consumer that needs to act on the reason rather than count it has to be able to
// read it back. The second result is false for anything outside the closed vocabulary,
// including the empty label, so a caller can never mistake an unknown value for
// [ReasonNone].
func ParseReason(label string) (Reason, bool) {
	for reason := ReasonExpanded; reason <= ReasonInvalidRewrite; reason++ {
		if reason.String() == label {
			return reason, true
		}
	}
	return ReasonNone, false
}

// Report is the content-free record of one expansion decision.
//
// Every field is a closed code, a count, or a byte total, so the whole value is safe
// to log, export, or use as a metric dimension. There is deliberately no error field,
// for the reason stated at the top of this file.
type Report struct {
	// Outcome is the pass-level verdict. It is always set.
	Outcome Outcome
	// Reason is the bounded reason the call decided the way it did. It is always set
	// to a member other than ReasonNone.
	Reason Reason
	// RootReason is the lexical core's own bounded refusal code, set only when Reason
	// is ReasonRootUnusable. It reuses that closed vocabulary unchanged, so
	// requirement 1.8's enumeration stays the single authority on why a root is
	// unusable and no second root code exists in this feature.
	RootReason pathvirtualization.SkipReason
	// Stats is the shared engine's content-free outcome for this payload: eligible and
	// replaced counts, decoded-value byte totals, and per-reason skip tallies over the
	// engine's own closed vocabulary. It is zero for every outcome that never reached
	// the selected leaves, and identical between audit and rewrite mode for the same
	// input (requirement 7.3).
	Stats rewrite.Stats
	// ArgsOverDeclaredBound reports that this completed call's assembled arguments
	// exceeded the mandatory bound THIS PASS declared, so the completeness requirement
	// the pass published did not hold for it.
	//
	// It is an OBSERVATION and never a decision: the report records it, and the call is
	// decided exactly as it would be without a reporter. Requirement 4.5's refusal is
	// not restated here because it is not this pass's to make - the tool-call assembler
	// owns the effective assembly bound, and past it the assembler refuses the call
	// WITHOUT invoking any finalizer, so a finalizer never observes that case at all.
	//
	// What this field does observe is the neighbouring and genuinely reachable case. The
	// assembler's effective bound is the MAXIMUM over every declaring finalizer, never
	// the minimum, so a deployment where another pass declares a larger bound hands this
	// pass completed calls that may exceed this pass's own declaration. Those calls
	// assemble, arrive here, and are decided normally while the declared completeness
	// silently does not hold - which is exactly the fact requirements.md 7.6's
	// mandatory-buffer counter needs and exactly what an operator who lowered
	// `mandatory_max_args_bytes` cannot otherwise discover.
	//
	// It is a BOOLEAN rather than a length on purpose. A length is a number derived from
	// a payload, which is one step away from the content requirements.md 7.7 forbids, and
	// it would be a second unbounded series on the hot path; "this call was past the
	// bound I declared" is the whole fact, and the count of such calls is the counter.
	ArgsOverDeclaredBound bool
}
