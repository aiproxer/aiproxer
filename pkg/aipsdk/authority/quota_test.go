package authority

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestQuotaPolicy_FreshMatchingGaugeAllowsAndReplayIsStable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	utilization := quotaField("provider:utilization", metering.UnitPercent)
	headroom := quotaField("provider:headroom", metering.UnitSecond)
	policy, err := CompileQuotaPolicy(QuotaPolicyConfig{
		ID:      "provider-quota",
		Version: "2026-01",
		Binding: QuotaBinding{
			StoreID: "store-1", ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset,
		},
		Freshness: 5 * time.Minute,
		Required:  []QuotaField{headroom, utilization},
		Thresholds: []QuotaThreshold{
			{Kind: QuotaThresholdMaxUtilization, Field: utilization, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "80")},
			{Kind: QuotaThresholdMinHeadroom, Field: headroom, ValueKind: QuotaValueAbsolute, Value: quotaDecimal(t, "1")},
		},
	})
	require.NoError(t, err)

	observed := now.Add(-time.Minute)
	first := quotaObservation(
		"quota-1", "account-a", "primary", "minute", reset, observed,
		quotaMeasure(utilization, "12.5", metering.QualityObserved),
		quotaMeasure(headroom, "2", metering.QualityObserved),
	)
	replay := first.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(30 * time.Second)

	input := QuotaEvaluationInput{At: now, Observations: []metering.Observation{replay, first}}
	got, err := policy.Evaluate(input)
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionAllow, got.Kind)
	require.Equal(t, QuotaReasonAllowed, got.Reason)
	require.Equal(t, QuotaEvidenceComplete, got.EvidenceStatus)
	require.Equal(t, policy.PolicyRef(), got.Policy)
	require.Len(t, got.EvidenceRefs, 1, "a transport replay is one evidence reference")
	require.Equal(t, 1, got.ReplayCount)
	require.Equal(t, first.ID, got.HeadObservationID)
	require.NoError(t, got.Validate())

	reversed, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{first, replay}})
	require.NoError(t, err)
	require.Equal(t, got, reversed, "arrival order must not affect quota admission")

	samePolicy, err := CompileQuotaPolicy(QuotaPolicyConfig{
		ID:      "provider-quota",
		Version: "2026-01",
		Binding: QuotaBinding{
			StoreID: "store-1", ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset,
		},
		Freshness: 5 * time.Minute,
		Required:  []QuotaField{utilization, headroom},
		Thresholds: []QuotaThreshold{
			{Kind: QuotaThresholdMinHeadroom, Field: headroom, ValueKind: QuotaValueAbsolute, Value: quotaDecimal(t, "1")},
			{Kind: QuotaThresholdMaxUtilization, Field: utilization, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "80")},
		},
	})
	require.NoError(t, err)
	require.Equal(t, policy.PolicyRef(), samePolicy.PolicyRef(), "unordered policy inputs have one immutable identity")
	require.NotEmpty(t, policy.Hash())
}

func TestQuotaPolicy_ThresholdTypesAndSafetyBoundaries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	utilization := quotaField("provider:utilization", metering.UnitPercent)
	headroom := quotaField("provider:headroom", metering.UnitSecond)

	maxPolicy := compileQuotaTestPolicy(t, reset, QuotaPolicyConfig{
		Thresholds: []QuotaThreshold{{Kind: QuotaThresholdMaxUtilization, Field: utilization, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "80")}},
		Required:   []QuotaField{utilization},
	})
	denyAt, err := maxPolicy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{
		quotaObservation("at-limit", "account-a", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(utilization, "80", metering.QualityObserved)),
	}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionDeny, denyAt.Kind, "max utilization is a safety ceiling: equal is denied")
	require.Equal(t, QuotaReasonThresholdExceeded, denyAt.Reason)

	below, err := maxPolicy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{
		quotaObservation("below-limit", "account-a", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(utilization, "79.99", metering.QualityObserved)),
	}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionAllow, below.Kind)

	headroomPolicy := compileQuotaTestPolicy(t, reset, QuotaPolicyConfig{
		Thresholds: []QuotaThreshold{{Kind: QuotaThresholdMinHeadroom, Field: headroom, ValueKind: QuotaValueAbsolute, Value: quotaDecimal(t, "1")}},
		Required:   []QuotaField{headroom},
	})
	low, err := headroomPolicy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{
		quotaObservation("low-headroom", "account-a", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(headroom, "0.99", metering.QualityObserved)),
	}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionDeny, low.Kind)
	require.Equal(t, QuotaReasonHeadroomInsufficient, low.Reason)

	good, err := headroomPolicy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{
		quotaObservation("good-headroom", "account-a", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(headroom, "1", metering.QualityObserved)),
	}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionAllow, good.Kind)

	invalid := []QuotaPolicyConfig{
		{Thresholds: []QuotaThreshold{{Kind: QuotaThresholdMaxUtilization, Field: headroom, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "80")}}},
		{Thresholds: []QuotaThreshold{{Kind: QuotaThresholdMinHeadroom, Field: utilization, ValueKind: QuotaValueAbsolute, Value: quotaDecimal(t, "1")}}},
		{Thresholds: []QuotaThreshold{{Kind: QuotaThresholdMinHeadroom, Field: headroom, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "101")}}},
		{Thresholds: []QuotaThreshold{{Kind: QuotaThresholdMinHeadroom, Field: headroom, ValueKind: QuotaValueAbsolute, Value: quotaDecimal(t, "-1")}}},
	}
	for i, config := range invalid {
		config.ID = "provider-quota"
		config.Version = "2026-01"
		config.Binding = QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset}
		config.Freshness = time.Minute
		if config.Required == nil {
			config.Required = []QuotaField{headroom, utilization}
		}
		_, err := CompileQuotaPolicy(config)
		require.Error(t, err, "invalid threshold %d must fail closed", i)
		require.ErrorIs(t, err, ErrInvalidQuotaPolicy)
	}
}

func TestQuotaPolicy_MissingStalePartialAndUnavailableUseTypedConfiguredActions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	utilization := quotaField("provider:utilization", metering.UnitPercent)
	headroom := quotaField("provider:headroom", metering.UnitSecond)
	policy := compileQuotaTestPolicy(t, reset, QuotaPolicyConfig{
		Required: []QuotaField{utilization, headroom},
		Failures: QuotaFailurePolicy{
			Missing:     QuotaFailureIndeterminate,
			Stale:       QuotaFailureDeny,
			Partial:     QuotaFailureIndeterminate,
			Unavailable: QuotaFailureDeny,
			Mismatch:    QuotaFailureIndeterminate,
		},
	})

	missing, err := policy.Evaluate(QuotaEvaluationInput{At: now})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionIndeterminate, missing.Kind)
	require.Equal(t, QuotaReasonMissingEvidence, missing.Reason)
	require.Equal(t, QuotaEvidenceMissing, missing.EvidenceStatus)

	stale, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{
		quotaObservation("stale", "account-a", "primary", "minute", reset, now.Add(-10*time.Minute), quotaMeasure(utilization, "10", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved)),
	}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionDeny, stale.Kind)
	require.Equal(t, QuotaReasonStaleEvidence, stale.Reason)
	require.Equal(t, QuotaEvidenceStale, stale.EvidenceStatus)

	partial, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{
		quotaObservation("partial", "account-a", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(utilization, "10", metering.QualityObserved)),
	}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionIndeterminate, partial.Kind)
	require.Equal(t, QuotaReasonPartialEvidence, partial.Reason)
	require.Equal(t, QuotaEvidencePartial, partial.EvidenceStatus)

	unavailable, err := policy.Evaluate(QuotaEvaluationInput{At: now, TelemetryUnavailable: true})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionDeny, unavailable.Kind)
	require.Equal(t, QuotaReasonTelemetryUnavailable, unavailable.Reason)
	require.Equal(t, QuotaEvidenceUnavailable, unavailable.EvidenceStatus)
}

func TestQuotaPolicy_BindingResetAndAsOfHeadNeverSumGaugeSnapshots(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	utilization := quotaField("provider:utilization", metering.UnitPercent)
	headroom := quotaField("provider:headroom", metering.UnitSecond)
	policy := compileQuotaTestPolicy(t, reset, QuotaPolicyConfig{
		Required: []QuotaField{utilization, headroom},
		Thresholds: []QuotaThreshold{{
			Kind: QuotaThresholdMaxUtilization, Field: utilization, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "50"),
		}},
		Failures: QuotaFailurePolicy{Mismatch: QuotaFailureIndeterminate},
	})
	old := quotaObservation("old", "account-a", "primary", "minute", reset, now.Add(-3*time.Minute),
		quotaMeasure(utilization, "12", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved))
	newer := quotaObservation("newer", "account-a", "primary", "minute", reset, now.Add(-2*time.Minute),
		quotaMeasure(utilization, "13", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityUnavailable))
	future := quotaObservation("future", "account-a", "primary", "minute", reset, now.Add(-30*time.Second),
		quotaMeasure(utilization, "99", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved))

	decision, err := policy.Evaluate(QuotaEvaluationInput{At: now.Add(-90 * time.Second), Observations: []metering.Observation{future, newer, old}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionAllow, decision.Kind)
	require.Equal(t, "newer", decision.HeadObservationID)
	require.Equal(t, "13/0", quotaMeasureValue(t, decision, utilization), "history is a replacement gauge, not an additive series")
	require.NotEqual(t, "25/0", quotaMeasureValue(t, decision, utilization))
	require.Equal(t, 0, decision.ReplayCount, "as-of exclusion is not a replay")

	for _, wrong := range []struct {
		name   string
		obs    metering.Observation
		reason QuotaReason
	}{
		{"account", quotaObservation("wrong-account", "other-account", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(utilization, "1", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved)), QuotaReasonAccountMismatch},
		{"pool", quotaObservation("wrong-pool", "account-a", "other-pool", "minute", reset, now.Add(-time.Minute), quotaMeasure(utilization, "1", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved)), QuotaReasonPoolMismatch},
		{"window", quotaObservation("wrong-window", "account-a", "primary", "hour", reset, now.Add(-time.Minute), quotaMeasure(utilization, "1", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved)), QuotaReasonWindowMismatch},
		{"reset", quotaObservation("wrong-reset", "account-a", "primary", "minute", reset.Add(-time.Hour), now.Add(-time.Minute), quotaMeasure(utilization, "1", metering.QualityObserved), quotaMeasure(headroom, "2", metering.QualityObserved)), QuotaReasonResetMismatch},
	} {
		got, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{wrong.obs}})
		require.NoError(t, err, wrong.name)
		require.Equal(t, QuotaDecisionIndeterminate, got.Kind, wrong.name)
		require.Equal(t, wrong.reason, got.Reason, wrong.name)
	}

	oldEpoch := old.Clone()
	oldEpoch.Subject.ResetAt = reset.Add(-time.Hour)
	oldEpoch.Correlation = metering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: "account-a"}
	oldEpoch.SourceEventKey = "old-epoch-event"
	oldEpoch.ID = "old-epoch"
	got, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{oldEpoch}})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionIndeterminate, got.Kind)
	require.Equal(t, QuotaReasonResetMismatch, got.Reason)
}

func TestQuotaPolicy_SameTimeAcquisitionAndLineageHaveTotalOrder(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	utilization := quotaField("provider:utilization", metering.UnitPercent)
	policy := compileQuotaTestPolicy(t, reset, QuotaPolicyConfig{
		Required: []QuotaField{utilization},
		Thresholds: []QuotaThreshold{{
			Kind: QuotaThresholdMaxUtilization, Field: utilization, ValueKind: QuotaValuePercent, Value: quotaDecimal(t, "80"),
		}},
	})

	header := quotaObservation("same-observation", "account-a", "primary", "minute", reset, now.Add(-time.Minute),
		quotaMeasure(utilization, "10", metering.QualityObserved))
	response := header.Clone()
	response.Acquisition = metering.AcquisitionProviderResponse
	response.Correlation.ParentWorkID = "parent-response"
	response.Measures = []metering.Measure{quotaMeasure(utilization, "90", metering.QualityObserved)}
	require.NotEqual(t, header.Acquisition, response.Acquisition, "the observations use distinct acquisition channels")
	require.NotEqual(t, header.NormalizedLineageIdentity(), response.NormalizedLineageIdentity(), "the observations use distinct normalized lineage")
	require.NotEqual(t, header.IdentityKey(), response.IdentityKey(), "acquisition and normalized lineage are source identity")
	require.True(t, quotaObservationLess(header, response), "the canonical tie-breaker must order the two observations")
	require.False(t, quotaObservationLess(response, header), "the canonical tie-breaker must be antisymmetric")

	input := QuotaEvaluationInput{At: now, Observations: []metering.Observation{response, header}}
	want, err := policy.Evaluate(input)
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionDeny, want.Kind, "provider response is the deterministic head")
	require.Equal(t, QuotaReasonThresholdExceeded, want.Reason)
	require.Equal(t, response.ID, want.HeadObservationID)
	require.Equal(t, "90/0", quotaMeasureValue(t, want, utilization))

	for i := range 32 {
		observations := []metering.Observation{header, response}
		if i%2 == 0 {
			observations[0], observations[1] = observations[1], observations[0]
		}
		got, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: observations})
		require.NoError(t, err)
		require.Equal(t, want.Kind, got.Kind, "iteration %d must not depend on input/map order", i)
		require.Equal(t, want.Reason, got.Reason, "iteration %d must not depend on input/map order", i)
		require.Equal(t, want.HeadObservationID, got.HeadObservationID, "iteration %d must not depend on input/map order", i)
		require.Equal(t, want.Measures, got.Measures, "iteration %d must not depend on input/map order", i)
	}

	wire, err := json.Marshal([]metering.Observation{response, header})
	require.NoError(t, err)
	var restarted []metering.Observation
	require.NoError(t, json.Unmarshal(wire, &restarted))
	got, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: restarted})
	require.NoError(t, err)
	require.Equal(t, want.Kind, got.Kind, "restart round-trip must preserve the decision")
	require.Equal(t, want.Reason, got.Reason)
	require.Equal(t, want.HeadObservationID, got.HeadObservationID)
	require.Equal(t, want.Measures, got.Measures)
}

func TestQuotaPolicy_ReplayConflictIsTyped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	utilization := quotaField("provider:utilization", metering.UnitPercent)
	policy := compileQuotaTestPolicy(t, reset, QuotaPolicyConfig{Required: []QuotaField{utilization}})
	first := quotaObservation("same", "account-a", "primary", "minute", reset, now.Add(-time.Minute), quotaMeasure(utilization, "10", metering.QualityObserved))
	conflict := first.Clone()
	conflict.Measures[0].Value = quotaDecimalPtr(t, "11")

	_, err := policy.Evaluate(QuotaEvaluationInput{At: now, Observations: []metering.Observation{first, conflict}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrQuotaEvidenceConflict)
}

func TestQuotaPolicy_CompiledInputsAndAccessorsAreImmutableCopies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := quotaField("provider:utilization", metering.UnitPercent)
	config := QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   QuotaBinding{StoreID: "store-1", ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []QuotaField{field},
	}
	policy, err := CompileQuotaPolicy(config)
	require.NoError(t, err)
	originalRef := policy.PolicyRef()

	config.Required[0].Dimensions = append(config.Required[0].Dimensions, metering.Dimension{Name: "mutated", Value: "caller"})
	gotFields := policy.RequiredFields()
	gotFields[0].Component = "caller-mutated"
	gotFields[0].Dimensions = append(gotFields[0].Dimensions, metering.Dimension{Name: "mutated", Value: "accessor"})
	require.Equal(t, originalRef, policy.PolicyRef(), "caller and accessor mutations cannot change policy identity")
	require.Equal(t, field.CanonicalKey(), policy.RequiredFields()[0].CanonicalKey())

	canonical := policy.CanonicalJSON()
	canonical[0] ^= 0xff
	require.Equal(t, originalRef, policy.PolicyRef(), "canonical bytes are returned as a copy")
	require.NoError(t, originalRef.Validate())
}

func TestQuotaPolicy_DefaultFailurePostureCannotSilentlyAllow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.January, 3, 12, 0, 0, 0, time.UTC)
	reset := now.Add(-time.Hour)
	field := quotaField("provider:utilization", metering.UnitPercent)
	policy, err := CompileQuotaPolicy(QuotaPolicyConfig{
		ID: "provider-quota", Version: "2026-01",
		Binding:   QuotaBinding{ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: time.Minute, Required: []QuotaField{field},
	})
	require.NoError(t, err)
	require.Equal(t, QuotaFailureDeny, policy.FailurePolicy().Missing)
	require.Equal(t, QuotaFailureDeny, policy.FailurePolicy().Stale)
	require.Equal(t, QuotaFailureDeny, policy.FailurePolicy().Partial)
	require.Equal(t, QuotaFailureDeny, policy.FailurePolicy().Unavailable)
	require.Equal(t, QuotaFailureDeny, policy.FailurePolicy().Mismatch)
	require.Equal(t, QuotaFailureDeny, policy.FailurePolicy().Future)
	got, err := policy.Evaluate(QuotaEvaluationInput{At: now})
	require.NoError(t, err)
	require.Equal(t, QuotaDecisionDeny, got.Kind)
	require.Equal(t, QuotaEvidenceMissing, got.EvidenceStatus)
}

func compileQuotaTestPolicy(t *testing.T, reset time.Time, overrides QuotaPolicyConfig) *QuotaPolicy {
	t.Helper()
	config := QuotaPolicyConfig{
		ID:        "provider-quota",
		Version:   "2026-01",
		Binding:   QuotaBinding{StoreID: "store-1", ProviderAccountKey: "account-a", PoolID: "primary", WindowID: "minute", ResetAt: reset},
		Freshness: 5 * time.Minute,
	}
	if overrides.ID != "" {
		config.ID = overrides.ID
	}
	if overrides.Version != "" {
		config.Version = overrides.Version
	}
	if overrides.Binding != (QuotaBinding{}) {
		config.Binding = overrides.Binding
	}
	if overrides.Freshness != 0 {
		config.Freshness = overrides.Freshness
	}
	config.Required = overrides.Required
	config.Thresholds = overrides.Thresholds
	config.Failures = overrides.Failures
	policy, err := CompileQuotaPolicy(config)
	require.NoError(t, err)
	return policy
}

func quotaField(component, unit string) QuotaField {
	return QuotaField{Direction: metering.DirectionNone, Component: component, Unit: unit, SchemaID: "quota.v1"}
}

func quotaMeasure(field QuotaField, value, quality string) metering.Measure {
	measure := metering.Measure{Key: field, Quality: quality}
	if quality != metering.QualityUnavailable && quality != metering.QualityNotApplicable {
		parsed, err := metering.ParseDecimal(value)
		if err != nil {
			panic(err)
		}
		measure.Value = &parsed
	}
	return measure
}

func quotaObservation(id, account, pool, window string, reset, observed time.Time, measures ...metering.Measure) metering.Observation {
	return metering.Observation{
		Version: metering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "quota-stream", Sequence: uint64(observed.Unix()), Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderHeader, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleAuxiliaryRequest,
		Subject:     metering.SubjectRef{Kind: metering.SubjectAccountWindow, StoreID: "store-1", ProviderAccountKey: account, PoolID: pool, WindowID: window, ResetAt: reset},
		Correlation: metering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: account},
		Semantics:   metering.SemanticsGauge, ObservedAt: observed.UTC(), ReceivedAt: observed.Add(time.Second).UTC(),
		MappingRef: "account-window.v1", Measures: measures,
	}
}

func quotaDecimal(t *testing.T, value string) metering.Decimal {
	t.Helper()
	parsed, err := metering.ParseDecimal(value)
	require.NoError(t, err)
	return parsed
}

func quotaDecimalPtr(t *testing.T, value string) *metering.Decimal {
	t.Helper()
	parsed := quotaDecimal(t, value)
	return &parsed
}

func quotaMeasureValue(t *testing.T, decision QuotaDecision, field QuotaField) string {
	t.Helper()
	for _, measure := range decision.Measures {
		if measure.Key.CanonicalKey() == field.CanonicalKey() && measure.Value != nil {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}
