package conversationview

import (
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
)

// Observer is an optional narrow callback for bounded, content-free diagnostics.
// Implementations must not log or label by OverlayID, ALegID, digest, or plaintext.
// All label values are bounded enums only: reason_class, placement, operation, policy, stage.
// Stage values are bounded: "early", "final", "sdk_resolve".
// Operation values are CacheDiscontinuityKind (create, replace, move, deactivate).
// Placement values are PlacementKind (stable_prefix, after_message).
// Policy values are AnchorMissingPolicy (stable_prefix_fallback, fail_closed).
// Nil observer is a no-op. All callbacks must be panic-isolated by callers via SafeObserver.
type Observer interface {
	conversationprojection.Observer
	OnSteeringMutation(kind CacheDiscontinuityKind, placement PlacementKind)
}

// Stage constants for projection failure/success diagnostics (bounded).
const (
	StageEarly      = conversationprojection.StageEarly
	StageFinal      = conversationprojection.StageFinal
	StageSDKResolve = conversationprojection.StageSDKResolve
)

// NopObserver is a no-op Observer.
type NopObserver struct{}

func (NopObserver) OnProjection(string, ProjectionSummary)                   {}
func (NopObserver) OnProjectionFailure(string)                               {}
func (NopObserver) OnAnchorFallback(string, AnchorMissingPolicy)             {}
func (NopObserver) OnAnchorFailure(AnchorMissingPolicy)                      {}
func (NopObserver) OnSteeringMutation(CacheDiscontinuityKind, PlacementKind) {}

// SafeObserver returns a panic-isolated wrapper. Nil returns NopObserver.
// All Observer callbacks are recovered so no observer panic can affect request or mutation.
func SafeObserver(o Observer) Observer {
	if o == nil {
		return NopObserver{}
	}
	// If already safe, avoid double wrapping.
	if _, ok := o.(safeObserverWrapper); ok {
		return o
	}
	if _, ok := o.(NopObserver); ok {
		return o
	}
	return safeObserverWrapper{inner: o}
}

type safeObserverWrapper struct {
	inner Observer
}

func (s safeObserverWrapper) OnProjection(stage string, summary ProjectionSummary) {
	defer func() { _ = recover() }()
	s.inner.OnProjection(stage, summary)
}

func (s safeObserverWrapper) OnProjectionFailure(stage string) {
	defer func() { _ = recover() }()
	s.inner.OnProjectionFailure(stage)
}

func (s safeObserverWrapper) OnAnchorFallback(stage string, policy AnchorMissingPolicy) {
	defer func() { _ = recover() }()
	s.inner.OnAnchorFallback(stage, policy)
}

func (s safeObserverWrapper) OnAnchorFailure(policy AnchorMissingPolicy) {
	defer func() { _ = recover() }()
	s.inner.OnAnchorFailure(policy)
}

func (s safeObserverWrapper) OnSteeringMutation(kind CacheDiscontinuityKind, placement PlacementKind) {
	defer func() { _ = recover() }()
	s.inner.OnSteeringMutation(kind, placement)
}

// Bounded label validation helpers (ensure only bounded enums are emitted).
func isValidStage(s string) bool {
	switch s {
	case StageEarly, StageFinal, StageSDKResolve:
		return true
	default:
		return false
	}
}

func sanitizeStage(s string) string {
	if isValidStage(s) {
		return s
	}
	return "unknown"
}
