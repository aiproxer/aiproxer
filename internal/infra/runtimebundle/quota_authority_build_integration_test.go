package runtimebundle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestBuild_QuotaPolicyWiresGenerationRequestAuthority(t *testing.T) {
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := metering.ComponentKey{Direction: metering.DirectionNone, Component: "provider:utilization", Unit: metering.UnitPercent, SchemaID: "quota.v1"}
	cfg := baseAuthorityConfig(false, "fail_closed")
	cfg.Accounting.Authority.Quota = &config.AccountingQuotaConfig{
		ID: "provider-quota", Version: "2026-01", StoreID: "quota-store", TenantID: "tenant-1", ProviderAccountKey: "account-a",
		PoolID: "primary", WindowID: "minute", ResetAt: reset.Format(time.RFC3339), Freshness: "5m",
		Required: []metering.ComponentKey{field},
	}
	store := &buildQuotaStore{observation: buildQuotaObservation(now, reset, field)}
	opts := baseAuthorityOptions(t)
	opts.Testing.Clock = func() time.Time { return now }
	opts.Production.MeteringAccountWindowStore = store
	_, candidate := mustProcessAndCandidate(t, cfg, opts)
	coord := candidate.Executor().RequestCoordinator
	require.NotNil(t, coord)
	require.Len(t, coord.Slots, 1)
	require.Equal(t, "provider-quota:provider-quota", coord.Slots[0].ID)

	decision, err := coord.Admit(context.Background(), authority.RequestAdmission{
		RequestID: "req-1", Perspective: metering.PerspectiveCustomer, Lifecycle: metering.LifecycleLogicalRequest,
		Exposure: economics.ExposureBasis{Perspective: metering.PerspectiveCustomer, Boundary: metering.BoundaryFrontendIngress, Lifecycle: metering.LifecycleLogicalRequest},
	})
	require.NoError(t, err)
	require.Equal(t, authority.DecisionAllow, decision.Kind)
	require.Empty(t, decision.Stack.Handles(), "gauge authority must not create a reservation hold")
	require.Len(t, decision.ProviderDecisions, 1)
	require.Equal(t, "2026-01", decision.ProviderDecisions[0].Evidence.QuotaPolicyRef.Version)
}

type buildQuotaStore struct {
	mu          sync.RWMutex
	observation metering.Observation
}

func (s *buildQuotaStore) ListAccountWindowObservations(ctx context.Context, _ coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error) {
	if err := ctx.Err(); err != nil {
		return coremetering.AccountWindowObservationPage{}, err
	}
	s.mu.RLock()
	observation := s.observation.Clone()
	s.mu.RUnlock()
	return coremetering.AccountWindowObservationPage{Observations: []metering.Observation{observation}}, nil
}

func (s *buildQuotaStore) ProjectAccountWindows(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowProjectionPage, error) {
	return coremetering.AccountWindowProjectionPage{}, nil
}

func buildQuotaObservation(now, reset time.Time, field metering.ComponentKey) metering.Observation {
	value, _ := metering.ParseDecimal("12")
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: "quota-1", SourceEventKey: "quota-1-event", Revision: 1,
		StreamID: "quota-stream", Sequence: 1, Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderHeader,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleAuxiliaryRequest,
		Subject:     metering.SubjectRef{Kind: metering.SubjectAccountWindow, StoreID: "quota-store", TenantID: "tenant-1", ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Correlation: metering.CorrelationV2{StoreID: "quota-store", TenantID: "tenant-1", ProviderAccountKey: "account-a"},
		Semantics:   metering.SemanticsGauge, ObservedAt: now.Add(-time.Minute), ReceivedAt: now.Add(-time.Minute).Add(time.Second), MappingRef: "account-window.v1",
		Measures: []metering.Measure{{Key: field, Value: &value, Quality: metering.QualityObserved}},
	}
}
