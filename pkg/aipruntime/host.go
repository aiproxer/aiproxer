package aipruntime

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// hostAPI is the unexported host seam satisfied by bundleHost.
type hostAPI interface {
	ExecutorView() aipsdk.ExecutorView
	Ready() bool
	Capabilities() HostCapabilities
	MeteringQuerier() metering.Querier
	ReadinessReport() controlplane.ReadinessReportReader
	RefreshSnapshots(context.Context) error
	Reload(context.Context, sdkreload.Trigger) sdkreload.Result
	Status() sdkreload.Status
	Close(context.Context) error
}

// bundleHost adapts a ready runtimebundle.Host; h is non-nil after adaptHost.
type bundleHost struct{ h *runtimebundle.Host }

// adaptHost validates readiness and closes partial hosts on failure.
func adaptHost(ctx context.Context, h *runtimebundle.Host) (hostAPI, error) {
	if h == nil || !h.Ready() || h.ExecutorView() == nil {
		if h != nil {
			_ = h.Close(context.WithoutCancel(ctx))
		}
		return nil, fmt.Errorf("aipruntime: BuildHost returned incomplete host")
	}
	return bundleHost{h: h}, nil
}

// newRuntime binds the validated host with the process-stable serve view,
// config, and logger captured at build time (task 5.1 C4). The serve view is
// the same bundle host behind the unexported seam; no host pointer, registry
// getter, or concrete host type is exposed.
func newRuntime(api hostAPI, h *runtimebundle.Host) *Runtime {
	return &Runtime{
		host:      api,
		serveCfg:  h.Config(),
		serveLog:  h.Logger(),
		serveHost: h,
	}
}

// bundleHost delegates executor, capability, and readiness queries to the bundle.
func (b bundleHost) ExecutorView() aipsdk.ExecutorView { return b.h.ExecutorView() }
func (b bundleHost) Ready() bool                       { return b.h.Ready() }
func (b bundleHost) Capabilities() HostCapabilities    { return b.h.Capabilities() }
func (b bundleHost) MeteringQuerier() metering.Querier { return b.h.MeteringQuerier() }
func (b bundleHost) ReadinessReport() controlplane.ReadinessReportReader {
	return b.h.ReadinessReport()
}

// RefreshSnapshots requires a bound host and non-nil context.
func (b bundleHost) RefreshSnapshots(ctx context.Context) error {
	if b.h == nil {
		return fmt.Errorf("aipruntime: snapshot refresh not available")
	}
	if ctx == nil {
		return fmt.Errorf("aipruntime: nil context")
	}
	return b.h.RefreshSnapshots(ctx)
}

// Reload and Status forward to the bound bundle coordinator seam.
func (b bundleHost) Reload(c context.Context, t sdkreload.Trigger) sdkreload.Result {
	return b.h.Reload(c, t)
}
func (b bundleHost) Status() sdkreload.Status { return b.h.Status() }

// Close forwards shutdown to the underlying runtimebundle host.
func (b bundleHost) Close(ctx context.Context) error { return b.h.Close(ctx) }
