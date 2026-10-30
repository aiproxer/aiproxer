package aggregate_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// Phase 3.1 RED aggregate/correction contracts (requirements 6.4–6.9, 13.1;
// design Corrections, D7, D17). Production graph validation and replacement
// semantics land in tasks 3.2/3.4.

func TestPhase3_AuthoritativeReplacement_PreservesUnrelatedComponents(t *testing.T) {
	t.Parallel()
	base := phase3AggFact("base", "stream-repl", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 10, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 5, Present: true},
	}, nil)
	repl := phase3AggFact("repl", "stream-repl", 2, metering.FactKindAuthoritativeReplacement, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 7, Present: true},
	}, []string{"base"})

	snap, err := aggregate.Apply([]metering.Fact{base, repl})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentOutputToken] != 7 {
		t.Fatalf("replacement output=%d want 7; quantities=%v", snap.Quantities[metering.ComponentOutputToken], snap.Quantities)
	}
	if got := snap.Quantities[metering.ComponentInputToken]; got != 10 {
		t.Fatalf("authoritative replacement erased unrelated input_token=%d want 10 (design Corrections; task 3.4)", got)
	}
}

func TestPhase3_Correction_AppliesSignedDeltaDeterministically(t *testing.T) {
	t.Parallel()
	base := phase3AggFact("base", "stream-corr", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 10, Present: true},
	}, nil)
	corr := phase3AggFact("corr", "stream-corr", 2, metering.FactKindCorrection, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: -3, Present: true},
	}, []string{"base"})

	snap, err := aggregate.Apply([]metering.Fact{base, corr})
	if err != nil {
		t.Fatalf("signed correction aggregate: %v (req 6.4, 6.8)", err)
	}
	if snap.Quantities[metering.ComponentOutputToken] != 7 {
		t.Fatalf("output=%d want 7 after signed correction; quantities=%v", snap.Quantities[metering.ComponentOutputToken], snap.Quantities)
	}
	if _, ok := snap.Superseded["base"]; !ok {
		t.Fatal("correction must mark superseded target (req 6.5)")
	}
	// Restart replay equality.
	again, err := aggregate.Apply([]metering.Fact{corr, base})
	if err != nil {
		t.Fatal(err)
	}
	if again.Quantities[metering.ComponentOutputToken] != snap.Quantities[metering.ComponentOutputToken] {
		t.Fatalf("restart aggregate mismatch %v vs %v (req 6.8)", again.Quantities, snap.Quantities)
	}
}

func TestPhase3_Cumulative_ReplacesOnlyPresentComponents(t *testing.T) {
	t.Parallel()
	d1 := phase3AggFact("d1", "stream-cum", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 3, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 4, Present: true},
	}, nil)
	cum := phase3AggFact("c1", "stream-cum", 2, metering.FactKindCumulative, []metering.Quantity{
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 9, Present: true},
	}, nil)

	snap, err := aggregate.Apply([]metering.Fact{d1, cum})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Quantities[metering.ComponentOutputToken] != 9 {
		t.Fatalf("cumulative output=%d want 9", snap.Quantities[metering.ComponentOutputToken])
	}
	if got := snap.Quantities[metering.ComponentInputToken]; got != 3 {
		t.Fatalf("cumulative erased unrelated input_token=%d want 3 (design Corrections)", got)
	}
}

func TestPhase3_HistoryImmutable_CorrectionDoesNotMutatePriorFactBody(t *testing.T) {
	t.Parallel()
	base := phase3AggFact("base", "stream-imm", 1, metering.FactKindDelta, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 2, Present: true},
	}, nil)
	corr := phase3AggFact("corr", "stream-imm", 2, metering.FactKindCorrection, []metering.Quantity{
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
	}, []string{"base"})
	facts := []metering.Fact{base, corr}
	if _, err := aggregate.Apply(facts); err != nil {
		t.Fatal(err)
	}
	if facts[0].Quantities[0].Value != 2 {
		t.Fatalf("aggregate mutated prior fact body to %d (req 6.9)", facts[0].Quantities[0].Value)
	}
}

func phase3AggFact(id, stream string, seq int64, kind metering.FactKind, qs []metering.Quantity, supersedes []string) metering.Fact {
	return metering.Fact{
		FactID:      id,
		StreamID:    stream,
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
}
