package openresponses

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// ContinuationResolver defines a narrow injected seam for resolving parent continuation state.
type ContinuationResolver interface {
	ResolveParent(ctx context.Context, scope aipcont.Scope, parentID string, baseCall aipapi.Call) (aipapi.Call, aipcont.ContinuationRecord, error)
}

// storeContinuationResolver delegates parent resolution to the protocol-neutral SDK materializer and store.
type storeContinuationResolver struct {
	store  aipcont.Store
	bounds aipcont.Bounds
}

// NewStoreContinuationResolver constructs a ContinuationResolver backed by a aipcont.Store.
func NewStoreContinuationResolver(store aipcont.Store, bounds aipcont.Bounds) ContinuationResolver {
	return &storeContinuationResolver{
		store:  store,
		bounds: bounds,
	}
}

var _ ContinuationResolver = (*storeContinuationResolver)(nil)

func (r *storeContinuationResolver) ResolveParent(ctx context.Context, scope aipcont.Scope, parentID string, baseCall aipapi.Call) (aipapi.Call, aipcont.ContinuationRecord, error) {
	if r.store == nil || parentID == "" {
		return aipapi.Call{}, aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
	}
	pid := aipcont.ResponseID(parentID)
	rec, err := aipcont.Lookup(ctx, r.store, scope, pid)
	if err != nil {
		if errors.Is(err, aipcont.ErrStorageFailure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return aipapi.Call{}, aipcont.ContinuationRecord{}, err
		}
		return aipapi.Call{}, aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
	}
	materializedCall, _, err := aipcont.MaterializeCall(ctx, aipcont.MaterializeInput{
		Store:    r.store,
		Scope:    scope,
		StartID:  pid,
		NewInput: baseCall.Items,
		Bounds:   r.bounds,
	}, baseCall)
	if err != nil {
		if isContinuationPassThroughError(err) {
			return aipapi.Call{}, aipcont.ContinuationRecord{}, err
		}
		return aipapi.Call{}, aipcont.ContinuationRecord{}, aipcont.ErrPreviousResponseNotFound
	}
	return materializedCall, rec, nil
}

func isContinuationPassThroughError(err error) bool {
	return errors.Is(err, aipcont.ErrStorageFailure) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, aipcont.ErrChainDepthExceeded) ||
		errors.Is(err, aipcont.ErrStorageLimitExceeded) ||
		errors.Is(err, aipcont.ErrMaterializedSizeExceeded) ||
		errors.Is(err, aipcont.ErrMaterializedItemsExceeded)
}

func continuationResolverFor(resolver ContinuationResolver, store aipcont.Store, bounds aipcont.Bounds) ContinuationResolver {
	if resolver != nil {
		return resolver
	}
	if store != nil {
		return NewStoreContinuationResolver(store, bounds)
	}
	return nil
}

func applyParentLineage(model *string, call *aipapi.Call, previousID *string, parent aipcont.ContinuationRecord) {
	if parent.ID != "" && previousID != nil {
		*previousID = parent.ID.String()
	}
	if model != nil && *model == "" {
		*model = parent.Lineage.Model
	}
	if call == nil {
		return
	}
	if call.Route.Selector == "" {
		call.Route.Selector = parent.Lineage.RouteSelector
		if call.Route.Selector == "" {
			call.Route.Selector = parent.Lineage.Model
		}
	}
}
