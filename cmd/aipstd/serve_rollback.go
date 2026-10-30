package main

import (
	"context"
	"errors"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
)

// serveHostCloseTimeout bounds the post-BuildHost host close, including the
// host-owned tracing flush that runs last.
const serveHostCloseTimeout = 12 * time.Second

type contextShutdowner interface {
	Shutdown(context.Context) error
}

// closeServeHostAfterBuild is the post-BuildHost startup failure path (req 4.8):
// close any separate management resource, then invoke Host.Close once. Callers
// must not reconstruct Manager/Process ownership or invoke ShutdownTracing here.
func closeServeHostAfterBuild(ctx context.Context, host *runtimebundle.Host, mgmt contextShutdowner) error {
	var out error
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serveHostCloseTimeout)
	defer cancel()
	if mgmt != nil {
		if err := mgmt.Shutdown(cleanupCtx); err != nil {
			out = errors.Join(out, err)
		}
	}
	if host != nil {
		if err := host.Close(cleanupCtx); err != nil {
			out = errors.Join(out, err)
		}
	}
	return out
}
