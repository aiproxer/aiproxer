package proxymemory

import (
	"context"
	"errors"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// Failure evidence vocabulary. Every value is a closed, content-free token: no
// project path, principal, prompt, model output or provider error text can
// become a stored kind or a metric label.
const (
	// FailureKindInvalidResult is a completed auxiliary collection whose
	// result the phase parser or validator rejected. It is the memory
	// processing outcome, separate from the provider-terminal outcome.
	FailureKindInvalidResult = "invalid_result"

	// FailurePhaseExtraction is the phase 1 background extraction.
	FailurePhaseExtraction = "extraction"
	// FailurePhaseConsolidation is the phase 2 background consolidation.
	FailurePhaseConsolidation = "consolidation"
)

// FailureOutcome is the result of one durable failure-evidence transaction.
// The zero value is not a success.
type FailureOutcome string

const (
	// FailureRecorded means the fact was inserted and the same live claim was
	// released and rescheduled in the same transaction.
	FailureRecorded FailureOutcome = "recorded"
	// FailureRecordedStale means the immutable fact was inserted but the
	// caller's claim was no longer the live claim, so no successor was
	// mutated.
	FailureRecordedStale FailureOutcome = "recorded_stale"
	// FailureDuplicate means an identical fact already exists. Nothing was
	// written and no claim was mutated.
	FailureDuplicate FailureOutcome = "duplicate"
	// FailureErased means the owner's erasure epoch advanced after the claim
	// was captured. Nothing was written.
	FailureErased FailureOutcome = "erased"
	// FailureExpired means the receipt's frozen retention horizon passed.
	// Nothing was written.
	FailureExpired FailureOutcome = "expired"
)

// ErrFailureConflict means the operation key already names a different
// immutable failure fact. It is never overwritten and no claim is released.
var ErrFailureConflict = errors.New("proxy-memory: failure evidence conflicts with an existing fact")

// ErrFailureInvalid means the evidence cannot be trusted as a linked fact
// (missing operation identity, unknown phase or kind, invalid timestamps).
var ErrFailureInvalid = errors.New("proxy-memory: invalid failure evidence")

// CollectionReceipt is the feature-owned, immutable view of the host billing
// operation one completed auxiliary collection belonged to. It is adapted from
// the core's internal receipt by the composition root; the feature never
// imports core billing or runtime packages. A zero receipt is explicitly
// unlinked, never a fabricated operation reference.
type CollectionReceipt struct {
	StoreID       string
	BillingCallID string
	CompletedAt   time.Time
}

// Linked reports whether the receipt names a complete host operation reference
// and a completion time.
func (r CollectionReceipt) Linked() bool {
	return r.StoreID != "" && r.BillingCallID != "" && !r.CompletedAt.IsZero()
}

// ReceiptCollector is the optional internal capability of the background
// client the composition root binds to the worker. It returns the collected
// result and the host receipt from one job snapshot, so a result can never be
// paired with another job's receipt.
type ReceiptCollector interface {
	AwaitCollection(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, CollectionReceipt, error)
}

// FailureEvidence is one immutable, owner-scoped memory-processing failure fact
// linked to the actual host billing operation. It carries no account, money,
// provider or content field.
type FailureEvidence struct {
	Owner         Owner
	Phase         string
	StoreID       string
	BillingCallID string
	// ClaimID is the claimed work identity: the phase 1 A-leg, or the phase 2
	// owner's bank project key. ClaimRevision is the phase 1 session revision;
	// ClaimSeq is the phase 2 dirty sequence. Together with the lease binding
	// they identify exactly one claim.
	ClaimID       string
	ClaimRevision int64
	ClaimSeq      int64
	LeaseOwner    string
	LeaseUntilMS  int64
	ErasureEpoch  int64
	// RetryAtMS is the phase's conditional backoff target. It is used only by
	// the claim-mutation half of the transaction and is not stored as part of
	// the immutable fact.
	RetryAtMS     int64
	Kind          string
	CompletedAtMS int64
	ExpiresAtMS   int64
	RecordedAtMS  int64
}

// Validate checks the closed vocabulary and the immutable operation linkage.
// It performs no database work.
func (f FailureEvidence) Validate() error {
	switch {
	case f.StoreID == "" || f.BillingCallID == "":
		return ErrFailureInvalid
	case f.Phase != FailurePhaseExtraction && f.Phase != FailurePhaseConsolidation:
		return ErrFailureInvalid
	case f.Kind != FailureKindInvalidResult:
		return ErrFailureInvalid
	case f.Owner.TenantID == "" || f.Owner.PrincipalID == "" || f.Owner.ProjectKey == "":
		return ErrFailureInvalid
	case f.ClaimID == "":
		return ErrFailureInvalid
	case f.LeaseUntilMS <= 0:
		return ErrFailureInvalid
	case f.RetryAtMS <= 0:
		return ErrFailureInvalid
	case f.CompletedAtMS <= 0 || f.ExpiresAtMS <= f.CompletedAtMS || f.RecordedAtMS <= 0:
		return ErrFailureInvalid
	}
	return nil
}
