package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Phase 3.1 RED journal store contracts (requirements 5.1–5.8, 6.1–6.9, 11.3–11.5,
// 13.1, 13.6; design D2, D6, D7, D12, D17).
//
// Deferred to later Phase 3 tasks (report only; no production fixes here):
//   3.2 — identity version/revision validation and SourceEventRef encoding
//   3.3 — runtime FE/BE ingress producers and rating fact references
//   3.4 — schema V2 (store_id,source_event_key) uniqueness, supersession table
//   3.5 — compatibility projections and expanded bounded query indexes

type phase3Journal interface {
	Append(ctx context.Context, fact metering.Fact) error
	List(ctx context.Context, q metering.Query) (metering.Page, error)
}

type phase3Adapter struct {
	name string
	// open returns a journal for storeID.
	open func(t *testing.T, storeID string) phase3Journal
	// openPeer opens another storeID against the same durable substrate.
	// Nil when the adapter cannot share substrate (memory).
	openPeer func(t *testing.T, storeID string) phase3Journal
	// reopen simulates process restart for the same storeID.
	// Nil when unsupported (memory).
	reopen func(t *testing.T, storeID string) phase3Journal
	// uniqueID, when set, maps logical test prefixes to globally unique store IDs
	// (required for shared PostgreSQL to avoid cross-run pollution).
	uniqueID func(prefix string) string
}

func TestPhase3_MemoryJournalContracts(t *testing.T) {
	t.Parallel()
	runPhase3JournalContracts(t, phase3Adapter{
		name: "memory",
		open: func(t *testing.T, storeID string) phase3Journal {
			t.Helper()
			s, err := journalstore.NewMemoryStore(journalstore.MemoryConfig{StoreID: storeID})
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	})
}

func TestPhase3_SQLiteJournalContracts(t *testing.T) {
	t.Parallel()
	sqlDB, err := sql.Open("sqlite", memorySQLiteDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := journalstore.Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}

	open := func(t *testing.T, storeID string) phase3Journal {
		t.Helper()
		store, err := journalstore.OpenStore(ctx, bunDB, journalstore.DurableConfig{StoreID: storeID})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	runPhase3JournalContracts(t, phase3Adapter{
		name:     "sqlite",
		open:     open,
		openPeer: open,
		reopen:   open,
	})
}

func runPhase3JournalContracts(t *testing.T, a phase3Adapter) {
	t.Helper()
	a = withPhase3UniqueStoreIDs(a)
	// Subtests share one durable substrate for sqlite/postgres adapters; keep
	// them serial to avoid SQLITE_BUSY / pooled connection cross-talk. Memory
	// opens independent stores and may still parallelize via t.Parallel below.
	parallelOK := a.openPeer == nil
	run := func(name string, fn func(t *testing.T, a phase3Adapter)) {
		t.Run(name, func(t *testing.T) {
			if parallelOK {
				t.Parallel()
			}
			fn(t, a)
		})
	}
	run("idempotent_replay_and_conflict", phase3ContractIdempotentAndConflict)
	run("store_id_isolation", phase3ContractStoreIDIsolation)
	run("restart_replay", phase3ContractRestartReplay)
	run("fe_be_ingress_reconstruction", phase3ContractIngressReconstruction)
	run("correction_target_same_stream", phase3ContractCorrectionSameStream)
	run("correction_target_must_exist", phase3ContractCorrectionTargetExists)
	run("correction_rejects_self_target", phase3ContractCorrectionRejectsSelf)
	run("correction_rejects_cycle", phase3ContractCorrectionRejectsCycle)
	run("correction_append_only_history", phase3ContractCorrectionAppendOnly)
	run("bounded_query_no_cross_store_leak", phase3ContractBoundedQueryNoLeak)
	run("filter_store_scoped_no_cross_store_false_positive", phase3ContractFilterStoreScopedNoFalsePositive)
	run("source_event_key_durable_uniqueness", phase3ContractSourceEventKeyUniqueness)
	run("signed_correction_only", phase3ContractSignedCorrectionOnly)
}

func withPhase3UniqueStoreIDs(a phase3Adapter) phase3Adapter {
	if a.uniqueID == nil {
		return a
	}
	var mu sync.Mutex
	ids := map[string]string{}
	resolve := func(storeID string) string {
		mu.Lock()
		defer mu.Unlock()
		if id, ok := ids[storeID]; ok {
			return id
		}
		id := a.uniqueID(storeID)
		ids[storeID] = id
		return id
	}
	wrap := func(open func(t *testing.T, storeID string) phase3Journal) func(t *testing.T, storeID string) phase3Journal {
		if open == nil {
			return nil
		}
		return func(t *testing.T, storeID string) phase3Journal {
			t.Helper()
			return open(t, resolve(storeID))
		}
	}
	a.open = wrap(a.open)
	a.openPeer = wrap(a.openPeer)
	a.reopen = wrap(a.reopen)
	return a
}

func phase3ContractIdempotentAndConflict(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-idem"
	s := a.open(t, storeID)
	ctx := context.Background()
	f := phase3CustomerFEIngress(storeID+"-req", storeID+"-fe", 1)
	if err := s.Append(ctx, f); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Append(ctx, f); err != nil {
		t.Fatalf("identical replay must be idempotent: %v", err)
	}
	conflict := f
	conflict.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	if err := s.Append(ctx, conflict); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("conflicting payload got %v want ErrIdentityCollision (req 6.3)", err)
	}
	revConflict := f
	revConflict.Sequence = 2
	if err := s.Append(ctx, revConflict); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("conflicting revision/sequence got %v want ErrIdentityCollision (req 6.1, 6.3)", err)
	}
}

func phase3ContractStoreIDIsolation(t *testing.T, a phase3Adapter) {
	t.Helper()
	if a.openPeer == nil {
		t.Skip("store_id isolation requires shared durable substrate; memory deferred (task 3.4 schema uniqueness)")
	}
	idA := "p3-" + a.name + "-iso-a"
	idB := "p3-" + a.name + "-iso-b"
	aStore := a.open(t, idA)
	bStore := a.openPeer(t, idB)
	ctx := context.Background()

	// Same stream/fact identity payload in two logical stores must not collide.
	factID := "shared-fact-id"
	stream := "customer-request:shared-req"
	fa := phase3CustomerFEIngress("shared-req", factID, 1)
	fa.StreamID = stream
	fb := fa
	fb.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     77,
		Present:   true,
	}}
	if err := aStore.Append(ctx, fa); err != nil {
		t.Fatalf("store A append: %v", err)
	}
	if err := bStore.Append(ctx, fb); err != nil {
		t.Fatalf("store B append with same identity must succeed under (store_id, source_event_key) uniqueness (req 6.2; research G-17): %v", err)
	}
	pageA, err := aStore.List(ctx, metering.Query{StreamID: stream, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	pageB, err := bStore.List(ctx, metering.Query{StreamID: stream, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageA.Facts) != 1 || len(pageB.Facts) != 1 {
		t.Fatalf("isolation list A=%d B=%d", len(pageA.Facts), len(pageB.Facts))
	}
	if pageA.Facts[0].Quantities[0].Value != 10 {
		t.Fatalf("store A polluted: %+v", pageA.Facts[0].Quantities)
	}
	if pageB.Facts[0].Quantities[0].Value != 77 {
		t.Fatalf("store B polluted: %+v", pageB.Facts[0].Quantities)
	}
}

func phase3ContractRestartReplay(t *testing.T, a phase3Adapter) {
	t.Helper()
	if a.reopen == nil {
		t.Skip("restart replay unsupported for " + a.name)
	}
	storeID := "p3-" + a.name + "-restart"
	s1 := a.open(t, storeID)
	ctx := context.Background()
	req := storeID + "-req"
	att := storeID + "-att"
	facts := []metering.Fact{
		phase3CustomerFEIngress(req, storeID+"-fe-in", 1),
		phase3CustomerFEEgress(req, storeID+"-fe-out", 2),
		phase3OperatorBEIngress(att, req, storeID+"-be-in", 1),
		phase3OperatorBEEgress(att, req, storeID+"-be-out", 2),
	}
	// Insert out of stream order to prove deterministic List+aggregate ordering.
	for _, idx := range []int{2, 0, 3, 1} {
		if err := s1.Append(ctx, facts[idx]); err != nil {
			t.Fatalf("append %d: %v", idx, err)
		}
	}
	liveCust, err := listStream(ctx, s1, "customer-request:"+req)
	if err != nil {
		t.Fatal(err)
	}
	liveOp, err := listStream(ctx, s1, "operator-attempt:"+att)
	if err != nil {
		t.Fatal(err)
	}
	liveCustSnap, err := aggregate.Apply(liveCust)
	if err != nil {
		t.Fatal(err)
	}
	liveOpSnap, err := aggregate.Apply(liveOp)
	if err != nil {
		t.Fatal(err)
	}

	s2 := a.reopen(t, storeID)
	restartCust, err := listStream(ctx, s2, "customer-request:"+req)
	if err != nil {
		t.Fatal(err)
	}
	restartOp, err := listStream(ctx, s2, "operator-attempt:"+att)
	if err != nil {
		t.Fatal(err)
	}
	if len(restartCust) != 2 || len(restartOp) != 2 {
		t.Fatalf("restart reconstruction cust=%d op=%d (req 5.1–5.4)", len(restartCust), len(restartOp))
	}
	restartCustSnap, err := aggregate.Apply(restartCust)
	if err != nil {
		t.Fatal(err)
	}
	restartOpSnap, err := aggregate.Apply(restartOp)
	if err != nil {
		t.Fatal(err)
	}
	if restartCustSnap.Quantities[metering.ComponentInputToken] != liveCustSnap.Quantities[metering.ComponentInputToken] ||
		restartCustSnap.Quantities[metering.ComponentOutputToken] != liveCustSnap.Quantities[metering.ComponentOutputToken] {
		t.Fatalf("customer restart aggregate mismatch live=%v restart=%v", liveCustSnap.Quantities, restartCustSnap.Quantities)
	}
	if restartOpSnap.Quantities[metering.ComponentInputToken] != liveOpSnap.Quantities[metering.ComponentInputToken] ||
		restartOpSnap.Quantities[metering.ComponentOutputToken] != liveOpSnap.Quantities[metering.ComponentOutputToken] {
		t.Fatalf("operator restart aggregate mismatch live=%v restart=%v", liveOpSnap.Quantities, restartOpSnap.Quantities)
	}
}

func phase3ContractIngressReconstruction(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-ingress"
	s := a.open(t, storeID)
	ctx := context.Background()
	req := storeID + "-req"
	att := storeID + "-att"
	feIn := phase3CustomerFEIngress(req, storeID+"-fe-in", 1)
	beIn := phase3OperatorBEIngress(att, req, storeID+"-be-in", 1)
	feOut := phase3CustomerFEEgress(req, storeID+"-fe-out", 2)
	beOut := phase3OperatorBEEgress(att, req, storeID+"-be-out", 2)
	for _, f := range []metering.Fact{feIn, beIn, feOut, beOut} {
		if err := s.Append(ctx, f); err != nil {
			t.Fatalf("append %s: %v", f.FactID, err)
		}
	}
	cust, err := listStream(ctx, s, "customer-request:"+req)
	if err != nil {
		t.Fatal(err)
	}
	op, err := listStream(ctx, s, "operator-attempt:"+att)
	if err != nil {
		t.Fatal(err)
	}
	if len(cust) != 2 || len(op) != 2 {
		t.Fatalf("cust=%d op=%d want 2/2 (req 5.1–5.4)", len(cust), len(op))
	}
	assertBoundary(t, cust, metering.BoundaryFrontendIngress, metering.BoundaryFrontendEgress)
	assertBoundary(t, op, metering.BoundaryBackendIngress, metering.BoundaryBackendEgress)
	for _, f := range cust {
		if f.Perspective != metering.PerspectiveCustomer || f.Lifecycle != metering.LifecycleLogicalRequest {
			t.Fatalf("customer stream fact invalid perspective/lifecycle: %+v (req 5.5; D2)", f)
		}
	}
	for _, f := range op {
		if f.Perspective != metering.PerspectiveOperator || f.Lifecycle != metering.LifecycleBackendAttempt {
			t.Fatalf("operator stream fact invalid perspective/lifecycle: %+v (req 5.5; D2)", f)
		}
	}
}

func phase3ContractCorrectionSameStream(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-same-stream"
	s := a.open(t, storeID)
	ctx := context.Background()
	base := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-base", 1)
	other := phase3OperatorBEEgress(storeID+"-att-other", storeID+"-req", storeID+"-other", 1)
	if err := s.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, other); err != nil {
		t.Fatal(err)
	}
	corr := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-corr", 2)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{other.FactID} // different stream
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	if err := s.Append(ctx, corr); err == nil {
		t.Fatal("correction must reject supersession target outside same stream (req 6.6; D7)")
	}
}

func phase3ContractCorrectionTargetExists(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-target-exist"
	s := a.open(t, storeID)
	ctx := context.Background()
	corr := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-corr", 1)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{storeID + "-missing"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	if err := s.Append(ctx, corr); err == nil {
		t.Fatal("correction must reject missing supersession target (req 6.6; D7)")
	}
}

func phase3ContractCorrectionRejectsSelf(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-self"
	s := a.open(t, storeID)
	ctx := context.Background()
	corr := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-self", 1)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{corr.FactID}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	if err := s.Append(ctx, corr); err == nil {
		t.Fatal("correction must reject self-target supersession (req 6.7; D7)")
	}
}

func phase3ContractCorrectionRejectsCycle(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-cycle"
	s := a.open(t, storeID)
	ctx := context.Background()
	aFact := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-a", 1)
	bFact := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-b", 2)
	bFact.Kind = metering.FactKindCorrection
	bFact.Supersedes = []string{aFact.FactID}
	bFact.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	cFact := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-c", 3)
	cFact.Kind = metering.FactKindCorrection
	cFact.Supersedes = []string{bFact.FactID}
	cFact.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	for _, f := range []metering.Fact{aFact, bFact, cFact} {
		if err := s.Append(ctx, f); err != nil {
			t.Fatalf("seed %s: %v", f.FactID, err)
		}
	}
	// Close a→…→c→a by reusing FactID a as a correction that supersedes c.
	cycleClose := aFact
	cycleClose.Kind = metering.FactKindCorrection
	cycleClose.Sequence = 4
	cycleClose.Supersedes = []string{cFact.FactID}
	cycleClose.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	err := s.Append(ctx, cycleClose)
	if err == nil {
		t.Fatal("correction must reject cyclic supersession (req 6.7; D7)")
	}
	if errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatal("cyclic supersession surfaced as identity collision; want cycle-specific rejection (req 6.7; task 3.4)")
	}
}

func phase3ContractCorrectionAppendOnly(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-append-only"
	s := a.open(t, storeID)
	ctx := context.Background()
	base := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-base", 1)
	if err := s.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	corr := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-corr", 2)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{base.FactID}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -1,
		Present:   true,
	}}
	if err := s.Append(ctx, corr); err != nil {
		t.Fatalf("same-stream existing-target correction: %v", err)
	}
	page, err := s.List(ctx, metering.Query{StreamID: base.StreamID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 2 {
		t.Fatalf("want immutable history len=2 got %d (req 6.9)", len(page.Facts))
	}
	var storedBase *metering.Fact
	for i := range page.Facts {
		if page.Facts[i].FactID == base.FactID {
			storedBase = &page.Facts[i]
		}
	}
	if storedBase == nil || storedBase.Quantities[0].Value != base.Quantities[0].Value {
		t.Fatalf("prior fact body mutated: %+v (req 6.9)", storedBase)
	}
}

func phase3ContractBoundedQueryNoLeak(t *testing.T, a phase3Adapter) {
	t.Helper()
	if a.openPeer == nil {
		t.Skip("cross-store query isolation requires shared durable substrate")
	}
	idA := "p3-" + a.name + "-q-a"
	idB := "p3-" + a.name + "-q-b"
	aStore := a.open(t, idA)
	bStore := a.openPeer(t, idB)
	ctx := context.Background()
	req := "shared-query-req"
	stream := "customer-request:" + req
	fa := phase3CustomerFEIngress(req, idA+"-f", 1)
	fa.StreamID = stream
	fb := phase3CustomerFEIngress(req, idB+"-f", 1)
	fb.StreamID = stream
	fb.Quantities[0].Value = 42
	if err := aStore.Append(ctx, fa); err != nil {
		t.Fatal(err)
	}
	if err := bStore.Append(ctx, fb); err != nil {
		t.Fatalf("store B append required for leak probe under store-scoped uniqueness (req 6.2): %v", err)
	}
	page, err := aStore.List(ctx, metering.Query{StreamID: stream, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range page.Facts {
		if f.FactID == idB+"-f" {
			t.Fatalf("cross-store leakage of fact %q into store %q (req 6.2)", f.FactID, idA)
		}
		if len(f.Quantities) > 0 && f.Quantities[0].Value == 42 && f.FactID != fa.FactID {
			t.Fatalf("cross-store leakage via payload: %+v", f)
		}
	}
	if _, err := aStore.List(ctx, metering.Query{Limit: 10}); !errors.Is(err, journalstore.ErrQueryTooBroad) {
		t.Fatalf("unbounded list got %v want ErrQueryTooBroad", err)
	}
}

func phase3ContractFilterStoreScopedNoFalsePositive(t *testing.T, a phase3Adapter) {
	t.Helper()
	if a.openPeer == nil {
		t.Skip("filter store isolation requires shared durable substrate")
	}
	idA := "p3-" + a.name + "-filt-a"
	idB := "p3-" + a.name + "-filt-b"
	aStore := a.open(t, idA)
	bStore := a.openPeer(t, idB)
	ctx := context.Background()

	req := "filt-shared-req"
	stream := "customer-request:" + req
	factID := "shared-filter-fact"
	fa := phase3CustomerFEIngress(req, factID, 1)
	fa.StreamID = stream
	fa.Scope.PrincipalID = scope.Known("alice")
	fb := fa
	fb.Scope.PrincipalID = scope.Known("bob")
	fb.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     77,
		Present:   true,
	}}
	if err := aStore.Append(ctx, fa); err != nil {
		t.Fatalf("store A append: %v", err)
	}
	if err := bStore.Append(ctx, fb); err != nil {
		t.Fatalf("store B append: %v", err)
	}

	// principal_id is indexed via metering_fact_filters EXISTS (not a facts column).
	page, err := aStore.List(ctx, metering.Query{
		StreamID: stream,
		Scope:    metering.ScopeFilters{PrincipalID: scope.Known("bob")},
		Limit:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 0 {
		t.Fatalf("store A List by principal=bob must not match via store B filters; got %d facts (req 6.2/D12)", len(page.Facts))
	}
	pageBob, err := bStore.List(ctx, metering.Query{
		StreamID: stream,
		Scope:    metering.ScopeFilters{PrincipalID: scope.Known("bob")},
		Limit:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageBob.Facts) != 1 || pageBob.Facts[0].Quantities[0].Value != 77 {
		t.Fatalf("store B principal=bob want one fact value=77 got %+v", pageBob.Facts)
	}
}

func phase3ContractSourceEventKeyUniqueness(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-sek"
	s := a.open(t, storeID)
	ctx := context.Background()

	base := phase3CustomerFEIngress(storeID+"-req", storeID+"-fact", 1)
	base.IdentityVersion = 1
	base.SourceRevision = 0
	base.SourceEventKind = string(base.Kind)
	base.SourceID = base.FactID
	if err := s.Append(ctx, base); err != nil {
		t.Fatalf("append base: %v", err)
	}
	if err := s.Append(ctx, base); err != nil {
		t.Fatalf("identical SourceEventKey replay must be idempotent: %v", err)
	}

	conflict := base
	conflict.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     99,
		Present:   true,
	}}
	if err := s.Append(ctx, conflict); !errors.Is(err, journalstore.ErrIdentityCollision) {
		t.Fatalf("same SourceEventKey different payload got %v want ErrIdentityCollision (D6)", err)
	}

	// Metering V2 primary row identity is (store_id, stream_id, fact_id). Distinct
	// SourceEventKey variants therefore require distinct FactIDs in-stream; the
	// key components below still prove SourceEventKey uniqueness (D6 / task 3.4).
	otherVersion := base
	otherVersion.FactID = base.FactID + "-iv2"
	otherVersion.SourceID = otherVersion.FactID
	otherVersion.Sequence = 2
	otherVersion.IdentityVersion = 2
	otherVersion.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     50,
		Present:   true,
	}}
	if err := s.Append(ctx, otherVersion); err != nil {
		t.Fatalf("IdentityVersion must participate in SourceEventKey uniqueness: %v", err)
	}

	otherRev := base
	otherRev.FactID = base.FactID + "-rev3"
	otherRev.SourceID = otherRev.FactID
	otherRev.Sequence = 3
	otherRev.SourceRevision = 3
	otherRev.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     51,
		Present:   true,
	}}
	if err := s.Append(ctx, otherRev); err != nil {
		t.Fatalf("SourceRevision must participate in SourceEventKey uniqueness: %v", err)
	}

	otherKind := base
	otherKind.FactID = base.FactID + "-kind"
	otherKind.SourceID = otherKind.FactID
	otherKind.Sequence = 4
	otherKind.SourceEventKind = "ingress-observed"
	otherKind.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     52,
		Present:   true,
	}}
	if err := s.Append(ctx, otherKind); err != nil {
		t.Fatalf("SourceEventKind must participate in SourceEventKey uniqueness: %v", err)
	}

	otherSource := base
	otherSource.FactID = base.FactID + "-src"
	otherSource.SourceID = base.FactID + "-alt"
	otherSource.Sequence = 5
	otherSource.Quantities = []metering.Quantity{{
		Component: metering.ComponentInputToken,
		Unit:      metering.UnitToken,
		Value:     53,
		Present:   true,
	}}
	if err := s.Append(ctx, otherSource); err != nil {
		t.Fatalf("SourceID must participate in SourceEventKey uniqueness: %v", err)
	}

	if a.reopen == nil {
		return
	}
	restarted := a.reopen(t, storeID)
	if err := restarted.Append(ctx, base); err != nil {
		t.Fatalf("restart replay same SourceEventKey must be idempotent: %v", err)
	}
	page, err := restarted.List(ctx, metering.Query{StreamID: base.StreamID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Facts) != 5 {
		t.Fatalf("want 5 distinct SourceEventKey facts after restart, got %d", len(page.Facts))
	}
}

func phase3ContractSignedCorrectionOnly(t *testing.T, a phase3Adapter) {
	t.Helper()
	storeID := "p3-" + a.name + "-signed"
	s := a.open(t, storeID)
	ctx := context.Background()
	neg := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-neg", 1)
	neg.Kind = metering.FactKindDelta
	neg.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -2,
		Present:   true,
	}}
	if err := s.Append(ctx, neg); err == nil {
		t.Fatal("store must reject negative ordinary delta (req 6.4; signed corrections only)")
	}
	base := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-base", 1)
	if err := s.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	corr := phase3OperatorBEEgress(storeID+"-att", storeID+"-req", storeID+"-corr", 2)
	corr.Kind = metering.FactKindCorrection
	corr.Supersedes = []string{base.FactID}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     -2,
		Present:   true,
	}}
	if err := s.Append(ctx, corr); err != nil {
		t.Fatalf("signed correction must append: %v", err)
	}
}

func listStream(ctx context.Context, s phase3Journal, streamID string) ([]metering.Fact, error) {
	page, err := s.List(ctx, metering.Query{StreamID: streamID, Limit: 100})
	if err != nil {
		return nil, err
	}
	return page.Facts, nil
}

func assertBoundary(t *testing.T, facts []metering.Fact, want ...metering.Boundary) {
	t.Helper()
	seen := map[metering.Boundary]bool{}
	for _, f := range facts {
		seen[f.Boundary] = true
	}
	for _, b := range want {
		if !seen[b] {
			t.Fatalf("missing boundary %q in facts", b)
		}
	}
}

func phase3CustomerFEIngress(requestID, factID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    "customer-request:" + requestID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveCustomer,
		Boundary:    metering.BoundaryFrontendIngress,
		Lifecycle:   metering.LifecycleLogicalRequest,
		Correlation: metering.Correlation{RequestID: requestID, TraceID: "trace-" + requestID, ALegID: "a-" + requestID},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("prin-" + requestID),
			TenantID:    scope.Known("ten-1"),
		},
		FrontendID: "openai-responses",
		Source:     metering.SourceObserved,
		Authority:  metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		RecordedAt: time.Unix(10, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     10,
			Present:   true,
		}},
	}
}

func phase3CustomerFEEgress(requestID, factID string, seq int64) metering.Fact {
	f := phase3CustomerFEIngress(requestID, factID, seq)
	f.Boundary = metering.BoundaryFrontendEgress
	f.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     6,
		Present:   true,
	}}
	return f
}

func phase3OperatorBEIngress(attemptID, requestID, factID string, seq int64) metering.Fact {
	return metering.Fact{
		FactID:      factID,
		StreamID:    "operator-attempt:" + attemptID,
		Sequence:    seq,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{
			RequestID: requestID,
			AttemptID: attemptID,
			ALegID:    "a-" + requestID,
			BLegID:    "b-" + attemptID,
		},
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("prin-" + requestID),
			TenantID:    scope.Known("ten-1"),
		},
		BackendID:  "openai",
		Model:      "gpt-test",
		Source:     metering.SourceDerived,
		Authority:  metering.AuthorityAuthoritative,
		Presence:   metering.PresencePresent,
		RecordedAt: time.Unix(11, 0).UTC(),
		Quantities: []metering.Quantity{{
			Component: metering.ComponentInputToken,
			Unit:      metering.UnitToken,
			Value:     12,
			Present:   true,
		}},
	}
}

func phase3OperatorBEEgress(attemptID, requestID, factID string, seq int64) metering.Fact {
	f := phase3OperatorBEIngress(attemptID, requestID, factID, seq)
	f.Boundary = metering.BoundaryBackendEgress
	f.Source = metering.SourceProviderReported
	f.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken,
		Unit:      metering.UnitToken,
		Value:     4,
		Present:   true,
	}}
	f.Money = &metering.MoneyObservation{
		NanoUnits: 100,
		Currency:  "USD",
		Present:   true,
		Source:    metering.SourceProviderReported,
	}
	return f
}
