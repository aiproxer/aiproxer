// Package resultmerge consumes one bounded background result and applies it to
// the authoritative parent continuity branch. It owns no branch store or
// scheduler; both are deliberately consumed through the small interfaces
// below.
package resultmerge

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

const (
	DefaultMaxCapsuleBytes  = 1 << 20
	DefaultMaxCapsuleTokens = 2_500
)

var (
	ErrInvalidJob         = errors.New("resultmerge: invalid pending job")
	ErrInvalidParentState = errors.New("resultmerge: invalid parent state")
	ErrInvalidResult      = errors.New("resultmerge: invalid collected result")
	ErrAwaitTimeout       = errors.New("resultmerge: result await timed out")
	ErrStaleResult        = errors.New("resultmerge: stale result")
	ErrRejected           = errors.New("resultmerge: result rejected")
)

// BackgroundClient is intentionally smaller than auxiliary.BackgroundClient:
// late-result consumption cannot submit work and therefore cannot accidentally
// create a new billable job.
type BackgroundClient interface {
	Await(context.Context, auxiliary.JobID) (aipapi.Collected, error)
	Forget(auxiliary.JobID)
}

// ParentCoordinator is an adapter seam for the process-owned coordinator. The
// branch binding is the only branch identity visible here; a child A-leg is
// never a lookup key. CommitCapsuleForJob must perform its own job-bound CAS.
type ParentCoordinator interface {
	ValidatePendingJob(context.Context, string, auxiliary.JobID) (ParentState, error)
	CommitCapsuleForJob(context.Context, string, auxiliary.JobID, string, uint64, []byte, [32]byte, string) (ParentState, error)
}

// DeltaDecoder is implemented by the strict extractor-result parser. A
// successful return promises that the JSON/schema and semantic bounds have
// already been checked; Service still rechecks branch and revision bindings.
type DeltaDecoder interface {
	Decode(aipapi.Collected, DecodeInput) (capsule.Delta, error)
}

// DecodeInput is built only after the parent capsule has been verified. It is
// the complete authority context required by a strict result parser.
type DecodeInput struct {
	Previous            capsule.Envelope
	ExpectedBranch      string
	SourceHighWatermark string
}

// Job identifies a pending result owned by one authoritative parent branch.
type Job struct {
	ID                  auxiliary.JobID
	ParentBranchBinding string
}

// ParentState is the opaque state needed by this feature-owned merger. The
// coordinator returns defensive copies, so this package never mutates a store
// snapshot in place.
type ParentState struct {
	BranchBinding            string
	Revision                 uint64
	CapsuleJSON              []byte
	CapsuleDigest            [32]byte
	SourceHighWatermark      string
	PendingJobID             auxiliary.JobID
	PendingJobTargetRevision uint64
	PendingJobBranchBinding  string
}

func (s ParentState) clone() ParentState {
	s.CapsuleJSON = append([]byte(nil), s.CapsuleJSON...)
	return s
}

// Config bounds the next serialized capsule. Both dimensions are enforced
// independently by capsule.PruneWithLimits before parent commit.
type Config struct {
	MaxCapsuleBytes  int
	MaxCapsuleTokens int
}

// Status describes the fail-open result of one consumption attempt.
type Status string

const (
	StatusMerged   Status = "merged"
	StatusPending  Status = "pending"
	StatusRejected Status = "rejected"
)

type Outcome struct {
	Status Status
	State  ParentState
}
