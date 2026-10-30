package aggregate_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestApply_DeltaCumulativeCorrectionReplacement(t *testing.T) {
	t.Parallel()
	base := fact("f1", 1, metering.FactKindDelta, qty(metering.ComponentInputToken, 10), nil)
	cum := fact("f2", 2, metering.FactKindCumulative, qty(metering.ComponentInputToken, 12), nil)
	corr := fact("f3", 3, metering.FactKindCorrection, qty(metering.ComponentOutputToken, 2), []string{"f1"})
	repl := fact("f4", 4, metering.FactKindAuthoritativeReplacement, qty(metering.ComponentTotalToken, 20), []string{"f2", "f3"})

	snap, err := aggregate.Apply([]metering.Fact{repl, corr, cum, base}) // out of order input
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentTotalToken] != 20 {
		t.Fatalf("quantities=%v", snap.Quantities)
	}
	if _, ok := snap.Superseded["f2"]; !ok {
		t.Fatal("expected superseded f2")
	}
	// Restart replay equals live aggregate.
	again, err := aggregate.Apply([]metering.Fact{base, cum, corr, repl})
	if err != nil {
		t.Fatal(err)
	}
	if again.Quantities[metering.ComponentTotalToken] != snap.Quantities[metering.ComponentTotalToken] {
		t.Fatalf("restart mismatch %v vs %v", again.Quantities, snap.Quantities)
	}
}

func TestApply_UnavailableTrackedAndIdempotentFactID(t *testing.T) {
	t.Parallel()
	u := fact("u1", 1, metering.FactKindUnavailable, nil, nil)
	u.Presence = metering.PresenceUnknown
	d := fact("d1", 2, metering.FactKindDelta, qty(metering.ComponentInputToken, 3), nil)
	snap, err := aggregate.Apply([]metering.Fact{u, d, d})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentInputToken] != 3 {
		t.Fatalf("double-count? %v", snap.Quantities)
	}
	if len(snap.Unavailable) != 1 || snap.Unavailable[0] != "u1" {
		t.Fatalf("unavailable=%v", snap.Unavailable)
	}
}

func TestApply_MixedCurrencyRejected(t *testing.T) {
	t.Parallel()
	usd := fact("m1", 1, metering.FactKindDelta, nil, nil)
	usd.Money = &metering.MoneyObservation{NanoUnits: 100, Currency: "USD", Present: true}
	eur := fact("m2", 2, metering.FactKindDelta, nil, nil)
	eur.Money = &metering.MoneyObservation{NanoUnits: 50, Currency: "EUR", Present: true}

	_, err := aggregate.Apply([]metering.Fact{usd, eur})
	if !errors.Is(err, aggregate.ErrMixedCurrency) {
		t.Fatalf("err=%v want errors.Is ErrMixedCurrency", err)
	}
}

func TestApply_QuantityOverflowRejected(t *testing.T) {
	t.Parallel()
	near := fact("q1", 1, metering.FactKindDelta, qty(metering.ComponentInputToken, math.MaxInt64-1), nil)
	bump := fact("q2", 2, metering.FactKindDelta, qty(metering.ComponentInputToken, 2), nil)

	_, err := aggregate.Apply([]metering.Fact{near, bump})
	if !errors.Is(err, aggregate.ErrOverflow) {
		t.Fatalf("err=%v want errors.Is ErrOverflow", err)
	}
}

func TestApply_MoneyNanoOverflowRejected(t *testing.T) {
	t.Parallel()
	near := fact("n1", 1, metering.FactKindDelta, nil, nil)
	near.Money = &metering.MoneyObservation{NanoUnits: math.MaxInt64 - 1, Currency: "USD", Present: true}
	bump := fact("n2", 2, metering.FactKindDelta, nil, nil)
	bump.Money = &metering.MoneyObservation{NanoUnits: 2, Currency: "USD", Present: true}

	_, err := aggregate.Apply([]metering.Fact{near, bump})
	if !errors.Is(err, aggregate.ErrOverflow) {
		t.Fatalf("err=%v want errors.Is ErrOverflow", err)
	}
}

func fact(id string, seq int64, kind metering.FactKind, qs []metering.Quantity, supersedes []string) metering.Fact {
	f := metering.Fact{
		FactID:      id,
		StreamID:    "stream-agg",
		Sequence:    seq,
		Kind:        kind,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Source:      metering.SourceObserved,
		Authority:   metering.AuthorityAuthoritative,
		Presence:    metering.PresencePresent,
		Quantities:  qs,
		Supersedes:  supersedes,
		RecordedAt:  time.Unix(1, 0).UTC(),
	}
	return f
}

func qty(component string, v int64) []metering.Quantity {
	return []metering.Quantity{{Component: component, Unit: metering.UnitToken, Value: v, Present: true}}
}
