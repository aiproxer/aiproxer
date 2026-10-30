package extensions

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// emptyCompletionGates is a shared length-0 slice. Returning a fresh []completion.Gate{} on every
// empty [CompletionGatesFromContext] result would allocate on each streaming recv turn when no
// gates are configured; tests still require a non-nil empty slice (see facade_contract_test).
var emptyCompletionGates = []completion.Gate{}

// CompletionGatesView is the narrow extension seam for completion-gate discovery (hexagonal task 5.1).
// Callers that only need gates should depend on this interface rather than the full snapshot.
type CompletionGatesView interface {
	CompletionGates() []completion.Gate
}

// RequestRuntimeSnapshot implements [CompletionGatesView] via [RequestRuntimeSnapshot.CompletionGates].

var _ CompletionGatesView = (*RequestRuntimeSnapshot)(nil)

// CompletionGatesFromContext returns completion gates from the request snapshot on ctx when present,
// otherwise from fallback. Semantics match the former [runtime.retryRecvStream.completionGatesFromContext]
// resolution order: context snapshot first, then executor snapshot.
// An empty result is always a non-nil slice so callers that iterate or serialize never see nil.
func CompletionGatesFromContext(ctx context.Context, fallback CompletionGatesView) []completion.Gate {
	var gates []completion.Gate
	if s := RequestRuntimeSnapshotFromContext(ctx); s != nil {
		gates = s.CompletionGates()
	} else if fallback != nil {
		gates = fallback.CompletionGates()
	}
	if gates == nil {
		return emptyCompletionGates
	}
	return gates
}

// TrafficPortBundle returns the frozen traffic emission triple for this snapshot (hexagonal task 5.1).
// Prefer this method over ad-hoc field access when only traffic observation ports are needed.
func (s *RequestRuntimeSnapshot) TrafficPortBundle() traffic.PortBundle {
	if s == nil {
		return traffic.PortBundle{}
	}
	return traffic.PortBundle{
		Raw: s.RawCapture(),
		Obs: s.TrafficObserver(),
		Red: s.TrafficRedactors(),
	}
}
