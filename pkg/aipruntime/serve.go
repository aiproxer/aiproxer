package aipruntime

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/internal/stdhttp"
)

// Serve admission error classes. Each is a stable public identity returned
// directly (or wrapped with %w) by [Runtime.Serve], so external callers
// classify rejections with errors.Is instead of parsing messages:
//
//   - [ErrServeNilContext] — a nil context;
//   - [ErrRuntimeUnbound] — a runtime with no bound host (zero or nil Runtime);
//   - [ErrRuntimeClosed] — a runtime whose [Runtime.Close] already began;
//   - [ErrRuntimeServed] — a second serve admission on the same runtime.
var (
	// ErrServeNilContext reports a nil context passed to [Runtime.Serve].
	ErrServeNilContext = errors.New("aipruntime: nil context")
	// ErrRuntimeUnbound reports [Runtime.Serve] on a runtime with no bound host,
	// including a zero or nil Runtime.
	ErrRuntimeUnbound = errors.New("aipruntime: runtime has no bound host")
	// ErrRuntimeClosed reports [Runtime.Serve] after [Runtime.Close] began
	// tearing the runtime down.
	ErrRuntimeClosed = errors.New("aipruntime: runtime closed")
	// ErrRuntimeServed reports a second serve admission on a runtime that
	// already admitted (or completed) one serve run.
	ErrRuntimeServed = errors.New("aipruntime: runtime already served")
)

// Serve runs the runtime's bound host dispatcher through the retained stdhttp
// generation host lifecycle: one serve admission per runtime, the process-stable
// generation dispatcher (including any registered admin mounts) served on the
// configured address, and the ordered shutdown (reject new reload triggers,
// drain HTTP, close the host exactly once) on cancellation or listener exit.
// Reloads stay owned by [Runtime.ReloadControl]/[Runtime.Reload] and are
// rejected once shutdown begins.
//
// Serve returns a classified rejection before any host work: [ErrServeNilContext]
// for a nil context, [ErrRuntimeUnbound] when the runtime has no bound host
// (including a zero or nil Runtime), [ErrRuntimeClosed] after [Runtime.Close]
// began, and [ErrRuntimeServed] for a second admission (concurrent or
// sequential) — the accepted run is never touched, so a failed second call can
// never tear down the first serve.
//
// A drain failure or timeout leaves the bound host open (the stdhttp
// contract); the recorded serve result is then reported idempotently by
// [Runtime.Close], which resumes the ordered drain instead of performing the
// direct host teardown used by runtimes that never called Serve.
func (r *Runtime) Serve(ctx context.Context) error {
	if r == nil {
		return ErrRuntimeUnbound
	}
	if ctx == nil {
		return ErrServeNilContext
	}

	r.serveMu.Lock()
	if err := r.admitServeLocked(); err != nil {
		r.serveMu.Unlock()
		return err
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.everServed = true
	r.serving = true
	r.serveCancel = cancel
	r.serveDone = done
	cfg, log, serveHost := r.serveCfg, r.serveLog, r.serveHost
	r.serveMu.Unlock()
	defer cancel()

	worker, startErr := stdhttp.StartGenerationHostWorker(serveCtx, stdhttp.GenerationHostInput{
		Config: cfg,
		Log:    log,
		Host:   serveHost,
	})
	serveErr := startErr
	if startErr == nil {
		select {
		case <-serveCtx.Done():
		case <-worker.Exited():
		}
		serveErr = worker.Shutdown(context.Background())
	}

	r.serveMu.Lock()
	r.worker = worker
	r.serveErr = serveErr
	r.serveCancel = nil
	r.serving = false
	if worker != nil && worker.HostClosed() {
		// Success path: record host closure before publishing completion, so a
		// later Close is a no-op and a later Serve is rejected.
		r.closeMu.Lock()
		r.closed = true
		r.closeMu.Unlock()
	}
	close(done)
	r.serveMu.Unlock()
	return serveErr
}

// admitServeLocked applies the serve admission rules. The caller holds serveMu;
// the fixed lock order is serveMu then closeMu.
func (r *Runtime) admitServeLocked() error {
	if r.everServed {
		return ErrRuntimeServed
	}
	r.closeMu.Lock()
	closed, attempted := r.closed, r.closeAttempted
	r.closeMu.Unlock()
	if closed || attempted {
		return ErrRuntimeClosed
	}
	if r.host == nil || r.serveCfg == nil || r.serveLog == nil || r.serveHost == nil {
		return ErrRuntimeUnbound
	}
	return nil
}

// closeServed converges an active serve run and reports the recorded serve
// result once the ordered shutdown is settled. It never performs the direct
// host teardown used by runtimes that never served: host closure stays inside
// the stdhttp ordered drain, so it cannot run under accepted HTTP handlers.
func (r *Runtime) closeServed(ctx context.Context) error {
	r.serveMu.Lock()
	serving := r.serving
	cancel := r.serveCancel
	done := r.serveDone
	r.serveMu.Unlock()

	if serving {
		if cancel != nil {
			cancel()
		}
		select {
		case <-done:
		case <-ctx.Done():
			// The owned shutdown keeps running to completion; a later Close
			// joins the same run.
			return ctx.Err()
		}
	}

	r.serveMu.Lock()
	worker := r.worker
	serveErr := r.serveErr
	r.serveMu.Unlock()

	if worker == nil {
		// Defensive: admission guarantees a started worker, but without one
		// there is no ordered seam to resume or closure to record. The serve
		// result is still reported and later Serve calls stay rejected.
		return serveErr
	}
	if worker.HostClosed() {
		r.markClosed()
		return serveErr
	}
	// The run left the host open (for example a drain timeout): resume the
	// ordered drain. The attempt is worker-owned, so an expired ctx returns
	// while it can still finish safely.
	if err := worker.Shutdown(ctx); err != nil {
		return err
	}
	r.markClosed()
	return serveErr
}

// markClosed records that the bound host was closed through the ordered serve
// shutdown.
func (r *Runtime) markClosed() {
	r.closeMu.Lock()
	r.closed = true
	r.closeMu.Unlock()
}
