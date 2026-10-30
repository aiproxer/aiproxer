package journalstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase35_RestartReconstruction_ReportInputsFromListedFacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p35-live"})
	if err != nil {
		t.Fatal(err)
	}
	facts := []metering.Fact{
		phase35StoreFact("fe-in", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 100, 0, 0, ""),
		phase35StoreFact("fe-out", "cust-req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 20, 500, "USD"),
		phase35StoreFact("be-in", "op-att", 1, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 40, 0, 0, ""),
		phase35StoreFact("be-out", "op-att", 2, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 22, 200, "USD"),
	}
	for _, f := range facts {
		if err := live.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	cust, err := live.List(ctx, metering.Query{StreamID: "cust-req", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	op, err := live.List(ctx, metering.Query{StreamID: "op-att", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	listed := append(append([]metering.Fact{}, cust.Facts...), op.Facts...)
	liveIn, err := controlplane.DualPlaneReportInputsFromFacts(listed)
	if err != nil {
		t.Fatal(err)
	}

	restart, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: "p35-restart"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range listed {
		if err := restart.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	cust2, err := restart.List(ctx, metering.Query{StreamID: "cust-req", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	op2, err := restart.List(ctx, metering.Query{StreamID: "op-att", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	listed2 := append(append([]metering.Fact{}, cust2.Facts...), op2.Facts...)
	restartIn, err := controlplane.DualPlaneReportInputsFromFacts(listed2)
	if err != nil {
		t.Fatal(err)
	}
	if liveIn != restartIn {
		t.Fatalf("restart mismatch live=%#v restart=%#v", liveIn, restartIn)
	}
}

func TestPhase35_SQLiteRestartReconstruction_ReportInputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "p35-restart.db")
	open := func(t *testing.T) *journalstore.DurableStore {
		t.Helper()
		sqlDB, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
		if err != nil {
			t.Fatal(err)
		}
		store, err := journalstore.NewDurableStore(ctx, bunDB, journalstore.DurableConfig{StoreID: "sqlite-p35-restart"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open(t)
	facts := []metering.Fact{
		phase35StoreFact("fe-in", "cust-req", 1, metering.PerspectiveCustomer, metering.BoundaryFrontendIngress, 80, 0, 0, ""),
		phase35StoreFact("fe-out", "cust-req", 2, metering.PerspectiveCustomer, metering.BoundaryFrontendEgress, 0, 10, 100, "USD"),
		phase35StoreFact("be-in", "op-att", 1, metering.PerspectiveOperator, metering.BoundaryBackendIngress, 30, 0, 0, ""),
		phase35StoreFact("be-out", "op-att", 2, metering.PerspectiveOperator, metering.BoundaryBackendEgress, 0, 12, 40, "USD"),
	}
	for _, f := range facts {
		if err := store.Append(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	beforeCust, err := store.List(ctx, metering.Query{StreamID: "cust-req", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	beforeOp, err := store.List(ctx, metering.Query{StreamID: "op-att", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	allBefore := append(append([]metering.Fact{}, beforeCust.Facts...), beforeOp.Facts...)
	inBefore, err := controlplane.DualPlaneReportInputsFromFacts(allBefore)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := open(t)
	afterCust, err := reopened.List(ctx, metering.Query{StreamID: "cust-req", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	afterOp, err := reopened.List(ctx, metering.Query{StreamID: "op-att", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	allAfter := append(append([]metering.Fact{}, afterCust.Facts...), afterOp.Facts...)
	inAfter, err := controlplane.DualPlaneReportInputsFromFacts(allAfter)
	if err != nil {
		t.Fatal(err)
	}
	if inBefore != inAfter {
		t.Fatalf("sqlite restart drift before=%#v after=%#v", inBefore, inAfter)
	}
	if inAfter.Completeness != controlplane.ReportCompletenessComplete {
		t.Fatalf("completeness=%q", inAfter.Completeness)
	}
}

func phase35StoreFact(id, stream string, seq int64, pers metering.EconomicPerspective, bound metering.Boundary, in, out, money int64, currency string) metering.Fact {
	f := validFact(id, stream, seq)
	f.Perspective = pers
	f.Boundary = bound
	if pers == metering.PerspectiveCustomer {
		f.Lifecycle = metering.LifecycleLogicalRequest
	} else {
		f.Lifecycle = metering.LifecycleBackendAttempt
	}
	f.Correlation.RequestID = "r1"
	f.Correlation.BLegID = stream
	f.RecordedAt = time.Unix(seq, 0).UTC()
	f.Surfaced = metering.SurfacedYes
	qs := make([]metering.Quantity, 0, 2)
	if in > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: in, Present: true})
	}
	if out > 0 {
		qs = append(qs, metering.Quantity{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: out, Present: true})
	}
	f.Quantities = qs
	if currency != "" {
		f.Money = &metering.MoneyObservation{NanoUnits: money, Currency: currency, Present: true, Source: metering.SourceObserved}
	} else {
		f.Money = nil
	}
	return f
}
