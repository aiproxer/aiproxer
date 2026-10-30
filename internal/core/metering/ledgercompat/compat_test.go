package ledgercompat_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/ledgercompat"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestProjectLedgerRecord_PreservesTokensOmitsMoney(t *testing.T) {
	t.Parallel()
	f := fact("p1", 1, metering.FactKindCumulative, qty(metering.ComponentInputToken, 4), nil)
	f.Quantities = append(f.Quantities, metering.Quantity{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 6, Present: true,
	})
	f.BackendID = "openai"
	f.Model = "gpt-test"
	f.Correlation.RequestID = "req-p"
	f.Correlation.AttemptID = "att-p"
	f.Money = &metering.MoneyObservation{NanoUnits: 99, Currency: "USD", Present: true, Source: metering.SourceProviderReported}
	rec, ok, err := ledgercompat.ProjectLedgerRecord(f)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if rec.InputTokens != 4 || rec.OutputTokens != 6 || rec.TotalTokens != 10 {
		t.Fatalf("rec=%+v", rec)
	}
	if rec.RequestID != "req-p" || rec.AttemptID != "att-p" {
		t.Fatalf("ids=%+v", rec)
	}
	// Money stays on journal side only — ledger.Record has no money fields.
}

func TestProjectLedgerRecord_SkipsUnavailable(t *testing.T) {
	t.Parallel()
	f := fact("u", 1, metering.FactKindUnavailable, nil, nil)
	f.Presence = metering.PresenceUnknown
	_, ok, err := ledgercompat.ProjectLedgerRecord(f)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
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
