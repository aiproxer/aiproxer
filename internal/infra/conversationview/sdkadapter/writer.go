package sdkadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// TrajectoryResolver returns the current accepted/backend-effective call and
// coherent snapshot for the A-leg bound to a Writer. It is invoked at Put time
// to resolve after_ingress_tail to a fixed semantic anchor. Implementations
// receive ctx per call and must not store ctx in structs.
type TrajectoryResolver func(ctx context.Context) (aipapi.Call, conversationview.Snapshot, error)

// Writer implements steering.Writer over the authoritative conversation-view
// SteeringStore port. It is explicitly constructed with an A-leg scope and a
// narrow trajectory resolver for after_ingress_tail placement. No global locator
// or client frontend exposure is provided. Rendered payloads are persisted
// verbatim per revision; semantic no-op Put remains a no-op.
type Writer struct {
	store    conversationview.SteeringStore
	aLegID   string
	resolver TrajectoryResolver
	observer conversationview.Observer
}

var _ steering.Writer = (*Writer)(nil)

// NewWriter constructs a trusted steering writer bound to aLegID.
// store and aLegID are required. resolver may be nil if the caller never uses
// after_ingress_tail; Put with after_ingress_tail and nil resolver fails closed.
func NewWriter(store conversationview.SteeringStore, aLegID string, resolver TrajectoryResolver) (*Writer, error) {
	return NewWriterWithObserver(store, aLegID, resolver, nil)
}

// NewWriterWithObserver constructs a Writer with an optional narrow observer for
// bounded steering mutation/cache-discontinuity diagnostics. The observer receives
// only bounded enums (operation, placement), never OverlayID/ALegID/digest/plaintext.
// Nil observer is a no-op.
func NewWriterWithObserver(store conversationview.SteeringStore, aLegID string, resolver TrajectoryResolver, observer conversationview.Observer) (*Writer, error) {
	if store == nil {
		return nil, fmt.Errorf("sdkadapter: steering store is required")
	}
	trimmed := strings.TrimSpace(aLegID)
	if trimmed == "" {
		return nil, fmt.Errorf("sdkadapter: a-leg id is required")
	}
	if len(trimmed) > conversationview.MaxALegIDBytes {
		return nil, fmt.Errorf("sdkadapter: a-leg id exceeds %d bytes", conversationview.MaxALegIDBytes)
	}
	return &Writer{store: store, aLegID: trimmed, resolver: resolver, observer: observer}, nil
}

// Put validates the SDK PutRequest, resolves placement (stable_prefix passes
// through, after_ingress_tail resolves NOW via ResolveAfterIngressTailAnchor
// against the injected trajectory resolver), maps to PutSteeringRequest with
// stored PlacementAfterMessage + MessageAnchor or stable prefix, persists the
// already rendered model-visible payload verbatim, and maps results to State.
func (w *Writer) Put(ctx context.Context, req steering.PutRequest) (steering.State, error) {
	if err := req.Validate(); err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
	}
	var placement conversationview.StoredPlacement
	msg := conversationview.StoredMessageV1{
		Role: req.Message.Role,
		Text: req.Message.Text,
	}
	// Validate stored message via domain rules (role/text already validated by SDK, but domain adds same limits).
	if err := msg.Validate(); err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
	}
	anchorPolicy := conversationview.AnchorMissingPolicy(req.AnchorMissingPolicy)
	if err := anchorPolicy.Validate(); err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
	}
	reason := conversationview.ReasonCode(req.Reason)
	if err := reason.Validate(); err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
	}
	switch req.Placement {
	case steering.StablePrefix:
		placement = conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}
	case steering.AfterIngressTail:
		if w.resolver == nil {
			return steering.State{}, fmt.Errorf("sdkadapter: trajectory resolver is required for after_ingress_tail: %w", conversationview.ErrTerminalUserNotFound)
		}
		call, snap, err := w.resolver(ctx)
		if err != nil {
			return steering.State{}, fmt.Errorf("sdkadapter: resolve trajectory: %w", err)
		}
		anchor, err := conversationview.ResolveAfterIngressTailAnchor(call, snap)
		if err != nil {
			return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
		}
		placement = conversationview.StoredPlacement{
			Kind:   conversationview.PlacementAfterMessage,
			Anchor: &anchor,
		}
	default:
		return steering.State{}, fmt.Errorf("sdkadapter: unknown placement %q", req.Placement)
	}
	if err := placement.Validate(); err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
	}
	putReq := conversationview.PutSteeringRequest{
		OverlayID:           string(req.OverlayID),
		Message:             msg,
		Placement:           placement,
		AnchorMissingPolicy: anchorPolicy,
		Reason:              reason,
	}
	st, err := w.store.PutSteering(ctx, w.aLegID, putReq)
	if err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: put steering: %w", err)
	}
	if st.CacheDiscontinuityKind != conversationview.CacheDiscontinuityNone {
		if w.observer != nil {
			conversationview.SafeObserver(w.observer).OnSteeringMutation(st.CacheDiscontinuityKind, st.CacheDiscontinuityPlacement)
		}
	}
	return steering.State{
		OverlayID:   steering.OverlayID(st.OverlayID),
		Revision:    st.Revision,
		SlotOrdinal: st.SlotOrdinal,
		Active:      st.Active,
	}, nil
}

// Deactivate validates the overlay ID, delegates to the store, and maps the
// result to steering.State.
func (w *Writer) Deactivate(ctx context.Context, id steering.OverlayID) (steering.State, error) {
	if err := id.Validate(); err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: %w", err)
	}
	st, err := w.store.DeactivateSteering(ctx, w.aLegID, string(id))
	if err != nil {
		return steering.State{}, fmt.Errorf("sdkadapter: deactivate steering: %w", err)
	}
	if st.CacheDiscontinuityKind != conversationview.CacheDiscontinuityNone {
		if w.observer != nil {
			conversationview.SafeObserver(w.observer).OnSteeringMutation(st.CacheDiscontinuityKind, st.CacheDiscontinuityPlacement)
		}
	}
	return steering.State{
		OverlayID:   steering.OverlayID(st.OverlayID),
		Revision:    st.Revision,
		SlotOrdinal: st.SlotOrdinal,
		Active:      st.Active,
	}, nil
}
