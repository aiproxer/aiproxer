package runtimebundle

import (
	"context"

	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
)

type terminalWorkProcessObserver struct {
	metrics *terminalworkapp.MetricsObserver
	prom    *metrics.TerminalWorkProm
}

func newTerminalWorkProcessObserver(m *terminalworkapp.MetricsObserver, prom *metrics.TerminalWorkProm) *terminalWorkProcessObserver {
	if m == nil || prom == nil {
		return nil
	}
	return &terminalWorkProcessObserver{metrics: m, prom: prom}
}

func (o *terminalWorkProcessObserver) ObserveTransition(state, kind, providerID string) {
	if o == nil || o.prom == nil {
		return
	}
	o.prom.ObserveTransition(state, kind, providerID)
}

func (o *terminalWorkProcessObserver) RefreshAfterBatch(ctx context.Context) {
	if o == nil || o.metrics == nil || o.prom == nil {
		return
	}
	ctx = ctxOrBackground(ctx)
	snap, err := o.metrics.Snapshot(ctx)
	if err != nil {
		return
	}
	o.prom.ApplySnapshot(metrics.TerminalWorkSnapshot{
		Backlog:      snap.Backlog,
		OldestAgeSec: snap.OldestAge.Seconds(),
		Pending:      snap.Pending,
		Retrying:     snap.Retrying,
		Quarantined:  snap.Quarantined,
		Completed:    snap.Completed,
		Claimed:      snap.Claimed,
	})
}
