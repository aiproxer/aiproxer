package journalstore_test

import (
	"context"
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestProviderDebitJournal_RoundTripsTypedScopeAndReduction(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	quantity, err := sdkmetering.ParseDecimal("0.125")
	require.NoError(t, err)
	debit := sdkmetering.ProviderDebit{
		Version:            sdkmetering.ProviderDebitVersionV1,
		ID:                 "journal-debit-1",
		SourceEventKey:     "journal-debit-source-1",
		Revision:           1,
		StreamID:           "journal-provider-debits",
		Sequence:           1,
		StoreID:            "sqlite-test",
		TenantID:           "tenant-journal",
		ProviderAccountKey: "account-journal",
		PoolID:             "pool-journal",
		WindowID:           "window-journal",
		ResetAt:            time.Unix(7_000, 0).UTC(),
		RequestID:          "request-journal",
		BillingCallID:      "billing-call-journal",
		BLegID:             "b-leg-journal",
		AttemptID:          "attempt-journal",
		ProviderRequestID:  "provider-request-journal",
		Component:          sdkmetering.ComponentKey{Direction: sdkmetering.DirectionNone, Component: sdkmetering.ComponentCredit, Unit: sdkmetering.UnitCredit},
		Quantity:           &quantity,
		Quality:            sdkmetering.QualityObserved,
		MethodRef:          "provider-finalizer-v1",
		Acquisition:        sdkmetering.AcquisitionProviderFinalizer,
		Authority:          sdkmetering.AuthorityObservedClaim,
		Semantics:          sdkmetering.SemanticsDelta,
		ObservedAt:         time.Unix(7_001, 0).UTC(),
		ReceivedAt:         time.Unix(7_002, 0).UTC(),
	}
	observation, err := debit.ToObservation()
	require.NoError(t, err)
	require.NoError(t, store.AppendObservation(ctx, observation))

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "sqlite-test", SubjectKind: sdkmetering.SubjectProviderDebit,
		SubjectID: observation.Subject.BLegID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	got := page.Observations[0]
	require.Equal(t, sdkmetering.SubjectProviderDebit, got.Subject.Kind)
	require.Equal(t, debit.RequestID, got.Subject.RequestID)
	require.Equal(t, debit.BillingCallID, got.Subject.BillingCallID)
	require.Equal(t, debit.BLegID, got.Subject.BLegID)
	require.Equal(t, debit.ProviderAccountKey, got.Subject.ProviderAccountKey)
	require.Equal(t, debit.PoolID, got.Subject.PoolID)
	require.Equal(t, debit.WindowID, got.Subject.WindowID)
	require.Equal(t, debit.ResetAt, got.Subject.ResetAt)
	require.Empty(t, got.Charges, "provider unit debit must remain nonmonetary in the canonical journal")

	reduced, err := coremetering.ReduceProviderDebitObservations(page.Observations)
	require.NoError(t, err)
	require.True(t, reduced.Complete)
	require.Len(t, reduced.Measures, 1)
	require.Equal(t, "125/3", reduced.Measures[0].Value.CanonicalString())
}
