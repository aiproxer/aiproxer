package journalstore_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// R8-B2 enforceable boundary, durable: a provider-supplied explicit source
// revision is unsupported, so a late older explicit revision can never be
// written to the durable journal as provider evidence that could duplicate an
// already-sealed charge. Every explicit revision drains only as the sticky
// unsupported-ordering loss marker, and no provider source observation for the
// rejected source key is ever persisted.
func TestProviderEvidenceR8BExplicitCrossDrainRevisionNotPersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "r8b2-cross-drain.db")

	identity := r8b2PersistIdentity()
	b := coremetering.NewProviderEvidenceBuffer()
	b.BindEconomicEvidence(identity)

	const key = "provider.r8b2.persist"

	store := openPhase34SQLiteStoreAt(t, path, identity.StoreID)
	defer func() { _ = store.Close() }()

	// The newer explicit revision is rejected, not sealed.
	b.Add(r8b2PersistDraft(key, 2, nil, nil))
	sealed := b.DrainEconomicObservations()
	if len(sealed) != 1 || sealed[0].Authority != sdkmetering.AuthorityUnavailableClaim {
		t.Fatalf("explicit newer revision must be rejected as a loss marker: %+v", sealed)
	}
	for _, observation := range sealed {
		if err := store.AppendObservation(ctx, observation); err != nil {
			t.Fatalf("append loss marker: %v", err)
		}
	}

	// The late older explicit revision is rejected the same way.
	b.Add(r8b2PersistDraft(key, 1, map[string]string{"sibling": "9"}, nil))
	late := b.DrainEconomicObservations()
	if len(late) != 1 || late[0].Authority != sdkmetering.AuthorityUnavailableClaim {
		t.Fatalf("late older explicit revision must be rejected as a loss marker: %+v", late)
	}

	page, err := store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: identity.StoreID, StreamID: "provider.v2", Limit: 10,
	})
	if err != nil {
		t.Fatalf("list sealed: %v", err)
	}
	for _, observation := range page.Observations {
		if observation.SourceEventKey == key {
			t.Fatalf("explicit source revision was persisted as provider evidence: %+v", observation)
		}
	}
}

func r8b2PersistIdentity() coremetering.ObservationIdentity {
	now := time.Unix(1700000000, 0).UTC()
	return coremetering.ObservationIdentity{
		StoreID: "store-1", RequestID: "request-1", CallID: "call-1", BillingCallID: "billing-1",
		ALegID: "a-leg-1", BLegID: "b-leg-1", AttemptID: "attempt-1", AttemptSeq: 1,
		ObservedAt: now, ReceivedAt: now,
	}
}

// r8b2PersistDraft builds a draft carrying a caller-supplied explicit source
// revision so the unsupported-ordering boundary can be exercised.
func r8b2PersistDraft(key string, revision uint64, overrides map[string]string, zero *string) coremetering.ProviderEvidenceDraft {
	image := strconv.FormatUint(revision, 10)
	if override, ok := overrides["image"]; ok {
		image = override
	}
	money := sdkmetering.Decimal{Coefficient: strconv.FormatUint(revision*100, 10)}
	measures := []sdkmetering.Measure{{
		Key: r8b2PersistImageKey(), Value: &sdkmetering.Decimal{Coefficient: image}, Quality: sdkmetering.QualityObserved,
	}}
	if sibling, ok := overrides["sibling"]; ok {
		measures = append(measures, sdkmetering.Measure{
			Key: r8b2PersistSiblingKey(), Value: &sdkmetering.Decimal{Coefficient: sibling}, Quality: sdkmetering.QualityObserved,
		})
	}
	if zero != nil {
		measures = append(measures, sdkmetering.Measure{
			Key: r8b2PersistZeroKey(), Value: &sdkmetering.Decimal{Coefficient: *zero}, Quality: sdkmetering.QualityObserved,
		})
	}
	return coremetering.ProviderEvidenceDraft{
		SourceEventKey: key, StreamID: "provider.v2", Revision: revision,
		Measures: measures,
		Charges: []sdkmetering.ReportedCharge{{
			ChargeItemID: "charge:" + key, Amount: &money, Currency: "USD", Kind: sdkmetering.ChargeKindAggregate,
		}},
	}
}

func r8b2PersistImageKey() sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{
		Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentImage,
		Unit: sdkmetering.UnitImage, SchemaID: "provider.test.v1",
	}
}

func r8b2PersistSiblingKey() sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{
		Direction: sdkmetering.DirectionInput, Component: sdkmetering.ComponentCacheReadInputToken,
		Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID,
	}
}

func r8b2PersistZeroKey() sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{
		Direction: sdkmetering.DirectionOutput, Component: sdkmetering.ComponentReasoningOutputToken,
		Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID,
	}
}
