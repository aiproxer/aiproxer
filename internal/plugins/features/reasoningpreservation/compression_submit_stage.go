package reasoningpreservation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// compressionCoalesceKey derives a versioned content-free digest for SubmitCollect
// coalescing. It contains no reasoning text, credentials, or raw session lineage.
// Version prefix "v1" ensures stable versioning if scheme changes.
// It hashes artifact ID, original/semantic digests, authoritative egress hash,
// policy revision and route with length-prefixed boundaries to avoid collisions.
func compressionCoalesceKey(pr PreparedReservation) string {
	h := sha256.New()
	_, _ = h.Write([]byte("aip.reasoning-preservation.compression.v1"))
	_, _ = h.Write([]byte{0})
	// Length-prefixed strings to avoid boundary collisions.
	for _, v := range []string{pr.Reservation.Correlation.ArtifactID, pr.Reservation.Correlation.PolicyRevision, pr.Route} {
		_, _ = fmt.Fprintf(h, "%d:", len(v))
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	// Fixed-size digests written raw with separators.
	_, _ = h.Write(pr.Reservation.Correlation.OriginalDigest[:])
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(pr.Reservation.Correlation.SemanticDigest[:])
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(pr.EgressPolicyHash[:])
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// NewPostEgressSubmitStage returns a PostEgressStage that builds the detached
// no-tools auxiliary compressor request from the prepared sanitized segments,
// submits it through the generation-bound BackgroundClient with timeout and
// versioned coalesce key, and binds the returned JobID via CAS.
// It never calls Await or Poll. On request-build or submit failure it clears
// only the expected reservation (CAS) and leaves the original intact. On
// accepted JobID it attempts BindCompressionJob; bind failure triggers
// Forget(jobID) and clears only the expected reservation. Empty JobID is
// treated as invalid and clears. Incurred accepted work remains
// accounting-owned (Forget does not cancel billing).
func NewPostEgressSubmitStage(cfg Config, store CompressionStore, svc CompressionServices) PostEgressStage {
	return NewPostEgressSubmitStageWithTelemetry(cfg, store, svc, nil)
}

// NewPostEgressSubmitStageWithTelemetry is the telemetry-aware submit stage.
// It records content-free queue/submit outcomes: submitted, coalesced, queue_saturated, submit_failed.
// Admission denial is asynchronous via PollFailed, not a synchronous SubmitCollect error today;
// OutcomeAdmissionDenied is reserved for future synchronous credit screening but currently never emitted here.
// No reasoning text or IDs are emitted.
func NewPostEgressSubmitStageWithTelemetry(cfg Config, store CompressionStore, svc CompressionServices, tel *Telemetry) PostEgressStage {
	return func(ctx context.Context, pr PreparedReservation) error {
		if store == nil {
			return nil
		}
		if !pr.Reservation.IsReserved() {
			return nil
		}
		if pr.Reservation.Claim.ReservationID == "" || pr.Reservation.Correlation.ArtifactID == "" {
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		if len(pr.Segments) == 0 {
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		if isNilCapability(svc.Client) {
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		params := CompressorAuxRequestParams{
			Route:               pr.Route,
			ParentTraceID:       pr.Reservation.Correlation.TraceID,
			ParentALegID:        pr.Reservation.Correlation.ALegID,
			ParentBLegID:        pr.Reservation.Correlation.BLegID,
			ParentBranchBinding: pr.Reservation.Correlation.BranchBinding,
			Segments:            pr.Segments,
			MaxOutputTokens:     cfg.Compression.MaxOutputTokens,
		}
		req, err := BuildCompressorAuxRequest(params)
		if err != nil {
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		coalesceKey := compressionCoalesceKey(pr)
		var coalesced bool
		opts := auxiliary.SubmitOptions{
			Timeout:        cfg.Compression.Timeout,
			CoalesceKey:    coalesceKey,
			OnCoalesced:    func(c bool) { coalesced = c },
			MaxOutputBytes: cfg.Compression.MaxOutputBytes,
		}
		submitCtx := context.WithoutCancel(ctx)
		submitCtx = scope.WithScope(submitCtx, pr.Reservation.Correlation.Scope)
		jobID, err := svc.Client.SubmitCollect(submitCtx, req, opts)
		if err != nil {
			if tel != nil && cfg.Compression.Enabled {
				// Synchronous admission is only via the bounded background queue (ErrQueueSaturated);
				// runner-side admission failures surface asynchronously via Await/Poll as PollFailed.
				if errors.Is(err, auxiliary.ErrQueueSaturated) {
					tel.RecordShadowMeasurement(OutcomeQueueSaturated, pr.Reservation.Correlation.SourceBytes, 0, 0, 0, 0)
				} else {
					tel.RecordShadowMeasurement(OutcomeSubmitFailed, pr.Reservation.Correlation.SourceBytes, 0, 0, 0, 0)
				}
			}
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		if jobID == "" {
			if tel != nil && cfg.Compression.Enabled {
				tel.RecordShadowMeasurement(OutcomeSubmitFailed, pr.Reservation.Correlation.SourceBytes, 0, 0, 0, 0)
			}
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		err = store.BindCompressionJob(ctx, pr.Reservation.Claim, jobID)
		if err != nil {
			if !isNilCapability(svc.Client) {
				svc.Client.Forget(jobID)
			}
			clearCompressionWithCleanup(ctx, store, pr.Reservation.Correlation.Partition, pr.Reservation.Correlation.ArtifactID, pr.Reservation.Claim.ReservationID)
			return nil
		}
		if tel != nil && cfg.Compression.Enabled {
			if coalesced {
				tel.RecordShadowMeasurement(OutcomeCoalesced, pr.Reservation.Correlation.SourceBytes, 0, 0, 0, 0)
			} else {
				tel.RecordShadowMeasurement(OutcomeSubmitted, pr.Reservation.Correlation.SourceBytes, 0, 0, 0, 0)
			}
		}
		return nil
	}
}
