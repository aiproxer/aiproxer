package controlplane

import (
	"context"
	"errors"
	"fmt"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// RecorderConfig configures a [RecorderService] (requirement 5.4, 5.5, 7.6).
type RecorderConfig struct {
	// Policy is the global recording policy applied to every Record call.
	Policy cp.RecordingPolicy
	// Required is the set of lifecycle categories that fail closed before
	// protected upstream work when Policy is required_pre_work. Categories not
	// listed here remain best-effort (design "Configuration and Readiness
	// Contract").
	Required []cp.Category
	// Clock supplies RecordedAt and last-failure timestamps. Defaults to
	// SystemClock when nil.
	Clock Clock
}

// RecorderService applies recording policy, appends validated events to the
// store, and maintains capability status without altering normal runtime
// outcomes (requirements 1.7, 1.8, 5.1–5.7, 7.1, 7.2, 7.3, 7.5, 7.6, 10.7).
//
// Behavior:
//   - When Policy is disabled, Record returns [ErrDisabled] and does not
//     touch the store or status (design: disabled capability preserves
//     current runtime behavior).
//   - When a category is in Required and Policy is required_pre_work, a
//     store append failure fails closed: the error is returned to the caller
//     so protected upstream work does not begin, and status is degraded.
//   - Otherwise (best-effort, or non-required categories), a store append
//     failure degrades status with [cp.ReasonRecordingFailure] and returns
//     nil to the caller so the request outcome is preserved (requirement 5.2,
//     5.3).
//   - [RecorderService.RecordBestEffort] always treats the call as best-effort
//     regardless of category or policy. Source adapters use it for post-output
//     and non-protected paths so post-output recording failures never trigger
//     retry, failover, or replacement (requirement 5.3, 10.7).
//   - Unsafe evidence ([ErrUnsafeEvidence]) is always returned to the caller
//     regardless of policy: it is a programmer error, not a transient store
//     failure.
type RecorderService struct {
	store    EventAppender
	status   *Status
	policy   cp.RecordingPolicy
	required map[cp.Category]struct{}
	clock    Clock
}

// NewRecorderService constructs a RecorderService. status must be non-nil; the
// service updates it on best-effort and required-pre-work failures.
func NewRecorderService(store EventAppender, status *Status, cfg RecorderConfig) *RecorderService {
	required := make(map[cp.Category]struct{}, len(cfg.Required))
	for _, c := range cfg.Required {
		required[c] = struct{}{}
	}
	clock := cfg.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	return &RecorderService{store: store, status: status, policy: cfg.Policy, required: required, clock: clock}
}

// Record appends one event through the configured store, applying per-category
// recording policy (requirement 5.4, 5.5). It implements [cp.Recorder].
func (s *RecorderService) Record(ctx context.Context, ev cp.Event) (cp.RecordResult, error) {
	res, guardErr, appendErr := s.prepareAppend(ctx, ev)
	if guardErr != nil {
		return cp.RecordResult{}, guardErr
	}
	if appendErr == nil {
		return res, nil
	}
	return s.handleAppendError(appendErr, ev.Category)
}

// RecordBestEffort appends one event without fail-closed semantics, regardless
// of category or policy. Source adapters use it for post-output and
// non-protected paths so post-output recording failures never trigger retry,
// failover, or replacement (requirement 5.3, 10.7). It still rejects unsafe
// evidence and disabled policy.
func (s *RecorderService) RecordBestEffort(ctx context.Context, ev cp.Event) (cp.RecordResult, error) {
	res, guardErr, appendErr := s.prepareAppend(ctx, ev)
	if guardErr != nil {
		return cp.RecordResult{}, guardErr
	}
	if appendErr == nil {
		return res, nil
	}
	if errors.Is(appendErr, ErrUnsafeEvidence) {
		return cp.RecordResult{}, appendErr
	}
	s.degrade(cp.ReasonRecordingFailure)
	return cp.RecordResult{}, nil
}

// prepareAppend enforces the shared disabled-policy and unsafe-evidence guards
// and performs the store append used by both Record and RecordBestEffort. When
// guardErr is non-nil, the caller returns it without further classification
// (disabled capability or unsafe evidence). When both errors are nil, res holds
// the successful store result. When appendErr is non-nil, the caller classifies
// it per its own policy path (fail-closed vs degrade).
func (s *RecorderService) prepareAppend(ctx context.Context, ev cp.Event) (res cp.RecordResult, guardErr error, appendErr error) {
	if s.policy == cp.RecordingDisabled {
		return cp.RecordResult{}, ErrDisabled, nil
	}
	if err := ValidateEvent(ev); err != nil {
		return cp.RecordResult{}, fmt.Errorf("%w: %v", ErrUnsafeEvidence, err), nil
	}
	res, appendErr = s.store.Append(ctx, ev)
	return res, nil, appendErr
}

// handleAppendError classifies a store append failure. Required pre-work
// categories fail closed (return the error); all other paths degrade status
// and return nil so the request outcome is preserved.
func (s *RecorderService) handleAppendError(err error, category cp.Category) (cp.RecordResult, error) {
	if errors.Is(err, ErrUnsafeEvidence) {
		// Programmer error: always surface, do not degrade (status unchanged).
		return cp.RecordResult{}, err
	}
	if s.policy == cp.RecordingRequiredPreWork {
		if _, required := s.required[category]; required {
			s.degrade(cp.ReasonRecordingFailure)
			if errors.Is(err, ErrUnavailable) {
				return cp.RecordResult{}, fmt.Errorf("%w: required pre-work recording failed: %v", ErrUnavailable, err)
			}
			return cp.RecordResult{}, fmt.Errorf("%w: required pre-work recording failed: %v", ErrDegraded, err)
		}
	}
	s.degrade(cp.ReasonRecordingFailure)
	return cp.RecordResult{}, nil
}

// degrade records a bounded failure reason on the capability status.
func (s *RecorderService) degrade(reason cp.ReasonCode) {
	if s.status == nil {
		return
	}
	s.status.RecordFailure(reason, s.clock.Now())
}

// Status returns the current capability status snapshot (requirement 7.1).
func (s *RecorderService) Status(context.Context) (cp.CapabilityStatus, error) {
	if s.status == nil {
		return cp.CapabilityStatus{State: cp.CapabilityDisabled, RecordingPolicy: s.policy}, nil
	}
	snap := s.status.Snapshot()
	if s.policy != "" && snap.RecordingPolicy == "" {
		snap.RecordingPolicy = s.policy
	}
	return snap, nil
}

// Compile-time assertion that RecorderService satisfies the SDK Recorder.
var _ cp.Recorder = (*RecorderService)(nil)
