package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// conversationViewReader returns the optional narrow reader.
func (e *Executor) conversationViewReader() conversationprojection.Reader {
	if e == nil {
		return nil
	}
	return e.ConversationViewReader
}

// conversationViewTagger returns the optional narrow tagger.
func (e *Executor) conversationViewTagger() ConversationViewTagger {
	if e == nil {
		return nil
	}
	return e.ConversationViewTagger
}

// conversationProjectionSummary is the bounded observable diagnostic for
// the 3.2 seam. It contains only counts/revisions/placement classes and
// StateRevision, never OverlayID, message identity/digest, or plaintext.
// Full ProjectionEvidence is retained internally for D14 final reassertion.
type conversationProjectionSummary struct {
	StateRevision uint64 `json:"state_revision"`
	FilteredCount int    `json:"filtered_count"`
	InjectedCount int    `json:"injected_count"`
	// Bounded placement/revision slot summaries (counts only, no IDs).
	StablePrefixCount  int    `json:"stable_prefix_count"`
	AfterMessageCount  int    `json:"after_message_count"`
	FallbackCount      int    `json:"fallback_count"`
	MaxOverlayRevision uint64 `json:"max_overlay_revision"`
	MaxSlotOrdinal     uint64 `json:"max_slot_ordinal"`
}

func newConversationProjectionSummary(snap conversationprojection.Snapshot, ev *conversationprojection.ProjectionEvidence) conversationProjectionSummary {
	if ev == nil {
		return conversationProjectionSummary{StateRevision: snap.StateRevision}
	}
	s := conversationProjectionSummary{
		StateRevision: snap.StateRevision,
		FilteredCount: ev.FilteredCount,
		InjectedCount: ev.InjectedCount,
		FallbackCount: len(ev.Fallbacks),
	}
	for _, p := range ev.Provenance {
		switch p.ResolvedKind {
		case conversationprojection.PlacementStablePrefix:
			s.StablePrefixCount++
		case conversationprojection.PlacementAfterMessage:
			s.AfterMessageCount++
		}
		if p.Revision > s.MaxOverlayRevision {
			s.MaxOverlayRevision = p.Revision
		}
		if p.SlotOrdinal > s.MaxSlotOrdinal {
			s.MaxSlotOrdinal = p.SlotOrdinal
		}
	}
	return s
}

// snapshotAndProject performs the single coherent per-turn snapshot read
// and pure projection. It is the 3.2 seam: after authoritative A-leg
// resolution (seal 3.1) and before backend request/pre-request transforms,
// context estimation, billing, routing/capability/baseline.
// Fail-closed on lookup or projection errors; evidence is bounded
// content-free (counts/revisions/placement only).
func (e *Executor) snapshotAndProject(ctx context.Context, aLegID string, call aipapi.Call) (conversationprojection.Snapshot, *conversationprojection.ProjectionEvidence, aipapi.Call, error) {
	reader := e.conversationViewReader()
	if reader == nil {
		// No capability: fast path preserves identity to avoid false mutate
		// detection in policy evidence (empty projection is a no-op).
		empty := conversationprojection.Snapshot{}
		return empty, &conversationprojection.ProjectionEvidence{}, call, nil
	}
	snap, err := reader.Snapshot(ctx, aLegID)
	if err != nil {
		if obs := e.conversationViewObserver(); obs != nil {
			safeObserver{obs: obs}.OnProjectionFailure(conversationprojection.StageEarly)
		}
		return conversationprojection.Snapshot{}, nil, aipapi.Call{}, fmt.Errorf("executor: conversation view snapshot: %w", err)
	}

	// Fast path: empty snapshot must remain identity-preserving (no clone)
	// to keep no-op evidence EffectNone and avoid spurious canonical diff.
	if len(snap.NeverBackend) == 0 && len(snap.Steering) == 0 {
		return snap, &conversationprojection.ProjectionEvidence{}, call, nil
	}
	out, ev, err := conversationprojection.Project(call, snap)
	if err != nil {
		if obs := e.conversationViewObserver(); obs != nil {
			safe := safeObserver{obs: obs}
			safe.OnProjectionFailure(conversationprojection.StageEarly)
			if errors.Is(err, conversationprojection.ErrAnchorMissing) || errors.Is(err, conversationprojection.ErrAnchorNotFound) {
				safe.OnAnchorFailure(conversationprojection.AnchorFailClosed)
			}
		}
		return conversationprojection.Snapshot{}, nil, aipapi.Call{}, fmt.Errorf("executor: conversation view projection: %w", err)
	}
	// Emit bounded diagnostics via narrow observer seam.
	if obs := e.conversationViewObserver(); obs != nil {
		safe := safeObserver{obs: obs}
		summary := conversationprojection.NewProjectionSummary(snap, ev)
		safe.OnProjection(conversationprojection.StageEarly, summary)
		for range ev.Fallbacks {
			safe.OnAnchorFallback(conversationprojection.StageEarly, conversationprojection.AnchorStablePrefixFallback)
		}
	}
	// Evidence already bounded: counts, revisions, placement classes, no plaintext
	return snap, ev, out, nil
}

// conversationViewObserver returns the optional narrow observer (nil is no-op).
func (e *Executor) conversationViewObserver() ConversationViewObserver {
	if e == nil {
		return nil
	}
	return e.ConversationViewObserver
}

type safeObserver struct {
	obs ConversationViewObserver
}

func (s safeObserver) OnProjection(stage string, summary conversationprojection.ProjectionSummary) {
	if s.obs == nil {
		return
	}
	defer func() { _ = recover() }()
	s.obs.OnProjection(stage, summary)
}

func (s safeObserver) OnProjectionFailure(stage string) {
	if s.obs == nil {
		return
	}
	defer func() { _ = recover() }()
	s.obs.OnProjectionFailure(stage)
}

func (s safeObserver) OnAnchorFallback(stage string, policy conversationprojection.AnchorMissingPolicy) {
	if s.obs == nil {
		return
	}
	defer func() { _ = recover() }()
	s.obs.OnAnchorFallback(stage, policy)
}

func (s safeObserver) OnAnchorFailure(policy conversationprojection.AnchorMissingPolicy) {
	if s.obs == nil {
		return
	}
	defer func() { _ = recover() }()
	s.obs.OnAnchorFailure(policy)
}
