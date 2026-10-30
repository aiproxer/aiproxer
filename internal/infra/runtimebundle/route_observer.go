package runtimebundle

import (
	"context"
	"log/slog"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type noopRouteObserver struct{}

var _ aipsdk.RouteObserver = noopRouteObserver{}

func (noopRouteObserver) ObserveRouteDecision(context.Context, string, string, string) {}

type slogRouteObserver struct {
	log *slog.Logger
}

var _ aipsdk.RouteObserver = (*slogRouteObserver)(nil)

func (o slogRouteObserver) ObserveRouteDecision(ctx context.Context, traceID, decision, detail string) {
	o.log.LogAttrs(
		ctx, slog.LevelInfo, "aip.route",
		slog.String("trace_id", traceID),
		slog.String("decision", decision),
		slog.String("detail", detail),
	)
}

func routeObserverFor(log *slog.Logger) aipsdk.RouteObserver {
	if log == nil {
		return noopRouteObserver{}
	}
	return slogRouteObserver{log: log}
}
