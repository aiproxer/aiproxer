package toolcall

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Mandatory-buffer bound contract for declared tool-call completeness
// requirements.
//
// The requested default bound is 1 MiB and the configurable range is
// [64 KiB, aipapi.MaxEventDeltaBytes]: the floor is the smallest bound that can
// still request more than the pre-existing shared assembly default guarantees,
// and the ceiling is the hard canonical limit on a single streamed argument
// delta, above which no bound could ever be honored.
//
// A declared bound is a completeness requirement, not a repair budget: it never
// widens or narrows any other finalizer's own size policy.

// DefaultMandatoryMaxArgsBytes is the requested mandatory bound when a
// declaration does not name one.
const DefaultMandatoryMaxArgsBytes = 1 << 20

// MinMandatoryMaxArgsBytes is the lower bound of the configurable range.
const MinMandatoryMaxArgsBytes = 64 << 10

// MaxMandatoryMaxArgsBytes is the upper bound of the configurable range and is
// the hard canonical ceiling: a single streamed argument delta can never exceed
// aipapi.MaxEventDeltaBytes, so a larger declared bound is unsatisfiable.
const MaxMandatoryMaxArgsBytes = aipapi.MaxEventDeltaBytes

// OverflowPolicy selects what must happen to a completed tool call whose
// assembled arguments exceed a declared mandatory bound.
//
// A policy is a declaration of intent. Honoring it is the assembler chokepoint's
// responsibility; a finalizer that only records the declaration changes nothing
// by itself.
type OverflowPolicy string

const (
	// OverflowPassThrough keeps the pre-existing behavior past the declared
	// bound: the call is released as the original argument fragments without the
	// declaring finalizer being invoked. This is the safe default for a
	// declaration whose policy is left unspecified.
	OverflowPassThrough OverflowPolicy = "pass_through"

	// OverflowReject fails the tool call closed past the declared bound instead
	// of releasing it, so a finalizer that cannot decide on incomplete
	// arguments never lets them reach the client.
	OverflowReject OverflowPolicy = "reject"
)

// CompletenessPolicy states what a declared bound is FOR, which is a different
// question from what must happen past it.
//
// A declarer that must DECIDE on the arguments cannot tolerate an incomplete
// view, so it declares [CompletenessMandatory] and may refuse the call. A
// declarer that only needs the complete document to OBSERVE - measure savings,
// count what it would change - is making no security decision, so making the
// call depend on it would convert enabling measurement into a new way to reject
// traffic it was only ever meant to watch. It declares
// [CompletenessBestEffort]: its bound still widens assembly so the document can
// be measured, and a well-formed declaration never requires a refusal.
//
// It is a declaration of intent. Honoring it is the assembler chokepoint's
// responsibility; a finalizer that only records the declaration changes nothing
// by itself.
type CompletenessPolicy string

const (
	// CompletenessMandatory is the default and the only value that makes a
	// declared bound binding: the declarer must decide on the complete arguments
	// before the call may be released, and an undecided requirement refuses the
	// call closed.
	CompletenessMandatory CompletenessPolicy = ""

	// CompletenessBestEffort asks for the complete arguments without making the
	// call depend on them. The declared bound still raises how much the call is
	// assembled, which is what makes observation possible at all, but the
	// requirement starts satisfied, so no failure of this or of any other
	// finalizer can turn it into a refusal. A malformed declaration still fails
	// validation and is refused as a publisher error.
	CompletenessBestEffort CompletenessPolicy = "best_effort"
)

// BufferingRequirement is the optional capability a finalizer implements when it
// must receive the complete assembled tool-call arguments before the call may be
// released.
//
// It is deliberately NOT part of [Finalizer]: adding a required method would
// break every already-shipped finalizer. Consumers read it with a plain type
// assertion and fall back to pre-existing behavior when it is absent:
//
//	if req, ok := f.(toolcall.BufferingRequirement); ok {
//		spec := req.ToolCallBufferingRequirement()
//		if spec.DeclaresMandatoryBound() {
//			// a declared completeness requirement applies to this finalizer
//		}
//	}
//
// A finalizer that does not implement it keeps exactly its pre-existing
// buffering behavior. A finalizer that implements it but returns the zero
// [BufferingSpec] also declares no requirement, so opting in and returning the
// zero spec is observationally identical to not implementing the capability at
// all.
//
// The contract is feature-neutral: it names argument size and overflow
// behavior only, never any concrete feature, payload domain, or target
// namespace. A declarer that needs its bound to govern only part of the traffic
// it receives narrows that scope with the separate optional
// [BufferingApplicability] capability rather than by weakening this one.
type BufferingRequirement interface {
	// ToolCallBufferingRequirement returns the declared completeness
	// requirement. It must be deterministic and side-effect free: it is read
	// during composition and must not depend on request state.
	// It may be called concurrently for shared finalizers. It must perform only
	// bounded in-memory work: no I/O, waiting on background work, or lock cycles.
	// There is no context or timeout on this synchronous declaration method.
	ToolCallBufferingRequirement() BufferingSpec
}

// BufferingApplicability is the optional capability a finalizer implements when
// its declared completeness requirement applies to only SOME of the calls it
// receives.
//
// It exists because a declared bound is a statement about the calls a finalizer
// must decide COMPLETELY, not about every call that happens to arrive on the
// same chain. A finalizer that inspects only operator-selected argument
// locations of one tool decides nothing at all about a call to another tool, so
// a consumer that applies such a declaration to every call would enforce - and
// potentially refuse - calls its declarer never claimed.
//
// It is deliberately NOT part of [Finalizer] or [BufferingRequirement], so every
// already-shipped implementation keeps compiling. A finalizer that does not
// implement it keeps the conservative answer: its declared requirement applies
// to EVERY call it is invoked for, which is exactly the pre-existing
// interpretation of a [BufferingSpec] and the one direction requirements forbid
// weakening.
//
// The answer must be derived from the three arguments and from nothing else. In
// particular it must not depend on request state, on the assembled argument
// bytes, or on anything a per-call decision varies, because the consumer reads
// it BEFORE the arguments are complete in order to decide how much of the call
// to buffer at all:
//
//	if appl, ok := f.(toolcall.BufferingApplicability); ok {
//	    if appl.ToolCallBufferingApplies(call.ToolName, tool, catalog) {
//	        // this finalizer's declared bound governs this call
//	    }
//	}
//
// If an earlier finalizer actually rewrites the tool name, the consumer may
// re-evaluate this capability for remaining finalizers against that new name.
// Newly applicable binding requirements must be armed before the next finalizer
// runs; this does not grant authority to hypothetical name repairs at call start.
//
// The contract is feature-neutral, exactly as [BufferingRequirement] is: it names
// a tool name, a tool definition, and a catalog, and nothing about any concrete
// feature, payload domain, or target namespace.
type BufferingApplicability interface {
	// ToolCallBufferingApplies reports whether this finalizer's declared
	// completeness requirement governs one tool call. It must be deterministic,
	// side-effect free, and independent of the call's argument bytes.
	// Calls can overlap across requests/attempts on the same instance. Inputs
	// are read-only; copy before retaining them. This synchronous method has no
	// cancellation context: use bounded in-memory classification only, never
	// I/O or waits for background work. The consumer isolates panics, not hangs.
	ToolCallBufferingApplies(toolName string, tool aipapi.ToolDef, catalog []aipapi.ToolDef) bool
}

// BufferingSpec is a declared completeness requirement: the largest completed
// tool call, in argument bytes, whose complete arguments this finalizer requires
// in order to decide, plus what must happen beyond that bound.
type BufferingSpec struct {
	// MaxArgsBytes is the declared mandatory bound. Zero declares no
	// requirement, which is the same as not implementing
	// [BufferingRequirement] at all.
	MaxArgsBytes int

	// Overflow selects the required behavior past MaxArgsBytes. The empty value
	// is unspecified and means [OverflowPassThrough], which is the
	// pre-existing behavior; it is not valid without a declared bound.
	Overflow OverflowPolicy

	// Completeness states whether this bound is required to DECIDE or only to
	// OBSERVE. The empty value is unspecified and means
	// [CompletenessMandatory], so an existing declaration is mandatory without
	// having said so.
	//
	// A best-effort declaration still raises the call's buffering ceiling - that
	// is what lets it measure a call larger than the shared default - but it
	// never makes the call fail closed. See [CompletenessPolicy].
	Completeness CompletenessPolicy
}

// DeclaresMandatoryBound reports whether this spec is a well-formed declaration
// of a mandatory completeness requirement.
//
// A malformed declaration deliberately reports false, so an unusable bound can
// never silently change behavior: consumers fall back to their pre-existing
// handling exactly as they do for a finalizer that never opted in.
//
// Consumers must not treat that fallback as sufficient on its own. A
// declaration that is PRESENT but malformed is a publisher error, not an
// absent capability: treating it as "did not opt in" would downgrade a
// mandatory completeness requirement into optional pass-through handling.
// A consumer that enforces a mandatory bound must therefore distinguish the
// two by branching on the capability assertion itself, and when the assertion
// succeeds while [BufferingSpec.Validate] reports an error it must fail the
// affected call closed rather than degrade.
func (s BufferingSpec) DeclaresMandatoryBound() bool {
	return s.MaxArgsBytes > 0 && s.Validate() == nil
}

// Validate reports whether this spec is a well-formed declaration.
//
// The zero spec is valid and declares nothing. Otherwise the bound must lie
// inside [MinMandatoryMaxArgsBytes, MaxMandatoryMaxArgsBytes], the policy must be
// either unspecified, [OverflowPassThrough], or [OverflowReject], and the
// completeness must be either unspecified or [CompletenessBestEffort]; a policy
// without a declared bound is rejected because it cannot bound anything, and a
// best-effort declaration that also rejects is rejected because it would claim
// both to bind and not to bind.
//
// Both a feature's configuration validation and a consumer reading a
// declaration should call this rather than re-deriving the range, so the
// configurable range has exactly one definition.
func (s BufferingSpec) Validate() error {
	if s.MaxArgsBytes == 0 {
		if s.Overflow != "" {
			return fmt.Errorf("toolcall: buffering overflow policy %q declared without a mandatory max args bound", s.Overflow)
		}
		if s.Completeness != CompletenessMandatory {
			return fmt.Errorf("toolcall: buffering completeness policy %q declared without a max args bound", s.Completeness)
		}
		return nil
	}
	if s.MaxArgsBytes < MinMandatoryMaxArgsBytes || s.MaxArgsBytes > MaxMandatoryMaxArgsBytes {
		return fmt.Errorf("toolcall: mandatory max args bound %d outside the configurable range [%d, %d]",
			s.MaxArgsBytes, MinMandatoryMaxArgsBytes, MaxMandatoryMaxArgsBytes)
	}
	switch s.Completeness {
	case CompletenessMandatory, CompletenessBestEffort:
	default:
		return fmt.Errorf("toolcall: unknown buffering completeness policy %q", s.Completeness)
	}
	switch s.Overflow {
	case "", OverflowPassThrough, OverflowReject:
	default:
		return fmt.Errorf("toolcall: unknown mandatory buffering overflow policy %q", s.Overflow)
	}
	if s.Completeness == CompletenessBestEffort && s.Overflow == OverflowReject {
		// Refusing IS the decision a best-effort declarer said it does not make.
		// Accepting both would let one declaration claim it neither binds nor
		// refuses, which is the one reading no consumer could honor; rejecting
		// the combination makes the publisher choose which it meant.
		return fmt.Errorf("toolcall: best-effort buffering completeness cannot reject past its bound")
	}
	return nil
}
