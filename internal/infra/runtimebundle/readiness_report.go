package runtimebundle

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/config"
	corecp "github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type readinessReportBuildInput struct {
	Cfg                *config.Config
	ControlPlaneStatus *corecp.Status
	UsageAuthority     usagelimit.Authority
	Concurrency        *concurrencyapp.Service
	Metering           *meteringRuntime
	SnapshotGeneration *snapshotgen.Publisher
	Executor           *runtime.Executor
	Production         ProductionOptions
	TerminalWork       *terminalWorkRuntime
}

func leaseSetConcurrencyStatus(
	ctx context.Context,
	svc *concurrencyapp.Service,
	tw *terminalWorkRuntime,
) (controlplane.ConcurrencyAuthorityStatus, error) {
	ready, err := svc.ReadinessDomain(ctx)
	if err != nil {
		return controlplane.ConcurrencyAuthorityStatus{}, err
	}
	st := controlplane.ConcurrencyAuthorityReady
	switch string(ready.State) {
	case "degraded":
		st = controlplane.ConcurrencyAuthorityDegraded
	case "unavailable":
		st = controlplane.ConcurrencyAuthorityUnavailable
	case "disabled":
		st = controlplane.ConcurrencyAuthorityDisabled
	}
	out := controlplane.ConcurrencyAuthorityStatus{State: st, Reason: ready.Reason}
	if counts, cerr := svc.LeaseSetOccupancyCounts(ctx); cerr == nil {
		out.LeaseSets = controlplane.LeaseSetOccupancyCounts{
			Active: counts.Active, Uncertain: counts.Uncertain, Expiring: counts.Expiring,
			Released: counts.Released, Failed: counts.Failed,
		}
		if counts.Uncertain > 0 || counts.Failed > 0 || counts.Expiring > 0 {
			out.State = controlplane.ConcurrencyAuthorityDegraded
			if out.Reason == "" {
				out.Reason = "lease_set_uncertain_failed_or_expiring"
			}
		}
	}
	if tw != nil && tw.Queries != nil {
		page, qerr := tw.Queries.List(ctx, terminalworkapp.WorkQuery{
			Kind:  sdk.WorkKindReleaseLeaseSet,
			Class: terminalworkapp.QueryClassPendingTerminalWork,
			Limit: 500,
		})
		if qerr == nil {
			out.LeaseSets.PendingRelease = len(page.Rows)
			if out.LeaseSets.PendingRelease > 0 {
				out.State = controlplane.ConcurrencyAuthorityDegraded
				if out.Reason == "" {
					out.Reason = "lease_set_release_pending"
				}
			}
		}
	}
	return out, nil
}

func buildReadinessReportService(in readinessReportBuildInput) *corecp.ReadinessReportService {
	src := corecp.ReadinessReportSources{
		StoreBackings: readinessStoreBackings(in.Cfg, in.Production),
	}
	if in.ControlPlaneStatus != nil {
		status := in.ControlPlaneStatus
		src.ControlPlane = func(ctx context.Context) (controlplane.CapabilityStatus, error) {
			_ = ctx
			return status.Snapshot(), nil
		}
	}
	// The readiness component appears only when the injected port exposes the
	// status surface; absent surface means no authority readiness component
	// (nothing is invented).
	if reader, ok := in.UsageAuthority.(controlplane.AccountingStatusReader); ok {
		src.UsageAuthority = reader.Status
	}
	if in.Concurrency != nil {
		svc := in.Concurrency
		tw := in.TerminalWork
		src.ConcurrencyAuthority = func(ctx context.Context) (controlplane.ConcurrencyAuthorityStatus, error) {
			return leaseSetConcurrencyStatus(ctx, svc, tw)
		}
	}
	if in.Metering != nil {
		m := in.Metering
		src.MeteringJournal = func(ctx context.Context) (controlplane.ReadinessComponentStatus, error) {
			state := controlplane.CapabilityReady
			if m.checkReady != nil {
				if err := m.checkReady(ctx); err != nil {
					state = controlplane.CapabilityUnavailable
				}
			}
			return controlplane.ReadinessComponentStatus{
				Component:        controlplane.ReadinessComponentMeteringJournal,
				State:            state,
				EnforcementScope: controlplane.EnforcementScopeForStoreBacking(m.StoreBacking, strings.EqualFold(strings.TrimSpace(m.StoreBacking), "postgres")),
				StoreBacking:     m.StoreBacking,
			}, nil
		}
	}
	src.BillingSpool = billingSpoolHealth(in.Production.BillingTerminalUsageSink)
	if in.SnapshotGeneration != nil {
		gen := in.SnapshotGeneration
		src.SnapshotStates = func() (controlplane.CapabilityState, controlplane.CapabilityState, controlplane.CapabilityState) {
			cur := gen.Current()
			if cur == nil {
				return controlplane.CapabilityDisabled, controlplane.CapabilityDisabled, controlplane.CapabilityDisabled
			}
			return mapSnapshotState(cur.Usage.State), mapSnapshotState(cur.Concurrency.State), mapSnapshotState(cur.Rating.State)
		}
		src.ExecutableGeneration = func() controlplane.ExecutableGenerationStatus {
			exec := gen.CurrentExecutable()
			if exec == nil {
				return controlplane.ExecutableGenerationStatus{
					State:  controlplane.CapabilityDisabled,
					Reason: controlplane.ReasonDisabled,
				}
			}
			return controlplane.ExecutableGenerationStatus{
				ID:               exec.ID,
				Version:          exec.Version,
				State:            mapSnapshotState(exec.State),
				EvidenceObjectID: exec.EvidenceObjectID(),
				SourceID:         exec.SourceID,
			}
		}
	}
	if in.Executor != nil {
		exec := in.Executor
		src.RequestCoordinatorEnabled = exec.RequestCoordinator != nil && len(exec.RequestCoordinator.Slots) > 0
		src.AttemptCoordinatorEnabled = exec.AttemptCoordinator != nil && len(exec.AttemptCoordinator.Slots) > 0
		src.RequestCoordinatorIDs = requestRegistrationIDs(in.Production, &exec.AccountingRuntime)
		src.AttemptCoordinatorIDs = attemptRegistrationIDs(in.Production, &exec.AccountingRuntime)
		if exec.SecureSession != nil && exec.RuntimeSnapshot != nil && len(exec.RuntimeSnapshot.SecretGuardPlane().Guards) > 0 {
			backing := "injected"
			if in.Cfg != nil {
				backing = strings.ToLower(strings.TrimSpace(in.Cfg.SecureSession.Store))
				if in.Cfg.SecureSessionEffectivelyEnabled() {
					if backing == "" {
						backing = "memory"
					}
				} else if backing == "" {
					backing = "injected"
				}
			}
			src.SecretGuardQuarantine = func(ctx context.Context) (controlplane.ReadinessComponentStatus, error) {
				_ = ctx
				state := controlplane.CapabilityReady
				reason := controlplane.ReasonNone
				if exec.QuarantinePersistenceFaulted() {
					state = controlplane.CapabilityUnavailable
					reason = controlplane.ReasonBackingUnavailable
				}
				return controlplane.ReadinessComponentStatus{
					Component:        controlplane.ReadinessComponentSecretGuardQuarantine,
					State:            state,
					Reason:           reason,
					EnforcementScope: controlplane.EnforcementScopeForStoreBacking(backing, strings.EqualFold(strings.TrimSpace(backing), "postgres")),
					StoreBacking:     backing,
				}, nil
			}
		}
	}
	if in.TerminalWork != nil {
		tw := in.TerminalWork
		src.TerminalRecovery = tw.readinessComponent
	}
	return corecp.NewReadinessReportService(src)
}

// BillingSpoolReadiness is implemented by a terminal usage sink that buffers
// billing evidence durably before delivery; the readiness report shows its
// state. The spool itself is Enterprise code, so the mapping from its health to
// a readiness state lives with it.
type BillingSpoolReadiness interface {
	BillingSpoolReadiness() (controlplane.CapabilityState, controlplane.ReasonCode)
}

func billingSpoolHealth(sink billingport.TerminalUsageSink) func(context.Context) (controlplane.ReadinessComponentStatus, error) {
	probe, ok := sink.(BillingSpoolReadiness)
	if !ok {
		return nil
	}
	return func(_ context.Context) (controlplane.ReadinessComponentStatus, error) {
		state, reason := probe.BillingSpoolReadiness()
		return controlplane.ReadinessComponentStatus{
			Component: controlplane.ReadinessComponentBillingSpool, State: state, Reason: reason,
			EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess, StoreBacking: "injected",
		}, nil
	}
}

func mapSnapshotState(s economics.SnapshotState) controlplane.CapabilityState {
	switch s {
	case economics.SnapshotReady, economics.SnapshotStale:
		return controlplane.CapabilityReady
	case economics.SnapshotDegraded:
		return controlplane.CapabilityDegraded
	case economics.SnapshotUnavailable:
		return controlplane.CapabilityUnavailable
	case economics.SnapshotDisabled:
		return controlplane.CapabilityDisabled
	default:
		return controlplane.CapabilityUnavailable
	}
}

func readinessStoreBackings(cfg *config.Config, prod ProductionOptions) corecp.ReadinessStoreBackings {
	out := corecp.ReadinessStoreBackings{}
	if cfg == nil {
		return out
	}
	out.ControlPlane = strings.ToLower(strings.TrimSpace(cfg.ControlPlane.Store))
	if out.ControlPlane == "" && cfg.ControlPlane.Enabled {
		out.ControlPlane = "memory"
	}
	if cfg.Metering.Enabled {
		out.Metering = strings.ToLower(strings.TrimSpace(cfg.Metering.Journal.Store))
		if out.Metering == "" {
			out.Metering = "memory"
		}
	}
	if prod.MeteringRecorder != nil {
		out.Metering = "injected"
	}
	if cfg.Accounting.Authority.Enabled {
		out.Usage = strings.ToLower(strings.TrimSpace(cfg.Accounting.Authority.Store))
		if out.Usage == "" {
			out.Usage = "memory"
		}
	}
	if cfg.Accounting.Concurrency.Enabled {
		out.Concurrency = strings.ToLower(strings.TrimSpace(cfg.Accounting.Concurrency.Store))
		if out.Concurrency == "" {
			out.Concurrency = "memory"
		}
	}
	return out
}
