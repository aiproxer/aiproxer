package usageevidence

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const CurrentRecordSchemaVersion = 1

// EvidenceFormatVersionV2 identifies the additive observation envelope carried
// by a call-leg record. The durable record schema remains at v1: old rows keep
// their exact payload shape and continue to use FinalBillingEvidence as a
// compatibility projection.
const EvidenceFormatVersionV2 = 2

// EvidenceProjectionV1 labels the legacy scalar fields retained on a record
// while the source-separated V2 observations remain the economic evidence.
const EvidenceProjectionV1 = "v1_compatibility_projection"

const (
	// MaxCallLegEvidenceObservations bounds terminal evidence retained for one
	// B-leg. A provider may emit many frames, but terminal accounting must stay
	// bounded and retain a deterministic incomplete/conflict signal when the
	// bound is reached.
	MaxCallLegEvidenceObservations = 1024
	MaxCallLegEvidenceRefs         = 1024
	MaxCallLegEvidenceConflicts    = 128
)

var (
	// ErrInvalidRecord aliases the canonical public maintenance/usage-evidence
	// sentinel (pkg/aipsdk/metering.ErrInvalidMaintenance); the message text is
	// the existing "billing: invalid usage record".
	ErrInvalidRecord     = sdkmetering.ErrInvalidMaintenance
	ErrReplayConflict    = errors.New("billing: replay fingerprint conflict")
	ErrReplayKeyMismatch = errors.New("billing: durable key mismatch")
)

type VersionRef struct {
	ID          string
	Version     string
	EffectiveAt time.Time
	FetchedAt   time.Time
}

// Quantity, MoneyEvidence, EvidenceSource, EvidenceAuthority, and
// FinalBillingEvidence alias the canonical public maintenance vocabulary
// (pkg/aipsdk/metering), so the runtime maintenance seam and the usage-evidence
// records share one definition with zero conversions. Field names, order, and
// absent-vs-explicit-zero semantics are preserved exactly; the durable store
// marshals the values with field names and the persisted JSON is unchanged.
type (
	Quantity       = sdkmetering.MaintenanceQuantity
	MoneyEvidence  = sdkmetering.MaintenanceMoney
	EvidenceSource = sdkmetering.MaintenanceSource
)

const (
	EvidenceSourceUnknown          = sdkmetering.MaintenanceSourceUnknown
	EvidenceSourceProviderReported = sdkmetering.MaintenanceSourceProviderReported
	EvidenceSourceProviderCountAPI = sdkmetering.MaintenanceSourceProviderCountAPI
	EvidenceSourceLocalTokenizer   = sdkmetering.MaintenanceSourceLocalTokenizer
	EvidenceSourceLocalEstimator   = sdkmetering.MaintenanceSourceLocalEstimator
	EvidenceSourceUnavailable      = sdkmetering.MaintenanceSourceUnavailable
)

type EvidenceAuthority = sdkmetering.MaintenanceAuthority

const (
	EvidenceAuthorityUnknown       = sdkmetering.MaintenanceAuthorityUnknown
	EvidenceAuthorityAuthoritative = sdkmetering.MaintenanceAuthorityAuthoritative
	EvidenceAuthorityDelegated     = sdkmetering.MaintenanceAuthorityDelegated
	EvidenceAuthorityEstimated     = sdkmetering.MaintenanceAuthorityEstimated
	EvidenceAuthorityUnavailable   = sdkmetering.MaintenanceAuthorityUnavailable
)

type FinalBillingEvidence = sdkmetering.MaintenanceEvidence

// EvidenceConflict records a source-event identity that was delivered with a
// changed payload. Exact identity/revision replays are collapsed; a changed
// payload is retained as a visible conflict instead of being silently merged.
// The conflict is diagnostic evidence and never becomes a charge by itself.
type EvidenceConflict struct {
	Identity               string                   `json:"identity"`
	ExistingHash           string                   `json:"existing_hash"`
	IncomingHash           string                   `json:"incoming_hash"`
	ExistingCoverage       EconomicEvidenceCoverage `json:"existing_coverage,omitempty"`
	ExistingCoverageReason string                   `json:"existing_coverage_reason,omitempty"`
	IncomingCoverage       EconomicEvidenceCoverage `json:"incoming_coverage,omitempty"`
	IncomingCoverageReason string                   `json:"incoming_coverage_reason,omitempty"`
}

func (c EvidenceConflict) valid() bool {
	if strings.TrimSpace(c.Identity) == "" || strings.TrimSpace(c.ExistingHash) == "" || strings.TrimSpace(c.IncomingHash) == "" {
		return false
	}
	if c.ExistingCoverage != "" || c.ExistingCoverageReason != "" {
		if err := validateEconomicEvidenceCoverage(c.ExistingCoverage, c.ExistingCoverageReason); err != nil {
			return false
		}
	}
	if c.IncomingCoverage != "" || c.IncomingCoverageReason != "" {
		if err := validateEconomicEvidenceCoverage(c.IncomingCoverage, c.IncomingCoverageReason); err != nil {
			return false
		}
	}
	return true
}

func (c EvidenceConflict) HasCoverageMetadata() bool {
	return c.ExistingCoverage != "" || c.ExistingCoverageReason != "" || c.IncomingCoverage != "" || c.IncomingCoverageReason != ""
}

type TurnOutcome string

const (
	TurnOutcomeCompleted TurnOutcome = "completed"
	TurnOutcomeFailed    TurnOutcome = "failed"
	TurnOutcomeCanceled  TurnOutcome = "canceled"
	TurnOutcomeUnknown   TurnOutcome = "unknown"
)

type LegOutcome string

const (
	LegOutcomeWinner       LegOutcome = "winner"
	LegOutcomeLoser        LegOutcome = "loser"
	LegOutcomeFailed       LegOutcome = "failed"
	LegOutcomeCanceled     LegOutcome = "canceled"
	LegOutcomeSwallowed    LegOutcome = "swallowed_failure"
	LegOutcomeRejected     LegOutcome = "rejected"
	LegOutcomeNeverStarted LegOutcome = "never_started"
	LegOutcomeUnknown      LegOutcome = "unknown"
)

type SurfacedState string

const (
	SurfacedYes     SurfacedState = "yes"
	SurfacedNo      SurfacedState = "no"
	SurfacedUnknown SurfacedState = "unknown"
)

func validateEvidence(e FinalBillingEvidence) error {
	for name, q := range map[string]Quantity{
		"input": e.InputTokens, "output": e.OutputTokens, "cache_read": e.CacheReadTokens,
		"cache_write": e.CacheWriteTokens, "reasoning": e.ReasoningTokens, "total": e.TotalTokens,
	} {
		if q.Value < 0 || (!q.Present && q.Value != 0) {
			return fmt.Errorf("%w: invalid %s quantity presence/value", ErrInvalidRecord, name)
		}
	}
	if e.Cost.NanoUnits < 0 || (!e.Cost.Present && e.Cost.NanoUnits != 0) {
		return fmt.Errorf("%w: invalid cost presence/value", ErrInvalidRecord)
	}
	if e.Cost.Present && strings.TrimSpace(e.Cost.Currency) == "" {
		return fmt.Errorf("%w: present cost requires currency", ErrInvalidRecord)
	}
	return nil
}

func validTurnOutcome(v TurnOutcome) bool {
	return v == TurnOutcomeCompleted || v == TurnOutcomeFailed || v == TurnOutcomeCanceled || v == TurnOutcomeUnknown
}

func validLegOutcome(v LegOutcome) bool {
	return v == LegOutcomeWinner || v == LegOutcomeLoser || v == LegOutcomeFailed || v == LegOutcomeCanceled || v == LegOutcomeSwallowed || v == LegOutcomeRejected || v == LegOutcomeNeverStarted || v == LegOutcomeUnknown
}

func validSurfacedState(v SurfacedState) bool {
	return v == SurfacedYes || v == SurfacedNo || v == SurfacedUnknown
}

func writeQuantity(c *canonicalWriter, q Quantity) {
	c.i64(q.Value)
	c.boolean(q.Present)
}

func writeVersionRef(c *canonicalWriter, r VersionRef) {
	c.string(r.ID)
	c.string(r.Version)
	c.time(r.EffectiveAt)
	c.time(r.FetchedAt)
}

func writeWorkloadIdentity(c *canonicalWriter, w WorkloadIdentity) {
	if w.IsZero() {
		return // preserve legacy fingerprints for unclassified rows
	}
	c.string("workload:v1")
	c.string(string(w.Class))
	c.string(string(w.Role))
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

type canonicalWriter struct{ data []byte }

func (w *canonicalWriter) bytes() []byte { return w.data }
func (w *canonicalWriter) string(v string) {
	w.u64(uint64(len(v)))
	w.data = append(w.data, v...)
}

func (w *canonicalWriter) u64(v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	w.data = append(w.data, buf[:]...)
}
func (w *canonicalWriter) i64(v int64) { w.u64(uint64(v)) }
func (w *canonicalWriter) boolean(v bool) {
	if v {
		w.data = append(w.data, 1)
		return
	}
	w.data = append(w.data, 0)
}

func (w *canonicalWriter) time(v time.Time) {
	if v.IsZero() {
		w.i64(0)
		return
	}
	w.i64(v.UTC().UnixNano())
}
