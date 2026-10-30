package metering

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/replay"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestProjectAccountWindows_GaugesArePartialResetScopedAndOrderIndependent(t *testing.T) {
	t.Parallel()
	resetOne := time.Unix(1_000, 0).UTC()
	resetTwo := time.Unix(2_000, 0).UTC()
	base := accountWindowObservation("base", "acct-a", "pool-a", "window-a", resetOne, time.Unix(10, 0),
		accountWindowMeasure("used_percent", "12.5"), accountWindowMeasure("limit", "100"))
	newerPartial := accountWindowObservation("newer-partial", "acct-a", "pool-a", "window-a", resetOne, time.Unix(20, 0),
		accountWindowMeasure("remaining_percent", "87.5"))
	lateOlder := accountWindowObservation("late-older", "acct-a", "pool-a", "window-a", resetOne, time.Unix(15, 0),
		accountWindowMeasure("used_percent", "13.0"))
	newReset := accountWindowObservation("new-reset", "acct-a", "pool-a", "window-a", resetTwo, time.Unix(21, 0),
		accountWindowMeasure("used_percent", "1.0"))
	otherPool := accountWindowObservation("other-pool", "acct-a", "pool-b", "window-a", resetOne, time.Unix(22, 0),
		accountWindowMeasure("used_percent", "99.0"))

	first, err := ProjectAccountWindows([]aipsdkmetering.Observation{newerPartial, otherPool, lateOlder, newReset, base}, time.Time{})
	require.NoError(t, err)
	second, err := ProjectAccountWindows([]aipsdkmetering.Observation{base, newReset, lateOlder, otherPool, newerPartial}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, first, second, "arrival order must not affect the current projection")
	require.Len(t, first, 3, "pool and reset identities remain independent")

	window := findAccountWindowProjection(t, first, "acct-a", "pool-a", "window-a", resetOne)
	require.Equal(t, time.Unix(20, 0).UTC(), window.ObservedAt)
	require.Equal(t, "13/0", accountWindowProjectionValue(t, window, "used_percent"), "late older gauge must not overwrite newer head")
	require.Equal(t, "100/0", accountWindowProjectionValue(t, window, "limit"), "partial snapshots must retain prior fields")
	require.Equal(t, "875/1", accountWindowProjectionValue(t, window, "remaining_percent"))
	require.NotEqual(t, "25.5/0", accountWindowProjectionValue(t, window, "used_percent"), "percentages must never be summed")

	resetWindow := findAccountWindowProjection(t, first, "acct-a", "pool-a", "window-a", resetTwo)
	require.Equal(t, "1/0", accountWindowProjectionValue(t, resetWindow, "used_percent"))
	poolWindow := findAccountWindowProjection(t, first, "acct-a", "pool-b", "window-a", resetOne)
	require.Equal(t, "99/0", accountWindowProjectionValue(t, poolWindow, "used_percent"))

	asOf, err := ProjectAccountWindows([]aipsdkmetering.Observation{newerPartial, otherPool, lateOlder, newReset, base}, time.Unix(16, 0).UTC())
	require.NoError(t, err)
	asOfWindow := findAccountWindowProjection(t, asOf, "acct-a", "pool-a", "window-a", resetOne)
	require.Equal(t, time.Unix(15, 0).UTC(), asOfWindow.ObservedAt)
	require.Equal(t, "13/0", accountWindowProjectionValue(t, asOfWindow, "used_percent"))
	require.Empty(t, accountWindowProjectionMeasure(asOfWindow, "remaining_percent"), "future gauge must be absent from as-of view")
}

func TestProjectAccountWindows_ReplayIsIdempotentAndConflictIsRejected(t *testing.T) {
	t.Parallel()
	observation := accountWindowObservation("replay", "acct-a", "pool-a", "window-a", time.Unix(1_000, 0).UTC(), time.Unix(10, 0),
		accountWindowMeasure("used_percent", "12.5"))
	lateReceipt := observation.Clone()
	lateReceipt.ReceivedAt = lateReceipt.ReceivedAt.Add(time.Minute)
	projected, err := ProjectAccountWindows([]aipsdkmetering.Observation{lateReceipt, observation}, time.Time{})
	require.NoError(t, err)
	require.Len(t, projected, 1)
	require.Len(t, projected[0].ObservationRefs, 1, "exact replay must not duplicate provenance")

	conflict := observation.Clone()
	conflict.Measures[0].Value = decimalForAccountWindow(t, "13.5")
	_, err = ProjectAccountWindows([]aipsdkmetering.Observation{observation, conflict}, time.Time{})
	require.Error(t, err)
	require.True(t, errors.Is(err, replay.ErrIdentityConflict), "conflicting immutable identity must fail: %v", err)
}

func accountWindowObservation(id, account, pool, window string, reset, observed time.Time, measures ...aipsdkmetering.Measure) aipsdkmetering.Observation {
	return aipsdkmetering.Observation{
		Version: aipsdkmetering.ObservationVersionV2, ID: id, SourceEventKey: id + "-event", Revision: 1,
		StreamID: "account-window", Sequence: uint64(observed.Unix()), Origin: aipsdkmetering.OriginProvider,
		Acquisition: aipsdkmetering.AcquisitionProviderHeader, Authority: aipsdkmetering.AuthorityObservedClaim,
		Perspective: aipsdkmetering.PerspectiveOperator, Boundary: aipsdkmetering.BoundaryBackendEgress,
		Lifecycle:   aipsdkmetering.LifecycleAuxiliaryRequest,
		Subject:     aipsdkmetering.SubjectRef{Kind: aipsdkmetering.SubjectAccountWindow, StoreID: "store-1", ProviderAccountKey: account, PoolID: pool, WindowID: window, ResetAt: reset},
		Correlation: aipsdkmetering.CorrelationV2{StoreID: "store-1", ProviderAccountKey: account},
		Semantics:   aipsdkmetering.SemanticsGauge, ObservedAt: observed.UTC(), ReceivedAt: observed.Add(time.Second).UTC(),
		MappingRef: "account-window.v1", Measures: measures,
	}
}

func accountWindowMeasure(component, value string) aipsdkmetering.Measure {
	parsed, err := aipsdkmetering.ParseDecimal(value)
	if err != nil {
		panic(err)
	}
	return aipsdkmetering.Measure{
		Key:   aipsdkmetering.ComponentKey{Direction: aipsdkmetering.DirectionNone, Component: component, Unit: aipsdkmetering.UnitPercent, SchemaID: "account-window.v1"},
		Value: &parsed, Quality: aipsdkmetering.QualityObserved,
	}
}

func decimalForAccountWindow(t *testing.T, value string) *aipsdkmetering.Decimal {
	t.Helper()
	parsed, err := aipsdkmetering.ParseDecimal(value)
	require.NoError(t, err)
	return &parsed
}

func findAccountWindowProjection(t *testing.T, projections []AccountWindowProjection, account, pool, window string, reset time.Time) AccountWindowProjection {
	t.Helper()
	for _, projection := range projections {
		if projection.Subject.ProviderAccountKey == account && projection.Subject.PoolID == pool && projection.Subject.WindowID == window && projection.Subject.ResetAt.Equal(reset) {
			return projection
		}
	}
	t.Fatalf("projection not found account=%s pool=%s window=%s reset=%s", account, pool, window, reset)
	return AccountWindowProjection{}
}

func accountWindowProjectionMeasure(projection AccountWindowProjection, component string) *aipsdkmetering.Measure {
	for i := range projection.Measures {
		if projection.Measures[i].Key.Component == component {
			return &projection.Measures[i]
		}
	}
	return nil
}

func accountWindowProjectionValue(t *testing.T, projection AccountWindowProjection, component string) string {
	t.Helper()
	measure := accountWindowProjectionMeasure(projection, component)
	if measure == nil || measure.Value == nil {
		return ""
	}
	return measure.Value.CanonicalString()
}
