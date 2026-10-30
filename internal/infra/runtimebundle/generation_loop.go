package runtimebundle

import (
	"context"
	"sync"
)

// startOwnedLoop starts a generation-owned loop and registers its cancel+join
// release with the ledger before application work begins (req 4.2). A nil
// ledger means there is no ownership context, so no loop is started.
func startOwnedLoop(parent context.Context, ledger *ResourceLedger, name string, phase ClosePhase, loop func(ctx context.Context)) {
	if ledger == nil {
		return
	}
	parent = ctxOrBackground(parent)
	ctx, cancel := context.WithCancel(parent)
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-ctx.Done():
			return
		case <-started:
		}
		// Re-check after the gate opens: cancel() may have won the select race,
		// so the loop must not enter application work once cancellation is
		// pending. Without this, a sealed or quiescing ledger could let one
		// iteration of work slip through after cancel().
		if ctx.Err() != nil {
			return
		}
		loop(ctx)
	})
	ledger.AddClose(name, phase, func() error {
		cancel()
		wg.Wait()
		return nil
	})
	close(started)
}
