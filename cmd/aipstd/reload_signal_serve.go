package main

import (
	"context"
	"os/signal"
)

// startServeSignalHandling registers INT/TERM for graceful shutdown and, when a
// sink is provided on platforms with SIGHUP, starts the production reload adapter.
// SIGHUP is never passed to NotifyContext (req 11.1-11.2).
func startServeSignalHandling(ctx context.Context, sink ReloadTriggerSink) (sigCtx context.Context, stop func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	sigCtx, stopShutdown := signal.NotifyContext(ctx, ShutdownSignals()...)

	var adapter *SIGHUPAdapter
	if sink != nil && len(ReloadSignals()) > 0 {
		adapter = NewSIGHUPAdapter(sink)
		_ = adapter.Start(sigCtx)
	}

	stop = func() {
		if adapter != nil {
			adapter.Stop()
		}
		stopShutdown()
	}
	return sigCtx, stop
}
