package resultmerge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

const DefaultCommitTimeout = time.Second

type Service struct {
	background BackgroundClient
	parent     ParentCoordinator
	decoder    DeltaDecoder
	maxBytes   int
	maxTokens  int
}

func New(background BackgroundClient, parent ParentCoordinator, decoder DeltaDecoder, cfg Config) (*Service, error) {
	if background == nil || parent == nil || decoder == nil {
		return nil, fmt.Errorf("%w: missing consumed capability", ErrInvalidJob)
	}
	if cfg.MaxCapsuleBytes <= 0 {
		cfg.MaxCapsuleBytes = DefaultMaxCapsuleBytes
	}
	if cfg.MaxCapsuleTokens <= 0 {
		cfg.MaxCapsuleTokens = DefaultMaxCapsuleTokens
	}
	return &Service{background: background, parent: parent, decoder: decoder, maxBytes: cfg.MaxCapsuleBytes, maxTokens: cfg.MaxCapsuleTokens}, nil
}

// Consume awaits and, if ready, atomically merges exactly one result. A
// timeout is intentionally returned as StatusPending without Forget: the
// parent branch may consume that raw result on a later eligible turn.
func (s *Service) Consume(ctx context.Context, job Job) (Outcome, error) {
	if s == nil || ctx == nil || strings.TrimSpace(string(job.ID)) == "" || strings.TrimSpace(job.ParentBranchBinding) == "" {
		return Outcome{Status: StatusRejected}, ErrInvalidJob
	}

	state, err := s.parent.ValidatePendingJob(ctx, job.ParentBranchBinding, job.ID)
	if err != nil {
		// Do not Forget here. No raw output was consumed and a wrong caller-side
		// branch key must not destroy the job owned by the real parent branch.
		return Outcome{Status: StatusRejected}, err
	}
	if err := validatePendingState(state, job); err != nil {
		return Outcome{Status: StatusRejected, State: state}, err
	}

	collected, err := s.background.Await(ctx, job.ID)
	if err != nil {
		if isAwaitTimeout(ctx, err) {
			return Outcome{Status: StatusPending, State: state}, fmt.Errorf("%w: %v", ErrAwaitTimeout, err)
		}
		// The scheduler has reached a terminal failure; no useful raw result
		// remains to retain. Forget is safe even if the registry already removed it.
		s.background.Forget(job.ID)
		return Outcome{Status: StatusRejected, State: state}, fmt.Errorf("%w: await: %v", ErrRejected, err)
	}
	// Every successful Await is terminal from this service's perspective. The
	// raw collected stream is never retained after validation/merge.
	defer s.background.Forget(job.ID)

	previous, err := validateParentCapsule(state, job.ParentBranchBinding)
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, err
	}
	delta, err := s.decoder.Decode(collected, DecodeInput{
		Previous: previous, ExpectedBranch: job.ParentBranchBinding,
		SourceHighWatermark: state.SourceHighWatermark,
	})
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, fmt.Errorf("%w: decode: %w", ErrInvalidResult, err)
	}
	if err := validateDelta(delta, previous, job.ParentBranchBinding); err != nil {
		return Outcome{Status: StatusRejected, State: state}, err
	}

	merged, err := capsule.Merge(previous, delta)
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, fmt.Errorf("%w: merge: %w", ErrInvalidResult, err)
	}
	merged, err = capsule.PruneWithLimits(merged, capsule.Limits{MaxBytes: s.maxBytes, MaxTokens: s.maxTokens})
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, fmt.Errorf("%w: bound capsule: %w", ErrInvalidResult, err)
	}
	serialized, err := capsule.Encode(merged)
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, fmt.Errorf("%w: encode capsule: %w", ErrInvalidResult, err)
	}
	digest, err := digestArray(merged.ContentDigest)
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, fmt.Errorf("%w: merged digest: %w", ErrInvalidResult, err)
	}
	highWatermark := state.SourceHighWatermark
	if strings.TrimSpace(delta.SourceHighWatermark) != "" {
		highWatermark = delta.SourceHighWatermark
	}
	// Await is bounded by the response barrier and may consume that entire
	// deadline. Commit is a separate, small state transition and must not
	// inherit an already-expired barrier context.
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), DefaultCommitTimeout)
	defer cancelCommit()
	committed, err := s.parent.CommitCapsuleForJob(commitCtx, job.ParentBranchBinding, job.ID, merged.BranchBinding, state.Revision, serialized, digest, highWatermark)
	if err != nil {
		return Outcome{Status: StatusRejected, State: state}, err
	}
	return Outcome{Status: StatusMerged, State: committed}, nil
}

func validatePendingState(state ParentState, job Job) error {
	if state.BranchBinding != job.ParentBranchBinding {
		return fmt.Errorf("%w: branch binding", ErrInvalidParentState)
	}
	if state.PendingJobID != job.ID || state.PendingJobBranchBinding != job.ParentBranchBinding {
		return fmt.Errorf("%w: pending job binding", ErrInvalidParentState)
	}
	if state.Revision == 0 || state.PendingJobTargetRevision == 0 {
		return fmt.Errorf("%w: missing revision", ErrInvalidParentState)
	}
	return nil
}

func validateParentCapsule(state ParentState, branchBinding string) (capsule.Envelope, error) {
	if len(state.CapsuleJSON) == 0 || state.CapsuleDigest == ([32]byte{}) {
		return capsule.Envelope{}, fmt.Errorf("%w: capsule or digest is absent", ErrInvalidParentState)
	}
	e, err := capsule.Verify(state.CapsuleJSON, branchBinding)
	if err != nil {
		return capsule.Envelope{}, fmt.Errorf("%w: capsule verify: %w", ErrInvalidParentState, err)
	}
	if e.Revision != state.Revision {
		return capsule.Envelope{}, fmt.Errorf("%w: capsule revision %d, state revision %d", ErrStaleResult, e.Revision, state.Revision)
	}
	if state.PendingJobTargetRevision != e.Revision {
		return capsule.Envelope{}, fmt.Errorf("%w: pending target revision %d, capsule revision %d", ErrStaleResult, state.PendingJobTargetRevision, e.Revision)
	}
	digest, err := digestArray(e.ContentDigest)
	if err != nil || digest != state.CapsuleDigest {
		return capsule.Envelope{}, fmt.Errorf("%w: stored digest", ErrInvalidParentState)
	}
	return e, nil
}

func validateDelta(value capsule.Delta, previous capsule.Envelope, branchBinding string) error {
	if value.SchemaVersion != capsule.SchemaVersion {
		return fmt.Errorf("%w: schema version", ErrInvalidResult)
	}
	if value.BranchBinding != branchBinding || value.BranchBinding != previous.BranchBinding {
		return fmt.Errorf("%w: branch binding", ErrInvalidResult)
	}
	if value.BaseRevision == 0 || value.BaseRevision != previous.Revision {
		return fmt.Errorf("%w: base revision", ErrStaleResult)
	}
	return nil
}

func digestArray(value string) ([32]byte, error) {
	var out [32]byte
	encoded := strings.TrimPrefix(value, "sha256:")
	if len(encoded) != sha256.Size*2 {
		return out, errors.New("digest is not sha256")
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil || len(raw) != sha256.Size {
		return out, errors.New("digest is not hexadecimal sha256")
	}
	copy(out[:], raw)
	return out, nil
}

// isAwaitTimeout reports whether a failed Await left the job alive for a later
// turn. The caller's own bound is retryable: an expired barrier deadline or a
// canceled request stops waiting without ending the detached job. A
// cancellation the caller's context does not explain comes from the scheduler's
// terminal shutdown instead, which also wraps context.Canceled so the cause
// stays reachable; such a job can never complete and must be rejected.
func isAwaitTimeout(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrAwaitTimeout) {
		return true
	}
	return errors.Is(err, context.Canceled) && ctx.Err() != nil
}

var _ BackgroundClient = auxiliary.BackgroundClient(nil)
