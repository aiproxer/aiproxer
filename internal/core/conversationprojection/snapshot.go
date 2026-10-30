package conversationprojection

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ReasonCode is a non-secret identifier for diagnostics.
type ReasonCode string

func (r ReasonCode) Validate() error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: reason code is required", ErrInvalidReasonCode)
	}
	if len(s) > 64 {
		return fmt.Errorf("%w: reason code exceeds 64 bytes", ErrInvalidReasonCode)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: reason code must be ascii", ErrInvalidReasonCode)
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("%w: invalid character %q in reason code", ErrInvalidReasonCode, ch)
		}
	}
	return nil
}

// OverlayID alias for documentation.
type OverlayID = string

// PlacementKind enumerates durable steering placement.
type PlacementKind string

const (
	PlacementStablePrefix PlacementKind = "stable_prefix"
	PlacementAfterMessage PlacementKind = "after_message"
)

func (k PlacementKind) Validate() error {
	switch k {
	case PlacementStablePrefix, PlacementAfterMessage:
		return nil
	default:
		return fmt.Errorf("%w: unknown placement %q", ErrInvalidPlacement, k)
	}
}

// AnchorMissingPolicy controls behavior when a fixed anchor disappears.
type AnchorMissingPolicy string

const (
	AnchorStablePrefixFallback AnchorMissingPolicy = "stable_prefix_fallback"
	AnchorFailClosed           AnchorMissingPolicy = "fail_closed"
)

func (p AnchorMissingPolicy) Validate() error {
	switch p {
	case AnchorStablePrefixFallback, AnchorFailClosed:
		return nil
	default:
		return fmt.Errorf("%w: unknown policy %q", ErrInvalidAnchorMissingPolicy, p)
	}
}

// Placement describes where an overlay should be injected.
type Placement struct {
	Kind   PlacementKind  `json:"kind"`
	Anchor *MessageAnchor `json:"anchor,omitempty"`
}

func (p Placement) Validate() error {
	if err := p.Kind.Validate(); err != nil {
		return err
	}
	if p.Kind == PlacementAfterMessage {
		if p.Anchor == nil {
			return fmt.Errorf("%w: after_message placement requires anchor", ErrInvalidPlacement)
		}
		if err := p.Anchor.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPlacement, err)
		}
	} else {
		if p.Anchor != nil {
			return fmt.Errorf("%w: stable_prefix must not have anchor", ErrInvalidPlacement)
		}
	}
	return nil
}

// OverlayMessage is the model-visible steering payload.
type OverlayMessage struct {
	Role aipapi.Role `json:"role"`
	Text string      `json:"text"`
}

// Overlay is the pure immutable per-turn steering record.
type Overlay struct {
	OverlayID           string              `json:"overlay_id"`
	Revision            uint64              `json:"revision"`
	SlotOrdinal         uint64              `json:"slot_ordinal"`
	Active              bool                `json:"active"`
	Message             OverlayMessage      `json:"message"`
	Placement           Placement           `json:"placement"`
	AnchorMissingPolicy AnchorMissingPolicy `json:"anchor_missing_policy"`
}

func (o Overlay) Clone() Overlay {
	out := o
	if o.Placement.Anchor != nil {
		cp := *o.Placement.Anchor
		out.Placement.Anchor = &cp
	}
	return out
}

// Tag is an A-leg never_backend exclusion record.
type Tag struct {
	Identity  MessageIdentity `json:"identity"`
	Reason    ReasonCode      `json:"reason,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

func (t Tag) Validate() error {
	return t.Identity.Validate()
}

// Snapshot is the coherent per-turn conversation view.
type Snapshot struct {
	StateRevision uint64    `json:"state_revision"`
	NeverBackend  []Tag     `json:"never_backend"`
	Steering      []Overlay `json:"steering"`
}

// Reader is the narrow read-only port for obtaining an A-leg snapshot.
type Reader interface {
	Snapshot(ctx context.Context, aLegID string) (Snapshot, error)
}

// Stage identifies the projection stage in diagnostics.
type Stage = string

const (
	StageEarly      Stage = "early"
	StageFinal      Stage = "final"
	StageLate       Stage = "late"
	StageSDKResolve Stage = "sdk_resolve"
)

// ProjectionSummary is a bounded, content-free diagnostic summary of a projection operation.
// It contains only counts, revisions, and placement classes (no plaintext, IDs, or digests).
type ProjectionSummary struct {
	StateRevision      uint64 `json:"state_revision"`
	FilteredCount      int    `json:"filtered_count"`
	InjectedCount      int    `json:"injected_count"`
	StablePrefixCount  int    `json:"stable_prefix_count"`
	AfterMessageCount  int    `json:"after_message_count"`
	FallbackCount      int    `json:"fallback_count"`
	MaxOverlayRevision uint64 `json:"max_overlay_revision"`
	MaxSlotOrdinal     uint64 `json:"max_slot_ordinal"`
}

// NewProjectionSummary builds a content-free ProjectionSummary from a snapshot and evidence.
func NewProjectionSummary(snap Snapshot, ev *ProjectionEvidence) ProjectionSummary {
	if ev == nil {
		return ProjectionSummary{StateRevision: snap.StateRevision}
	}
	s := ProjectionSummary{
		StateRevision: snap.StateRevision,
		FilteredCount: ev.FilteredCount,
		InjectedCount: ev.InjectedCount,
		FallbackCount: len(ev.Fallbacks),
	}
	for _, p := range ev.Provenance {
		switch p.ResolvedKind {
		case PlacementStablePrefix:
			s.StablePrefixCount++
		case PlacementAfterMessage:
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

// Observer receives bounded, content-free diagnostics for conversation-view events.
type Observer interface {
	OnProjection(stage Stage, summary ProjectionSummary)
	OnProjectionFailure(stage Stage)
	OnAnchorFallback(stage Stage, policy AnchorMissingPolicy)
	OnAnchorFailure(policy AnchorMissingPolicy)
}
