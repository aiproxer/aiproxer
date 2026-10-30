package compactioncontinuity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/carriers"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/extractor"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/observability"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/source"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

// ParentBranch is the bounded authority captured before any detached child is
// created. Binding is the only value used as a continuity lookup key. The
// remaining fields are correlation metadata for the detached request and are
// never included in its prompt.
type ParentBranch struct {
	Binding string
	TraceID string
	ALegID  string
	BLegID  string
}

// ParentState is an opaque defensive snapshot of one authoritative parent.
// CapsuleJSON and SourceJSON are feature-owned serialized values; a runtime
// adapter remains responsible for persistence, bounds, and compare-and-swap.
type ParentState struct {
	Revision                  uint64
	CapsuleJSON               []byte
	CapsuleDigest             [32]byte
	SourceJSON                []byte
	SourceHighWatermark       string
	PendingJobID              auxiliary.JobID
	PendingJobTargetRevision  uint64
	PendingJobBranchBinding   string
	PendingPreviewIntent      *PreviewIntent
	PendingInjection          *InjectionTarget
	LastReleasedInjection     *InjectionWatermark
	LastCompactionTransaction string
}

// ParentPort is the smallest authority/state seam consumed by this feature.
// Capture must derive the parent from trusted scope in ctx plus PreservationMeta;
// it must not select a branch from child A-leg or untrusted call hints.
// ValidatePendingJob is the per-parent late-result access guard used by later
// response-boundary work; RequestOpened itself never awaits a result.
type ParentPort interface {
	Capture(context.Context, aipapi.Call, compaction.PreservationMeta) (ParentBranch, error)
	CaptureMeta(context.Context, compaction.PreservationMeta) (ParentBranch, error)
	Snapshot(context.Context, ParentBranch) (ParentState, error)
	CommitSource(context.Context, ParentBranch, uint64, []byte, string) (ParentState, error)
	CommitCapsule(context.Context, ParentBranch, uint64, []byte, [32]byte, string) (ParentState, error)
	RecordPendingJob(context.Context, ParentBranch, auxiliary.JobID, uint64) (ParentState, error)
	ValidatePendingJob(context.Context, ParentBranch, auxiliary.JobID) (ParentState, error)
	CommitCapsuleForJob(context.Context, ParentBranch, auxiliary.JobID, string, uint64, []byte, [32]byte, string) (ParentState, error)
	RecordPreviewIntent(context.Context, ParentBranch, PreviewIntent) (ParentState, error)
	BindPreviewIntent(context.Context, ParentBranch, string, string) (ParentState, error)
	SetPendingInjection(context.Context, ParentBranch, InjectionTarget) (ParentState, error)
	ValidateInjection(context.Context, ParentBranch, InjectionTarget) (ParentState, error)
	CommitReleasedInjection(context.Context, ParentBranch, InjectionWatermark) (ParentState, error)
}

// Plugin is the feature-owned successful-Open preserver. It has no global
// state; config is normalized once and the parent port is explicitly bound.
type Plugin struct {
	cfg      Config
	parent   ParentPort
	obs      observability.Sink
	markerMu sync.Mutex
	markers  map[string]preparedInjectionMarker
}

// New constructs one immutable-generation feature instance.
func New(cfg Config, parent ParentPort) (*Plugin, error) {
	return NewWithObservability(cfg, parent, nil)
}

// NewWithObservability constructs one immutable-generation feature instance
// with an optional content-free diagnostics sink. The sink is deliberately a
// feature-local seam; scheduler queue, token, cost and accounting truth stays
// in the existing auxiliary/billing surfaces.
func NewWithObservability(cfg Config, parent ParentPort, sink observability.Sink) (*Plugin, error) {
	if parent == nil {
		return nil, errors.New("compaction-continuity: parent port is required")
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	return &Plugin{cfg: normalized.Snapshot(), parent: parent, obs: sink, markers: make(map[string]preparedInjectionMarker)}, nil
}

// FeatureBundleWithPort contributes the feature callback after runtime
// composition has supplied the process-owned parent authority port.
func FeatureBundleWithPort(cfg Config, parent ParentPort) (aipfeature.FeatureBundle, error) {
	return FeatureBundleWithPortAndObservability(cfg, parent, nil)
}

// FeatureBundleWithPortAndObservability is the explicit composition seam for
// a host that has a content-free feature diagnostics sink. A nil sink keeps
// the existing no-observability behavior.
func FeatureBundleWithPortAndObservability(cfg Config, parent ParentPort, sink observability.Sink) (aipfeature.FeatureBundle, error) {
	p, err := NewWithObservability(cfg, parent, sink)
	if err != nil {
		return aipfeature.FeatureBundle{}, err
	}
	cs := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(cs, aipfeature.PlaneCompactionPreservers, ID, []compaction.Preserver{p}); err != nil {
		return aipfeature.FeatureBundle{}, fmt.Errorf("%s: %w", ID, err)
	}
	return aipfeature.BundleFromPlanes(cs.Freeze(), nil), nil
}

// FeatureBundle retains the registry's configuration-only compatibility
// surface. Runtime composition must use FeatureBundleWithPort to enable work.
func FeatureBundle(_ Config) aipfeature.FeatureBundle {
	return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}
}

func (p *Plugin) ID() string { return ID }

// observe is intentionally panic-isolated. Diagnostics must never become a
// source of primary retry/failover authority or change callback behavior.
func (p *Plugin) observe(sample observability.Observation) {
	if p == nil || p.obs == nil {
		return
	}
	if sample.Count == 0 {
		sample.Count = 1
	}
	defer func() { _ = recover() }()
	p.obs.Observe(sample)
}

// observeFailure records a local fail-open outcome without accepting an error
// string, prompt, output, capsule or BranchKey. correlation is hashed before
// it reaches the sink and detail is intentionally unused.
func (p *Plugin) observeFailure(stage observability.Stage, outcome observability.Outcome, correlation, _ string) {
	p.observe(observability.Observation{Stage: stage, Outcome: outcome, CorrelationHash: observability.HashID(correlation)})
}

func (p *Plugin) observeError(stage observability.Stage, err error, correlation string) {
	p.observe(observability.Observation{Stage: stage, Outcome: errorOutcome(err), CorrelationHash: observability.HashID(correlation)})
}

func (p *Plugin) observeErrorDuration(stage observability.Stage, err error, correlation string, duration time.Duration) {
	p.observe(observability.Observation{Stage: stage, Outcome: errorOutcome(err), CorrelationHash: observability.HashID(correlation), Duration: duration})
}

func errorOutcome(err error) observability.Outcome {
	if err == nil {
		return observability.OutcomeFailed
	}
	text := strings.ToLower(err.Error())
	outcome := observability.OutcomeFailed
	switch {
	case errors.Is(err, resultmerge.ErrRejected):
		outcome = observability.OutcomeRejected
	case errors.Is(err, resultmerge.ErrAwaitTimeout), errors.Is(err, context.DeadlineExceeded):
		outcome = observability.OutcomeTimeout
	case errors.Is(err, context.Canceled):
		outcome = observability.OutcomeCanceled
	case errors.Is(err, resultmerge.ErrStaleResult):
		outcome = observability.OutcomeStale
	case errors.Is(err, capsule.ErrInvalidBranch), errors.Is(err, capsule.ErrBranchMismatch):
		outcome = observability.OutcomeInvalid
	case errors.Is(err, resultmerge.ErrInvalidJob), errors.Is(err, resultmerge.ErrInvalidParentState):
		outcome = observability.OutcomeInvalid
	case strings.Contains(text, "queue"), strings.Contains(text, "saturat"):
		outcome = observability.OutcomeSaturated
	case strings.Contains(text, "timeout"), strings.Contains(text, "deadline"):
		outcome = observability.OutcomeTimeout
	case strings.Contains(text, "cancel"):
		outcome = observability.OutcomeCanceled
	case strings.Contains(text, "admission"), strings.Contains(text, "credit"), strings.Contains(text, "denied"), strings.Contains(text, "not configured"), strings.Contains(text, "retain"):
		outcome = observability.OutcomeDenied
	case strings.Contains(text, "digest"):
		outcome = observability.OutcomeDigestMismatch
	case strings.Contains(text, "stale"), strings.Contains(text, "revision"), strings.Contains(text, "branch"):
		outcome = observability.OutcomeStale
	case strings.Contains(text, "conflict"):
		outcome = observability.OutcomeConflict
	case strings.Contains(text, "rollback"), strings.Contains(text, "restore"):
		outcome = observability.OutcomeRollback
	case strings.Contains(text, "invalid"), strings.Contains(text, "schema"), strings.Contains(text, "malformed"):
		outcome = observability.OutcomeInvalid
	}
	return outcome
}

func (p *Plugin) observeEvent(event compaction.Event) {
	outcome := observability.OutcomeObserved
	if event.TransactionID == "" {
		outcome = observability.OutcomeSkipped
	}
	p.observe(observability.Observation{
		Stage:           observability.StageEvent,
		Outcome:         outcome,
		Evidence:        observability.BoundedID(string(event.Evidence)),
		RuleID:          observability.BoundedID(event.RuleID),
		Phase:           observability.BoundedID(string(event.Phase)),
		CorrelationHash: observability.HashID(event.TransactionID),
	})
}

func (p *Plugin) observeCapsule(outcome observability.Outcome, value capsule.Envelope, size int, correlation string) {
	facts := len(value.Plan.Steps) + len(value.Decisions) + len(value.Constraints) + len(value.RejectedAlternatives) + len(value.OpenQuestions)
	p.observe(observability.Observation{
		Stage:           observability.StageCapsule,
		Outcome:         outcome,
		CorrelationHash: observability.HashID(correlation),
		Revision:        value.Revision,
		SizeBytes:       size,
		FactCount:       facts,
	})
}

// RequestOpened schedules at most one detached extraction for one committed
// detector transaction. All failures are deliberately feature-local and
// fail-open because the primary request is already open.
func (p *Plugin) RequestOpened(ctx context.Context, call aipapi.Call, events []compaction.Event, meta compaction.PreservationMeta, services compaction.Services) (err error) {
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
	for _, event := range events {
		p.observeEvent(event)
	}
	if p == nil || p.parent == nil || ctx == nil || len(events) == 0 {
		return nil
	}
	if services.BackgroundAux == nil {
		p.observeFailure(observability.StageJob, observability.OutcomeDenied, meta.TransactionID, "")
		return nil
	}
	cfg, enabled := p.effectiveConfig(ctx)
	if !enabled {
		p.observeFailure(observability.StageEligibility, observability.OutcomeSkipped, meta.TransactionID, "")
		return nil
	}
	event, ok := committedEvent(events)
	if !ok {
		p.observeFailure(observability.StageEvent, observability.OutcomeSkipped, meta.TransactionID, "")
		return nil
	}

	parent, err := p.parent.Capture(ctx, call, meta)
	if err != nil || strings.TrimSpace(parent.Binding) == "" {
		p.observeError(observability.StageCallback, err, meta.TransactionID)
		return nil
	}
	if strings.TrimSpace(parent.TraceID) == "" {
		parent.TraceID = strings.TrimSpace(meta.TraceID)
	}
	if strings.TrimSpace(parent.ALegID) == "" {
		parent.ALegID = strings.TrimSpace(meta.ALegID)
	}
	if strings.TrimSpace(parent.BLegID) == "" {
		parent.BLegID = strings.TrimSpace(event.BLegID)
	}
	if !validParentBranch(parent) {
		p.observeFailure(observability.StageCallback, observability.OutcomeInvalid, meta.TransactionID, "")
		return nil
	}

	state, err := p.parent.Snapshot(ctx, parent)
	if err != nil {
		p.observeError(observability.StageCallback, err, meta.TransactionID)
		return nil
	}
	previewBound := false
	if state.PendingPreviewIntent != nil {
		bound, bindErr := p.parent.BindPreviewIntent(ctx, parent, state.PendingPreviewIntent.Key, event.TransactionID)
		if bindErr == nil {
			state = bound
			previewBound = true
			p.observe(observability.Observation{Stage: observability.StagePreviewIntent, Outcome: observability.OutcomeBound, CorrelationHash: observability.HashID(event.TransactionID)})
		} else {
			p.observeError(observability.StagePreviewIntent, bindErr, event.TransactionID)
		}
	}
	previous, sourceWindow, err := p.previousState(parent, state)
	if err != nil {
		p.observeError(observability.StageCapsule, err, event.TransactionID)
		return nil
	}

	prepared, err := source.Prepare(ctx, source.Input{
		Call:       call,
		Existing:   sourceWindow,
		Previous:   sourceWindow.HighWatermark,
		Recognizer: carrierRecognizer{},
		Config: source.Config{
			MaxBytes: stateBound(cfg.Source.MaxBytes, source.DefaultConfig().MaxBytes),
		},
	})
	if err != nil {
		p.observeError(observability.StageCallback, err, event.TransactionID)
		return nil
	}

	// Source/high-watermark commit is intentionally before deterministic plan
	// and semantic eligibility. The source is canonical JSON, bounded by the
	// source package, and contains no parent identifiers or raw tool dumps.
	watermark := encodeWatermark(prepared.HighWatermark)
	state, err = p.parent.CommitSource(ctx, parent, state.Revision, []byte(prepared.Envelope.Canonical()), watermark)
	if err != nil {
		p.observeError(observability.StageCapsule, err, event.TransactionID)
		return nil
	}

	deterministicCount := 0
	capsuleDirty := len(state.CapsuleJSON) == 0
	if len(previous.BranchBinding) == 0 {
		previous, err = capsule.New(parent.Binding)
		if err != nil {
			return nil
		}
		capsuleDirty = true
	}
	for _, entry := range prepared.NewEntries {
		if entry.Carrier == nil || !cfg.Preserve.Plan {
			if entry.Carrier != nil {
				p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: observability.OutcomeUnmatched, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(event.TransactionID)})
			}
			continue
		}
		update, matched, extractErr := extractCarrierUpdate(*entry.Carrier)
		if extractErr != nil || !matched {
			outcome := observability.OutcomeUnmatched
			if extractErr != nil {
				outcome = observability.OutcomeInvalid
			}
			p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: outcome, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(event.TransactionID)})
			continue
		}
		p.observe(observability.Observation{Stage: observability.StageCarrier, Outcome: observability.OutcomeMatched, RuleID: observability.BoundedID(entry.Carrier.Type), CorrelationHash: observability.HashID(event.TransactionID)})
		previous, err = carriers.Apply(previous, update)
		if err != nil {
			p.observeError(observability.StageCapsule, err, event.TransactionID)
			return nil
		}
		deterministicCount++
		capsuleDirty = true
	}

	if capsuleDirty {
		previous, err = capsule.PruneWithLimits(previous, capsule.Limits{
			MaxBytes:  cfg.Capsule.MaxBytes,
			MaxTokens: cfg.Capsule.MaxTokens,
		})
		if err != nil {
			p.observeError(observability.StageCapsule, err, event.TransactionID)
			return nil
		}
		serialized, encodeErr := capsule.Encode(previous)
		if encodeErr != nil {
			p.observeError(observability.StageCapsule, encodeErr, event.TransactionID)
			return nil
		}
		digest, digestErr := digestArray(previous.ContentDigest)
		if digestErr != nil {
			p.observeError(observability.StageCapsule, digestErr, event.TransactionID)
			return nil
		}
		state, err = p.parent.CommitCapsule(ctx, parent, state.Revision, serialized, digest, watermark)
		if err != nil {
			p.observeError(observability.StageCapsule, err, event.TransactionID)
			return nil
		}
		p.observeCapsule(observability.OutcomeCommitted, previous, len(serialized), event.TransactionID)
	}
	if previewBound && state.PendingInjection != nil && state.PendingInjection.BoundaryKey != event.TransactionID && injectionContainsProjection(call, previous, parent.Binding, cfg) {
		_, _ = p.parent.SetPendingInjection(ctx, parent, InjectionTarget{BoundaryKey: event.TransactionID, CapsuleRevision: state.PendingInjection.CapsuleRevision})
		p.rebindPreparedMarker(meta, event.TransactionID, state.PendingInjection.CapsuleRevision)
	}

	// A plan carrier is sufficient only when the operator requested plan-only
	// preservation. User decisions, constraints, rationale, or rejected
	// alternatives remain semantic categories and keep the model eligible.
	if !semanticExtractionEligible(cfg, prepared.Candidate || previewBound, deterministicCount > 0) || state.PendingJobID != "" {
		if state.PendingJobID != "" {
			p.observe(observability.Observation{Stage: observability.StageJob, Outcome: observability.OutcomeCoalesced, CorrelationHash: observability.HashID(event.TransactionID)})
		} else {
			p.observe(observability.Observation{Stage: observability.StageEligibility, Outcome: observability.OutcomeIneligible, CorrelationHash: observability.HashID(event.TransactionID), Count: 1})
		}
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StageEligibility, Outcome: observability.OutcomeEligible, CorrelationHash: observability.HashID(event.TransactionID)})
	coalesceKey := coalesceKey(parent.Binding, event.TransactionID, watermark)
	input := extractor.Input{
		Route:               cfg.Extractor.Route,
		Inherit:             cfg.Extractor.Inherit,
		InheritedRoute:      call.Route.Selector,
		ParentBranchBinding: parent.Binding,
		ParentTraceID:       parent.TraceID,
		ParentALegID:        parent.ALegID,
		ParentBLegID:        parent.BLegID,
		Previous:            previous,
		SanitizedDelta:      prepared.NewEntries,
		SourceHighWatermark: watermark,
		MaxInputTokens:      cfg.Extractor.MaxInputTokens,
		MaxOutputTokens:     cfg.Extractor.MaxOutputTokens,
		Timeout:             cfg.Extractor.Timeout,
	}
	jobID, err := extractor.Submit(ctx, services.BackgroundAux, input, coalesceKey)
	if err != nil {
		p.observeError(observability.StageJob, err, event.TransactionID)
		if strings.TrimSpace(string(jobID)) != "" {
			services.BackgroundAux.Forget(jobID)
		}
		return nil
	}
	if strings.TrimSpace(string(jobID)) == "" {
		p.observeFailure(observability.StageJob, observability.OutcomeRejected, event.TransactionID, "")
		return nil
	}
	p.observe(observability.Observation{Stage: observability.StageJob, Outcome: observability.OutcomeSubmitted, CorrelationHash: observability.HashID(string(jobID))})
	if _, err = p.parent.RecordPendingJob(ctx, parent, jobID, state.Revision); err != nil {
		p.observeError(observability.StageJob, err, event.TransactionID)
		// Submit has already crossed the accounting boundary. Forgetting only
		// releases unusable retained raw output; it does not cancel child usage.
		services.BackgroundAux.Forget(jobID)
	}
	return nil
}
