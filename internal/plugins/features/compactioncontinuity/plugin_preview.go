package compactioncontinuity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/carriers"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/injection"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/observability"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/source"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

const minimumBarrierTimeout = time.Millisecond

// BeforeRequest consumes only pure detector metadata. It may prepare a
// non-billable intent and inject an already available capsule, but it never
// submits fresh auxiliary work before the primary request opens.
// Pre-open staging is authorized by requirement 7.12; §571 holds operatively:
// failed opens never bind preview intents, submit billable jobs, or commit
// release watermarks (see RequestOpenFailed).
func (p *Plugin) BeforeRequest(ctx context.Context, call *aipapi.Call, preview compaction.RequestPreview, meta compaction.PreservationMeta, services compaction.Services) (err error) {
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
	p.observe(observability.Observation{Stage: observability.StagePreview, Outcome: previewOutcome, Evidence: observability.BoundedID(string(preview.Evidence)), RuleID: observability.BoundedID(preview.RuleID), CorrelationHash: observability.HashID(previewBoundary(preview))})
	if p == nil || p.parent == nil || ctx == nil || call == nil || preview.Kind != compaction.PreviewCompletionCandidate {
		p.observeFailure(observability.StagePreview, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	cfg, enabled := p.effectiveConfig(ctx)
	if !enabled {
		p.observeFailure(observability.StageEligibility, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	parent, err := p.parent.Capture(ctx, *call, meta)
	if err != nil || !validParentBranch(parent) {
		p.observeError(observability.StageCallback, err, preview.TransactionID)
		return nil
	}
	boundary := previewBoundary(preview)
	if boundary == "" {
		p.observeFailure(observability.StagePreview, observability.OutcomeSkipped, preview.TransactionID, "")
		return nil
	}
	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		p.observeError(observability.StagePreviewIntent, err, boundary)
		return nil
	}
	intent := PreviewIntent{Key: previewIntentKey(parent.Binding, boundary, state.Revision), TargetSourceRevision: state.Revision}
	if _, err = p.parent.RecordPreviewIntent(ctx, parent, intent); err != nil {
		p.observeError(observability.StagePreviewIntent, err, boundary)
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StagePreviewIntent, Outcome: observability.OutcomeCreated, CorrelationHash: observability.HashID(boundary), Revision: state.Revision})
	previous, window, err := p.previousState(parent, state)
	if err != nil {
		p.observeError(observability.StageCapsule, err, boundary)
		return nil
	}
	prepared, err := source.Prepare(ctx, source.Input{
		Call: *call, Existing: window, Previous: window.HighWatermark,
		Recognizer: carrierRecognizer{}, Config: source.Config{MaxBytes: stateBound(cfg.Source.MaxBytes, source.DefaultConfig().MaxBytes)},
	})
	if err != nil {
		return nil
	}
	// P1: preserve staged semantic delta until successful open. Do not commit
	// the source snapshot or its high watermark pre-open; RequestOpened
	// persists both after binding the preview intent and before extraction.
	// Persisting the new watermark now would cause source.Prepare in
	// RequestOpened to recognize original items as already processed
	// (injection only appends a developer item) and produce an empty
	// SanitizedDelta while previewBound still triggers extraction, losing
	// user decisions/constraints.
	// Pre-open persists only the preview intent and the deterministic capsule,
	// the latter with the prior watermark so the semantic delta remains
	// visible to RequestOpened.
	oldWatermark := state.SourceHighWatermark
	if oldWatermark == "" {
		oldWatermark = encodeWatermark(window.HighWatermark)
	}
	// Do not call CommitSource pre-open; persist capsule with old watermark
	// to preserve delta while still making deterministic plan available for
	// injection and for the post-open path.
	previous, state, err = p.applyPreviewDeterministic(ctx, parent, state, previous, prepared, oldWatermark, boundary, cfg)
	if err != nil {
		p.observeError(observability.StageCapsule, err, boundary)
		return nil
	}
	if strings.TrimSpace(string(state.PendingJobID)) != "" && services.BackgroundAux != nil {
		state, _ = p.consumePending(ctx, parent, state, services, cfg)
		if len(state.CapsuleJSON) != 0 {
			previous, _, err = p.previousState(parent, state)
			if err != nil {
				p.observeError(observability.StageCapsule, err, boundary)
				return nil
			}
		}
	}
	if previous.BranchBinding == "" {
		p.observeFailure(observability.StageEligibility, observability.OutcomeIneligible, boundary, "")
		return nil
	}
	target := InjectionTarget{BoundaryKey: boundary, CapsuleRevision: previous.Revision}
	if state.PendingInjection != nil {
		if _, err = p.parent.ValidateInjection(ctx, parent, *state.PendingInjection); err != nil {
			p.observeError(observability.StageReinjection, err, boundary)
			return nil
		}
	}
	injected, err := injection.Inject(injection.Input{
		Call: *call, Capsule: previous, ExpectedBranchBinding: parent.Binding,
		BoundaryKey: boundary, Limits: injection.ProjectionLimits{MaxBytes: cfg.Capsule.MaxBytes, MaxTokens: cfg.Capsule.MaxTokens},
		Marker: p.preparedMarker(meta, parent.Binding, InjectionTarget{BoundaryKey: boundary, CapsuleRevision: previous.Revision}),
	})
	if err != nil {
		p.observeFailure(observability.StageAugmentation, observability.OutcomeRollback, boundary, "")
		return nil
	}
	if _, err = p.parent.SetPendingInjection(ctx, parent, target); err != nil {
		p.observeFailure(observability.StageReinjection, observability.OutcomeRollback, boundary, "")
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StageAugmentation, Outcome: observability.OutcomeCompleted, CorrelationHash: observability.HashID(boundary), Revision: previous.Revision})
	p.observe(observability.Observation{Stage: observability.StageReinjection, Outcome: observability.OutcomeCreated, CorrelationHash: observability.HashID(boundary), Revision: previous.Revision})
	p.recordPreparedMarker(meta, target)
	// The continuity projection is a change to the cached prefix, so the
	// request records the bounded structural cause exactly when the projection
	// is committed. A no-op re-application, a discarded pass and a failed pass
	// record nothing; the first cause on the request wins.
	if injected.Applied {
		recordStructuralCause(&injected.Call)
	}
	*call = injected.Call
	return nil
}

func previewBoundary(preview compaction.RequestPreview) string {
	if value := strings.TrimSpace(preview.BoundaryFingerprint); value != "" {
		return value
	}
	return strings.TrimSpace(preview.TransactionID)
}

func previewIntentKey(binding, boundary string, revision uint64) string {
	h := sha256.New()
	_, _ = h.Write([]byte("aip.compaction-continuity.preview.v1\x00"))
	for _, value := range []string{strings.TrimSpace(binding), strings.TrimSpace(boundary), fmt.Sprint(revision)} {
		_, _ = fmt.Fprintf(h, "%d:", len(value))
		_, _ = h.Write([]byte(value))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (p *Plugin) applyPreviewDeterministic(ctx context.Context, parent ParentBranch, state ParentState, previous capsule.Envelope, prepared source.Prepared, watermark, boundary string, cfg Config) (capsule.Envelope, ParentState, error) {
	dirty := len(state.CapsuleJSON) == 0
	if previous.BranchBinding == "" && prepared.Candidate {
		var err error
		previous, err = capsule.New(parent.Binding)
		if err != nil {
			return capsule.Envelope{}, ParentState{}, err
		}
		dirty = true
	}
	for _, entry := range prepared.NewEntries {
		if entry.Carrier == nil || !cfg.Preserve.Plan {
			continue
		}
		update, matched, err := extractCarrierUpdate(*entry.Carrier)
		if err != nil || !matched {
			outcome := observability.OutcomeUnmatched
			if err != nil {
				outcome = observability.OutcomeInvalid
			}
			p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: outcome, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(boundary)})
			continue
		}
		p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: observability.OutcomeMatched, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(boundary)})
		previous, err = carriers.Apply(previous, update)
		if err != nil {
			return capsule.Envelope{}, ParentState{}, err
		}
		dirty = true
	}
	if !dirty {
		return previous, state, nil
	}
	previous, err := capsule.PruneWithLimits(previous, capsule.Limits{MaxBytes: cfg.Capsule.MaxBytes, MaxTokens: cfg.Capsule.MaxTokens})
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	serialized, err := capsule.Encode(previous)
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	digest, err := digestArray(previous.ContentDigest)
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	state, err = p.parent.CommitCapsule(ctx, parent, state.Revision, serialized, digest, watermark)
	if err != nil {
		return capsule.Envelope{}, ParentState{}, err
	}
	p.observeCapsule(observability.OutcomeCommitted, previous, len(serialized), boundary)
	return previous, state, nil
}

func (p *Plugin) consumePending(ctx context.Context, parent ParentBranch, state ParentState, services compaction.Services, cfg Config) (ParentState, error) {
	pendingJobID := state.PendingJobID
	previous, window, err := p.previousState(parent, state)
	if err != nil {
		return state, err
	}
	decoder := resultmerge.NewExtractorDecoder(resultmerge.ExtractorDecoderConfig{AllowedSourceRefs: sourceRefs(previous, window)})
	service, err := resultmerge.New(services.BackgroundAux, parentCoordinator{port: p.parent, parent: parent}, decoder, resultmerge.Config{MaxCapsuleBytes: cfg.Capsule.MaxBytes, MaxCapsuleTokens: cfg.Capsule.MaxTokens})
	if err != nil {
		p.observeError(observability.StageBarrier, err, string(pendingJobID))
		return state, err
	}
	barrierTimeout := cfg.Barrier.Timeout
	if barrierTimeout <= 0 {
		barrierTimeout = minimumBarrierTimeout
	}
	barrierCtx, cancel := context.WithTimeout(ctx, barrierTimeout)
	defer cancel()
	started := time.Now()
	outcome, consumeErr := service.Consume(barrierCtx, resultmerge.Job{ID: pendingJobID, ParentBranchBinding: parent.Binding})
	barrierOutcome := observability.OutcomeCompleted
	switch outcome.Status {
	case resultmerge.StatusPending:
		barrierOutcome = observability.OutcomePending
	case resultmerge.StatusRejected:
		barrierOutcome = observability.OutcomeRejected
	}
	if consumeErr != nil {
		p.observeErrorDuration(observability.StageBarrier, consumeErr, string(pendingJobID), time.Since(started))
	} else {
		p.observe(observability.Observation{Stage: observability.StageBarrier, Outcome: barrierOutcome, CorrelationHash: observability.HashID(string(pendingJobID)), Duration: time.Since(started)})
	}
	if outcome.State.Revision != 0 {
		state.Revision = outcome.State.Revision
		state.CapsuleJSON = append([]byte(nil), outcome.State.CapsuleJSON...)
		state.CapsuleDigest = outcome.State.CapsuleDigest
		state.SourceHighWatermark = outcome.State.SourceHighWatermark
		state.PendingJobID = outcome.State.PendingJobID
		state.PendingJobTargetRevision = outcome.State.PendingJobTargetRevision
		state.PendingJobBranchBinding = outcome.State.PendingJobBranchBinding
		if len(state.CapsuleJSON) != 0 {
			if decoded, decodeErr := capsule.Decode(state.CapsuleJSON); decodeErr == nil {
				p.observeCapsule(barrierOutcome, decoded, len(state.CapsuleJSON), string(pendingJobID))
			}
		}
	}
	return state, consumeErr
}
