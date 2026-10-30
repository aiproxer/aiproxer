package authoritycoord_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestQuotaRequestProvider_QueriesExactBindingAndMapsFreshGaugeWithoutMoney(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding: authority.QuotaBinding{
			StoreID: "metering-1", TenantID: "tenant-1", ProviderAccountKey: "account-a",
			PoolID: "primary", WindowID: "minute", ResetAt: reset,
		},
		Freshness: time.Minute,
		Required:  []authority.QuotaField{field},
		Thresholds: []authority.QuotaThreshold{{
			Kind: authority.QuotaThresholdMaxUtilization, Field: field,
			ValueKind: authority.QuotaValuePercent, Value: testQuotaDecimal(t, "80"),
		}},
	})
	require.NoError(t, err)
	store := &quotaPageStore{pages: []coremetering.AccountWindowObservationPage{
		{Observations: []metering.Observation{testQuotaObservation("quota-1", "account-a", "primary", "minute", reset, now.Add(-time.Minute), "tenant-1", field, "12")}, NextCursor: "next"},
		{},
	}}
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{
		ID: "provider-quota-account-a", Policy: policy, Store: store, Now: func() time.Time { return now }, PageSize: 1,
	})
	require.NoError(t, err)

	got, err := provider.AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionAllow, got.Kind)
	require.Equal(t, "provider-quota-account-a", got.ProviderID)
	require.Equal(t, authority.StageRequestAdmit, got.Stage)
	require.Empty(t, got.Reservations)
	require.Empty(t, got.Exposure.Quantities)
	require.False(t, got.Exposure.Money.Present)
	require.Equal(t, policy.PolicyRef(), *got.Evidence.QuotaPolicyRef)
	require.Len(t, got.Evidence.EvidenceRefs, 1)
	require.Equal(t, policy.PolicyRef().Hash, got.Evidence.Attrs["policy_hash"])
	require.Equal(t, authority.ReadinessReady, got.Readiness)
	require.Len(t, store.queries, 2)
	require.Empty(t, store.queries[0].Cursor)
	require.Equal(t, "next", store.queries[1].Cursor)
	for _, query := range store.queries {
		require.Equal(t, "metering-1", query.StoreID)
		require.Equal(t, "tenant-1", query.TenantID)
		require.Equal(t, "account-a", query.ProviderAccountKey)
		require.Equal(t, "primary", query.PoolID)
		require.Equal(t, "minute", query.WindowID)
		require.NotNil(t, query.ResetAt)
		require.True(t, query.ResetAt.Equal(reset))
		require.Equal(t, now, query.AsOf)
	}
}

func TestQuotaRequestProvider_MapsConfiguredEvidenceFailuresToNonIndeterminateDecision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []authority.QuotaField{field},
		Failures: authority.QuotaFailurePolicy{Missing: authority.QuotaFailureIndeterminate},
	})
	require.NoError(t, err)
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{
		Policy: policy, Store: &quotaPageStore{}, Now: func() time.Time { return now },
	})
	require.NoError(t, err)

	got, err := provider.AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionDeny, got.Kind, "generic authority has no indeterminate result")
	require.Equal(t, "missing_evidence", got.Evidence.Attrs["quota_reason"])
	require.Equal(t, "missing", got.Evidence.Attrs["evidence_status"])
	require.Equal(t, authority.ReadinessUnavailable, got.Readiness)
	require.Empty(t, got.Reservations)
}

func TestQuotaRequestProvider_MapsFreshThresholdDenyAndStalePartialPostures(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	newProvider := func(t *testing.T, failures authority.QuotaFailurePolicy, observation metering.Observation) *authoritycoord.QuotaRequestProvider {
		t.Helper()
		policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
			ID: "provider-quota", Version: "2026-01",
			Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
			Freshness: time.Minute, Required: []authority.QuotaField{field},
			Thresholds: []authority.QuotaThreshold{{Kind: authority.QuotaThresholdMaxUtilization, Field: field, ValueKind: authority.QuotaValuePercent, Value: testQuotaDecimal(t, "80")}},
			Failures:   failures,
		})
		require.NoError(t, err)
		provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{
			Policy: policy, Store: &quotaPageStore{pages: []coremetering.AccountWindowObservationPage{{Observations: []metering.Observation{observation}}}}, Now: func() time.Time { return now },
		})
		require.NoError(t, err)
		return provider
	}

	atLimit := testQuotaObservation("at-limit", "account-a", "primary", "minute", reset, now.Add(-time.Second), "", field, "80")
	denied, err := newProvider(t, authority.QuotaFailurePolicy{}, atLimit).AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionDeny, denied.Kind)
	require.Equal(t, "threshold_exceeded", denied.Evidence.Attrs["quota_reason"])
	require.Equal(t, authority.ReadinessReady, denied.Readiness, "a complete threshold denial is deterministic")

	stale := testQuotaObservation("stale", "account-a", "primary", "minute", reset, now.Add(-10*time.Minute), "", field, "12")
	staleDecision, err := newProvider(t, authority.QuotaFailurePolicy{Stale: authority.QuotaFailureIndeterminate}, stale).AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionDeny, staleDecision.Kind)
	require.Equal(t, "stale", staleDecision.Evidence.Attrs["evidence_status"])
	require.Equal(t, "stale_evidence", staleDecision.Evidence.Attrs["quota_reason"])

	partial := testQuotaObservation("partial", "account-a", "primary", "minute", reset, now.Add(-time.Second), "", field, "12")
	partial.Measures[0] = metering.Measure{Key: field, Quality: metering.QualityUnavailable}
	partialDecision, err := newProvider(t, authority.QuotaFailurePolicy{Partial: authority.QuotaFailureIndeterminate}, partial).AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionDeny, partialDecision.Kind)
	require.Equal(t, "partial", partialDecision.Evidence.Attrs["evidence_status"])
	require.Equal(t, "partial_evidence", partialDecision.Evidence.Attrs["quota_reason"])
}

func TestQuotaRequestProvider_ReaderErrorsAreTypedBoundedAndHonorCancellation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []authority.QuotaField{field},
	})
	require.NoError(t, err)
	readerErr := errors.New("provider backend contains an unbounded private diagnostic")
	store := &quotaPageStore{err: readerErr}
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{Policy: policy, Store: store, Now: func() time.Time { return now }})
	require.NoError(t, err)
	decision, err := provider.AdmitRequest(context.Background(), validRequestAdmission())
	var typed *authoritycoord.QuotaReaderError
	require.ErrorAs(t, err, &typed)
	require.ErrorIs(t, err, readerErr)
	require.LessOrEqual(t, len(err.Error()), 256)
	require.Equal(t, authority.DecisionDeny, decision.Kind)
	require.Equal(t, authority.ReadinessUnavailable, decision.Readiness)
	require.Equal(t, policy.PolicyRef(), *decision.Evidence.QuotaPolicyRef)
	require.Equal(t, "telemetry_unavailable", decision.Evidence.Attrs["quota_reason"])
	require.Equal(t, "unavailable", decision.Evidence.Attrs["evidence_status"])

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before := store.callCount.Load()
	decision, err = provider.AdmitRequest(canceled, validRequestAdmission())
	require.ErrorAs(t, err, &typed)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, store.callCount.Load(), "cancellation is checked before querying")
	require.Equal(t, authority.DecisionDeny, decision.Kind)
	require.Equal(t, authority.ReadinessUnavailable, decision.Readiness)
	require.Equal(t, policy.PolicyRef(), *decision.Evidence.QuotaPolicyRef)
	require.Equal(t, "telemetry_unavailable", decision.Evidence.Attrs["quota_reason"])
	require.Equal(t, "unavailable", decision.Evidence.Attrs["evidence_status"])
}

func TestQuotaRequestProvider_MidReadCancellationPreservesUnavailableDecision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []authority.QuotaField{field},
		Failures: authority.QuotaFailurePolicy{Unavailable: authority.QuotaFailureIndeterminate},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAfterQuotaPageStore{
		cancel: cancel,
		page: coremetering.AccountWindowObservationPage{
			Observations: []metering.Observation{testQuotaObservation("quota-1", "account-a", "primary", "minute", reset, now.Add(-time.Second), "", field, "12")},
			NextCursor:   "next",
		},
	}
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{
		Policy: policy, Store: store, Now: func() time.Time { return now }, PageSize: 1,
	})
	require.NoError(t, err)

	decision, err := provider.AdmitRequest(ctx, validRequestAdmission())
	var typed *authoritycoord.QuotaReaderError
	require.ErrorAs(t, err, &typed)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, authority.DecisionDeny, decision.Kind)
	require.Equal(t, authority.ReadinessUnavailable, decision.Readiness)
	require.Equal(t, policy.PolicyRef(), *decision.Evidence.QuotaPolicyRef)
	require.Equal(t, "telemetry_unavailable", decision.Evidence.Attrs["quota_reason"])
	require.Equal(t, "unavailable", decision.Evidence.Attrs["evidence_status"])
	require.Equal(t, int32(1), store.calls.Load(), "cancellation after the first page must prevent a second read")
}

func TestQuotaRequestProvider_ConfiguredUnavailablePosturesNeverAllowOnCancellation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	for _, failure := range []authority.QuotaFailureAction{authority.QuotaFailureDeny, authority.QuotaFailureIndeterminate} {
		t.Run(string(failure), func(t *testing.T) {
			t.Parallel()
			policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
				ID: "provider-quota", Version: "2026-01",
				Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
				Freshness: time.Minute, Required: []authority.QuotaField{field},
				Failures: authority.QuotaFailurePolicy{Unavailable: failure},
			})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{
				Policy: policy, Now: func() time.Time { return now },
			})
			require.NoError(t, err)

			decision, err := provider.AdmitRequest(ctx, validRequestAdmission())
			var typed *authoritycoord.QuotaReaderError
			require.ErrorAs(t, err, &typed)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, authority.DecisionDeny, decision.Kind, "generic authority maps both quota failure postures fail-closed")
			require.Equal(t, authority.ReadinessUnavailable, decision.Readiness)
			require.Equal(t, policy.PolicyRef(), *decision.Evidence.QuotaPolicyRef)
			require.Equal(t, "telemetry_unavailable", decision.Evidence.Attrs["quota_reason"])
			require.Equal(t, "unavailable", decision.Evidence.Attrs["evidence_status"])
		})
	}
}

func TestQuotaRequestProvider_IsConcurrentAndPolicyRefIsFrozen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []authority.QuotaField{field},
	})
	require.NoError(t, err)
	store := &quotaPageStore{repeat: true, pages: []coremetering.AccountWindowObservationPage{{Observations: []metering.Observation{
		testQuotaObservation("quota-1", "account-a", "primary", "minute", reset, now.Add(-time.Second), "", field, "12"),
	}}}}
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{Policy: policy, Store: store, Now: func() time.Time { return now }})
	require.NoError(t, err)
	ref := provider.PolicyRef()
	require.Equal(t, policy.PolicyRef(), ref)
	const calls = 32
	var wg sync.WaitGroup
	results := make(chan authority.Decision, calls)
	for range calls {
		wg.Go(func() {
			decision, callErr := provider.AdmitRequest(context.Background(), validRequestAdmission())
			require.NoError(t, callErr)
			results <- decision
		})
	}
	wg.Wait()
	close(results)
	for decision := range results {
		require.Equal(t, authority.DecisionAllow, decision.Kind)
		require.Equal(t, ref, *decision.Evidence.QuotaPolicyRef)
	}
}

func TestQuotaRequestProvider_ReadsLaterGaugeChangesWithoutChangingFrozenPolicyRef(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := testQuotaField("provider:utilization", metering.UnitPercent)
	policy, err := authority.CompileQuotaPolicy(authority.QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   authority.QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []authority.QuotaField{field},
		Thresholds: []authority.QuotaThreshold{{Kind: authority.QuotaThresholdMaxUtilization, Field: field, ValueKind: authority.QuotaValuePercent, Value: testQuotaDecimal(t, "80")}},
	})
	require.NoError(t, err)
	store := &mutableQuotaStore{observation: testQuotaObservation("quota-1", "account-a", "primary", "minute", reset, now.Add(-time.Second), "", field, "12")}
	provider, err := authoritycoord.NewQuotaRequestProvider(authoritycoord.QuotaRequestProviderConfig{Policy: policy, Store: store, Now: func() time.Time { return now }})
	require.NoError(t, err)
	first, err := provider.AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionAllow, first.Kind)
	ref := *first.Evidence.QuotaPolicyRef

	store.set(testQuotaObservation("quota-2", "account-a", "primary", "minute", reset, now.Add(-time.Second), "", field, "80"))
	second, err := provider.AdmitRequest(context.Background(), validRequestAdmission())
	require.NoError(t, err)
	require.Equal(t, authority.DecisionDeny, second.Kind)
	require.Equal(t, "threshold_exceeded", second.Evidence.Attrs["quota_reason"])
	require.Equal(t, ref, *second.Evidence.QuotaPolicyRef, "gauge changes affect later decisions, not the frozen policy identity")
}

type quotaPageStore struct {
	mu        sync.Mutex
	queries   []coremetering.AccountWindowQuery
	pages     []coremetering.AccountWindowObservationPage
	repeat    bool
	err       error
	callCount atomic.Int32
}

type mutableQuotaStore struct {
	mu          sync.RWMutex
	observation metering.Observation
}

type cancelAfterQuotaPageStore struct {
	page   coremetering.AccountWindowObservationPage
	cancel context.CancelFunc
	calls  atomic.Int32
}

func (s *cancelAfterQuotaPageStore) ListAccountWindowObservations(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error) {
	if s.calls.Add(1) == 1 {
		s.cancel()
		return s.page, nil
	}
	return coremetering.AccountWindowObservationPage{}, context.Canceled
}

func (s *cancelAfterQuotaPageStore) ProjectAccountWindows(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowProjectionPage, error) {
	return coremetering.AccountWindowProjectionPage{}, nil
}

func (s *mutableQuotaStore) set(observation metering.Observation) {
	s.mu.Lock()
	s.observation = observation
	s.mu.Unlock()
}

func (s *mutableQuotaStore) ListAccountWindowObservations(ctx context.Context, _ coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error) {
	if err := ctx.Err(); err != nil {
		return coremetering.AccountWindowObservationPage{}, err
	}
	s.mu.RLock()
	observation := s.observation.Clone()
	s.mu.RUnlock()
	return coremetering.AccountWindowObservationPage{Observations: []metering.Observation{observation}}, nil
}

func (s *mutableQuotaStore) ProjectAccountWindows(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowProjectionPage, error) {
	return coremetering.AccountWindowProjectionPage{}, nil
}

func (s *quotaPageStore) ListAccountWindowObservations(ctx context.Context, query coremetering.AccountWindowQuery) (coremetering.AccountWindowObservationPage, error) {
	if err := ctx.Err(); err != nil {
		return coremetering.AccountWindowObservationPage{}, err
	}
	s.callCount.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
	if s.err != nil {
		return coremetering.AccountWindowObservationPage{}, s.err
	}
	if len(s.pages) == 0 {
		return coremetering.AccountWindowObservationPage{}, nil
	}
	if s.repeat {
		return s.pages[0], nil
	}
	page := s.pages[0]
	s.pages = s.pages[1:]
	return page, nil
}

func (s *quotaPageStore) ProjectAccountWindows(context.Context, coremetering.AccountWindowQuery) (coremetering.AccountWindowProjectionPage, error) {
	return coremetering.AccountWindowProjectionPage{}, nil
}

func testQuotaField(component, unit string) authority.QuotaField {
	return authority.QuotaField{Direction: metering.DirectionNone, Component: component, Unit: unit, SchemaID: "quota.v1"}
}

func testQuotaDecimal(t *testing.T, value string) metering.Decimal {
	t.Helper()
	parsed, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return parsed
}

func testQuotaObservation(id, account, pool, window string, reset, observed time.Time, tenant string, field authority.QuotaField, value string) metering.Observation {
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "quota-stream", Sequence: uint64(observed.Unix()), Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderHeader, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleAuxiliaryRequest,
		Subject:     metering.SubjectRef{Kind: metering.SubjectAccountWindow, StoreID: "metering-1", TenantID: tenant, ProviderAccountKey: account, PoolID: pool, WindowID: window, ResetAt: reset},
		Correlation: metering.CorrelationV2{StoreID: "metering-1", TenantID: tenant, ProviderAccountKey: account},
		Semantics:   metering.SemanticsGauge, ObservedAt: observed.UTC(), ReceivedAt: observed.Add(time.Second).UTC(), MappingRef: "account-window.v1",
		Measures: []metering.Measure{{Key: field, Value: testQuotaDecimalPtr(value), Quality: metering.QualityObserved}},
	}
}

func testQuotaDecimalPtr(value string) *metering.Decimal {
	parsed, err := metering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return &parsed
}

var (
	_ coremetering.AccountWindowStore = (*quotaPageStore)(nil)
	_ authority.RequestProvider       = (*authoritycoord.QuotaRequestProvider)(nil)
)
