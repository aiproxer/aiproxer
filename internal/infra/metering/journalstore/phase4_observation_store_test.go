package journalstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func phase4Observation(store, id string, revision uint64) metering.Observation {
	now := time.Unix(1_700_000_000+int64(revision), 0).UTC()
	component := metering.ComponentKey{Direction: metering.DirectionInput, Component: "vendor:audio_frames", Unit: metering.UnitFrame, SchemaID: "vendor:media:v1", Dimensions: []metering.Dimension{{Name: "codec", Value: "pcm"}}}
	value := metering.Decimal{Coefficient: "125", Scale: 1}
	chargeComponent := metering.ComponentKey{Direction: metering.DirectionNone, Component: "vendor:media_charge", Unit: metering.UnitCredit, SchemaID: "vendor:media:v1"}
	amount := metering.Decimal{Coefficient: "25", Scale: 1}
	return metering.Observation{
		Version: 2, ID: id, SourceEventKey: "source-event", Revision: revision, StreamID: "stream-" + store, Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: store, ALegID: "a-" + store, BillingCallID: "call-" + store, BLegID: "b-" + store},
		Correlation: metering.CorrelationV2{StoreID: store, CallID: "call-" + store, BillingCallID: "call-" + store, ALegID: "a-" + store, BLegID: "b-" + store},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "provider:media:v1",
		Measures: []metering.Measure{
			{Key: component, Value: &value, Quality: metering.QualityObserved},
			{Key: metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:missing", Unit: metering.UnitFrame, SchemaID: "vendor:media:v1"}, Quality: metering.QualityUnavailable, Reason: "not reported"},
		},
		Charges: []metering.ReportedCharge{
			{ChargeItemID: "component-charge", Component: &chargeComponent, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindComponent},
			{ChargeItemID: "aggregate-charge", Amount: &amount, Currency: "USD", Kind: metering.ChargeKindAggregate},
		},
	}
}

func TestPhase4ObservationAppendProjectsArbitraryComponentsAndCharges(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	observation := phase4Observation("sqlite-test", "obs-1", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))

	page, err := store.ListObservationComponents(ctx, structToComponentQuery(observation, 20))
	require.NoError(t, err)
	require.Len(t, page.Components, 4)
	var aggregate, unavailable bool
	for _, component := range page.Components {
		switch component.ItemID {
		case "aggregate-charge":
			aggregate = true
			require.Equal(t, "", component.ComponentKey)
			require.Equal(t, "", component.ComponentKeyHash)
			require.True(t, component.MoneyPresent)
			require.Equal(t, "25", component.Coefficient)
		case "{\"direction\":\"output\",\"component\":\"vendor:missing\",\"unit\":\"frame\",\"schema_id\":\"vendor:media:v1\"}":
			unavailable = true
			require.False(t, component.ValuePresent)
		}
	}
	require.True(t, aggregate)
	require.True(t, unavailable)

	keyPage, err := store.ListObservationComponents(ctx, journalstore.ComponentQuery{StoreID: observation.Subject.StoreID, ComponentKeyHash: observation.Measures[0].Key.Fingerprint(), Limit: 20})
	require.NoError(t, err)
	require.Len(t, keyPage.Components, 1)
}

func TestPhase4ObservationReplayRevisionAndStoreIsolation(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "obs-replay", 1)
	require.NoError(t, store.AppendObservation(ctx, observation))
	require.NoError(t, store.AppendObservation(ctx, observation))
	changed := observation.Clone()
	changed.Measures[0].Value = &metering.Decimal{Coefficient: "126", Scale: 1}
	require.ErrorIs(t, store.AppendObservation(ctx, changed), journalstore.ErrIdentityCollision)
	next := observation.Clone()
	next.Revision = 2
	next.Sequence = 2
	next.ReceivedAt = next.ReceivedAt.Add(time.Second)
	next.ObservedAt = next.ObservedAt.Add(time.Second)
	require.NoError(t, store.AppendObservation(ctx, next))
	page, err := store.ListObservations(ctx, ObservationQueryForPhase4(observation, 20))
	require.NoError(t, err)
	require.Len(t, page.Observations, 2)

	other := phase4Observation("other-store", "obs-other", 1)
	otherStore, err := journalstore.OpenStore(ctx, store.DB(), journalstore.DurableConfig{StoreID: "other-store"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = otherStore.Close() })
	require.NoError(t, otherStore.AppendObservation(ctx, other))
	_, err = store.GetObservation(ctx, other.ID, other.Revision)
	require.Error(t, err)
	require.False(t, errors.Is(err, journalstore.ErrIdentityCollision))
}

func TestPhase4ObservationTenantFilterIsIsolated(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	first := phase4Observation("sqlite-test", "obs-tenant-a", 1)
	first.Subject.TenantID = "tenant-a"
	first.Correlation.TenantID = "tenant-a"
	second := phase4Observation("sqlite-test", "obs-tenant-b", 1)
	second.Subject.TenantID = "tenant-b"
	second.Correlation.TenantID = "tenant-b"
	require.NoError(t, store.AppendObservation(ctx, first))
	require.NoError(t, store.AppendObservation(ctx, second))

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: "sqlite-test", SubjectKind: first.Subject.Kind, SubjectID: first.Subject.BLegID,
		TenantID: "tenant-a", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, first.ID, page.Observations[0].ID)

	subject := first.Subject
	page, err = store.ListObservations(ctx, journalstore.ObservationQuery{StoreID: "sqlite-test", Subject: &subject, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, first.ID, page.Observations[0].ID)
}

func TestPhase4ObservationCursorIsFilterBoundAndHardBounded(t *testing.T) {
	store := newSQLiteJournal(t)
	ctx := context.Background()
	for i := uint64(1); i <= 3; i++ {
		o := phase4Observation("sqlite-test", "obs-page-"+string(rune('0'+i)), i)
		require.NoError(t, store.AppendObservation(ctx, o))
	}
	q := ObservationQueryForPhase4(phase4Observation("sqlite-test", "ignored", 1), 1)
	page, err := store.ListObservations(ctx, q)
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.NotEmpty(t, page.NextCursor)
	q.Cursor = page.NextCursor
	page, err = store.ListObservations(ctx, q)
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	q.SubjectID = "different"
	_, err = store.ListObservations(ctx, q)
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor)
	q.Cursor = "not-a-cursor"
	_, err = store.ListObservations(ctx, q)
	require.ErrorIs(t, err, journalstore.ErrInvalidCursor)
	q.Cursor = ""
	q.Limit = 501
	_, err = store.ListObservations(ctx, q)
	require.ErrorIs(t, err, journalstore.ErrPageSizeExceeded)
}

func TestPhase4SQLiteUpgradePreservesLegacyV1PayloadAndSourceKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bunDB := openEmptySQLiteBun(t)
	seedPrePhase3BaselineSchema(t, bunDB)

	legacy := upgradeProbeFact("legacy-v1", "legacy-stream")
	payload, err := json.Marshal(legacy)
	require.NoError(t, err)
	sourceKey := legacy.SourceEventKey()
	_, err = bunDB.NewRaw(`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`, "store-a", legacy.FactID, legacy.StreamID, legacy.Sequence, sourceKey,
		string(legacy.Kind), string(legacy.Perspective), string(legacy.Boundary), string(legacy.Lifecycle),
		legacy.Correlation.RequestID, legacy.Correlation.ALegID, legacy.Correlation.BLegID, legacy.Correlation.AttemptID,
		legacy.FrontendID, legacy.BackendID, legacy.Model, string(legacy.Presence), string(legacy.Source), string(legacy.Authority),
		legacy.RecordedAt.UnixNano(), string(payload)).Exec(ctx)
	require.NoError(t, err)

	var beforePayload, beforeSourceKey string
	require.NoError(t, bunDB.NewRaw(`SELECT payload_json, source_event_key FROM metering_facts WHERE fact_id = ?`, legacy.FactID).Scan(ctx, &beforePayload, &beforeSourceKey))
	require.NoError(t, journalstore.Migrate(ctx, bunDB))

	var afterPayload, afterSourceKey string
	require.NoError(t, bunDB.NewRaw(`SELECT payload_json, source_event_key FROM metering_facts WHERE fact_id = ?`, legacy.FactID).Scan(ctx, &afterPayload, &afterSourceKey))
	require.Equal(t, beforePayload, afterPayload)
	require.Equal(t, beforeSourceKey, afterSourceKey)

	store, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "store-a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	v2 := phase4Observation("store-a", "upgrade-v2", 1)
	require.NoError(t, store.AppendObservation(ctx, v2))
	page, err := store.ListObservations(ctx, ObservationQueryForPhase4(v2, 10))
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, v2.ID, page.Observations[0].ID)
}

func ObservationQueryForPhase4(observation metering.Observation, limit int) journalstore.ObservationQuery {
	return journalstore.ObservationQuery{StoreID: observation.Subject.StoreID, SubjectKind: observation.Subject.Kind, SubjectID: observation.Subject.BLegID, Limit: limit}
}

func structToComponentQuery(observation metering.Observation, limit int) journalstore.ComponentQuery {
	return journalstore.ComponentQuery{StoreID: observation.Subject.StoreID, SubjectKind: observation.Subject.Kind, SubjectID: observation.Subject.BLegID, Limit: limit}
}

func openSQLiteJournalForPhase4(t *testing.T, storeID string) *journalstore.DurableStore {
	t.Helper()
	base := newSQLiteJournal(t)
	store, err := journalstore.OpenStore(context.Background(), base.DB(), journalstore.DurableConfig{StoreID: storeID})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}
