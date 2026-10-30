package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	featurecontinuity "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

var ErrInvalidCompactionContinuityParentPort = errors.New("featurehost/compaction: invalid compaction-continuity parent port")

// ParentPort is the featurehost-owned adapter for the process coordinator.
// The coordinator remains the only state/CAS authority; this adapter retains a
// bounded-in-purpose binding-to-key map because the feature deliberately
// exposes only opaque branch bindings after Capture.
//
// Parent identity is taken from trusted execution context and detector
// metadata. The canonical Call is intentionally not consulted for identity:
// detached calls carry a private child A-leg and must never replace the
// authoritative parent A-leg.
type ParentPort struct {
	coordinator *state.BranchCoordinator

	mu      sync.RWMutex
	keys    map[string]state.BranchKey
	maxKeys int
}

// NewParentPort constructs an authoritative ParentPort adapter backed by the coordinator.
func NewParentPort(coordinator *state.BranchCoordinator) (*ParentPort, error) {
	if coordinator == nil {
		return nil, fmt.Errorf("%w: nil branch coordinator", ErrInvalidCompactionContinuityParentPort)
	}
	return &ParentPort{
		coordinator: coordinator,
		keys:        make(map[string]state.BranchKey),
		maxKeys:     state.DefaultMaxEntries,
	}, nil
}

// Capture derives and records the authoritative parent before any detached
// child is created. For an already-detached context, the trusted parent
// marker wins over PreservationMeta's child A-leg.
func (p *ParentPort) Capture(ctx context.Context, _ aipapi.Call, meta compaction.PreservationMeta) (featurecontinuity.ParentBranch, error) {
	return p.capture(ctx, meta)
}

// CaptureMeta is the response-boundary capture path. It deliberately shares
// the same trusted identity derivation as Capture and therefore cannot bind a
// result to the private child A-leg present in response metadata.
func (p *ParentPort) CaptureMeta(ctx context.Context, meta compaction.PreservationMeta) (featurecontinuity.ParentBranch, error) {
	return p.capture(ctx, meta)
}

func (p *ParentPort) capture(ctx context.Context, meta compaction.PreservationMeta) (featurecontinuity.ParentBranch, error) {
	if p == nil || p.coordinator == nil {
		return featurecontinuity.ParentBranch{}, ErrInvalidCompactionContinuityParentPort
	}
	key, traceID, aLegID, err := trustedParentKey(ctx, meta)
	if err != nil {
		return featurecontinuity.ParentBranch{}, err
	}
	binding, err := p.coordinator.Capture(ctx, key)
	if err != nil {
		return featurecontinuity.ParentBranch{}, err
	}
	p.remember(binding, key)
	return featurecontinuity.ParentBranch{
		Binding: binding,
		TraceID: traceID,
		ALegID:  aLegID,
		BLegID:  strings.TrimSpace(meta.BLegID),
	}, nil
}

func (p *ParentPort) remember(binding string, key state.BranchKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.keys[binding]; !exists && p.maxKeys > 0 && len(p.keys) >= p.maxKeys {
		// BranchCoordinator owns expiry and cardinality. This defensive cap keeps
		// the opaque reverse map bounded even when old branches have expired.
		for stale := range p.keys {
			delete(p.keys, stale)
			break
		}
	}
	p.keys[binding] = key
}

func (p *ParentPort) Snapshot(ctx context.Context, parent featurecontinuity.ParentBranch) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, found, err := p.coordinator.Snapshot(ctx, key)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	if !found {
		return featurecontinuity.ParentState{}, state.ErrBranchNotFound
	}
	return featureParentState(st), nil
}

func (p *ParentPort) CommitSource(ctx context.Context, parent featurecontinuity.ParentBranch, revision uint64, src []byte, watermark string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.CommitSource(ctx, key, revision, src, watermark)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) CommitCapsule(ctx context.Context, parent featurecontinuity.ParentBranch, revision uint64, capsule []byte, digest [32]byte, watermark string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.CommitCapsule(ctx, key, revision, capsule, digest, watermark)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) RecordPendingJob(ctx context.Context, parent featurecontinuity.ParentBranch, jobID auxiliary.JobID, revision uint64) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.RecordPendingJob(ctx, key, jobID, revision)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) ValidatePendingJob(ctx context.Context, parent featurecontinuity.ParentBranch, jobID auxiliary.JobID) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.ValidatePendingJob(ctx, key, jobID)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) CommitCapsuleForJob(ctx context.Context, parent featurecontinuity.ParentBranch, jobID auxiliary.JobID, resultBinding string, revision uint64, capsule []byte, digest [32]byte, watermark string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	if strings.TrimSpace(resultBinding) != parent.Binding {
		return featurecontinuity.ParentState{}, state.ErrBranchMismatch
	}
	st, err := p.coordinator.CommitCapsuleForJob(ctx, key, jobID, resultBinding, revision, capsule, digest, watermark)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) RecordPreviewIntent(ctx context.Context, parent featurecontinuity.ParentBranch, intent featurecontinuity.PreviewIntent) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.RecordPreviewIntent(ctx, key, state.PreviewIntent{Key: intent.Key, TargetSourceRevision: intent.TargetSourceRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) BindPreviewIntent(ctx context.Context, parent featurecontinuity.ParentBranch, intentKey, transactionID string) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.BindPreviewIntent(ctx, key, intentKey, transactionID)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) SetPendingInjection(ctx context.Context, parent featurecontinuity.ParentBranch, target featurecontinuity.InjectionTarget) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.SetPendingInjection(ctx, key, state.InjectionTarget{BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) ValidateInjection(ctx context.Context, parent featurecontinuity.ParentBranch, target featurecontinuity.InjectionTarget) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.ValidateInjection(ctx, key, state.InjectionTarget{BoundaryKey: target.BoundaryKey, CapsuleRevision: target.CapsuleRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) CommitReleasedInjection(ctx context.Context, parent featurecontinuity.ParentBranch, watermark featurecontinuity.InjectionWatermark) (featurecontinuity.ParentState, error) {
	key, err := p.key(parent)
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	st, err := p.coordinator.CommitReleasedInjection(ctx, key, state.InjectionWatermark{BranchBinding: watermark.BranchBinding, BoundaryKey: watermark.BoundaryKey, CapsuleRevision: watermark.CapsuleRevision})
	if err != nil {
		return featurecontinuity.ParentState{}, err
	}
	return featureParentState(st), nil
}

func (p *ParentPort) key(parent featurecontinuity.ParentBranch) (state.BranchKey, error) {
	if p == nil || p.coordinator == nil {
		return state.BranchKey{}, ErrInvalidCompactionContinuityParentPort
	}
	binding := strings.TrimSpace(parent.Binding)
	if binding == "" {
		return state.BranchKey{}, state.ErrBranchMismatch
	}
	p.mu.RLock()
	key, ok := p.keys[binding]
	p.mu.RUnlock()
	if !ok || key.Binding() != binding {
		return state.BranchKey{}, state.ErrBranchMismatch
	}
	return key, nil
}

func featureParentState(st state.BranchState) featurecontinuity.ParentState {
	var pendingPreviewIntent *featurecontinuity.PreviewIntent
	if st.PendingPreviewIntent != nil {
		value := featurecontinuity.PreviewIntent{Key: st.PendingPreviewIntent.Key, TargetSourceRevision: st.PendingPreviewIntent.TargetSourceRevision}
		pendingPreviewIntent = &value
	}
	var pendingInjection *featurecontinuity.InjectionTarget
	if st.PendingInjection != nil {
		value := featurecontinuity.InjectionTarget{BoundaryKey: st.PendingInjection.BoundaryKey, CapsuleRevision: st.PendingInjection.CapsuleRevision}
		pendingInjection = &value
	}
	var lastReleasedInjection *featurecontinuity.InjectionWatermark
	if st.LastReleasedInjection != nil {
		value := featurecontinuity.InjectionWatermark{BranchBinding: st.LastReleasedInjection.BranchBinding, BoundaryKey: st.LastReleasedInjection.BoundaryKey, CapsuleRevision: st.LastReleasedInjection.CapsuleRevision}
		lastReleasedInjection = &value
	}
	return featurecontinuity.ParentState{
		Revision:                  st.Revision,
		CapsuleJSON:               append([]byte(nil), st.CapsuleJSON...),
		CapsuleDigest:             st.CapsuleDigest,
		SourceJSON:                append([]byte(nil), st.SanitizedSourceJSON...),
		SourceHighWatermark:       st.SourceHighWatermark,
		PendingJobID:              st.PendingJobID,
		PendingJobTargetRevision:  st.PendingJobTargetRevision,
		PendingJobBranchBinding:   st.PendingJobBranchBinding,
		PendingPreviewIntent:      pendingPreviewIntent,
		PendingInjection:          pendingInjection,
		LastReleasedInjection:     lastReleasedInjection,
		LastCompactionTransaction: st.LastCompactionTransaction,
	}
}

// trustedParentKey uses execctx's trusted detached marker and generation views
// first. PreservationMeta is detector-produced metadata and is used only when
// no stronger value is available; canonical call session fields are ignored.
func trustedParentKey(ctx context.Context, meta compaction.PreservationMeta) (state.BranchKey, string, string, error) {
	sessionID := strings.TrimSpace(meta.SessionID)
	aLegID := strings.TrimSpace(meta.ALegID)
	traceID := strings.TrimSpace(meta.TraceID)
	var principalPartition string
	trustedContext := false

	if detached, ok := execctx.DetachedSessionFromContext(ctx); ok {
		trustedContext = true
		// The metadata on a detached request describes the private child. Only
		// the trusted parent marker may supply the continuity identity here.
		sessionID = strings.TrimSpace(detached.ParentSessionID)
		aLegID = strings.TrimSpace(detached.ParentALegID)
		if strings.TrimSpace(detached.ParentTraceID) != "" {
			traceID = strings.TrimSpace(detached.ParentTraceID)
		}
	} else if views, ok := execctx.FromContext(ctx); ok {
		trustedContext = true
		if sid := strings.TrimSpace(views.Session.AuthoritativeSessionID); sid != "" {
			if sessionID != "" && sessionID != sid {
				return state.BranchKey{}, "", "", fmt.Errorf("%w: trusted session mismatch", state.ErrBranchMismatch)
			}
			sessionID = sid
		}
		if aleg := strings.TrimSpace(views.Session.ALegID); aleg != "" {
			if aLegID != "" && aLegID != aleg {
				return state.BranchKey{}, "", "", fmt.Errorf("%w: trusted A-leg mismatch", state.ErrBranchMismatch)
			}
			aLegID = aleg
		}
		if strings.TrimSpace(views.Attempt.TraceID) != "" && traceID == "" {
			traceID = strings.TrimSpace(views.Attempt.TraceID)
		}
		principalPartition = trustedPrincipalPartition(views.Scope, views.Principal.ID)
	} else if trustedScope, ok := scope.ScopeFromContext(ctx); ok {
		trustedContext = true
		principalPartition = trustedPrincipalPartition(trustedScope, "")
	}
	if !trustedContext {
		return state.BranchKey{}, "", "", fmt.Errorf("%w: trusted execution context is required", state.ErrInvalidBranchKey)
	}

	if sessionID == "" && principalPartition == "" {
		return state.BranchKey{}, "", "", fmt.Errorf("%w: secure session or trusted principal is required", state.ErrInvalidBranchKey)
	}
	key, err := state.CaptureParentBranchKey(sessionID, aLegID, principalPartition)
	if err != nil {
		return state.BranchKey{}, "", "", err
	}
	return key, traceID, aLegID, nil
}

func trustedPrincipalPartition(v scope.PrincipalScopeView, projectedPrincipal string) string {
	if value := strings.TrimSpace(v.PrincipalID.String()); value != "" {
		return value
	}
	if value := strings.TrimSpace(projectedPrincipal); value != "" {
		return value
	}
	if value := strings.TrimSpace(v.TenantID.String()); value != "" {
		return "tenant:" + value
	}
	return ""
}

var _ featurecontinuity.ParentPort = (*ParentPort)(nil)
