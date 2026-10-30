package metering

import "context"

// ObservationSink is the compatibility single-observation durable-capture
// seam. Implementations own storage and transaction behavior; callers provide
// one complete immutable Observation and never pass provider-shaped or
// raw-content payloads. An Append error has unknown commit status: callers
// must not retry it unless the sink also implements AtomicObservationSink.
type ObservationSink interface {
	Append(ctx context.Context, observation Observation) error
}

// AtomicObservationSink is the retry-safe batch capability for durable
// checkpoint capture. AppendObservations must atomically process the complete
// batch: it must not expose a successful prefix when returning an error. Exact
// replays of a previously accepted observation must be idempotent no-ops, and
// a changed payload for an existing source identity must reject the complete
// batch without exposing any new observation. These guarantees let callers
// retry the same batch after an error, including an ambiguous commit result,
// without duplicating or losing observations. An empty batch performs no
// durable operation.
//
// Implementations also satisfy ObservationSink for compatibility. Runtime
// economic checkpoint flushing uses AppendObservations exclusively after
// verifying this capability; a plain ObservationSink is not sufficient.
type AtomicObservationSink interface {
	ObservationSink
	AppendObservations(ctx context.Context, observations []Observation) error
}

// ObservationSource is the optional host-only sideband drain implemented by
// backend streams. A source returns canonical observations, never client
// events; callers own replay/conflict handling at the B-leg terminal boundary.
type ObservationSource interface {
	DrainEconomicObservations() []Observation
}
