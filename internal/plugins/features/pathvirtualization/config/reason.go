package config

// This file owns the bounded classification of a rejected configuration and the
// single error type that carries it.
//
// The design is the same one the rest of this feature uses for its decision
// vocabulary: an error is a CLOSED SET OF LITERAL LABELS plus a fixed
// configuration LOCATION, never text derived from what the operator wrote.
// `toolcallrepair` and several other features interpolate the offending value
// into their messages, which is fine for a value that is either a small enum
// spelling or a key name. It is not fine here, because the values a path
// virtualization subtree can carry include tool names and JSON Pointers, and a
// configuration error is precisely the string an operator pastes into a bug
// report, into a CI log, and into a ticket tracker.
//
// So every byte an Error can render is chosen at compile time:
//
//   - the reason label, which is a member of a closed enum;
//   - the location, which names one of a fixed set of configuration paths and is
//     never assembled from an operator's key.
//
// requirements.md 7.7 forbids real paths, virtualized suffixes, payloads, and
// high-cardinality hashes in anything a deployment can observe, and 7.8 asks
// for a diagnostics inventory of the bounded configuration SHAPE. A reason code
// plus a fixed location is exactly that: an operator can find the offending key
// among the six the design permits, and nothing they typed can travel.
//
// The selector/profile/path-key dimension is NOT duplicated here. It already has
// a closed, content-free vocabulary in the lexical core
// (pathvirtualization.SelectorReject), so an Error carries one of those rather
// than a second set of overlapping reasons that could disagree with it.

import (
	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// Reason is the bounded, content-free classification of a refused
// path-virtualization configuration.
//
// It is a closed enum because it only ever reaches fixed-count observability
// dimensions, an operator-facing classification, and a test assertion. It never
// carries a path, an alias, a workspace tag, a tool name, a pointer, or a payload
// byte: every String below returns a compile-time literal, and an integer this
// build does not define degrades to a bounded label rather than rendering
// something it was handed.
type Reason uint8

const (
	// ReasonNone marks an accepted configuration. It is the zero value so a
	// rejection is never accidentally reported as an acceptance.
	ReasonNone Reason = iota
	// ReasonNotAMapping marks a subtree that is neither a mapping nor an absent
	// or null node. A scalar or a sequence cannot carry this feature's typed
	// configuration at all.
	ReasonNotAMapping
	// ReasonUnknownKey marks a top-level key outside the closed design set. It is
	// the refusal that makes requirement 7.4 structural: because the decoder
	// reads exactly six keys, no spelling an operator invents can reach the
	// reserved alias namespace, its version, the workspace-tag encoding, or a
	// pattern surface this build does not have.
	ReasonUnknownKey
	// ReasonUnknownProfileKey marks a key inside one declared tool profile that is
	// outside the closed design set. The same argument as ReasonUnknownKey, one
	// level down: an operator profile is the place a namespace or pattern knob
	// would otherwise be tempting.
	ReasonUnknownProfileKey
	// ReasonMissingMode marks an enabled configuration that spells no mode. It is
	// a refusal rather than a default, and the reasoning is on DecodeConfig: the
	// two modes are not equivalent, so guessing one would either disable a
	// rollout the operator asked for or mutate without an opt-in.
	ReasonMissingMode
	// ReasonUnknownMode marks a mode spelling outside the closed audit|rewrite
	// enum. It is refused rather than read as audit, so a near miss such as
	// "Rewrite" cannot silently measure instead of mutating.
	ReasonUnknownMode
	// ReasonMandatoryBound marks a declared mandatory expansion bound the shared
	// SDK validator refuses. The range has exactly one definition
	// (toolcall.BufferingSpec.Validate) and this reason carries no number, so the
	// rejection text cannot leak the value an operator wrote.
	ReasonMandatoryBound
	// ReasonSelector marks a refusal owned by the shared exact-name profile and
	// resolver validators. SelectorReject carries which rule was broken.
	ReasonSelector
	// ReasonPathKeySelector marks a refusal owned by the shared path-key
	// vocabulary validator. It is a DISTINCT reason from ReasonSelector because
	// the two own different configuration keys: a profile rule names
	// `.tool_profiles` and a vocabulary rule names `.path_keys`, and one reason
	// cannot carry two locations.
	ReasonPathKeySelector
	// ReasonMalformedValue marks a value whose YAML type is not the declared
	// field's type. The decoder's own type error names the offending value, so
	// it is translated rather than wrapped.
	ReasonMalformedValue
	// ReasonMalformedProfile marks a `tool_profiles` value that is not a profile
	// mapping at all. It is a DISTINCT reason from ReasonMalformedValue for the
	// same reason the vocabulary has its own: the refusal is about a declared
	// profile, so it names `.tool_profiles` rather than the subtree root.
	ReasonMalformedProfile
	// ReasonRepairOrder marks the cross-feature composition guard's refusal: a
	// generation in which tool-call repair's effective finalizer order would sort
	// at or after the path-expansion finalizer's declared order. Requirements.md
	// 8.4 asks mandatory expansion to receive valid completed JSON, and that
	// clause cannot hold in such a generation.
	ReasonRepairOrder
	// ReasonAmbiguousRegistration marks a generation the guard cannot decide
	// because more than one enabled registration claims a guarded feature.
	ReasonAmbiguousRegistration
	// ReasonRepairConfigUnreadable marks a repair subtree this build cannot
	// decode. Its effective order is then unknown, and an unknown order cannot be
	// proven to sort before the expansion pass, so publication is refused.
	ReasonRepairConfigUnreadable
	// ReasonSelfConfigUnreadable marks a path-virtualization subtree this build
	// refuses. The guard decodes its own registration, so a caller cannot reach
	// publication by asking the guard about a configuration it could not compile.
	ReasonSelfConfigUnreadable
)

// String returns the fixed, low-cardinality label of a configuration refusal.
//
// Every label is a compile-time literal and every one is a single snake_case
// token, which is what makes the value safe as a metric dimension and safe to
// assert on. An integer this build does not define degrades to "unknown" rather
// than rendering something it was handed.
func (r Reason) String() string {
	switch r {
	case ReasonNone:
		return "none"
	case ReasonNotAMapping:
		return "not_a_mapping"
	case ReasonUnknownKey:
		return "unknown_key"
	case ReasonUnknownProfileKey:
		return "unknown_profile_key"
	case ReasonMissingMode:
		return "missing_mode"
	case ReasonUnknownMode:
		return "unknown_mode"
	case ReasonMandatoryBound:
		return "mandatory_bound"
	case ReasonSelector:
		return "selector"
	case ReasonPathKeySelector:
		return "path_key_selector"
	case ReasonMalformedValue:
		return "malformed_value"
	case ReasonMalformedProfile:
		return "malformed_profile"
	case ReasonRepairOrder:
		return "repair_order"
	case ReasonAmbiguousRegistration:
		return "ambiguous_registration"
	case ReasonRepairConfigUnreadable:
		return "repair_config_unreadable"
	case ReasonSelfConfigUnreadable:
		return "self_config_unreadable"
	default:
		return "unknown"
	}
}

// ReasonLocation returns the fixed configuration path a reason refers to.
//
// The returned value is always a literal from the set below, and it names a
// LOCATION rather than a key: an unknown-key rejection reports the subtree whose
// key set was violated, never the key the operator wrote, because the operator's
// key is operator input and operator input does not travel in an error.
//
// The set is deliberately small and closed. It is the whole reason a
// configuration rejection stays actionable - an operator with six permitted keys
// and four permitted profile keys can find the offending one - while never
// widening into a place a value could hide.
//
// It is also the ONLY source of a location, which is what makes "a reason and its
// location are one fact" a property of this package rather than a convention every
// call site has to honour: no constructor accepts a location, so a rejection
// cannot name one place for a reason whose own place is another. The two reasons
// the shared validator owns are therefore SPLIT by the key they validate rather
// than sharing one reason with two locations.
func ReasonLocation(r Reason) string {
	switch r {
	case ReasonNotAMapping, ReasonUnknownKey, ReasonMalformedValue:
		return ID
	case ReasonUnknownProfileKey, ReasonSelector, ReasonMalformedProfile:
		return ID + ".tool_profiles"
	case ReasonPathKeySelector:
		return ID + ".path_keys"
	case ReasonMissingMode, ReasonUnknownMode:
		return ID + ".mode"
	case ReasonMandatoryBound:
		return ID + ".mandatory_max_args_bytes"
	case ReasonRepairOrder:
		return ID + ".tool_call_repair_order"
	case ReasonAmbiguousRegistration:
		return ID + ".registration"
	case ReasonRepairConfigUnreadable:
		return ID + ".tool_call_repair_config"
	case ReasonSelfConfigUnreadable:
		return ID + ".config"
	default:
		return ""
	}
}

// Error is the one typed refusal this package produces.
//
// It is a typed error rather than a bare string for two reasons. A composition
// root can branch on Reason() and on SelectorReject() to decide whether a
// configuration problem is its own to report, and a caller can classify a
// rejection without parsing text - which is the only way to classify safely when
// the text is deliberately content-free.
//
// It carries no unwrapped cause. The decoder's own type error and the shared
// validators' messages both interpolate operator values, so their verdicts are
// translated into a reason rather than wrapped; that is why Is and As are the
// whole classification story here.
type Error struct {
	// reason is the bounded classification, and field is the one fixed
	// configuration location that classification owns. Both are assigned together
	// by the constructors below and nowhere else.
	reason Reason
	field  string
	// selector is the shared selector/profile/path-key verdict, and is
	// SelectorRejectNone for every reason that is neither ReasonSelector nor
	// ReasonPathKeySelector.
	selector pathvirtualization.SelectorReject
}

// Error implements error.
//
// The rendering is a compile-time prefix, a literal label, and a literal
// location. Nothing else reaches it, which is what requirements.md 7.7 asks of
// anything a deployment can observe about this feature.
func (e *Error) Error() string {
	if e == nil {
		return ID + ": " + ReasonNone.String()
	}
	return ID + ": " + e.reason.String() + " at " + e.field
}

// Reason returns the bounded classification of the refusal.
func (e *Error) Reason() Reason {
	if e == nil {
		return ReasonNone
	}
	return e.reason
}

// Field returns the fixed configuration location of the refusal.
//
// It is a compile-time literal from ReasonLocation's set, so an operator learns
// WHICH part of the subtree is wrong while nothing they wrote is echoed back.
func (e *Error) Field() string {
	if e == nil {
		return ""
	}
	return e.field
}

// SelectorReject returns the shared selector, profile, or path-key verdict, or
// SelectorRejectNone when the refusal is not owned by those validators.
//
// Reading it through the lexical core's own vocabulary rather than a second copy
// is what keeps one rule, one reason code, and one bounded label per broken
// selector rule.
func (e *Error) SelectorReject() pathvirtualization.SelectorReject {
	if e == nil {
		return pathvirtualization.SelectorRejectNone
	}
	return e.selector
}

// reject builds the refusal for a reason whose location is that reason's own
// fixed configuration path.
//
// It is the ONLY constructor, which is the whole point: a reason and its location
// are one fact, so pairing them in one call is what makes it impossible to attach
// a location that contradicts the reason. Nothing in this package can build an
// Error whose Field() disagrees with ReasonLocation(Reason()), because nothing
// takes a location as an argument.
func reject(reason Reason) *Error {
	return &Error{reason: reason, field: ReasonLocation(reason)}
}

// newSelectorError builds a refusal owned by one of the shared validators,
// carrying its verdict alongside the reason.
//
// The caller passes the REASON rather than a location, for the same reason
// reject takes no location: the two shared validators own different configuration
// keys, so the reason is what says which location the rejection names
// (`.tool_profiles` for the profile and resolver rules, `.path_keys` for the
// vocabulary), and SelectorReject still says which rule was broken.
func newSelectorError(reason Reason, verdict pathvirtualization.SelectorReject) *Error {
	err := reject(reason)
	err.selector = verdict
	return err
}
