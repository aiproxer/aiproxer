package aipapi

import (
	"errors"
	"fmt"
	"strings"
)

// ValidationError reports a canonical request invariant violation.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e *ValidationError) Unwrap() error { return ErrInvalidCall }

// ErrInvalidCall is the shared root for call validation failures.
var ErrInvalidCall = errors.New("aipapi: invalid canonical call")

// ErrCollectLimitExceeded is returned when stream aggregation in Collect would
// exceed the configured CollectLimits.
var ErrCollectLimitExceeded = errors.New("aipapi: collect limit exceeded")

// ErrNilEventStream is returned by Collect, CollectUnbounded, and CollectWithLimits
// when the EventStream argument is nil.
var ErrNilEventStream = errors.New("aipapi: nil EventStream")

// ErrNilContext is returned when a nil context.Context is passed to Recv, Collect, or
// other APIs that require a non-nil Context (same rule as context package: never pass nil;
// use context.Background if no cancellation/deadline is needed).
var ErrNilContext = errors.New("aipapi: nil Context")

// ErrNilFixedEventStream is returned by (*FixedEventStream).Recv when the receiver is nil.
var ErrNilFixedEventStream = errors.New("aipapi: nil FixedEventStream")

// ErrStreamTerminal is the stable root for terminal upstream stream error events (EventError).
var ErrStreamTerminal = errors.New("aipapi: stream error")

// StreamError carries provider-specific codes/messages for a terminal stream failure without
// putting variable text in Error() (use Code and Message in structured logs).
type StreamError struct {
	Code    string
	Message string
}

func (e *StreamError) Error() string {
	return ErrStreamTerminal.Error()
}

func (e *StreamError) Unwrap() error {
	return ErrStreamTerminal
}

// NewStreamError returns a *StreamError for propagation from adapters and encoders.
func NewStreamError(code, message string) error {
	return &StreamError{Code: code, Message: message}
}

// ErrMaxRouteAttempts is returned when routing.max_attempts would be exceeded by another B-leg.
var ErrMaxRouteAttempts = errors.New("aipapi: routing max_attempts exhausted")

// ErrTTFTTimeout is returned when a route-level time-to-first-token budget expires before output commits.
var ErrTTFTTimeout = errors.New("aipapi: time to first token timeout")

// ErrUnresolvedModelOnlySelector is returned when a model-only route selector cannot be
// resolved because no default backend was configured.
var ErrUnresolvedModelOnlySelector = errors.New("aipapi: model-only route selector without default backend")

// ErrAllCandidatesContextLimitExceeded is returned when every evaluated route candidate was
// excluded before upstream open because known context limits are below the conservative
// request-size estimate (pre-output routing only).
var ErrAllCandidatesContextLimitExceeded = errors.New("aipapi: all route candidates excluded by context limit")

// ErrAllCandidatesUnrepresentableReplay is returned when every evaluated route candidate was
// excluded by attempt transforms with the canonical unrepresentable_replay reason.
var ErrAllCandidatesUnrepresentableReplay = errors.New("aipapi: all route candidates excluded: unrepresentable_replay")

// ErrAllCandidatesExcluded is returned when every evaluated route candidate was excluded by
// attempt transforms with a non-canonical (but sanitized) reason.
var ErrAllCandidatesExcluded = errors.New("aipapi: all route candidates excluded: candidate_excluded")

// IsAllCandidatesContextLimitExceeded reports whether err is or wraps [ErrAllCandidatesContextLimitExceeded].
func IsAllCandidatesContextLimitExceeded(err error) bool {
	return errors.Is(err, ErrAllCandidatesContextLimitExceeded)
}

// RejectError is returned when capability negotiation deterministically rejects
// a request before any upstream work begins.
type RejectError struct {
	Missing []Capability
	Reason  string
}

func (e *RejectError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	if len(e.Missing) == 0 {
		return "capability negotiation rejected request"
	}
	names := make([]string, 0, len(e.Missing))
	for _, c := range e.Missing {
		names = append(names, string(c))
	}
	return fmt.Sprintf("missing required capabilities: %s", strings.Join(names, ", "))
}

func (e *RejectError) Unwrap() error { return ErrCapabilityReject }

// ErrCapabilityReject is the stable root error for hard capability rejects.
var ErrCapabilityReject = errors.New("aipapi: capability reject")

// IsReject reports whether err is or wraps a RejectError.
func IsReject(err error) bool {
	var rej *RejectError
	return errors.As(err, &rej)
}

// ErrHookMutation is the stable root for hook-produced canonical mutations that fail validation.
var ErrHookMutation = errors.New("aipapi: hook mutation invalid")

// HookMutationError reports a part or event rewrite that violated canonical invariants.
type HookMutationError struct {
	HookID  string
	Details string
	Cause   error
}

func (e *HookMutationError) Error() string {
	if e.HookID != "" && e.Details != "" {
		return "hook " + e.HookID + ": " + e.Details
	}
	if e.HookID != "" {
		return "hook " + e.HookID + ": invalid mutation"
	}
	if e.Details != "" {
		return e.Details
	}
	return "invalid hook mutation"
}

func (e *HookMutationError) Unwrap() error { return e.Cause }

// IsHookMutation reports whether err is or wraps a HookMutationError or ErrHookMutation.
func IsHookMutation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrHookMutation) {
		return true
	}
	var hm *HookMutationError
	return errors.As(err, &hm)
}
