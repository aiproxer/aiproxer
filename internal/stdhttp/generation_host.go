package stdhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/safety"
)

// GenerationServeHost is the focused host-facing seam this adapter needs
// (req 8.6-8.7). It exposes the stable data-plane handler, early reload-trigger
// rejection, and the canonical process shutdown coordinator — never the
// generation manager, process runtime, or tracing primitives behind them.
type GenerationServeHost interface {
	// HTTPHandler returns the process-stable request dispatcher.
	HTTPHandler() http.Handler
	// BeginShutdown rejects new reload triggers before the HTTP drain starts.
	BeginShutdown()
	// Close runs the host-owned shutdown ordering after the HTTP drain.
	Close(context.Context) error
}

// GenerationHostInput configures [RunWithGenerationHost].
type GenerationHostInput struct {
	Config *config.Config
	Log    *slog.Logger
	// Host owns every generation/process/tracing shutdown decision.
	Host GenerationServeHost
	// Management is the optional process-owned management listener, closed in
	// its established position after the data plane drains and before the
	// host-owned teardown (task 5.6).
	Management interface {
		Shutdown(context.Context) error
	}
	// ShutdownTimeout bounds the HTTP drain and the host close. Zero uses
	// [config.ServerConfig.EffectiveShutdownTimeout] (15s when omitted).
	ShutdownTimeout time.Duration
}

// httpServerShutdown is the [http.Server.Shutdown] implementation (overridable in tests).
var httpServerShutdown = func(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

// GenerationHostWorker is the retained owner of one started generation host
// serve attempt: the http.Server, the serve worker that runs it, and the
// single-flight ordered shutdown (reject reload triggers, drain HTTP, await
// the serve worker, close the optional management listener, close the host).
//
// It is the retained form of the lifecycle [RunWithGenerationHost] wraps: a
// failed drain leaves the host open, and a later [GenerationHostWorker.Shutdown]
// resumes the same ordered sequence instead of tearing the host down directly
// under accepted handlers.
type GenerationHostWorker struct {
	in      GenerationHostInput
	timeout time.Duration
	srv     *http.Server

	exitMu   sync.Mutex
	exited   bool
	exitErr  error
	exitDone chan struct{}

	mu               sync.Mutex
	attempt          *generationHostShutdownAttempt
	hostClosed       bool // host close succeeded (never re-entered)
	managementClosed bool // optional management shutdown succeeded
	shutdownErr      error
}

type generationHostShutdownAttempt struct {
	done chan struct{}
	err  error
}

// StartGenerationHostWorker validates the serve input and starts one long-lived
// serve attempt. Startup listener/timeouts are fixed from Config; exactly the
// validated handler is mounted (HTTPHandler is never re-read).
//
// On a post-host input failure it returns the joined failure together with the
// retained cleanup worker whose ordered cleanup already ran once, so host
// ownership is never stranded and a caller can finish the cleanup retryably;
// a nil worker is returned only when there is no ownership seam at all (nil
// ctx or nil Host). Callers must check the error first.
func StartGenerationHostWorker(ctx context.Context, in GenerationHostInput) (*GenerationHostWorker, error) {
	if ctx == nil {
		return nil, errors.New("stdhttp: nil context")
	}
	if in.Host == nil {
		return nil, errors.New("stdhttp: nil generation serve host")
	}
	timeout := in.ShutdownTimeout
	if timeout <= 0 && in.Config != nil {
		timeout = in.Config.Server.EffectiveShutdownTimeout()
	}
	if timeout <= 0 {
		timeout = config.DefaultShutdownTimeout
	}
	handler, err := validateGenerationHostInput(in)
	if err != nil {
		worker := &GenerationHostWorker{in: in, timeout: timeout, exitDone: make(chan struct{})}
		worker.finishExit(nil)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		worker.in.Host.BeginShutdown()
		cleanupErr := worker.closeServeComponents(cleanupCtx)
		cancel()
		worker.mu.Lock()
		worker.shutdownErr = cleanupErr
		worker.mu.Unlock()
		return worker, errors.Join(err, cleanupErr)
	}

	worker := &GenerationHostWorker{
		in:       in,
		timeout:  timeout,
		exitDone: make(chan struct{}),
		srv: &http.Server{
			Addr:              in.Config.Server.Address,
			Handler:           handler,
			ReadHeaderTimeout: in.Config.Server.EffectiveReadHeaderTimeout(),
			ReadTimeout:       in.Config.Server.EffectiveReadTimeout(),
			WriteTimeout:      in.Config.Server.EffectiveWriteTimeout(),
			IdleTimeout:       in.Config.Server.EffectiveIdleTimeout(),
		},
	}
	in.Log.InfoContext(ctx, "listening", "addr", in.Config.Server.Address)
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					pe := safety.Capture(safety.BoundaryWorker, "listen_and_serve", p)
					if in.Log != nil {
						logCtx := context.WithoutCancel(ctx)
						attrs := diag.IsolatedCrashAttrs(logCtx, pe, diag.CrashAttrOpts{})
						attrs = diag.AppendIsolatedCrashStack(attrs, pe)
						in.Log.LogAttrs(logCtx, slog.LevelError, "stdhttp: isolated panic in listenAndServe worker", attrs...)
					}
					err = pe
				}
			}()
			return listenAndServe(worker.srv)
		}()
		worker.finishExit(err)
	}()
	return worker, nil
}

// Exited is closed once the serve worker has stopped listening.
func (w *GenerationHostWorker) Exited() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.exitDone
}

// HostClosed reports whether the host close succeeded. It stays false after a
// failed host close so a later Shutdown retries through the same ordered
// drain; a failed optional management shutdown leaves it true while
// [GenerationHostWorker.Shutdown] still retries only the management listener.
func (w *GenerationHostWorker) HostClosed() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hostClosed
}

// serveComponentsClosedLocked reports whether every serve component that must
// close has closed successfully. The caller holds w.mu.
func (w *GenerationHostWorker) serveComponentsClosedLocked() bool {
	return w.hostClosed && (w.in.Management == nil || w.managementClosed)
}

// closeServeComponents closes the optional management listener and then the
// host — the established order — attempting every component that is still open
// and remembering per-component success, so a later attempt retries only what
// failed and never re-enters a closed component. A management failure is
// joined into the returned error but never skips the host close.
func (w *GenerationHostWorker) closeServeComponents(ctx context.Context) error {
	var out error
	w.mu.Lock()
	managementOpen := w.in.Management != nil && !w.managementClosed
	hostOpen := !w.hostClosed
	w.mu.Unlock()
	if managementOpen {
		if err := w.in.Management.Shutdown(ctx); err != nil {
			out = errors.Join(out, err)
		} else {
			w.mu.Lock()
			w.managementClosed = true
			w.mu.Unlock()
		}
	}
	if hostOpen {
		if err := w.in.Host.Close(ctx); err != nil {
			out = errors.Join(out, err)
		} else {
			w.mu.Lock()
			w.hostClosed = true
			w.mu.Unlock()
		}
	}
	return out
}

// Shutdown runs or joins the single-flight ordered shutdown attempt and
// returns its result. An in-flight attempt always takes precedence over the
// cached component completion, so callers arriving while a result is still
// being published join it and observe the same final error. ctx bounds only
// the caller's wait: an expired caller returns ctx.Err() while the owned
// attempt keeps running to completion, so the next call joins it. A failed
// attempt (drain/await failure, or a failed management/host close) leaves that
// work open and a later call starts a fresh ordered attempt that retries only
// the components which failed; a completed attempt is idempotent and never
// re-enters a closed component.
func (w *GenerationHostWorker) Shutdown(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	attempt := w.attempt
	if attempt == nil {
		if w.serveComponentsClosedLocked() {
			err := w.shutdownErr
			w.mu.Unlock()
			return err
		}
		attempt = &generationHostShutdownAttempt{done: make(chan struct{})}
		w.attempt = attempt
		go func() {
			err := w.runOrderedShutdown()
			// Publish the attempt result before releasing waiters: every store
			// happens under w.mu before close(attempt.done), so all joined
			// callers observe the same completed error.
			w.mu.Lock()
			attempt.err = err
			w.shutdownErr = err
			w.attempt = nil
			close(attempt.done)
			w.mu.Unlock()
		}()
	}
	done := attempt.done
	w.mu.Unlock()

	select {
	case <-done:
		w.mu.Lock()
		err := attempt.err
		w.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishExit records the serve worker result once and publishes it.
func (w *GenerationHostWorker) finishExit(err error) {
	w.exitMu.Lock()
	w.exitErr = err
	w.exited = true
	w.exitMu.Unlock()
	close(w.exitDone)
}

// serveError returns the recorded abnormal serve error, or nil for a clean (or
// not yet finished) serve worker.
func (w *GenerationHostWorker) serveError() error {
	w.exitMu.Lock()
	defer w.exitMu.Unlock()
	if !w.exited || w.exitErr == nil || errors.Is(w.exitErr, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("stdhttp: serve: %w", w.exitErr)
}

// runOrderedShutdown executes one ordered shutdown attempt bounded by the
// worker's own timeout: reject reload triggers, drain HTTP, await the serve
// worker, then close the optional management listener and the host. Each
// successful component close is remembered, so a later attempt retries only
// the components which failed; the returned error carries any serve or
// management failure truthfully.
func (w *GenerationHostWorker) runOrderedShutdown() error {
	w.in.Host.BeginShutdown()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	if w.srv == nil {
		// Input validation failed before any listener existed: finish the same
		// cleanup seam (management then host close) retryably.
		return w.closeServeComponents(shutdownCtx)
	}
	if err := httpServerShutdown(shutdownCtx, w.srv); err != nil {
		return errors.Join(fmt.Errorf("stdhttp: http server shutdown: %w", err), w.serveError())
	}
	// Successful Shutdown closes listeners and drains accepted connections.
	// Confirm the serving worker has exited before host teardown.
	select {
	case <-w.exitDone:
	case <-shutdownCtx.Done():
		return fmt.Errorf("stdhttp: await server exit: %w", shutdownCtx.Err())
	}
	return errors.Join(w.serveError(), w.closeServeComponents(shutdownCtx))
}

// RunWithGenerationHost serves one long-lived http.Server backed by the
// host-provided stable generation dispatcher, then shuts it down in the order
// documented on [GenerationHostWorker]: reject reload triggers, drain HTTP,
// close the optional management listener, close the host exactly once. If the
// HTTP drain fails or times out, host.Close is not invoked so live handlers
// are never closed underneath. Management and host errors are joined with any
// serve error and returned truthfully. It is the compatibility wrapper over
// [StartGenerationHostWorker].
func RunWithGenerationHost(ctx context.Context, in GenerationHostInput) error {
	worker, err := StartGenerationHostWorker(ctx, in)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
	case <-worker.Exited():
	}
	return worker.Shutdown(context.Background())
}

// validateGenerationHostInput checks config/log/security/handler after a non-nil
// Host is already present and returns the resolved handler so the serve path
// mounts exactly the validated value (HTTPHandler is never re-read). Failures
// must converge on the retained worker's cleanup seam so process/tracing
// ownership is never stranded.
func validateGenerationHostInput(in GenerationHostInput) (http.Handler, error) {
	if in.Config == nil {
		return nil, errors.New("stdhttp: nil config")
	}
	if in.Log == nil {
		return nil, errors.New("stdhttp: nil logger")
	}
	if err := validateStartupSecurity(in.Config); err != nil {
		return nil, fmt.Errorf("stdhttp: validate startup security: %w", err)
	}
	handler := in.Host.HTTPHandler()
	if handler == nil {
		return nil, errors.New("stdhttp: nil generation handler")
	}
	return handler, nil
}
