// Package protocolstate owns the preferred-strategy missing-signal protocol
// state: a bounded progress fingerprint, bounded counters, and an independent
// opaque state token.
//
// It is a pure value package. It owns no runtime, terminal, provider,
// transcript, verifier, or admission authority, and it holds no globals,
// goroutines, durable maps, or stored contexts. Callers receive validated
// numeric limits from the parent feature; this package deliberately does not
// import the parent agentloopguard package, which would create a cycle.
//
// Counting semantics: Reprompts counts emitted, validated continuation
// intents. Observations never consume a reprompt, and new progress never
// replenishes the immutable total budget. Platform continuation attempts are
// a different unit; a positive platform snapshot cap constrains candidate
// attempts and is intersected with, never substituted for, the feature budget.
package protocolstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

// V1 bounds. MaxReprompts is the absolute accepted total cap for a V1 token;
// the effective cap is additionally narrowed by the configured limit and by
// platform continuation policy.
const (
	MinReprompts       = 1
	MaxReprompts       = 3
	MinNoProgressLimit = 1
	MaxNoProgressLimit = 64

	// MaxConsecutiveNoProgress is the absolute supported bounded no-progress
	// count. Observation saturates here instead of emitting an invalid value.
	MaxConsecutiveNoProgress = MaxNoProgressLimit

	// FingerprintPrefix and FingerprintHexDigits fix the canonical digest
	// representation: "sha256:" plus 64 lowercase hex characters.
	FingerprintPrefix    = "sha256:"
	FingerprintHexDigits = 64
)

// Bounded classification errors. Wrapped errors stay errors.Is-compatible and
// never carry raw state tokens or evidence text.
var (
	ErrInvalidState       = errors.New("agent-loop-guard protocol state: invalid state")
	ErrInvalidLimits      = errors.New("agent-loop-guard protocol state: invalid limits")
	ErrInvalidPlatform    = errors.New("agent-loop-guard protocol state: invalid platform snapshot")
	ErrInvalidToken       = errors.New("agent-loop-guard protocol state: invalid token")
	ErrRepromptsExhausted = errors.New("agent-loop-guard protocol state: reprompt budget exhausted")
)

// State is a request-scoped value holding only bounded counters and a stable
// progress digest, so a caller can own it without globals or shared state.
//
// An empty LastFingerprint means no evidence baseline has been established
// yet. Because observation precedes intent emission, an empty fingerprint
// cannot carry nonzero reprompt or no-progress counters. A nonempty fingerprint
// with zero Reprompts is valid: establishing a baseline is not an emitted
// intent. Terminal is absorbing and preserves the other three fields.
type State struct {
	// Reprompts counts emitted, validated continuation intents.
	Reprompts int
	// LastFingerprint is "" or the canonical SHA-256 digest representation.
	LastFingerprint string
	// ConsecutiveNoProgress counts equal consecutive observations.
	ConsecutiveNoProgress int
	// Terminal is the absorbing stop flag.
	Terminal bool
}

// Limits carries validated numeric bounds supplied by the parent feature.
// This package does not define configuration defaults: passing the accepted
// task-6.1 normalized values is the caller's responsibility.
type Limits struct {
	MaxReprompts    int
	NoProgressLimit int
}

// Validate checks the supported finite ranges 1..MaxReprompts and
// 1..MaxNoProgressLimit.
func (l Limits) Validate() error {
	if l.MaxReprompts < MinReprompts || l.MaxReprompts > MaxReprompts {
		return ErrInvalidLimits
	}
	if l.NoProgressLimit < MinNoProgressLimit || l.NoProgressLimit > MaxNoProgressLimit {
		return ErrInvalidLimits
	}
	return nil
}

// Platform is the candidate-attempt view of the platform continuation cap.
// Cap counts Continuation.Attempt values, not protocol reprompts. A
// non-positive Cap is an unresolved platform default that this package must
// not copy or reinterpret; core owns that default.
type Platform struct {
	Cap     int
	Attempt int
}

// Validate rejects negative snapshots. Cap zero and negative-cap semantics
// beyond that remain platform-owned.
func (p Platform) Validate() error {
	if p.Cap < 0 || p.Attempt < 0 {
		return ErrInvalidPlatform
	}
	return nil
}

// Eligibility is the pure result of intersecting the configured total cap, the
// no-progress limit, the terminal flag, and a positive platform cap.
type Eligibility struct {
	// Allowed reports whether another reprompt may be considered.
	Allowed bool
	// Remaining is the intersection of configured and platform slots and is
	// never negative.
	Remaining int
}

// Validate checks state validity without repairing it. An invalid Reprompts
// value is never reset or clamped here or by Observe/Remaining.
func (s State) Validate() error {
	if s.Reprompts < 0 || s.Reprompts > MaxReprompts {
		return ErrInvalidState
	}
	if s.ConsecutiveNoProgress < 0 || s.ConsecutiveNoProgress > MaxConsecutiveNoProgress {
		return ErrInvalidState
	}
	if !validFingerprint(s.LastFingerprint) {
		return ErrInvalidState
	}
	// Without a baseline there is nothing to have made no progress against and
	// no observation to have preceded an emitted intent.
	if s.LastFingerprint == "" && (s.Reprompts != 0 || s.ConsecutiveNoProgress != 0) {
		return ErrInvalidState
	}
	return nil
}

// validFingerprint accepts only the empty baseline marker or the fixed
// canonical digest representation.
func validFingerprint(fingerprint string) bool {
	if fingerprint == "" {
		return true
	}
	digest, ok := strings.CutPrefix(fingerprint, FingerprintPrefix)
	if !ok || len(digest) != FingerprintHexDigits {
		return false
	}
	for i := range len(digest) {
		c := digest[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// normalize maps CRLF and CR line endings to LF and trims surrounding
// whitespace so formatting-only differences never look like progress.
func normalize(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}

// Fingerprint hashes the stable canonical evidence concepts: normalized
// objective, candidate, and recent text; canonical cause; the
// explicit-completion expectation and observation booleans; and the active
// ordered canonical action kind/status/name tuples.
//
// Volatile identity and budget metadata are excluded: candidate and action
// identifiers, request and leg identifiers, lineage references, timestamps and
// deadlines, policy revisions, continuation attempts, configured limits,
// platform caps, and verifier verdicts. Only the active fixed-array action
// count is iterated, so unused capacity is never hashed.
//
// Fingerprint is a pure projection helper. It performs no platform validation:
// an overflowing ActionCount is clamped rather than panicking, and it does not
// weaken terminaldecision.ValidateInput, which still rejects evidence that is
// populated beyond ActionCount.
func Fingerprint(in terminaldecision.Input) string {
	hash := sha256.New()
	write := func(name, value string) {
		// Length framing keeps adjacent values unambiguous.
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte(":"))
		_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hash.Write([]byte(":"))
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte("\n"))
	}

	write("cause", string(in.Candidate.Cause))
	write("explicit_expected", strconv.FormatBool(in.Evidence.ExplicitCompletionExpected))
	write("explicit_observed", strconv.FormatBool(in.Evidence.ExplicitCompletion))
	write("objective", normalize(in.Evidence.Objective))
	write("candidate", normalize(in.Evidence.CandidateText))
	write("recent", normalize(in.Evidence.RecentText))

	count := min(int(in.Evidence.ActionCount), len(in.Evidence.Actions))
	write("action_count", strconv.Itoa(count))
	for i := range count {
		action := in.Evidence.Actions[i]
		write("action_kind", string(action.Kind))
		write("action_status", string(action.Status))
		write("action_name", normalize(action.Name))
	}
	return FingerprintPrefix + hex.EncodeToString(hash.Sum(nil))
}

// Observe records one evidence observation and returns the next state.
//
// The first stable evidence establishes the baseline with a consecutive
// no-progress count of zero. An equal fingerprint increments the bounded
// consecutive count; a changed fingerprint resets only that count.
//
// Observation never increments Reprompts, because Reprompts counts emitted
// intents rather than observations or platform admissions. A terminal state is
// absorbing and is returned unchanged with all four fields preserved.
//
// Invalid prior state is rejected rather than reset or clamped, and the
// consecutive count saturates at its absolute bound instead of overflowing.
func Observe(prior State, fingerprint string) (State, error) {
	if err := prior.Validate(); err != nil {
		return prior, err
	}
	if !validFingerprint(fingerprint) || fingerprint == "" {
		return prior, ErrInvalidState
	}
	if prior.Terminal {
		return prior, nil
	}

	next := prior
	changed := prior.LastFingerprint != fingerprint
	next.LastFingerprint = fingerprint
	if changed {
		next.ConsecutiveNoProgress = 0
		return next, nil
	}
	if next.ConsecutiveNoProgress < MaxConsecutiveNoProgress {
		next.ConsecutiveNoProgress++
	}
	return next, nil
}

// Advance returns the proposed next state for one successfully validated
// continuation intent, incrementing Reprompts exactly once.
//
// The prior state is returned unchanged on every error path, so a caller can
// build and validate its intent against the proposed state and only then
// commit it. Encode or build failures therefore never consume a slot, and
// platform admission is neither retried nor counted as another emitted intent.
// Progress never resets this immutable total.
//
// An emitted intent implies an observed baseline, so an unobserved prior state
// cannot yield a valid proposal. The proposed state is checked against the
// same central invariants before success is reported, so this helper never
// returns a state that its own Validate or Encode would reject. Counters are
// never reset and no fingerprint is invented to make a proposal valid.
func Advance(prior State, limits Limits) (State, error) {
	if err := prior.Validate(); err != nil {
		return prior, err
	}
	if err := limits.Validate(); err != nil {
		return prior, err
	}
	if prior.Terminal || prior.Reprompts >= limits.MaxReprompts {
		return prior, ErrRepromptsExhausted
	}
	next := prior
	next.Reprompts++
	if err := next.Validate(); err != nil {
		return prior, err
	}
	return next, nil
}

// Remaining intersects the configured total budget with platform continuation
// capacity, using the correct units for each.
//
// The configured cap counts Reprompts. The platform snapshot cap counts
// candidate Continuation.Attempt values, so it is compared against the current
// attempt, never against Reprompts. Only a positive snapshot cap constrains
// this feature; a zero cap imposes no feature-side default because core owns
// its own resolution. New progress never replenishes the configured total.
func Remaining(state State, limits Limits, platform Platform) (Eligibility, error) {
	if err := state.Validate(); err != nil {
		return Eligibility{}, err
	}
	if err := limits.Validate(); err != nil {
		return Eligibility{}, err
	}
	if err := platform.Validate(); err != nil {
		return Eligibility{}, err
	}
	if state.Terminal {
		return Eligibility{}, nil
	}
	if state.ConsecutiveNoProgress >= limits.NoProgressLimit {
		return Eligibility{}, nil
	}
	if state.Reprompts >= limits.MaxReprompts {
		return Eligibility{}, nil
	}
	remaining := limits.MaxReprompts - state.Reprompts
	if platform.Cap > 0 {
		if platform.Attempt >= platform.Cap {
			return Eligibility{}, nil
		}
		remaining = min(remaining, platform.Cap-platform.Attempt)
	}
	if remaining <= 0 {
		return Eligibility{}, nil
	}
	return Eligibility{Allowed: true, Remaining: remaining}, nil
}
