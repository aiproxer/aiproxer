package reasoningpreservation

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// PollForAttemptKind is a typed outcome for the one-shot poll attempt.
type PollForAttemptKind string

const (
	PollKindNoPending   PollForAttemptKind = "no_pending"
	PollKindUnavailable PollForAttemptKind = "unavailable"
	PollKindPending     PollForAttemptKind = "pending"
	PollKindFailed      PollForAttemptKind = "failed"
	PollKindNotFound    PollForAttemptKind = "not_found"
	PollKindCompleted   PollForAttemptKind = "completed"
	PollKindPollError   PollForAttemptKind = "poll_error"
)

// CompletedPollCandidate is a typed completed adoption candidate for the next
// stage (5.2). In 5.1 the candidate is produced but original is still replayed
// (shadow). It carries the defensive Collected copy plus correlation IDs.
type CompletedPollCandidate struct {
	Partition     SessionPartition
	ArtifactID    string
	ReservationID string
	JobID         auxiliary.JobID
	Collected     aipapi.Collected
	PollState     auxiliary.PollState
}

// CompressionPollAttemptResult is the typed result of the one-shot poll.
type CompressionPollAttemptResult struct {
	Kind      PollForAttemptKind
	Candidate *CompletedPollCandidate
	State     auxiliary.PollState
	Err       error
}

// PollOnceForMatchingArtifact performs a single non-blocking Poll for the first
// restorable artifact (ClassMissing and destination-supported) that has a pending
// bound JobID. It shares ClassifyAssistantTurns / dialectSet semantics with
// RestoreMissingReasoning via collectRestoreCandidates. Never calls Await or busy-waits;
// operational errors are compression-local and must NOT trigger authoritative
// on_state_error reject.
func PollOnceForMatchingArtifact(ctx context.Context, call *aipapi.Call, cs CompressionStore, partition SessionPartition, artifacts []TurnArtifact, support aipapi.ReasoningReplaySupport, svc CompressionServices) CompressionPollAttemptResult {
	_, candidates, err := collectRestoreCandidates(call, artifacts, support)
	if err != nil {
		// Classification/validation errors are poll-local; fall back to direct scan if call is nil for test helper
		if call == nil {
			return pollOnceDirectScan(ctx, cs, partition, artifacts, svc)
		}
		return CompressionPollAttemptResult{Kind: PollKindPollError, Err: err}
	}
	// Filter to supported only for poll
	var supported []restoreCandidate
	for _, c := range candidates {
		if !c.Unsupported {
			supported = append(supported, c)
		}
	}
	if len(supported) == 0 && call != nil {
		return CompressionPollAttemptResult{Kind: PollKindNoPending}
	}
	if len(supported) == 0 && call == nil {
		return pollOnceDirectScan(ctx, cs, partition, artifacts, svc)
	}
	return pollOnceWithCandidates(ctx, cs, partition, supported, svc)
}

// pollOnceWithCandidates polls the first candidate with pending JobID without reclassifying.
func pollOnceWithCandidates(ctx context.Context, cs CompressionStore, partition SessionPartition, candidates []restoreCandidate, svc CompressionServices) CompressionPollAttemptResult {
	if cs == nil {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	if isNilCapability(svc.Poller) {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	if len(candidates) == 0 {
		return CompressionPollAttemptResult{Kind: PollKindNoPending}
	}
	var target restoreCandidate
	var targetState CompressionState
	var found bool
	for _, c := range candidates {
		st, ok, gerr := cs.GetCompressionState(ctx, partition, c.ArtifactID)
		if gerr != nil {
			return CompressionPollAttemptResult{Kind: PollKindPollError, Err: gerr}
		}
		if !ok || st.Pending == nil || string(st.Pending.JobID) == "" {
			continue
		}
		target = c
		targetState = st
		found = true
		break
	}
	if !found {
		return CompressionPollAttemptResult{Kind: PollKindNoPending}
	}
	return pollOnceForTarget(ctx, cs, partition, target, targetState, svc)
}

func pollOnceDirectScan(ctx context.Context, cs CompressionStore, partition SessionPartition, artifacts []TurnArtifact, svc CompressionServices) CompressionPollAttemptResult {
	if cs == nil {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	if isNilCapability(svc.Poller) {
		return CompressionPollAttemptResult{Kind: PollKindUnavailable}
	}
	for i := range artifacts {
		id := artifacts[i].ID
		st, ok, gerr := cs.GetCompressionState(ctx, partition, id)
		if gerr != nil {
			return CompressionPollAttemptResult{Kind: PollKindPollError, Err: gerr}
		}
		if !ok || st.Pending == nil || string(st.Pending.JobID) == "" {
			continue
		}
		return pollOnceForTarget(ctx, cs, partition, restoreCandidate{Artifact: artifacts[i], ArtifactID: id}, CompressionState{ReservationID: st.ReservationID, Pending: st.Pending}, svc)
	}
	return CompressionPollAttemptResult{Kind: PollKindNoPending}
}

func pollOnceForTarget(ctx context.Context, cs CompressionStore, partition SessionPartition, target restoreCandidate, targetState CompressionState, svc CompressionServices) CompressionPollAttemptResult {
	targetID := target.ArtifactID
	reservationID := targetState.ReservationID
	jobID := targetState.Pending.JobID
	pr, err := svc.Poller.Poll(ctx, jobID)
	if err != nil {
		return CompressionPollAttemptResult{Kind: PollKindPollError, Err: err}
	}
	switch pr.State {
	case auxiliary.PollPending:
		return CompressionPollAttemptResult{Kind: PollKindPending, State: pr.State}
	case auxiliary.PollFailed:
		clearCompressionWithCleanup(ctx, cs, partition, targetID, reservationID)
		if !isNilCapability(svc.Client) {
			svc.Client.Forget(jobID)
		}
		return CompressionPollAttemptResult{Kind: PollKindFailed, State: pr.State, Err: pr.Err}
	case auxiliary.PollNotFound:
		clearCompressionWithCleanup(ctx, cs, partition, targetID, reservationID)
		if !isNilCapability(svc.Client) {
			svc.Client.Forget(jobID)
		}
		return CompressionPollAttemptResult{Kind: PollKindNotFound, State: pr.State}
	case auxiliary.PollCompleted:
		// Defensive copy at the adoption boundary: the feature must not depend on a
		// third-party BackgroundPoller honoring the documented defensive-copy contract.
		// aipapi.CloneCollectedInto gives the candidate ownership of every collected
		// interior so a poller that keeps writing to its payload cannot corrupt the
		// raw guard/decode.
		var collected aipapi.Collected
		aipapi.CloneCollectedInto(&collected, &pr.Collected)
		cand := &CompletedPollCandidate{
			Partition:     partition,
			ArtifactID:    targetID,
			ReservationID: reservationID,
			JobID:         jobID,
			Collected:     collected,
			PollState:     pr.State,
		}
		return CompressionPollAttemptResult{Kind: PollKindCompleted, Candidate: cand, State: pr.State}
	default:
		return CompressionPollAttemptResult{Kind: PollKindPollError, Err: fmt.Errorf("unknown poll state %d", pr.State), State: pr.State}
	}
}

// AdoptionOutcome is a typed content-free outcome for the raw guard stage (5.2).
type AdoptionOutcome string

const (
	AdoptionOutcomeNone              AdoptionOutcome = "none"
	AdoptionOutcomeBoundedRaw        AdoptionOutcome = "bounded_raw"
	AdoptionOutcomeRawOversize       AdoptionOutcome = "raw_oversize"
	AdoptionOutcomeRawInvalidChannel AdoptionOutcome = "raw_invalid_channel"
	AdoptionOutcomeRawInvalidLimit   AdoptionOutcome = "raw_invalid_limit"
)

// AdoptionResult carries bounded raw bytes for the next stage (5.3) or a rejection outcome.
// It is typed, content-free, and holds only byte counts, not raw reasoning text.
// BoundedRaw is non-nil only for AdoptionOutcomeBoundedRaw.
type AdoptionResult struct {
	Outcome      AdoptionOutcome
	Candidate    *CompletedPollCandidate
	BoundedRaw   []byte
	RawByteCount int
	Err          error
}

// handleCompletedPollCandidate implements the raw byte guard before parser invocation (5.2).
// It feeds Candidate.Collected through ExtractBoundedRaw using cfg.Compression.MaxOutputBytes
// BEFORE DecodeSurrogate (decode is 5.3). For raw_oversize / non-text / invalid channel it
// clears the expected reservation and forgets the JobID once, records a content-free outcome
// via telemetry if safe, and returns a rejection result with original fallback semantics.
// For bounded raw success it returns the bounded bytes for the next local stage without
// forgetting (Forget is deferred to 5.3 after decode/attach). It never re-polls or decodes.
func handleCompletedPollCandidate(ctx context.Context, cfg Config, cs CompressionStore, svc CompressionServices, tel *Telemetry, res CompressionPollAttemptResult, call *aipapi.Call) AdoptionResult {
	if res.Kind != PollKindCompleted || res.Candidate == nil {
		return AdoptionResult{Outcome: AdoptionOutcomeNone}
	}
	cand := res.Candidate
	maxBytes := cfg.Compression.MaxOutputBytes
	// When compression is disabled MaxOutputBytes is zero; treat as invalid limit and reject content-free.
	if maxBytes <= 0 {
		// content-free error, no string payload
		err := fmt.Errorf("%w: max_output_bytes %d must be > 0", ErrRawInvalidLimit, maxBytes)
		if cs != nil {
			clearCompressionWithCleanup(ctx, cs, cand.Partition, cand.ArtifactID, cand.ReservationID)
		}
		if !isNilCapability(svc.Client) {
			svc.Client.Forget(cand.JobID)
		}
		if tel != nil {
			// Record only content-free outcome; byte count is the attempted size without exposing content.
			tel.RecordCompression(OutcomeRawInvalidLimit, cand.Collected.Text.Len())
		}
		return AdoptionResult{Outcome: AdoptionOutcomeRawInvalidLimit, Candidate: cand, RawByteCount: cand.Collected.Text.Len(), Err: err}
	}
	raw, err := ExtractBoundedRaw(cand.Collected, maxBytes)
	if err != nil {
		var outcome AdoptionOutcome
		switch {
		case errors.Is(err, ErrRawOversize):
			outcome = AdoptionOutcomeRawOversize
		case errors.Is(err, ErrRawInvalidChannel):
			outcome = AdoptionOutcomeRawInvalidChannel
		case errors.Is(err, ErrRawInvalidLimit):
			outcome = AdoptionOutcomeRawInvalidLimit
		default:
			outcome = AdoptionOutcomeRawInvalidChannel
		}
		// Clear expected reservation and forget once, as required for rejection paths.
		if cs != nil {
			clearCompressionWithCleanup(ctx, cs, cand.Partition, cand.ArtifactID, cand.ReservationID)
		}
		if !isNilCapability(svc.Client) {
			svc.Client.Forget(cand.JobID)
		}
		// Record only content-free outcome and byte count via telemetry if safe.
		if tel != nil {
			var telOutcome SafeOutcome
			switch outcome {
			case AdoptionOutcomeRawOversize:
				telOutcome = OutcomeRawOversize
			case AdoptionOutcomeRawInvalidChannel:
				telOutcome = OutcomeRawInvalidChannel
			case AdoptionOutcomeRawInvalidLimit:
				telOutcome = OutcomeRawInvalidLimit
			default:
				telOutcome = OutcomeRawInvalidChannel
			}
			tel.RecordCompression(telOutcome, cand.Collected.Text.Len())
		}
		// Ensure error is content-free (ExtractBoundedRaw already is) and wrap with typed sentinel for errors.Is.
		wrapped := fmt.Errorf("%w", err)
		// Do not include raw text length details beyond what Extract already provides; callers check errors.Is.
		_ = call // shadow: do not mutate call
		return AdoptionResult{Outcome: outcome, Candidate: cand, RawByteCount: cand.Collected.Text.Len(), Err: wrapped}
	}
	// Success: bounded raw, do not clear/forget yet (deferred to 5.3 after decode/attach).
	// Pass raw to next local stage; no double poll/decode.
	if tel != nil {
		tel.RecordCompression(OutcomeBoundedRaw, len(raw))
	}
	_ = call // shadow: keep original for this attempt
	return AdoptionResult{Outcome: AdoptionOutcomeBoundedRaw, Candidate: cand, BoundedRaw: raw, RawByteCount: len(raw)}
}

// CompletedAdoptionStage is a composable hook for the adoption chain (5.2 -> 5.3 -> ...).
// It receives the raw-guard AdoptionResult and returns the (possibly) transformed result.
// The transform holds this stage immutably; default is identity. Task 5.3 will inject the
// decoder/attach stage via constructor. No cross-attempt state is stored.
type CompletedAdoptionStage func(context.Context, AdoptionResult) AdoptionResult

// identityAdoptionStage is the default stage that leaves the raw-guard result unchanged.
// It is retained locally and used when no explicit stage is injected.
func identityAdoptionStage(_ context.Context, r AdoptionResult) AdoptionResult { return r }

// handlePollAndGuardRaw is a local chain helper that ensures poll is performed once and
// raw extraction is applied exactly once per attempt without double poll/decode.
// It is used by AttemptTransform to keep 5.2 and 5.3 on the same attempt's candidate.
func handlePollAndGuardRaw(ctx context.Context, cfg Config, cs CompressionStore, svc CompressionServices, tel *Telemetry, pollRes CompressionPollAttemptResult, call *aipapi.Call) AdoptionResult {
	return handleCompletedPollCandidate(ctx, cfg, cs, svc, tel, pollRes, call)
}
