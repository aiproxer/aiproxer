package aipruntime

import (
	"context"
	"log/slog"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
)

// Runtime is an opaque handle over a successfully built OSS composition.
// Close synchronization follows the public retry/idempotency contract (req 10.1-10.4).
type Runtime struct {
	host hostAPI

	// closeMu guards closed and the direct-teardown path; closeAttempted pins
	// that a Close already began on a runtime that never served, so Serve stays
	// rejected instead of mounting a host whose shutdown already began.
	closeMu        sync.Mutex
	closed         bool
	closeAttempted bool

	// Serve view, config, and logger are captured at build time (task 5.1 C4).
	// They stay unexported: no host pointer, registry getter, or host type is
	// exposed.
	serveCfg  *config.Config
	serveLog  *slog.Logger
	serveHost stdhttp.GenerationServeHost

	// serveMu guards only the serve state for short transitions. The fixed lock
	// order is serveMu then closeMu, and no facade lock is held while the
	// server runs or while Close joins a run.
	serveMu     sync.Mutex
	everServed  bool
	serving     bool
	serveCancel context.CancelFunc
	serveDone   chan struct{}
	serveErr    error
	worker      *stdhttp.GenerationHostWorker
}

// Build constructs a production runtime from public options.
// Callers must not import internal packages; Build installs the standard registry.
// The returned Runtime owns one immutable host-facing dependency graph.
// Build binds one complete Host via runtimebundle.BuildHost (req 4.1, 10.1-10.4).
// Build stays non-money: monetary composition requires BuildWithBilling.
func Build(ctx context.Context, opts Options) (*Runtime, error) {
	return buildCommon(ctx, opts, false, nil)
}

// Close releases runtime resources. Calls are serialized: a successful Close is
// idempotent, and a deadline or teardown failure remains retryable. A nil ctx
// is tolerated and substituted with [context.Background].
//
// A runtime that never called [Runtime.Serve] keeps the direct host-teardown
// path below. A runtime whose serve run completed (or was converged by Close)
// never performs that direct teardown: Close joins an active serve run
// (respecting the Close deadline), resumes the ordered stdhttp drain if the
// run left the host open, and reports the recorded serve result idempotently.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.serveMu.Lock()
	if r.everServed {
		r.serveMu.Unlock()
		return r.closeServed(ctx)
	}
	// Direct teardown path for runtimes that never served. serveMu is held only
	// to make the Serve/Close admission decision atomic; closeMu then stays
	// held across the teardown exactly as before, so concurrent Closes remain
	// serialized and an admitted Serve can only observe a closed/attempted
	// runtime.
	r.closeMu.Lock()
	r.serveMu.Unlock()
	defer r.closeMu.Unlock()
	if r.closed {
		return nil
	}
	r.closeAttempted = true
	if r.host != nil {
		if err := r.host.Close(ctx); err != nil {
			return err
		}
	}
	r.closed = true
	return nil
}
