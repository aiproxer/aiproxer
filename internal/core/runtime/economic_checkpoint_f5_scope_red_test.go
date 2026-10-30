package runtime

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

// F5 adversarial repair.
//
// Two individually valid cumulative observations can share every source-event
// identity axis (normalized lineage, source key, stream, origin/acquisition)
// while differing only in a reduction-scope axis that aggregate.Scope
// partitions on (perspective, boundary, lifecycle, provider account, inferred
// provider charge scope). The pre-terminal checkpoint coalesces cumulative
// revisions by economicCheckpointPendingKey, which stripped revision from the
// source-event identity without carrying those reduction-scope axes. A later
// snapshot was therefore treated as dominating an older snapshot that the
// reducer keeps in a separate partition, in both the pending queue
// (cumulativeSnapshotCovers) and the durable-head cache (durableHeadCovers).
//
// These tests compare the reduction over all admitted source evidence with the
// reduction recovered from a file-backed restarted durable journal, so a
// checkpoint that loses a whole reduced partition is caught even when no
// component key is dropped from the remaining partition.

const (
	f5StoreID   = "store-f5"
	f5BLegID    = "b-f5"
	f5StreamID  = "stream-f5"
	f5SourceKey = "provider-usage-f5"
	f5ObsID     = "obs-f5"
)

func f5Decimal(value string) *metering.Decimal {
	return &metering.Decimal{Coefficient: value, Scale: 0}
}

func f5InputMeasure(value string) metering.Measure {
	return metering.Measure{
		Key:   metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken},
		Value: f5Decimal(value), Quality: metering.QualityObserved,
	}
}

func f5OutputMeasure(value string) metering.Measure {
	return metering.Measure{
		Key:   metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken},
		Value: f5Decimal(value), Quality: metering.QualityObserved,
	}
}

func f5ProviderCharge(item, amount string) metering.ReportedCharge {
	return metering.ReportedCharge{
		ChargeItemID: item,
		Amount:       f5Decimal(amount),
		Currency:     "USD",
		Kind:         metering.ChargeKindAggregate,
		Payer:        metering.PaymentParty{Kind: metering.PaymentPartyOperator},
	}
}

// f5ScopeVariant mutates one reduction-scope axis. which==1 is the older
// revision (scope B in the durable-head scenario), which==2 is the newer one.
type f5ScopeVariant struct {
	name  string
	apply func(*metering.Observation, int)
}

var f5ScopeVariants = []f5ScopeVariant{
	{
		name: "boundary",
		apply: func(o *metering.Observation, which int) {
			if which == 2 {
				o.Boundary = metering.BoundaryBackendEgress
			}
		},
	},
	{
		name: "perspective",
		apply: func(o *metering.Observation, which int) {
			if which == 2 {
				o.Perspective = metering.PerspectiveCustomer
			}
		},
	},
	{
		name: "lifecycle",
		apply: func(o *metering.Observation, which int) {
			if which == 2 {
				o.Lifecycle = metering.LifecycleLogicalRequest
			}
		},
	},
	{
		// ProviderAccountKey is part of the normalized lineage, so this axis is
		// already carried by the source-event identity. It is retained as a
		// control that the scope-qualified key does not regress an axis the
		// reducer partitions and the identity already separated.
		name: "account",
		apply: func(o *metering.Observation, which int) {
			key := "acct-a"
			if which == 2 {
				key = "acct-b"
			}
			o.Subject.ProviderAccountKey = key
			o.Correlation.ProviderAccountKey = key
		},
	},
	{
		// A single charge item infers Scope.ChargeScope when no explicit
		// ProviderChargeID is present; the charge-item id is not part of the
		// source-event identity, so this is a true scope-only partition.
		name: "inferred_charge_scope",
		apply: func(o *metering.Observation, which int) {
			if which == 2 {
				o.Charges = []metering.ReportedCharge{f5ProviderCharge("fee-b", "7")}
			}
		},
	},
}

func f5Observation(revision uint64, variant f5ScopeVariant, which int, measures ...metering.Measure) metering.Observation {
	now := time.Unix(1_700_200_000+int64(revision), 0).UTC()
	observation := metering.Observation{
		Version: metering.ObservationVersionV2, ID: f5ObsID, SourceEventKey: f5SourceKey,
		Revision: revision, StreamID: f5StreamID, Sequence: revision,
		Origin: metering.OriginProvider, Acquisition: metering.AcquisitionProviderResponse,
		Authority: metering.AuthorityObservedClaim, Perspective: metering.PerspectiveOperator,
		Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind: metering.SubjectBLeg, StoreID: f5StoreID, ALegID: "a-f5",
			BillingCallID: "call-f5", BLegID: f5BLegID,
		},
		Correlation: metering.CorrelationV2{
			StoreID: f5StoreID, CallID: "call-f5", BillingCallID: "call-f5", ALegID: "a-f5", BLegID: f5BLegID,
		},
		Semantics: metering.SemanticsCumulative, ObservedAt: now, ReceivedAt: now, MappingRef: "f5",
		Measures: measures,
	}
	if variant.apply != nil {
		variant.apply(&observation, which)
	}
	return observation
}

func f5AssertValid(t *testing.T, observations ...metering.Observation) {
	t.Helper()
	for i, observation := range observations {
		if err := observation.Validate(); err != nil {
			t.Fatalf("F5 variant observation[%d] revision %d is not valid: %v", i, observation.Revision, err)
		}
	}
}

// f5ScopeReduction is the order-independent canonical form of the reducer's
// per-partition measures. It includes the full reduction scope key, so two
// partitions that share a component key and value are still distinct evidence.
func f5ScopeReduction(t *testing.T, observations []metering.Observation) []string {
	t.Helper()
	snapshot, err := aggregate.ApplyObservations(observations)
	if err != nil {
		t.Fatalf("aggregate reduction of %d observations: %v", len(observations), err)
	}
	signatures := make([]string, 0, len(snapshot.Measures))
	for _, measure := range snapshot.Measures {
		signatures = append(signatures, measure.Scope.Key()+"\x00"+measure.Key.CanonicalKey()+"\x00"+measure.Value.CanonicalString())
	}
	sort.Strings(signatures)
	return signatures
}

func f5OpenStore(t *testing.T, path string) *journalstore.DurableStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", durableCheckpointSQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: f5StoreID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func f5DurableObservations(t *testing.T, store *journalstore.DurableStore) []metering.Observation {
	t.Helper()
	page, err := store.ListObservations(context.Background(), journalstore.ObservationQuery{
		StoreID: f5StoreID, SubjectKind: metering.SubjectBLeg, SubjectID: f5BLegID, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list durable observations: %v", err)
	}
	return page.Observations
}

func f5Attempt(sink metering.ObservationSink) *attemptSession {
	return &attemptSession{
		observationSink: sink,
		now:             func() time.Time { return time.Unix(1_700_200_500, 0).UTC() },
	}
}

func f5ReopenReduction(t *testing.T, path string, store *journalstore.DurableStore) []string {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := f5OpenStore(t, path)
	return f5ScopeReduction(t, f5DurableObservations(t, reopened))
}

// TestEconomicCheckpointCumulativeReductionScopePreserved drives two valid
// cumulative revisions that differ only in a reduction-scope axis through the
// exact file-backed checkpoint/close/reopen path. The durable reduction must
// equal the reduction over both original immutable observations.
func TestEconomicCheckpointCumulativeReductionScopePreserved(t *testing.T) {
	t.Parallel()

	for _, variant := range f5ScopeVariants {
		t.Run(variant.name, func(t *testing.T) {
			t.Parallel()

			// Pending coalescing: the newer (revision 2, scope A) snapshot arrives
			// while the older (revision 1, scope B) snapshot is still pending.
			t.Run("pending_coalescing", func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "f5-pending.db")
				store := f5OpenStore(t, path)
				attempt := f5Attempt(journalstore.NewObservationSink(store))

				rev1 := f5Observation(1, variant, 1, f5InputMeasure("100"))
				rev2 := f5Observation(2, variant, 2, f5InputMeasure("200"))
				f5AssertValid(t, rev1, rev2)

				if admission := attempt.queueEconomicCheckpoint(rev1); admission != economicCheckpointRetained {
					t.Fatalf("revision 1 admission=%v, want retained", admission)
				}
				if admission := attempt.queueEconomicCheckpoint(rev2); admission != economicCheckpointRetained {
					t.Fatalf("revision 2 admission=%v, want retained", admission)
				}
				if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
					t.Fatalf("flush: %v", err)
				}

				want := f5ScopeReduction(t, []metering.Observation{rev1, rev2})
				got := f5ReopenReduction(t, path, store)
				if !slices.Equal(want, got) {
					t.Fatalf("durable reduction lost a scope partition\n source=%q\n durable=%q", want, got)
				}
			})

			// Durable-head suppression: the newer (revision 2, scope A) snapshot is
			// flushed first, then the older (revision 1, scope B) snapshot arrives.
			t.Run("durable_head_reorder", func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "f5-durable-head.db")
				store := f5OpenStore(t, path)
				attempt := f5Attempt(journalstore.NewObservationSink(store))

				rev1 := f5Observation(1, variant, 1, f5InputMeasure("100"))
				rev2 := f5Observation(2, variant, 2, f5InputMeasure("200"))
				f5AssertValid(t, rev1, rev2)

				attempt.queueEconomicCheckpoint(rev2)
				if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
					t.Fatalf("flush newer revision: %v", err)
				}
				if admission := attempt.queueEconomicCheckpoint(rev1); admission != economicCheckpointRetained {
					t.Fatalf("reordered older revision admission=%v, want retained", admission)
				}
				if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
					t.Fatalf("flush reordered older revision: %v", err)
				}

				want := f5ScopeReduction(t, []metering.Observation{rev1, rev2})
				got := f5ReopenReduction(t, path, store)
				if !slices.Equal(want, got) {
					t.Fatalf("durable head suppressed a distinct scope partition\n source=%q\n durable=%q", want, got)
				}
			})
		})
	}
}

// TestEconomicCheckpointSameScopeCumulativeStillCoalesces is the control that
// the scope-qualified coalescing key did not stop intended same-scope
// coalescing: a later full same-scope snapshot still subsumes the older one.
func TestEconomicCheckpointSameScopeCumulativeStillCoalesces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "f5-same-scope.db")
	store := f5OpenStore(t, path)
	attempt := f5Attempt(journalstore.NewObservationSink(store))

	rev1 := f5Observation(1, f5ScopeVariant{}, 0, f5InputMeasure("100"))
	rev2 := f5Observation(2, f5ScopeVariant{}, 0, f5InputMeasure("200"))
	f5AssertValid(t, rev1, rev2)

	attempt.queueEconomicCheckpoint(rev1)
	attempt.queueEconomicCheckpoint(rev2)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := f5OpenStore(t, path)
	durable := f5DurableObservations(t, reopened)
	if len(durable) != 1 || durable[0].Revision != 2 {
		t.Fatalf("same-scope durable coalescing = %d observation(s), want the newest revision only", len(durable))
	}
	want := f5ScopeReduction(t, []metering.Observation{rev1, rev2})
	got := f5ScopeReduction(t, durable)
	if !slices.Equal(want, got) {
		t.Fatalf("same-scope coalescing changed reduction\n source=%q\n durable=%q", want, got)
	}
}

// TestEconomicCheckpointSameScopeSparseFieldsPreserved is the sparse-field
// control: a later same-scope sparse snapshot that omits an earlier component
// must not coalesce it away.
func TestEconomicCheckpointSameScopeSparseFieldsPreserved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "f5-sparse-control.db")
	store := f5OpenStore(t, path)
	attempt := f5Attempt(journalstore.NewObservationSink(store))

	rev1 := f5Observation(1, f5ScopeVariant{}, 0, f5InputMeasure("100"))
	rev2 := f5Observation(2, f5ScopeVariant{}, 0, f5OutputMeasure("20"))
	f5AssertValid(t, rev1, rev2)

	attempt.queueEconomicCheckpoint(rev1)
	attempt.queueEconomicCheckpoint(rev2)
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := f5OpenStore(t, path)
	durable := f5DurableObservations(t, reopened)
	want := f5ScopeReduction(t, []metering.Observation{rev1, rev2})
	got := f5ScopeReduction(t, durable)
	if !slices.Equal(want, got) {
		t.Fatalf("sparse-field control changed reduction\n source=%q\n durable=%q", want, got)
	}
	if len(got) != 2 {
		t.Fatalf("sparse-field control durable partitions=%d, want 2 (input and output)", len(got))
	}
}
