package aipruntime

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type HostCapabilities = controlplane.HostCapabilities

func (r *Runtime) api() hostAPI {
	if r == nil {
		return nil
	}
	return r.host
}

// runtimeField delegates to hostAPI when Runtime has a bound host.
func runtimeField[T any](r *Runtime, pick func(hostAPI) T, zero T) T {
	if h := r.api(); h != nil {
		return pick(h)
	}
	return zero
}

func (r *Runtime) ExecutorView() aipsdk.ExecutorView {
	return runtimeField(r, hostAPI.ExecutorView, nil)
}

func (r *Runtime) Ready() bool { h := r.api(); return h != nil && h.Ready() }

func (r *Runtime) Capabilities() HostCapabilities {
	return runtimeField(r, hostAPI.Capabilities, HostCapabilities{ExecutableState: controlplane.CapabilityDisabled})
}
func (r *Runtime) HasProductionMetering() bool     { return r.Capabilities().ProductionMetering }
func (r *Runtime) HasTrafficObservers() bool       { return r.Capabilities().TrafficObservers }
func (r *Runtime) HasUsageObservers() bool         { return r.Capabilities().UsageObservers }
func (r *Runtime) HasProductionEvidenceSink() bool { return r.Capabilities().ProductionEvidenceSink }

func (r *Runtime) HasProductionMeteringQuerier() bool {
	return r.Capabilities().ProductionMeteringQuerier
}

func (r *Runtime) SnapshotGenerationID() int64 { return r.Capabilities().SnapshotGenerationID }

func (r *Runtime) SnapshotUsageVersion() string { return r.Capabilities().SnapshotUsageVersion }

func (r *Runtime) ExecutableGenerationID() int64       { return r.Capabilities().ExecutableGenerationID }
func (r *Runtime) ExecutableGenerationVersion() string { return r.Capabilities().ExecutableVersion }
func (r *Runtime) ExecutableGenerationState() controlplane.CapabilityState {
	return r.Capabilities().ExecutableState
}

func (r *Runtime) ExecutableEvidenceObjectID() string {
	return r.Capabilities().ExecutableEvidenceObjectID
}

func (r *Runtime) MeteringQuerier() metering.Querier {
	return runtimeField(r, hostAPI.MeteringQuerier, nil)
}

func (r *Runtime) ReadinessReport() controlplane.ReadinessReportReader {
	return runtimeField(r, hostAPI.ReadinessReport, nil)
}

// RefreshSnapshots returns an error when no host is bound to this runtime.
func (r *Runtime) RefreshSnapshots(ctx context.Context) error {
	if h := r.api(); h != nil {
		return h.RefreshSnapshots(ctx)
	}
	return fmt.Errorf("aipruntime: snapshot refresh not available")
}
