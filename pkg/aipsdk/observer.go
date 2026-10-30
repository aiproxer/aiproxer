package aipsdk

import "context"

// RouteObserver receives lightweight routing/orchestration notifications (hexagonal task 5.2).
// traceID correlates with diagnostics and traffic metadata; decision and detail are opaque,
// human-oriented labels (for example route planner outcomes). Values must not embed transport
// requests, provider payloads, or executor-private structs—only redacted or safe string summaries.
//
// Implementations must be non-blocking or very fast; the executor invokes them on the hot path
// only when non-nil.
type RouteObserver interface {
	ObserveRouteDecision(ctx context.Context, traceID, decision, detail string)
}
