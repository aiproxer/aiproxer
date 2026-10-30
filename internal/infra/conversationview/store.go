package conversationview

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Bounds from requirements 3.4 and 9.17.
const (
	MaxNeverBackendTags   = 4096
	MaxActiveOverlays     = 64
	MaxSteeringTextBytes  = 64 * 1024
	MaxTotalSteeringBytes = 256 * 1024
	MaxReasonCodeBytes    = 64
	MaxOverlayIDBytes     = 128
	MaxALegIDBytes        = 256
)

// Re-exported kernel types and DTOs.
type (
	Snapshot            = conversationprojection.Snapshot
	Tag                 = conversationprojection.Tag
	Overlay             = conversationprojection.Overlay
	OverlayMessage      = conversationprojection.OverlayMessage
	Placement           = conversationprojection.Placement
	PlacementKind       = conversationprojection.PlacementKind
	AnchorMissingPolicy = conversationprojection.AnchorMissingPolicy
	ReasonCode          = conversationprojection.ReasonCode
	MessageIdentity     = conversationprojection.MessageIdentity
	MessageAnchor       = conversationprojection.MessageAnchor
	Reader              = conversationprojection.Reader
	ProjectionEvidence  = conversationprojection.ProjectionEvidence
	FallbackEvidence    = conversationprojection.FallbackEvidence
	OverlayProvenance   = conversationprojection.OverlayProvenance
	Stage               = conversationprojection.Stage
	ProjectionSummary   = conversationprojection.ProjectionSummary
)

// TagRequest is one element of a TagNeverBackend batch.
type TagRequest struct {
	Identity MessageIdentity `json:"identity"`
	Reason   ReasonCode      `json:"reason"`
}

func (r TagRequest) Validate() error {
	if err := r.Identity.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMessageIdentity, err)
	}
	return ValidateReasonCode(r.Reason)
}

// TagResult is returned from a successful TagNeverBackend call.
type TagResult struct {
	StateRevision uint64 `json:"state_revision"`
	Tags          []Tag  `json:"tags"`
}

// Re-exported placement and policy constants.
const (
	PlacementStablePrefix      = conversationprojection.PlacementStablePrefix
	PlacementAfterMessage      = conversationprojection.PlacementAfterMessage
	AnchorStablePrefixFallback = conversationprojection.AnchorStablePrefixFallback
	AnchorFailClosed           = conversationprojection.AnchorFailClosed
)

// Re-exported kernel projection functions.
var (
	Project                       = conversationprojection.Project
	Reassert                      = conversationprojection.Reassert
	FilterNeverBackend            = conversationprojection.FilterNeverBackend
	MessageIdentityOf             = conversationprojection.MessageIdentityOf
	ItemIdentityOf                = conversationprojection.ItemIdentityOf
	ComputeItemAnchors            = conversationprojection.ComputeItemAnchors
	ComputeCallAnchors            = conversationprojection.ComputeCallAnchors
	ResolveAfterIngressTailAnchor = conversationprojection.ResolveAfterIngressTailAnchor
	NewProjectionSummary          = conversationprojection.NewProjectionSummary
)

// ReasonCode validation.
func ValidateReasonCode(r ReasonCode) error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%w: reason code is required", ErrInvalidReasonCode)
	}
	if len(s) > MaxReasonCodeBytes {
		return fmt.Errorf("%w: reason code exceeds %d bytes", ErrInvalidReasonCode, MaxReasonCodeBytes)
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

// OverlayID alias for documentation; validated as bounded identifier.
type OverlayID = string

func ValidateOverlayID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: overlay id is required", ErrInvalidOverlayID)
	}
	if len(id) > MaxOverlayIDBytes {
		return fmt.Errorf("%w: overlay id exceeds %d bytes", ErrInvalidOverlayID, MaxOverlayIDBytes)
	}
	for _, ch := range id {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("%w: overlay id must be ascii", ErrInvalidOverlayID)
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("%w: invalid character %q in overlay id", ErrInvalidOverlayID, ch)
		}
	}
	return nil
}

func validateOverlayID(id string) error {
	return ValidateOverlayID(id)
}

func validateALegID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: a-leg id is required", ErrInvalidALegID)
	}
	if len(trimmed) > MaxALegIDBytes {
		return fmt.Errorf("%w: a-leg id exceeds %d bytes", ErrInvalidALegID, MaxALegIDBytes)
	}
	return nil
}

// StoredPlacement is the persisted placement for a steering overlay.
type StoredPlacement struct {
	Kind   PlacementKind  `json:"kind"`
	Anchor *MessageAnchor `json:"anchor,omitempty"`
}

func (sp StoredPlacement) Validate() error {
	if err := sp.Kind.Validate(); err != nil {
		return err
	}
	if sp.Kind == PlacementAfterMessage {
		if sp.Anchor == nil {
			return fmt.Errorf("%w: after_message placement requires anchor", ErrInvalidPlacement)
		}
		if err := sp.Anchor.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidPlacement, err)
		}
	} else {
		if sp.Anchor != nil {
			return fmt.Errorf("%w: stable_prefix must not have anchor", ErrInvalidPlacement)
		}
	}
	return nil
}

// StoredMessageV1 is the persisted model-visible steering payload.
type StoredMessageV1 struct {
	Role aipapi.Role `json:"role"`
	Text string      `json:"text"`
}

func (m StoredMessageV1) Validate() error {
	role := aipapi.Role(strings.TrimSpace(string(m.Role)))
	if role == "" {
		return fmt.Errorf("%w: steering role is required", ErrInvalidSteeringMessage)
	}
	switch role {
	case aipapi.RoleSystem, aipapi.RoleDeveloper, aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleTool:
	default:
		return fmt.Errorf("%w: invalid steering role %q", ErrInvalidSteeringMessage, role)
	}
	if len(m.Text) > MaxSteeringTextBytes {
		return fmt.Errorf("%w: steering text exceeds %d bytes", ErrInvalidSteeringMessage, MaxSteeringTextBytes)
	}
	if strings.TrimSpace(m.Text) == "" {
		return fmt.Errorf("%w: steering text is required", ErrInvalidSteeringMessage)
	}
	return nil
}

func (m StoredMessageV1) Equal(other StoredMessageV1) bool {
	return m.Role == other.Role && m.Text == other.Text
}

func (m StoredMessageV1) equal(other StoredMessageV1) bool {
	return m.Equal(other)
}

// SteeringOverlay is a persisted steering record.
type SteeringOverlay struct {
	OverlayID           string              `json:"overlay_id"`
	Revision            uint64              `json:"revision"`
	SlotOrdinal         uint64              `json:"slot_ordinal"`
	Active              bool                `json:"active"`
	Message             StoredMessageV1     `json:"message"`
	Placement           StoredPlacement     `json:"placement"`
	AnchorMissingPolicy AnchorMissingPolicy `json:"anchor_missing_policy"`
	Reason              ReasonCode          `json:"reason"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

func (o SteeringOverlay) Validate() error {
	if err := ValidateOverlayID(o.OverlayID); err != nil {
		return err
	}
	if err := o.Message.Validate(); err != nil {
		return err
	}
	if err := o.Placement.Validate(); err != nil {
		return err
	}
	if err := o.AnchorMissingPolicy.Validate(); err != nil {
		return err
	}
	if err := ValidateReasonCode(o.Reason); err != nil {
		return err
	}
	return nil
}

func (o SteeringOverlay) Clone() SteeringOverlay {
	out := o
	if o.Placement.Anchor != nil {
		cp := *o.Placement.Anchor
		out.Placement.Anchor = &cp
	}
	return out
}

func (o SteeringOverlay) ToProjectionOverlay() conversationprojection.Overlay {
	var anchor *MessageAnchor
	if o.Placement.Anchor != nil {
		cp := *o.Placement.Anchor
		anchor = &cp
	}
	return conversationprojection.Overlay{
		OverlayID:   o.OverlayID,
		Revision:    o.Revision,
		SlotOrdinal: o.SlotOrdinal,
		Active:      o.Active,
		Message: conversationprojection.OverlayMessage{
			Role: o.Message.Role,
			Text: o.Message.Text,
		},
		Placement: conversationprojection.Placement{
			Kind:   o.Placement.Kind,
			Anchor: anchor,
		},
		AnchorMissingPolicy: o.AnchorMissingPolicy,
	}
}

// PutSteeringRequest is the writer-facing steering mutation.
type PutSteeringRequest struct {
	OverlayID           string              `json:"overlay_id"`
	Message             StoredMessageV1     `json:"message"`
	Placement           StoredPlacement     `json:"placement"`
	AnchorMissingPolicy AnchorMissingPolicy `json:"anchor_missing_policy"`
	Reason              ReasonCode          `json:"reason"`
}

func (r PutSteeringRequest) Validate() error {
	if err := ValidateOverlayID(r.OverlayID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.Message.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.Placement.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := r.AnchorMissingPolicy.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	if err := ValidateReasonCode(r.Reason); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSteeringRequest, err)
	}
	return nil
}

// CacheDiscontinuityKind is the bounded cache-discontinuity operation recorded for steering mutations.
type CacheDiscontinuityKind string

const (
	CacheDiscontinuityNone       CacheDiscontinuityKind = "none"
	CacheDiscontinuityCreate     CacheDiscontinuityKind = "create"
	CacheDiscontinuityReplace    CacheDiscontinuityKind = "replace"
	CacheDiscontinuityMove       CacheDiscontinuityKind = "move"
	CacheDiscontinuityDeactivate CacheDiscontinuityKind = "deactivate"
)

func (k CacheDiscontinuityKind) Validate() error {
	switch k {
	case CacheDiscontinuityNone, CacheDiscontinuityCreate, CacheDiscontinuityReplace, CacheDiscontinuityMove, CacheDiscontinuityDeactivate:
		return nil
	default:
		return fmt.Errorf("unknown cache discontinuity kind %q", k)
	}
}

// SteeringState is the post-mutation steering summary.
type SteeringState struct {
	OverlayID                   string                 `json:"overlay_id"`
	Revision                    uint64                 `json:"revision"`
	SlotOrdinal                 uint64                 `json:"slot_ordinal"`
	Active                      bool                   `json:"active"`
	StateRevision               uint64                 `json:"state_revision"`
	CacheDiscontinuityKind      CacheDiscontinuityKind `json:"cache_discontinuity_kind,omitempty"`
	CacheDiscontinuityPlacement PlacementKind          `json:"cache_discontinuity_placement,omitempty"`
}

// Narrow ports.

type Tagger interface {
	TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error)
}

type SteeringStore interface {
	PutSteering(ctx context.Context, aLegID string, req PutSteeringRequest) (SteeringState, error)
	DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (SteeringState, error)
}

// Store is the combined port for implementations.
type Store interface {
	Reader
	Tagger
	SteeringStore
}

// AsStore reports whether v implements the optional conversation-view store capability.
func AsStore(v any) (Store, bool) {
	if s, ok := v.(Store); ok {
		return s, true
	}
	if p, ok := v.(interface{ ConversationViewStore() Store }); ok {
		return p.ConversationViewStore(), true
	}
	return nil, false
}

// AsReader reports whether v implements the optional conversation-view reader capability.
func AsReader(v any) (Reader, bool) {
	if r, ok := v.(Reader); ok {
		return r, true
	}
	if p, ok := v.(interface{ ConversationViewStore() Store }); ok {
		return p.ConversationViewStore(), true
	}
	return nil, false
}

// AsTagger reports whether v implements the optional conversation-view tagger capability.
func AsTagger(v any) (Tagger, bool) {
	if t, ok := v.(Tagger); ok {
		return t, true
	}
	if p, ok := v.(interface{ ConversationViewStore() Store }); ok {
		return p.ConversationViewStore(), true
	}
	return nil, false
}

// AsSteeringStore reports whether v implements the optional steering store capability.
func AsSteeringStore(v any) (SteeringStore, bool) {
	if s, ok := v.(SteeringStore); ok {
		return s, true
	}
	if p, ok := v.(interface{ ConversationViewStore() Store }); ok {
		return p.ConversationViewStore(), true
	}
	return nil, false
}

// RegistersNewAfterMessageAnchor reports whether this request would newly bind a fixed
// after_message anchor at the persistence point.
func RegistersNewAfterMessageAnchor(req PutSteeringRequest, exists, placementChanged bool) bool {
	if req.Placement.Kind != PlacementAfterMessage || req.Placement.Anchor == nil {
		return false
	}
	return !exists || placementChanged
}
