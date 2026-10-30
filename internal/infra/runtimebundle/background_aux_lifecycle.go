package runtimebundle

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
)

type BackgroundAuxScheduler = auxreq.BackgroundScheduler

func releaseProcessInputOwnership(in *ProcessServicesInput, release func()) {
	release()
	if in.BackgroundAux != nil {
		_ = in.BackgroundAux.Close()
		in.BackgroundAux = nil
	}
}

func adoptBackgroundAuxAndDetector(ctx context.Context, in *ProcessServicesInput, ps *ProcessServices, register func(func() error)) {
	if in.BackgroundAux == nil {
		bounds := featurehost.CompactionSchedulerBounds(in.Cfg)
		in.BackgroundAux = auxiliary.NewProductionBackgroundScheduler(ctx, bounds)
	}
	ps.BackgroundAux, in.BackgroundAux = in.BackgroundAux, nil
	if ps.BackgroundAux != nil {
		register(ps.BackgroundAux.Close)
	}
}
