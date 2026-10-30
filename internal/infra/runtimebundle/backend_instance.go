package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
)

// Sentinel errors for backend instance lifecycle and preflight (req 8.9, 8.11).
var (
	ErrPreflightUnsupported       = errors.New("runtimebundle: backend preflight capability unsupported")
	ErrBillablePreflightForbidden = errors.New("runtimebundle: billable backend preflight is forbidden")
)

// BackendPreflightResult is an optional non-billable readiness probe outcome.
// Billable must be false; WrapBackendInstance rejects billable results (req 8.11).
type BackendPreflightResult struct {
	Ready       bool
	Billable    bool
	Description string
}

// OptionalBackendHooks are generation-local optional backend lifecycle seams.
// Existing backends without hooks remain compatible.
type OptionalBackendHooks struct {
	Start                 func(context.Context) error
	Stop                  func(context.Context) error
	CleanupIdleTransports func(context.Context) error
	PreflightCapability   func(context.Context) (BackendPreflightResult, error)
}

// BackendInstance wraps an execbackend.Backend with idempotent close, optional
// lifecycle, idle-transport cleanup, and explicit non-billable preflight.
type BackendInstance struct {
	Backend execbackend.Backend
	hooks   OptionalBackendHooks

	startOnce sync.Once
	startErr  error
	// startAttempted distinguishes a partial/failed Start, which must be
	// conservatively stopped, from rollback before prepare, which must not call
	// Stop on a lifecycle that was never entered.
	startAttempted atomic.Bool
	closeOnce      sync.Once
	closeErr       error
	started        atomic.Bool
}

// WrapBackendInstance returns a generation-owned backend wrapper.
func WrapBackendInstance(be execbackend.Backend, hooks OptionalBackendHooks) *BackendInstance {
	return &BackendInstance{Backend: be, hooks: hooks}
}

// Start runs the optional start hook exactly once. Concurrent and repeated
// callers share the first result. A failed/partial start still allows Close
// cleanup (Stop + backend Close) to run.
func (b *BackendInstance) Start(ctx context.Context) error {
	if b == nil || b.hooks.Start == nil {
		return nil
	}
	ctx = ctxOrBackground(ctx)
	b.startOnce.Do(func() {
		b.startAttempted.Store(true)
		b.startErr = b.hooks.Start(ctx)
		if b.startErr == nil {
			b.started.Store(true)
		}
	})
	return b.startErr
}

// Close stops optional lifecycle then closes the backend exactly once (req 8.9).
// Idle-transport cleanup runs before Stop/backend Close so generation release
// ordering stays correct when BackendInstance.Close is the ledger callback.
func (b *BackendInstance) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		var out error
		if b.hooks.CleanupIdleTransports != nil {
			if err := b.hooks.CleanupIdleTransports(context.Background()); err != nil {
				out = errors.Join(out, err)
			}
		}
		if b.hooks.Stop != nil && (b.hooks.Start == nil || b.startAttempted.Load()) {
			if err := b.hooks.Stop(context.Background()); err != nil {
				out = errors.Join(out, err)
			}
		}
		if b.Backend.Close != nil {
			if err := b.Backend.Close(); err != nil {
				out = errors.Join(out, err)
			}
		}
		b.closeErr = out
	})
	return b.closeErr
}

// CleanupIdleTransports runs the optional idle-transport cleanup hook.
func (b *BackendInstance) CleanupIdleTransports(ctx context.Context) error {
	if b == nil || b.hooks.CleanupIdleTransports == nil {
		return nil
	}
	return b.hooks.CleanupIdleTransports(ctxOrBackground(ctx))
}

// PreflightCapability runs an optional non-billable readiness probe.
// It never invents billable inference traffic (req 8.11).
func (b *BackendInstance) PreflightCapability(ctx context.Context) (BackendPreflightResult, error) {
	if b == nil || b.hooks.PreflightCapability == nil {
		return BackendPreflightResult{}, ErrPreflightUnsupported
	}
	res, err := b.hooks.PreflightCapability(ctxOrBackground(ctx))
	if err != nil {
		return BackendPreflightResult{}, err
	}
	if res.Billable {
		return BackendPreflightResult{}, fmt.Errorf("%w", ErrBillablePreflightForbidden)
	}
	return res, nil
}

// AsBackend returns the underlying backend value for executor maps.
func (b *BackendInstance) AsBackend() execbackend.Backend {
	if b == nil {
		return execbackend.Backend{}
	}
	return b.Backend
}
