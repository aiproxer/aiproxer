package aipruntime_test

import (
	"context"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// facadeFakeHost is a public-boundary host double for Runtime facade tests.
type facadeFakeHost struct {
	view         aipsdk.ExecutorView
	ready        bool
	caps         aipruntime.HostCapabilities
	querier      metering.Querier
	refreshErr   error
	refreshCalls atomic.Int32
	closeErrs    []error
	closeCalls   atomic.Int32
	reload       func(context.Context, sdkreload.Trigger) sdkreload.Result
	status       sdkreload.Status
	readiness    controlplane.ReadinessReportReader
}

func (f *facadeFakeHost) ExecutorView() aipsdk.ExecutorView { return f.view }
func (f *facadeFakeHost) Ready() bool                       { return f.ready }
func (f *facadeFakeHost) Capabilities() aipruntime.HostCapabilities {
	caps := f.caps
	if caps.ExecutableState == "" {
		caps.ExecutableState = controlplane.CapabilityDisabled
	}
	return caps
}
func (f *facadeFakeHost) MeteringQuerier() metering.Querier { return f.querier }
func (f *facadeFakeHost) ReadinessReport() controlplane.ReadinessReportReader {
	return f.readiness
}

func (f *facadeFakeHost) RefreshSnapshots(ctx context.Context) error {
	f.refreshCalls.Add(1)
	if ctx == nil {
		return context.Canceled
	}
	return f.refreshErr
}

func (f *facadeFakeHost) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	if f.reload != nil {
		return f.reload(ctx, trigger)
	}
	return sdkreload.Result{Category: sdkreload.ResultNoop, ActiveGeneration: 1}
}
func (f *facadeFakeHost) Status() sdkreload.Status { return f.status }
func (f *facadeFakeHost) Close(context.Context) error {
	n := int(f.closeCalls.Add(1))
	if n-1 < len(f.closeErrs) {
		return f.closeErrs[n-1]
	}
	return nil
}
