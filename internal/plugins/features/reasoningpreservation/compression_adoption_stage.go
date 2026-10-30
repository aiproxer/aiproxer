package reasoningpreservation

import (
	"context"
	"crypto/sha256"
	"errors"
)

// NewDecoderAdoptionStage returns a CompletedAdoptionStage that validates/correlation-checks,
// decodes, and CAS-attaches a surrogate under aggregate byte budgets.
// It derives ExpectedIndexes and SourceBytes from the authoritative artifact's semantic
// segments, enforces MaxSurrogateBytes/min savings, verifies reservation/job/artifact/
// original/semantic/egress/policy, decodes typed outcomes, always Forget exactly once
// after terminal handling, atomically moves pending->surrogate on success, and clears
// expected pending (CAS) on stale/budget/decode/insufficient while preserving original.
// Shadow mode always keeps original; no active selection.
func NewDecoderAdoptionStage(cfg Config, store CompressionStore, svc CompressionServices, tel *Telemetry) CompletedAdoptionStage {
	// Capture immutably; no cross-attempt state.
	return func(ctx context.Context, ar AdoptionResult) AdoptionResult {
		if ar.Outcome != AdoptionOutcomeBoundedRaw || ar.Candidate == nil || len(ar.BoundedRaw) == 0 {
			return ar
		}
		cand := ar.Candidate
		raw := ar.BoundedRaw
		rawCount := ar.RawByteCount
		// helper to forget exactly once
		forgotten := false
		doForget := func() {
			if !forgotten && !isNilCapability(svc.Client) {
				svc.Client.Forget(cand.JobID)
				forgotten = true
			}
		}
		// ensure forget on all stale/decode/attach paths; success also forgets.
		// We will call doForget explicitly on each return; no defer to avoid double.

		// Verify store available
		if store == nil {
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			// content-free error
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Fetch pending state with CAS verification
		st, ok, err := store.GetCompressionState(ctx, cand.Partition, cand.ArtifactID)
		if err != nil {
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			// clear expected pending if possible (best effort)
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: err}
		}
		if !ok || st.Pending == nil {
			// stale: no pending or double result - do not clear surrogate
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		pending := st.Pending
		// verify reservation/job correlation
		if pending.ReservationID != cand.ReservationID || pending.JobID != cand.JobID {
			// stale: CAS clear expected pending preserving surrogate
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		if !pending.PolicyHashAuthoritative {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Fetch authoritative artifact
		snap, err := store.Snapshot(ctx, cand.Partition)
		if err != nil {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		var artifact *TurnArtifact
		for i := range snap {
			if snap[i].ID == cand.ArtifactID {
				artifact = &snap[i]
				break
			}
		}
		if artifact == nil {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Verify original digest current
		if artifact.Anchor != pending.OriginalDigest {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Verify policy revision current
		if pending.PolicyRevision != cfg.Compression.EgressPolicyRef {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Derive semantic segments and correlation digests
		segs := ExtractSemanticSegments(artifact.Reasoning)
		if len(segs) == 0 {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Verify semantic digest current
		semDigest := computeSemanticDigest(artifact.Reasoning)
		if semDigest != pending.SemanticDigest {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Verify egress policy hash current (already authoritative)
		if pending.EgressPolicyHash == [32]byte{} {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		if !isValidSanitization(pending.Sanitization) {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		var zeroRouteHash [32]byte
		if pending.AuthorizedRouteHash == zeroRouteHash {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		routeHash := sha256.Sum256([]byte(cfg.Compression.Route))
		if pending.AuthorizedRouteHash != routeHash {
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: ErrCompressionConflict}
		}
		// Build decode params
		expectedIdx := make([]int, 0, len(segs))
		sourceBytes := 0
		for _, s := range segs {
			expectedIdx = append(expectedIdx, s.Index)
			sourceBytes += len(s.Text)
		}
		sanitization := pending.Sanitization
		params := SurrogateDecodeParams{
			ExpectedIndexes:     expectedIdx,
			SourceBytes:         sourceBytes,
			MaxSurrogateBytes:   cfg.Compression.MaxSurrogateBytes,
			MinSavedBytes:       cfg.Compression.MinSavedBytes,
			MinSavingsRatio:     cfg.Compression.MinSavingsRatio,
			OriginalDigest:      pending.OriginalDigest,
			PolicyRevision:      pending.PolicyRevision,
			Sanitization:        sanitization,
			SemanticDigest:      pending.SemanticDigest,
			EgressPolicyHash:    pending.EgressPolicyHash,
			AuthorizedRouteHash: pending.AuthorizedRouteHash,
		}
		sur, outcome, derr := DecodeSurrogate(raw, params)
		if derr != nil {
			// map decode outcome to telemetry
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				tel.RecordCompressionMeasurement(outcome, rawCount, 0, 0)
			}
			// content-free error already
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: derr}
		}
		// Success decode, attempt attach.
		// Reconstructed claims are untrusted input; the store validates the complete claim.
		claim := CompressionClaim{
			Partition:      cand.Partition,
			ArtifactID:     cand.ArtifactID,
			ReservationID:  cand.ReservationID,
			OriginalDigest: pending.OriginalDigest,
			PolicyRevision: pending.PolicyRevision,
		}
		err = store.AttachSurrogate(ctx, claim, cand.JobID, sur)
		if err != nil {
			// distinguish budget vs stale — budget exhaustion is distinct from stale/CAS.
			clearCompressionWithCleanup(ctx, store, cand.Partition, cand.ArtifactID, cand.ReservationID)
			doForget()
			if tel != nil {
				if errors.Is(err, ErrCompressionBudgetExceeded) {
					if be, ok := errors.AsType[*BudgetError](err); ok {
						switch be.Kind {
						case BudgetSurrogatePerTurn:
							tel.RecordShadowMeasurement(OutcomeBudgetSurrogatePerTurn, sourceBytes, rawCount, sur.Bytes, 0, 0)
						case BudgetSurrogatePerSession:
							tel.RecordShadowMeasurement(OutcomeBudgetSurrogatePerSession, sourceBytes, rawCount, sur.Bytes, 0, 0)
						case BudgetSurrogateTotal:
							tel.RecordShadowMeasurement(OutcomeBudgetSurrogateTotal, sourceBytes, rawCount, sur.Bytes, 0, 0)
						case BudgetPendingPerSession, BudgetPendingTotal:
							tel.RecordShadowMeasurement(OutcomeReservationBudgetExceeded, sourceBytes, rawCount, sur.Bytes, 0, 0)
						default:
							tel.RecordShadowMeasurement(OutcomeReservationBudgetExceeded, sourceBytes, rawCount, sur.Bytes, 0, 0)
						}
					} else {
						tel.RecordShadowMeasurement(OutcomeReservationBudgetExceeded, sourceBytes, rawCount, sur.Bytes, 0, 0)
					}
				} else if errors.Is(err, ErrCompressionConflict) {
					tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
				} else {
					tel.RecordCompressionMeasurement(OutcomeStale, rawCount, 0, 0)
				}
			}
			return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, Err: err}
		}
		// success attach
		doForget()
		if tel != nil {
			decodedBytes := sur.Bytes
			savedBytes := max(sourceBytes-decodedBytes, 0)
			tel.RecordCompressionMeasurement(OutcomeSurrogateAttached, rawCount, decodedBytes, savedBytes)
			if cfg.Compression.Mode == CompressionShadow {
				tel.RecordCompressionMeasurement(OutcomeShadowReady, 0, 0, savedBytes)
			}
			// Active mode does not record shadow_ready; active_used is recorded at view injection when actually surrogate text is used.
		}
		// shadow always original, no active selection; active selection is gated strictly on explicit mode at view time.
		return AdoptionResult{Outcome: AdoptionOutcomeNone, Candidate: cand, RawByteCount: rawCount, BoundedRaw: raw}
	}
}
