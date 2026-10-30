package runtime

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
)

func boundRouteTraceModelView(ctx context.Context) *diag.RouteTraceModelView {
	id, ok := modelview.FromContext(ctx)
	if !ok {
		return nil
	}
	return &diag.RouteTraceModelView{
		Digest:             id.Digest,
		ConfigGeneration:   id.ConfigGeneration,
		ConfigFingerprint:  id.ConfigFingerprint,
		RegistryGeneration: id.RegistryGeneration,
		CatalogGeneration:  id.CatalogGeneration,
	}
}

func (e *Executor) observeRoute(ctx context.Context, traceID, decision, detail string) {
	if e == nil || e.RouteObserver == nil {
		return
	}
	e.RouteObserver.ObserveRouteDecision(ctx, traceID, decision, detail)
}

func (e *Executor) notePlanCandidate(ctx context.Context, traceID, candidateKey string, cat *diag.RouteTraceCatalog) {
	if e == nil {
		return
	}
	if e.RouteTrace != nil {
		e.RouteTrace.Append(diag.RouteTraceEntry{
			TraceID:   traceID,
			Decision:  "plan_candidate",
			Detail:    candidateKey,
			ModelView: boundRouteTraceModelView(ctx),
			Catalog:   cat,
		})
	}
	if e.RouteObserver != nil {
		e.RouteObserver.ObserveRouteDecision(ctx, traceID, "plan_candidate", candidateKey)
	}
}

func (e *Executor) noteRouteDecision(ctx context.Context, traceID, decision, detail string) {
	if e == nil {
		return
	}
	if e.RouteTrace != nil {
		e.RouteTrace.Append(diag.RouteTraceEntry{
			TraceID: traceID, Decision: decision, Detail: detail,
			ModelView: boundRouteTraceModelView(ctx),
		})
	}
	if e.RouteObserver != nil {
		e.RouteObserver.ObserveRouteDecision(ctx, traceID, decision, detail)
	}
}

func (e *Executor) noteSelectorAuthority(ctx context.Context, traceID string, snap routeAuthoritySnapshot) {
	if e == nil {
		return
	}
	source := snap.Source
	if source == "" {
		source = routeSelectorSourceClient
	}
	var rev int64
	if snap.active() {
		rev = snap.State.Revision
		source = routeSelectorSourceAdmin
	}
	if e.RouteTrace != nil {
		e.RouteTrace.Append(diag.RouteTraceEntry{
			TraceID:          traceID,
			Decision:         "selector_authority",
			Detail:           source,
			SelectorSource:   source,
			OverrideRevision: rev,
			ModelView:        boundRouteTraceModelView(ctx),
		})
	}
	if e.RouteObserver != nil {
		e.RouteObserver.ObserveRouteDecision(ctx, traceID, "selector_authority", source)
	}
}
