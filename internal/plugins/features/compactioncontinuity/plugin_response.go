package compactioncontinuity

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/augmentation"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/observability"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// BeforeResponseRelease consumes only an existing pending result. The
// augmentation matcher is an explicit empty allowlist for today's canonical
// compaction contract, so it records the capsule for the next eligible request
// and leaves the canonical event byte-for-byte intact.
func (p *Plugin) BeforeResponseRelease(ctx context.Context, ev *aipapi.Event, preview compaction.ResponsePreview, meta compaction.PreservationMeta, services compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			p.observeFailure(observability.StageCallback, observability.OutcomePanic, meta.TransactionID, "")
			err = nil
			return
		}
		if err != nil {
			p.observeError(observability.StageCallback, err, meta.TransactionID)
		}
	}()
	previewOutcome := observability.OutcomeObserved
	if preview.Kind == compaction.PreviewCompletionCandidate {
		previewOutcome = observability.OutcomeCandidate
	}
	p.observe(observability.Observation{
		Stage:           observability.StagePreview,
		Outcome:         previewOutcome,
		Evidence:        observability.BoundedID(string(preview.Evidence)),
		RuleID:          observability.BoundedID(preview.RuleID),
		CorrelationHash: observability.HashID(preview.TransactionID),
	})
	if p == nil || p.parent == nil || ctx == nil || preview.Kind != compaction.PreviewCompletionCandidate {
		p.observeFailure(observability.StagePreview, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	cfg, enabled := p.effectiveConfig(ctx)
	if !enabled {
		p.observeFailure(observability.StageEligibility, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	boundary := strings.TrimSpace(preview.TransactionID)
	if boundary == "" {
		p.observeFailure(observability.StagePreview, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	parent, err := p.parent.CaptureMeta(ctx, meta)
	if err != nil || !validParentBranch(parent) {
		p.observeError(observability.StageCallback, err, boundary)
		return nil
	}
	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		p.observeError(observability.StageCapsule, err, boundary)
		return nil
	}
	prepared := state.PendingInjection != nil && state.PendingInjection.BoundaryKey == boundary
	if strings.TrimSpace(string(state.PendingJobID)) != "" && services.BackgroundAux != nil {
		state, _ = p.consumePending(ctx, parent, state, services, cfg)
	}
	if state.Revision == 0 || len(state.CapsuleJSON) == 0 {
		p.observeFailure(observability.StageAugmentation, observability.OutcomeSkipped, boundary, "")
		return nil
	}
	_, plaintextCarrier := augmentation.Match(ev)
	if prepared {
		outcome := observability.OutcomePending
		if !plaintextCarrier && ev != nil && ev.Item != nil && ev.Item.Compaction != nil {
			outcome = observability.OutcomeOpaque
		}
		p.observe(observability.Observation{Stage: observability.StageAugmentation, Outcome: outcome, CorrelationHash: observability.HashID(boundary), Revision: state.Revision})
		return nil
	}
	if _, err = p.parent.SetPendingInjection(ctx, parent, InjectionTarget{BoundaryKey: boundary, CapsuleRevision: state.Revision}); err != nil {
		p.observeFailure(observability.StageReinjection, observability.OutcomeRollback, boundary, "")
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StageReinjection, Outcome: observability.OutcomeCreated, CorrelationHash: observability.HashID(boundary), Revision: state.Revision})
	return nil
}

// RequestOpenFailed lets the coordinator's bounded TTL cleanup expire the
// non-billable preview intent while preserving any pending injection target.
func (p *Plugin) RequestOpenFailed(_ context.Context, meta compaction.PreservationMeta, _ compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			p.observeFailure(observability.StageCallback, observability.OutcomePanic, meta.TransactionID, "")
			err = nil
		}
	}()
	p.clearPreparedMarker(meta)
	p.observe(observability.Observation{Stage: observability.StagePreviewIntent, Outcome: observability.OutcomeExpired, CorrelationHash: observability.HashID(meta.TransactionID)})
	return nil
}

// AfterResponseRelease is the only release-side watermark commit point. Core
// invokes it after detector finalization and before returning the event.
func (p *Plugin) AfterResponseRelease(ctx context.Context, ev aipapi.Event, meta compaction.PreservationMeta, _ compaction.Services) (err error) {
	defer func() {
		if recover() != nil {
			p.observeFailure(observability.StageCallback, observability.OutcomePanic, meta.TransactionID, "")
			err = nil
			return
		}
		if err != nil {
			p.observeError(observability.StageCallback, err, meta.TransactionID)
		}
	}()
	if p == nil || p.parent == nil || ctx == nil || ev.Kind != aipapi.EventResponseFinished {
		p.observeFailure(observability.StageWatermark, observability.OutcomeSkipped, meta.TransactionID, "")
		return nil
	}
	parent, err := p.parent.CaptureMeta(ctx, meta)
	if err != nil {
		p.observeError(observability.StageWatermark, err, meta.TransactionID)
		return nil
	}
	if !validParentBranch(parent) {
		p.observeFailure(observability.StageWatermark, observability.OutcomeSkipped, meta.TransactionID, "")
		return nil
	}
	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		p.observeError(observability.StageWatermark, err, meta.TransactionID)
		return nil
	}
	if state.PendingInjection == nil {
		p.observeFailure(observability.StageWatermark, observability.OutcomeSkipped, meta.TransactionID, "")
		return nil
	}
	boundary := strings.TrimSpace(meta.TransactionID)
	if boundary == "" || state.PendingInjection.BoundaryKey != boundary || !p.takePreparedMarker(meta, *state.PendingInjection) {
		p.observeFailure(observability.StageWatermark, observability.OutcomeSkipped, boundary, "")
		return nil
	}
	if _, err = p.parent.CommitReleasedInjection(ctx, parent, InjectionWatermark{BranchBinding: parent.Binding, BoundaryKey: boundary, CapsuleRevision: state.PendingInjection.CapsuleRevision}); err != nil {
		p.observeError(observability.StageWatermark, err, boundary)
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StageWatermark, Outcome: observability.OutcomeReleased, CorrelationHash: observability.HashID(boundary), Revision: state.PendingInjection.CapsuleRevision})
	return nil
}

var (
	_ compaction.RequestOpenFailedPreserver    = (*Plugin)(nil)
	_ compaction.AfterResponseReleasePreserver = (*Plugin)(nil)
)
