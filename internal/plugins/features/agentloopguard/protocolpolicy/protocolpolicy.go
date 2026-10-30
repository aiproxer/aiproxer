// Package protocolpolicy holds the preferred-strategy missing-completion-signal
// policy as pure values.
//
// The policy decides one canonical terminal candidate and returns only the
// bounded terminaldecision.Decision plus the protocol state the caller should
// commit afterwards. It has no provider, runtime, terminal, transcript,
// verifier, placement, lifecycle, or admission authority, and it starts no
// goroutine, stores no context, keeps no request-local registry, and holds no
// globals. Context cancellation, deadline handling, provider dispatch, and
// platform continuation admission belong to the provider dispatch task, not
// here.
//
// Counting contract: Reprompts is incremented exactly once, in the proposed
// state attached to a successfully SDK-validated Continue decision. Every stop
// path, every encode failure, and every intent-build failure returns the prior
// state unchanged, so no bounded vocabulary stop consumes a reprompt. The
// number of admitted platform continuations is core-owned and is not counted
// here.
//
// No prose heuristic, semantic guess, verifier verdict, or candidate-text
// inspection participates in any decision. Only canonical evidence fields and
// canonical protocol state do.
package protocolpolicy

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolstate"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// Bounded reason codes. Every value is a fixed classification well inside the
// SDK reason bound and never carries candidate text, raw identifiers,
// arguments, results, or provider payloads.
const (
	// ReasonInvalidInput reports invalid or incomplete SDK input, limits, or
	// platform snapshot.
	ReasonInvalidInput = "invalid_input"
	// ReasonInvalidState reports unusable preferred protocol state.
	ReasonInvalidState = "invalid_protocol_state"
	// ReasonAuthoritative reports an authoritative non-continuable cause.
	ReasonAuthoritative = "authoritative_candidate"
	// ReasonExplicitCompletion reports a trusted explicit completion signal.
	ReasonExplicitCompletion = "explicit_completion"
	// ReasonPreOutputFailure reports a pre-output transport or provider
	// failure, which stays with the existing stream-recovery owner.
	ReasonPreOutputFailure = "pre_output_failure"
	// ReasonUnsafeAction reports incomplete or ambiguous ordinary tool state.
	ReasonUnsafeAction = "unsafe_action_state"
	// ReasonMissingTrajectory reports a missing resumable trajectory.
	ReasonMissingTrajectory = "missing_trajectory"
	// ReasonMissingObjective reports a missing bounded objective.
	ReasonMissingObjective = "missing_objective"
	// ReasonProtocolInactive reports that no completion protocol was active for
	// this candidate, so a missing signal is not missing work.
	ReasonProtocolInactive = "completion_protocol_inactive"
	// ReasonProtocolTerminal reports an absorbing protocol stop state.
	ReasonProtocolTerminal = "protocol_terminal"
	// ReasonNoProgress reports the consecutive no-progress breaker.
	ReasonNoProgress = "no_progress"
	// ReasonBudgetExhausted reports configured-cap or platform-cap exhaustion.
	ReasonBudgetExhausted = "budget_exhausted"
	// ReasonIntentBuildFailed reports a state-token or intent build failure. It
	// consumes no reprompt.
	ReasonIntentBuildFailed = "intent_build_failed"
	// ReasonMissingSignal is the only fixed reason for an emitted repair.
	ReasonMissingSignal = "missing_completion_signal"
)

// provenanceInternalControl is the existing internal-control provenance marker.
// The platform owns appending and placement of this control text.
const provenanceInternalControl = "internal-control"

// referencePresent reports whether an opaque reference carries any non-space
// content. It is used only to detect an absent reference; the raw bytes are
// never modified.
func referencePresent(ref string) bool { return strings.TrimSpace(ref) != "" }

// The approved repair instruction. Every clause is semantically fixed and must
// never be shortened, reordered, or omitted to satisfy a size bound; only the
// bounded objective between the preamble and the clauses may be truncated.
const (
	instructionPreamble = "<automated-completion-protocol-repair>\n" +
		"The previous model turn ended without the required `attempt_completion` signal.\n" +
		"This is proxy-internal recovery control, not a new user request, approval,\n" +
		"permission, or scope expansion.\n\n"

	objectiveHeading = "Retained objective:\n"

	instructionClauses = "If all work requested by the user is complete, call `attempt_completion` now\n" +
		"with a concise final `result`.\n\n" +
		"If concrete requested work remains and can proceed without new user input,\n" +
		"continue exactly that work from the retained safe point.\n\n" +
		"If further progress requires user input, permission, credentials, clarification,\n" +
		"or a choice, request that input normally and end. Do not assume it.\n\n" +
		"Do not invent, repeat, broaden, optimize, or discover work merely because this\n" +
		"recovery message was sent.\n"

	instructionPostamble = "</automated-completion-protocol-repair>"
)

// ErrInvalidIntent is the bounded sentinel for a failed intent build. Its
// message never contains candidate text, identifiers, arguments, or results.
var ErrInvalidIntent = errors.New("agent-loop-guard protocol policy: invalid recovery intent")

// Result is the pure policy outcome.
//
// NextState is the state the caller commits after the platform accepts the
// decision. It equals the proposed state only for a committed Continue decision
// and equals the prior state for every stop and every build failure, so an
// allow_stop never silently rewrites protocol counters.
type Result struct {
	Decision  terminaldecision.Decision
	NextState protocolstate.State
	// Committed reports that NextState records one emitted intent.
	Committed bool
}

// Reserved state namespaces are recognized by family before any opaque
// initial-reference bootstrap, so a reserved reference can never be silently
// reinterpreted as a fresh initial state. Only the current supported preferred
// prefix is decoded, and it is decoded from the original raw reference, which
// makes any padded or otherwise noncanonical spelling invalid.
const (
	// preferredStateFamily is the whole preferred protocol-token family.
	preferredStateFamily = "alg-proto-"
	// legacyStateFamily is the whole legacy state-token family.
	legacyStateFamily = "alg-state-"
)

// LoadState resolves the preferred protocol state carried by the candidate's
// lineage.
//
// Every reserved state namespace is checked first. A reference belonging to the
// preferred or legacy token family is either decoded strictly, when it carries
// exactly the current supported preferred prefix, or refused as invalid state:
// an unsupported version, a malformed payload, a noncanonical spelling, and any
// legacy reference are rejected even when they match the live b-leg or the
// existing trajectory on an initial candidate. Legacy state is never
// translated, and corrupted counters are never reset.
//
// An otherwise unmatched reference is an opaque initial lineage reference. An
// exact, byte-for-byte match with the current request B-leg denotes the initial
// zero state, including an initial b-leg sequence or attempt above one after a
// retry. With a missing B-leg, only an exact existing trajectory reference is
// recognized. Only an empty reference bootstraps, and only for an initial
// attempt of zero or one; a later empty or mismatched foreign reference stops
// conservatively.
//
// No reference value is trimmed, normalized, or rewritten before comparison or
// decoding. No request-local registry, map, or cross-call memory is consulted:
// the decision uses only this candidate's bounded values.
func LoadState(in terminaldecision.Input) (protocolstate.State, error) {
	ref := in.Evidence.Lineage.ProgressRef
	attempt := max(in.Continuation.Attempt, in.Evidence.Lineage.Attempt)
	familyRef := strings.TrimSpace(ref)

	// Absent reference: an initial candidate only.
	if familyRef == "" {
		if attempt > 1 {
			return protocolstate.State{}, fmt.Errorf("%w: missing reference on a later candidate", protocolstate.ErrInvalidState)
		}
		return protocolstate.State{}, nil
	}

	// Reserved families are refused or decoded strictly before any bootstrap.
	switch {
	case strings.HasPrefix(familyRef, preferredStateFamily), strings.HasPrefix(familyRef, legacyStateFamily):
		if !strings.HasPrefix(ref, protocolstate.TokenPrefix) {
			return protocolstate.State{}, fmt.Errorf("%w: unsupported or legacy reserved state namespace", protocolstate.ErrInvalidState)
		}
		// Decode the original raw reference so padded or embedded whitespace
		// fails the strict codec instead of being normalized away.
		state, err := protocolstate.Decode(ref)
		if err != nil {
			return protocolstate.State{}, fmt.Errorf("%w: %w", protocolstate.ErrInvalidState, err)
		}
		return state, nil
	}

	if ref == in.Request.BLegID {
		return protocolstate.State{}, nil
	}
	// A live B-leg identity is authoritative for the initial reference. When it
	// is present, only that exact raw reference bootstraps, so an equal existing
	// trajectory never silently discards continuity. Trajectory bootstrap is
	// reachable only with a missing B-leg, at any attempt.
	if referencePresent(in.Request.BLegID) {
		return protocolstate.State{}, fmt.Errorf("%w: reference matches no current lineage identity", protocolstate.ErrInvalidState)
	}
	if ref == trajectoryRef(in) {
		return protocolstate.State{}, nil
	}
	return protocolstate.State{}, fmt.Errorf("%w: reference matches no current lineage identity", protocolstate.ErrInvalidState)
}

// BuildIntent builds the bounded missing-signal continuation intent from the
// existing objective, the existing trajectory reference, the caller's encoded
// preferred control reference, and the fixed bounded reason.
//
// The intent carries no A-leg append, placement, execution, or recovery
// ownership change: it is only canonical references, internal-control
// provenance, and control text. Completed ordinary tool facts are never carried
// into the instruction, so nothing is replayed. If the objective would exceed
// the SDK instruction bound it is truncated on a UTF-8 boundary; the fixed
// clauses are never shortened or omitted to make room. The complete result is
// validated against the SDK intent bounds before it is returned.
func BuildIntent(in terminaldecision.Input, controlToken string) (terminaldecision.ContinuationIntent, error) {
	if err := in.Validate(); err != nil {
		return terminaldecision.ContinuationIntent{}, fmt.Errorf("%w: %w", ErrInvalidIntent, err)
	}
	intent := terminaldecision.ContinuationIntent{
		TrajectoryRef: trajectoryRef(in),
		ControlRef:    controlToken,
		Provenance:    provenanceInternalControl,
		ReasonCode:    ReasonMissingSignal,
		Instruction:   instructionPreamble + instructionClauses + instructionPostamble,
	}

	if objective := strings.TrimSpace(in.Evidence.Objective); objective != "" {
		budget := terminaldecision.MaxInstructionBytes - fixedInstructionBytes()
		if budget < 0 {
			return terminaldecision.ContinuationIntent{}, fmt.Errorf("%w: instruction budget exhausted", ErrInvalidIntent)
		}
		intent.Instruction = instructionPreamble +
			objectiveHeading +
			truncateUTF8(objective, budget) +
			"\n\n" +
			instructionClauses +
			instructionPostamble
	}
	if err := intent.Validate(); err != nil {
		return terminaldecision.ContinuationIntent{}, fmt.Errorf("%w: %w", ErrInvalidIntent, err)
	}
	return intent, nil
}

// Evaluate is the pure missing-signal policy.
//
// An authoritative cause, a trusted explicit completion signal, a pre-output
// failure, unsafe ordinary tool state, a missing objective, a missing resumable
// trajectory, an inactive protocol expectation, an absorbing terminal state, and
// cap or no-progress exhaustion all stop conservatively with a bounded reason.
//
// Otherwise the evidence is observed once, the resulting state is checked for
// eligibility against the configured and platform budgets, a proposed state
// advances the total exactly once, and that proposed state is what the returned
// Continue decision carries as its control reference. The caller commits
// NextState only after the platform accepts the decision.
//
// Evaluation never contacts a provider, never constructs or calls a verifier,
// and issues no auxiliary request. An empty or prose candidate text is never
// inspected for meaning: classifying an actually empty failure that canonical
// facts report as Normal remains the dispatch task's responsibility.
func Evaluate(in terminaldecision.Input, prior protocolstate.State, limits protocolstate.Limits, platform protocolstate.Platform) (Result, error) {
	return evaluate(in, prior, limits, platform, protocolstate.Encode)
}

// evaluate carries the state encoder as a parameter so the build-failure
// invariant is directly testable; production always uses protocolstate.Encode.
func evaluate(
	in terminaldecision.Input,
	prior protocolstate.State,
	limits protocolstate.Limits,
	platform protocolstate.Platform,
	encode func(protocolstate.State) (string, error),
) (Result, error) {
	if err := in.Validate(); err != nil {
		return stop(prior, ReasonInvalidInput), err
	}
	if err := limits.Validate(); err != nil {
		return stop(prior, ReasonInvalidInput), err
	}
	if err := platform.Validate(); err != nil {
		return stop(prior, ReasonInvalidInput), err
	}
	if err := prior.Validate(); err != nil {
		return stop(prior, ReasonInvalidState), err
	}

	if in.Candidate.Cause.Authoritative() {
		return stop(prior, ReasonAuthoritative), nil
	}
	if in.Evidence.ExplicitCompletion {
		return stop(prior, ReasonExplicitCompletion), nil
	}
	// A pre-output canonical failure stays with the existing stream-recovery
	// owner; a semantic protocol reprompt must not compete with it, and no
	// downstream content may be replayed. Only an actual canonical failure
	// cause qualifies here: a clean Normal candidate with no committed output
	// and no prose is the missing-signal case, not a failure.
	if !in.Candidate.OutputCommitted && canonicalFailureCause(in.Candidate.Cause) {
		return stop(prior, ReasonPreOutputFailure), nil
	}
	if !safeOrdinaryActions(in) {
		return stop(prior, ReasonUnsafeAction), nil
	}
	if !referencePresent(trajectoryRef(in)) {
		return stop(prior, ReasonMissingTrajectory), nil
	}
	if strings.TrimSpace(in.Evidence.Objective) == "" {
		return stop(prior, ReasonMissingObjective), nil
	}
	if !in.Evidence.ExplicitCompletionExpected {
		return stop(prior, ReasonProtocolInactive), nil
	}
	if prior.Terminal {
		return stop(prior, ReasonProtocolTerminal), nil
	}

	observed, err := protocolstate.Observe(prior, protocolstate.Fingerprint(in))
	if err != nil {
		return stop(prior, ReasonInvalidState), err
	}
	eligibility, err := protocolstate.Remaining(observed, limits, platform)
	if err != nil {
		return stop(prior, ReasonInvalidState), err
	}
	if !eligibility.Allowed {
		if observed.ConsecutiveNoProgress >= limits.NoProgressLimit {
			return stop(prior, ReasonNoProgress), nil
		}
		return stop(prior, ReasonBudgetExhausted), nil
	}

	proposed, err := protocolstate.Advance(observed, limits)
	if err != nil {
		return stop(prior, ReasonBudgetExhausted), nil
	}
	token, err := encode(proposed)
	if err != nil {
		return stop(prior, ReasonIntentBuildFailed), nil
	}
	intent, err := BuildIntent(in, token)
	if err != nil {
		return stop(prior, ReasonIntentBuildFailed), nil
	}
	decision := terminaldecision.Decision{
		Kind:       terminaldecision.DecisionContinue,
		ReasonCode: ReasonMissingSignal,
		Continue:   &intent,
	}
	if err := decision.Validate(); err != nil {
		return stop(prior, ReasonIntentBuildFailed), nil
	}
	return Result{Decision: decision, NextState: proposed, Committed: true}, nil
}

// safeOrdinaryActions reports whether every ordinary tool fact is complete.
// Completed ordinary tools are safe and are preserved rather than replayed;
// incomplete or in-progress ordinary tool state is an unsafe boundary.
func safeOrdinaryActions(in terminaldecision.Input) bool {
	for i := range int(in.Evidence.ActionCount) {
		action := in.Evidence.Actions[i]
		switch action.Kind {
		case aipapi.ItemKindToolCall, aipapi.ItemKindToolResult:
			if action.Status != aipapi.ItemStatusCompleted {
				return false
			}
		case aipapi.ItemKindMessage:
		}
	}
	return true
}

// trajectoryRef prefers the existing continuation trajectory and falls back to
// the canonical evidence lineage. Whitespace is inspected only to detect an
// absent reference; the chosen opaque reference is returned byte-for-byte so
// identifiers are never trimmed, normalized, or rewritten.
func trajectoryRef(in terminaldecision.Input) string {
	if strings.TrimSpace(in.Continuation.TrajectoryRef) != "" {
		return in.Continuation.TrajectoryRef
	}
	return in.Evidence.Lineage.TrajectoryRef
}

// canonicalFailureCause reports whether the cause is an actual canonical
// failure class whose pre-output handling remains owned by the existing
// stream-recovery policy. A normal completion candidate is not a failure class,
// and an actually empty failure that canonical facts still classify as Normal is
// resolved by the provider dispatch task from canonical facts, never guessed
// from candidate text here.
func canonicalFailureCause(cause terminaldecision.CandidateCause) bool {
	switch cause {
	case terminaldecision.CandidateCauseTransport,
		terminaldecision.CandidateCauseProviderError,
		terminaldecision.CandidateCauseLimit:
		return true
	default:
		return false
	}
}

// stop returns a conservative allow-stop decision that preserves the prior
// state exactly, so no stop consumes a reprompt or rewrites counters.
func stop(prior protocolstate.State, reason string) Result {
	return Result{
		Decision:  terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: reason},
		NextState: prior,
	}
}

// fixedInstructionBytes is the exact byte cost of everything in the instruction
// that must never be shortened.
func fixedInstructionBytes() int {
	return len(instructionPreamble) + len(objectiveHeading) + len("\n\n") +
		len(instructionClauses) + len(instructionPostamble)
}

// truncateUTF8 keeps at most maxBytes bytes, dropping a partial trailing rune so
// the result is always valid UTF-8.
func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
