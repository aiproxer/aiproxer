package service

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// declareExclusiveInputUsage marks the usage events a stream publishes as
// declaring that the producer's input counter excludes the provider cache
// reads and cache writes.
//
// The relation is a property of the upstream agent, not of ACP: the ACP usage
// object carries the counters but does not define whether the input counter
// contains the cached and rewritten prompt subsets, and shipping agents
// disagree (agent-client-protocol#1860 records the Claude Code bridge
// reporting an input counter outside its cached subsets, the Copilot CLI
// reporting an inclusive one, and other agents reporting no usage at all).
// A producer therefore declares the relation it was measured with; the
// declaration travels with the counters so a frontend can count the prompt in
// its own semantics.
//
// The wrapper is applied only where the operator established the relation.
// A nil stream is returned unchanged so the caller's own nil-stream guard
// still sees it.
func declareExclusiveInputUsage(stream aipapi.ManagedEventStream) aipapi.ManagedEventStream {
	if stream == nil {
		return nil
	}
	return &exclusiveInputUsageStream{ManagedEventStream: stream}
}

// exclusiveInputUsageStream declares the ACP usage input relation before the
// events reach the host.
type exclusiveInputUsageStream struct {
	aipapi.ManagedEventStream
}

func (s *exclusiveInputUsageStream) Recv(ctx context.Context) (aipapi.Event, error) {
	event, err := s.ManagedEventStream.Recv(ctx)
	if event.Kind == aipapi.EventUsageDelta {
		// The exclusive shape excludes both cached subsets from the input
		// counter, so the two relations are declared independently.
		event.Accounting.InputTokensExcludeCacheRead = true
		event.Accounting.InputTokensExcludeCacheWrite = true
	}
	return event, err
}
