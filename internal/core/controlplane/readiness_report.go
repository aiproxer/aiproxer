package controlplane

import (
	"context"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// ReadinessStoreBackings names configured store backings for readiness scope
// classification (15.8).
type ReadinessStoreBackings struct {
	ControlPlane string
	Metering     string
	Usage        string
	Concurrency  string
}

// ReadinessReportSources supplies optional readiness probes for independent
// components (15.7). Nil callbacks mean the component is disabled.
type ReadinessReportSources struct {
	Now                       func() time.Time
	ControlPlane              func(context.Context) (cp.CapabilityStatus, error)
	UsageAuthority            func(context.Context) (cp.AccountingAuthorityStatus, error)
	ConcurrencyAuthority      func(context.Context) (cp.ConcurrencyAuthorityStatus, error)
	MeteringJournal           func(context.Context) (cp.ReadinessComponentStatus, error)
	BillingSpool              func(context.Context) (cp.ReadinessComponentStatus, error)
	SnapshotStates            func() (usage, concurrency, rating cp.CapabilityState)
	ExecutableGeneration      func() cp.ExecutableGenerationStatus
	RequestCoordinatorEnabled bool
	AttemptCoordinatorEnabled bool
	RequestCoordinatorIDs     []string
	AttemptCoordinatorIDs     []string
	SecretGuardQuarantine     func(context.Context) (cp.ReadinessComponentStatus, error)
	TerminalRecovery          func(context.Context) (cp.ReadinessComponentStatus, error)
	StoreBackings             ReadinessStoreBackings
}

// ReadinessReportService aggregates independent readiness components.
type ReadinessReportService struct {
	src ReadinessReportSources
}

// NewReadinessReportService returns a readiness report reader.
func NewReadinessReportService(src ReadinessReportSources) *ReadinessReportService {
	return &ReadinessReportService{src: src}
}

// Report returns independent component rows and aggregate protected-traffic
// posture (15.7).
func (s *ReadinessReportService) Report(ctx context.Context) (cp.ReadinessReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	if s != nil && s.src.Now != nil {
		now = s.src.Now().UTC()
	}
	src := ReadinessReportSources{}
	if s != nil {
		src = s.src
	}
	components := make([]cp.ReadinessComponentStatus, 0, 12)
	var execStatus cp.ExecutableGenerationStatus
	if src.ExecutableGeneration != nil {
		execStatus = src.ExecutableGeneration()
		execStatus.LastUpdatedAt = now
	} else {
		execStatus = cp.ExecutableGenerationStatus{
			State:         cp.CapabilityDisabled,
			Reason:        cp.ReasonDisabled,
			LastUpdatedAt: now,
		}
	}
	components = appendReadinessProbe(ctx, components, now, cp.ReadinessComponentMeteringJournal, src.MeteringJournal, cp.ReadinessComponentStatus{})
	components = appendReadinessProbe(ctx, components, now, cp.ReadinessComponentBillingSpool, src.BillingSpool, cp.ReadinessComponentStatus{
		State: cp.CapabilityUnavailable, Reason: cp.ReasonBackingUnavailable,
		EnforcementScope: cp.EnforcementScopeAdvisorySingleProcess, StoreBacking: "injected",
	})
	if src.ControlPlane != nil {
		if st, err := src.ControlPlane(ctx); err == nil {
			components = append(components, cp.ReadinessComponentStatus{
				Component:        cp.ReadinessComponentControlPlane,
				State:            st.State,
				Reason:           st.Reason,
				EnforcementScope: cp.EnforcementScopeForStoreBacking(src.StoreBackings.ControlPlane, false),
				StoreBacking:     src.StoreBackings.ControlPlane,
				LastUpdatedAt:    now,
			})
		} else {
			components = append(components, unavailableComponent(cp.ReadinessComponentControlPlane, src.StoreBackings.ControlPlane, now))
		}
	} else {
		components = append(components, disabledComponent(cp.ReadinessComponentControlPlane, now))
	}
	if src.UsageAuthority != nil {
		if st, err := src.UsageAuthority(ctx); err == nil {
			components = append(components, cp.ReadinessComponentStatus{
				Component:        cp.ReadinessComponentUsageAuthority,
				State:            mapAccountingAuthorityState(st.State),
				Reason:           st.Reason,
				EnforcementScope: cp.EnforcementScopeForStoreBacking(src.StoreBackings.Usage, true),
				StoreBacking:     src.StoreBackings.Usage,
				LastUpdatedAt:    now,
			})
		} else {
			components = append(components, unavailableComponent(cp.ReadinessComponentUsageAuthority, src.StoreBackings.Usage, now))
		}
	} else {
		components = append(components, disabledComponent(cp.ReadinessComponentUsageAuthority, now))
	}
	if src.ConcurrencyAuthority != nil {
		if st, err := src.ConcurrencyAuthority(ctx); err == nil {
			components = append(components, cp.ReadinessComponentStatus{
				Component:        cp.ReadinessComponentConcurrencyAuthority,
				State:            mapConcurrencyAuthorityState(st.State),
				Reason:           mapConcurrencyReason(st.Reason),
				EnforcementScope: cp.EnforcementScopeForStoreBacking(src.StoreBackings.Concurrency, true),
				StoreBacking:     src.StoreBackings.Concurrency,
				LastUpdatedAt:    now,
			})
		} else {
			components = append(components, unavailableComponent(cp.ReadinessComponentConcurrencyAuthority, src.StoreBackings.Concurrency, now))
		}
	} else {
		components = append(components, disabledComponent(cp.ReadinessComponentConcurrencyAuthority, now))
	}
	components = append(components, enabledComponent(cp.ReadinessComponentRequestCoordinator, src.RequestCoordinatorEnabled, src.RequestCoordinatorIDs, now))
	components = append(components, enabledComponent(cp.ReadinessComponentAttemptCoordinator, src.AttemptCoordinatorEnabled, src.AttemptCoordinatorIDs, now))
	if src.SnapshotStates != nil {
		usage, concurrency, rating := src.SnapshotStates()
		components = append(components, snapshotComponent(cp.ReadinessComponentUsageSnapshot, usage, now))
		components = append(components, snapshotComponent(cp.ReadinessComponentConcurrencySnapshot, concurrency, now))
		components = append(components, snapshotComponent(cp.ReadinessComponentRatingSnapshot, rating, now))
	} else {
		components = append(components, disabledComponent(cp.ReadinessComponentUsageSnapshot, now))
		components = append(components, disabledComponent(cp.ReadinessComponentConcurrencySnapshot, now))
		components = append(components, disabledComponent(cp.ReadinessComponentRatingSnapshot, now))
	}
	components = append(components, executableGenerationComponent(execStatus, now))
	components = appendReadinessProbe(ctx, components, now, cp.ReadinessComponentSecretGuardQuarantine, src.SecretGuardQuarantine, cp.ReadinessComponentStatus{
		State: cp.CapabilityUnavailable, Reason: cp.ReasonBackingUnavailable, EnforcementScope: cp.EnforcementScopeDisabled,
	})
	components = appendReadinessProbe(ctx, components, now, cp.ReadinessComponentTerminalRecovery, src.TerminalRecovery, cp.ReadinessComponentStatus{
		State: cp.CapabilityUnavailable, Reason: cp.ReasonBackingUnavailable, EnforcementScope: cp.EnforcementScopeDisabled,
	})
	return cp.ReadinessReport{
		Components:           components,
		ExecutableGeneration: execStatus,
		Posture:              cp.AggregateProtectedTrafficPosture(components, now),
	}, nil
}

func executableGenerationComponent(status cp.ExecutableGenerationStatus, at time.Time) cp.ReadinessComponentStatus {
	state := status.State
	if state == "" {
		state = cp.CapabilityDisabled
	}
	reason := status.Reason
	if state == cp.CapabilityDisabled && reason == "" {
		reason = cp.ReasonDisabled
	}
	return cp.ReadinessComponentStatus{
		Component:         cp.ReadinessComponentExecutableGeneration,
		State:             state,
		Reason:            reason,
		EnforcementScope:  cp.EnforcementScopeAdvisorySingleProcess,
		GenerationID:      status.ID,
		GenerationVersion: status.Version,
		EvidenceObjectID:  status.EvidenceObjectID,
		LastUpdatedAt:     at,
	}
}

func disabledComponent(id cp.ReadinessComponentID, at time.Time) cp.ReadinessComponentStatus {
	return cp.ReadinessComponentStatus{
		Component:        id,
		State:            cp.CapabilityDisabled,
		Reason:           cp.ReasonDisabled,
		EnforcementScope: cp.EnforcementScopeDisabled,
		LastUpdatedAt:    at,
	}
}

func unavailableComponent(id cp.ReadinessComponentID, backing string, at time.Time) cp.ReadinessComponentStatus {
	return cp.ReadinessComponentStatus{
		Component:        id,
		State:            cp.CapabilityUnavailable,
		Reason:           cp.ReasonBackingUnavailable,
		EnforcementScope: cp.EnforcementScopeForStoreBacking(backing, true),
		StoreBacking:     backing,
		LastUpdatedAt:    at,
	}
}

func appendReadinessProbe(ctx context.Context, components []cp.ReadinessComponentStatus, now time.Time, id cp.ReadinessComponentID, probe func(context.Context) (cp.ReadinessComponentStatus, error), fallback cp.ReadinessComponentStatus) []cp.ReadinessComponentStatus {
	if probe == nil {
		return append(components, disabledComponent(id, now))
	}
	row, err := probe(ctx)
	if err != nil {
		if fallback.State == "" {
			return append(components, disabledComponent(id, now))
		}
		fallback.Component, fallback.LastUpdatedAt = id, now
		return append(components, fallback)
	}
	row.LastUpdatedAt = now
	return append(components, row)
}

func enabledComponent(id cp.ReadinessComponentID, enabled bool, providerIDs []string, at time.Time) cp.ReadinessComponentStatus {
	if !enabled {
		return disabledComponent(id, at)
	}
	return cp.ReadinessComponentStatus{
		Component: id, State: cp.CapabilityReady,
		EnforcementScope: cp.EnforcementScopeAdvisorySingleProcess,
		ProviderIDs:      append([]string(nil), providerIDs...),
		LastUpdatedAt:    at,
	}
}

func snapshotComponent(id cp.ReadinessComponentID, state cp.CapabilityState, at time.Time) cp.ReadinessComponentStatus {
	if state == "" {
		state = cp.CapabilityDisabled
	}
	return cp.ReadinessComponentStatus{
		Component:        id,
		State:            state,
		EnforcementScope: cp.EnforcementScopeAdvisorySingleProcess,
		LastUpdatedAt:    at,
	}
}

func mapAccountingAuthorityState(s cp.AccountingAuthorityState) cp.CapabilityState {
	switch s {
	case cp.AccountingAuthorityReady:
		return cp.CapabilityReady
	case cp.AccountingAuthorityDegraded, cp.AccountingAuthorityAdvisoryOnly:
		return cp.CapabilityDegraded
	case cp.AccountingAuthorityUnavailable:
		return cp.CapabilityUnavailable
	case cp.AccountingAuthorityDisabled:
		return cp.CapabilityDisabled
	default:
		return cp.CapabilityUnavailable
	}
}

func mapConcurrencyAuthorityState(s cp.ConcurrencyAuthorityState) cp.CapabilityState {
	switch s {
	case cp.ConcurrencyAuthorityReady:
		return cp.CapabilityReady
	case cp.ConcurrencyAuthorityDegraded:
		return cp.CapabilityDegraded
	case cp.ConcurrencyAuthorityUnavailable:
		return cp.CapabilityUnavailable
	case cp.ConcurrencyAuthorityDisabled:
		return cp.CapabilityDisabled
	default:
		return cp.CapabilityUnavailable
	}
}

func mapConcurrencyReason(reason string) cp.ReasonCode {
	switch reason {
	case "backing_degraded":
		return cp.ReasonStoreNotReady
	case "backing_unavailable":
		return cp.ReasonBackingUnavailable
	default:
		return cp.ReasonNone
	}
}
