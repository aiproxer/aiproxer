package aipapi

import "time"

// AttemptOutcome classifies how a single B-leg attempt ended for lineage and diagnostics.
type AttemptOutcome string

const (
	AttemptSuccess          AttemptOutcome = "success"
	AttemptSwallowedFailure AttemptOutcome = "swallowed_failure"
	AttemptSurfacedFailure  AttemptOutcome = "surfaced_failure"
	AttemptCancelled        AttemptOutcome = "cancelled"
)

// AttemptRecord is one row of B2BUA attempt lineage (protocol-neutral).
type AttemptRecord struct {
	BLegID         string
	ALegID         string
	Seq            int
	BackendID      string
	EffectiveModel string
	StartedAt      time.Time
	FinishedAt     time.Time
	Outcome        AttemptOutcome
	Reason         string
}
