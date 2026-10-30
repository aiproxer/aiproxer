// Package compaction defines the typed, fail-open observer seam for
// proxy-derived coding-agent session compaction lifecycle observations.
//
// A compaction.Observer receives metadata-only lifecycle events (started and
// completed) derived by the core detector from the canonical request/response
// flow. Events never carry prompt, response, tool-result, raw-body, or
// encrypted-compaction content, and observers are strictly non-mutating:
// OnCompaction returns no replacement or decision and can never affect
// routing, retries, completion gates, tool policy, accounting, or client
// framing.
package compaction

import (
	"context"
	"time"
)

// Phase is the compaction lifecycle phase carried by an Event.
type Phase string

const (
	// PhaseStarted marks the beginning of a detected compaction transaction.
	// It is emitted only after an upstream B-leg actually opened.
	PhaseStarted Phase = "started"
	// PhaseCompleted marks the end of a compaction transaction. It is emitted
	// only when evidence proves or strongly infers the compaction installed.
	PhaseCompleted Phase = "completed"
)

// Evidence is the epistemic class of the observation.
type Evidence string

const (
	// EvidenceProtocolStrict means canonical protocol semantics directly prove
	// compaction (explicit compact operation or released compaction item).
	EvidenceProtocolStrict Evidence = "protocol_strict"
	// EvidenceSignatureStrict means a versioned, deterministic conjunction of
	// implementation markers identified a compaction utility call or an
	// installed summary/post marker.
	EvidenceSignatureStrict Evidence = "signature_strict"
	// EvidenceHistoryHeuristic means a conservative same-A-leg history rewrite
	// strongly inferred a local-only compaction when no strict signal existed.
	EvidenceHistoryHeuristic Evidence = "history_heuristic"
)

// Event is a metadata-only compaction lifecycle observation. It carries
// correlation and evidence fields only; it never exposes canonical request or
// response content and carries no numeric confidence score.
type Event struct {
	Phase         Phase
	Evidence      Evidence
	RuleID        string
	TransactionID string
	TraceID       string
	ALegID        string
	BLegID        string
	AttemptSeq    int
	SessionID     string
	OccurredAt    time.Time
}

// Observer subscribes to typed compaction lifecycle observations. Implementors
// must be non-mutating: the callback has no replacement/decision result.
// Errors and panics raised by an observer are isolated by the dispatcher and
// never fail or alter the request.
type Observer interface {
	OnCompaction(context.Context, Event) error
}

// Dispatch delivers events to observers in order, isolating per-observer
// errors and panics so one failing listener can never suppress later
// listeners or fail the request. It is synchronous, ordered, and performs no
// background work. A nil or empty observer set makes Dispatch a no-op.
func Dispatch(ctx context.Context, observers []Observer, events []Event) {
	if len(observers) == 0 || len(events) == 0 {
		return
	}
	for _, ev := range events {
		for _, ob := range observers {
			if ob == nil {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				_ = ob.OnCompaction(ctx, ev)
			}()
		}
	}
}
