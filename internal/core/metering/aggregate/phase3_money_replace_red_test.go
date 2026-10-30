package aggregate_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase3_AuthoritativeReplacement_PreservesPriorMoneyWhenAbsent(t *testing.T) {
	t.Parallel()
	base := phase3AggFact("base", "stream-money", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 5, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 2, Present: true},
	}, nil)
	base.Money = &metering.MoneyObservation{
		NanoUnits: 100, Currency: "USD", Present: true, Source: metering.SourceObserved,
	}
	repl := phase3AggFact("repl", "stream-money", 2, metering.FactKindAuthoritativeReplacement, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 9, Present: true},
	}, []string{"base"})

	snap, err := aggregate.Apply([]metering.Fact{base, repl})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentOutputToken] != 9 {
		t.Fatalf("output=%d want 9", snap.Quantities[metering.ComponentOutputToken])
	}
	if snap.Quantities[metering.ComponentInputToken] != 5 {
		t.Fatalf("input erased=%d", snap.Quantities[metering.ComponentInputToken])
	}
	if !snap.MoneyPresent || snap.MoneyNano != 100 || snap.MoneyCurrency != "USD" {
		t.Fatalf("nil/absent Money on authoritative replacement erased prior money: present=%v nano=%d cur=%q",
			snap.MoneyPresent, snap.MoneyNano, snap.MoneyCurrency)
	}
}

func TestPhase3_AuthoritativeReplacement_ExplicitZeroMoneyReplaces(t *testing.T) {
	t.Parallel()
	base := phase3AggFact("base", "stream-zero", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 2, Present: true},
	}, nil)
	base.Money = &metering.MoneyObservation{
		NanoUnits: 50, Currency: "USD", Present: true, Source: metering.SourceObserved,
	}
	repl := phase3AggFact("repl", "stream-zero", 2, metering.FactKindAuthoritativeReplacement, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 2, Present: true},
	}, []string{"base"})
	repl.Money = &metering.MoneyObservation{
		NanoUnits: 0, Currency: "USD", Present: true, Source: metering.SourceObserved,
	}

	snap, err := aggregate.Apply([]metering.Fact{base, repl})
	if err != nil {
		t.Fatal(err)
	}
	if !snap.MoneyPresent || snap.MoneyNano != 0 || snap.MoneyCurrency != "USD" {
		t.Fatalf("explicit zero Present money: present=%v nano=%d cur=%q",
			snap.MoneyPresent, snap.MoneyNano, snap.MoneyCurrency)
	}
}

func TestPhase3_AuthoritativeReplacement_MoneyPreserveIsOrderIndependent(t *testing.T) {
	t.Parallel()
	base := phase3AggFact("base", "stream-ord", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
	}, nil)
	base.Money = &metering.MoneyObservation{
		NanoUnits: 7, Currency: "USD", Present: true, Source: metering.SourceObserved,
	}
	repl := phase3AggFact("repl", "stream-ord", 2, metering.FactKindAuthoritativeReplacement, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 3, Present: true},
	}, []string{"base"})
	a, err := aggregate.Apply([]metering.Fact{base, repl})
	if err != nil {
		t.Fatal(err)
	}
	b, err := aggregate.Apply([]metering.Fact{repl, base})
	if err != nil {
		t.Fatal(err)
	}
	if a.MoneyPresent != b.MoneyPresent || a.MoneyNano != b.MoneyNano || a.MoneyCurrency != b.MoneyCurrency {
		t.Fatalf("order-dependent money: %#v vs %#v", a, b)
	}
	if !a.MoneyPresent || a.MoneyNano != 7 {
		t.Fatalf("money=%v nano=%d", a.MoneyPresent, a.MoneyNano)
	}
}
