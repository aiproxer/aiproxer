package compactiondetect

import (
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// fakeClock is a deterministic, concurrency-safe clock for expiry tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Unix(100, 0).UTC()}
}

// TestTransactions_seriesReuseSuppressesStarts proves a series rule reuses one
// active A-leg transaction across matching utility subcalls and emits one
// started/one completed pair (requirement 6.2, 6.4).
func TestTransactions_seriesReuseSuppressesStarts(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	start1 := d.RequestOpened(reqMeta("tr-s1"), textCall("<conversation>\nSummarize with checkpoint.", 0))
	if len(start1) != 1 || start1[0].RuleID != "pi_openclaw.compaction_summary.v1" {
		t.Fatalf("first start wrong: %+v", start1)
	}
	// A later matching utility subcall reuses the transaction: no new start.
	if start2 := d.RequestOpened(reqMeta("tr-s2"), textCall("<conversation>\nRe-checkpoint the summary.", 0)); len(start2) != 0 {
		t.Fatalf("series utility subcall duplicated start: %+v", start2)
	}
	got := d.ResponseReleased(resMeta("tr-s2"), assistantItem("<summary>installed</summary>"))
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted {
		t.Fatalf("series completion wrong: %+v", got)
	}
	if got[0].TransactionID != start1[0].TransactionID {
		t.Fatalf("series completion tx %q != start tx %q", got[0].TransactionID, start1[0].TransactionID)
	}
	// A second completion signal emits nothing.
	if got2 := d.ResponseReleased(resMeta("tr-s2"), assistantItem("<summary>again</summary>")); len(got2) != 0 {
		t.Fatalf("series duplicate completion emitted: %+v", got2)
	}
}

// TestTransactions_seriesOrdinaryRequestClosesSilently proves an ordinary
// request closes an unprovable series transaction silently and a later start
// begins a fresh transaction (requirements 6.5, 6.7).
func TestTransactions_seriesOrdinaryRequestClosesSilently(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	start := d.RequestOpened(reqMeta("tr-o1"), textCall("<conversation>\nSummarize with checkpoint.", 0))
	if len(start) != 1 {
		t.Fatalf("start missing: %+v", start)
	}
	if evs := d.RequestOpened(reqMeta("tr-o2"), textCall("ordinary turn", 0)); len(evs) != 0 {
		t.Fatalf("ordinary request emitted: %+v", evs)
	}
	start2 := d.RequestOpened(reqMeta("tr-o3"), textCall("<conversation>\nSummarize with checkpoint.", 0))
	if len(start2) != 1 {
		t.Fatalf("second series start missing: %+v", start2)
	}
	if start2[0].TransactionID == start[0].TransactionID {
		t.Fatal("fresh series transaction must not reuse the closed one")
	}
}

// TestTransactions_singleEachStartIsNew proves a single rule treats each
// matching request as a new logical compaction (no cross-request reuse).
func TestTransactions_singleEachStartIsNew(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	start1 := d.RequestOpened(reqMeta("tr-g1"), textCall("CONTEXT CHECKPOINT COMPACTION\nfirst", 0))
	if len(start1) != 1 {
		t.Fatalf("first start missing: %+v", start1)
	}
	start2 := d.RequestOpened(reqMeta("tr-g2"), textCall("CONTEXT CHECKPOINT COMPACTION\nsecond", 0))
	if len(start2) != 1 {
		t.Fatalf("second start missing: %+v", start2)
	}
	if start2[0].TransactionID == start1[0].TransactionID {
		t.Fatal("single-rule transactions must be distinct per compaction")
	}
}

// TestTransactions_oldCompletedBeforeNewStarted proves a request that completes
// the old transaction heuristically and starts a new rule emits old completed
// before new started (requirement 6.6).
func TestTransactions_oldCompletedBeforeNewStarted(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	// The summarizer request itself carries the large history and matches the
	// pi/openclaw series start rule.
	prev := itemCall("<conversation>\nSummarize with checkpoint.\n"+bigText(40000), "tail-one", "tail-two")
	start := d.RequestOpened(reqMeta("tr-ob1"), prev)
	if len(start) != 1 || start[0].RuleID != "pi_openclaw.compaction_summary.v1" {
		t.Fatalf("series start missing: %+v", start)
	}
	// The compacted follow-up closes the old series transaction heuristically
	// AND starts a new codex checkpoint transaction.
	cur := itemCall("CONTEXT CHECKPOINT COMPACTION\n"+bigText(6000), "tail-one", "tail-two")
	evs := d.RequestOpened(reqMeta("tr-ob2"), cur)
	if len(evs) != 2 {
		t.Fatalf("events=%v want [completed, started]", evs)
	}
	if evs[0].Phase != compaction.PhaseCompleted || evs[0].Evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("first event must be heuristic completed: %+v", evs[0])
	}
	if evs[0].TransactionID != start[0].TransactionID {
		t.Fatalf("heuristic completion must close the old transaction %q, got %q", start[0].TransactionID, evs[0].TransactionID)
	}
	if evs[1].Phase != compaction.PhaseStarted || evs[1].RuleID != "codex.local_checkpoint.v1" {
		t.Fatalf("second event must be a new started: %+v", evs[1])
	}
}

// TestTransactions_deterministicIDs proves transaction ids are deterministic
// for the same A-leg/rule/trigger and differ across triggers (requirement 6.3).
func TestTransactions_deterministicIDs(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	a := d.RequestOpened(reqMeta("tr-id1"), textCall("CONTEXT CHECKPOINT COMPACTION\nx", 0))
	b := d.RequestOpened(reqMeta("tr-id2"), textCall("CONTEXT CHECKPOINT COMPACTION\nx", 0))
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("starts missing: %v %v", a, b)
	}
	if a[0].TransactionID == b[0].TransactionID {
		t.Fatal("different triggers must yield different transaction ids")
	}
	c := d.RequestOpened(reqMeta("tr-id1"), textCall("CONTEXT CHECKPOINT COMPACTION\nx", 0))
	if len(c) != 1 || c[0].TransactionID != a[0].TransactionID {
		t.Fatalf("same trigger must yield the same transaction id: %q vs %q", a[0].TransactionID, c[0].TransactionID)
	}
}

// TestTransactions_staleExpiry proves stale transactions expire so they cannot
// suppress later compactions indefinitely (requirement 6.7).
func TestTransactions_staleExpiry(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	d := New(Config{IdleTTL: time.Minute, Now: clock.Now})
	start1 := d.RequestOpened(reqMeta("tr-e1"), textCall("CONTEXT CHECKPOINT COMPACTION\nfirst", 0))
	if len(start1) != 1 {
		t.Fatalf("first start missing: %+v", start1)
	}
	clock.Advance(2 * time.Minute)
	start2 := d.RequestOpened(reqMeta("tr-e2"), textCall("CONTEXT CHECKPOINT COMPACTION\nsecond", 0))
	if len(start2) != 1 {
		t.Fatalf("second start missing after expiry: %+v", start2)
	}
	if start2[0].TransactionID == start1[0].TransactionID {
		t.Fatal("expired transaction must not suppress a later compaction")
	}
}

// TestTransactions_amortizedSweep proves the inactivity sweep is time-sampled
// (F3): a leg that becomes stale inside the amortization interval survives with
// its history intact, and is evicted at the next sweep boundary. Before the
// fix a per-call sweep evicted the leg immediately, losing in-flight
// fingerprint state (requirement 7.4; the max-entry bound still applies on
// every call).
func TestTransactions_amortizedSweep(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	// The TTL is smaller than the sweep interval so staleness can fall
	// between two sweep boundaries.
	idleTTL := defaultSweepInterval / 2
	d := New(Config{IdleTTL: idleTTL, Now: clock.Now})

	// t=0: first call sweeps (lastSweep is zero) and stores a large fingerprint.
	if evs := d.RequestOpened(reqMeta("tr-am1"), itemCall(bigText(40000), "t1", "t2")); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}

	// The leg is stale but inside the
	// amortization interval since the t=0 sweep, so its history must survive
	// and the heuristic rewrite still fires.
	clock.Advance(idleTTL + time.Second)
	evs := d.RequestOpened(reqMeta("tr-am2"), itemCall(bigText(6000), "t1", "t2"))
	if len(evs) != 1 || evs[0].Evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("amortized sweep lost in-flight history: %+v", evs)
	}

	// A detector call crosses the next sweep boundary; the stale leg
	// is evicted and recreated fresh, so its
	// prior fingerprint history is gone.
	clock.Advance(defaultSweepInterval)
	if evs := d.RequestOpened(reqMeta("tr-am3"), itemCall(bigText(40000), "u1", "u2")); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	d.mu.Lock()
	ls := d.legs["a-leg-1"]
	fresh := ls != nil && ls.lastFP.TraceID == "tr-am3"
	d.mu.Unlock()
	if !fresh {
		t.Fatal("stale leg survived the next sweep boundary with old history")
	}
}

// TestTransactions_maxEntryEviction proves the max-entry bound evicts the
// least-recently-seen A-leg so old state cannot accumulate (requirement 7.4).
func TestTransactions_maxEntryEviction(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	d := New(Config{MaxLegs: 2, Now: clock.Now})

	open := func(leg, trace string) []compaction.Event {
		meta := compaction.PreservationMeta{TraceID: trace, ALegID: leg, BLegID: "b", AttemptSeq: 1}
		evs := d.RequestOpened(meta, itemCall(bigText(40000), "t1", "t2"))
		clock.Advance(time.Second)
		return evs
	}
	// Fill and overflow the bound: leg-a is the oldest and must be evicted.
	if evs := open("leg-a", "tr-a"); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	if evs := open("leg-b", "tr-b"); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	if evs := open("leg-c", "tr-c"); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	// leg-a's fingerprint was evicted: the heuristic rewrite must not fire.
	meta := compaction.PreservationMeta{TraceID: "tr-a2", ALegID: "leg-a", BLegID: "b", AttemptSeq: 1}
	if evs := d.RequestOpened(meta, itemCall(bigText(6000), "t1", "t2")); len(evs) != 0 {
		t.Fatalf("evicted leg retained state: %+v", evs)
	}
}

// TestTransactions_concurrentALegs proves concurrent turns on many A-legs are
// race-safe (requirement 7.5). Run under -race.
func TestTransactions_concurrentALegs(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	const legs = 16
	const rounds = 40
	var wg sync.WaitGroup
	for i := range legs {
		wg.Go(func() {
			leg := "concurrent-leg-" + string(rune('a'+i%26)) + string(rune('0'+i))
			for r := range rounds {
				trace := leg + "-" + string(rune('0'+r/10)) + string(rune('0'+r%10))
				meta := compaction.PreservationMeta{TraceID: trace, ALegID: leg, BLegID: "b", AttemptSeq: 1}
				_ = d.RequestOpened(meta, textCall("CONTEXT CHECKPOINT COMPACTION\nround", 0))
				_ = d.ResponseReleased(compaction.PreservationMeta{TraceID: trace, ALegID: leg, BLegID: "b", AttemptSeq: 1}, assistantItem("CONTEXT CHECKPOINT COMPACTION\n<handoff>"))
			}
		})
	}
	wg.Wait()
}
