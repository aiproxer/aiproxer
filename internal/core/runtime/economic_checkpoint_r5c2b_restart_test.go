package runtime

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

// R5-C2b durable restart proof.
//
// Reviewer finding R5-P1: a crash after a failed checkpoint write / capacity
// exhaustion, but before the terminal append, leaves only the accepted-prefix
// durable journal. The sticky capture-loss disposition lives on the in-memory
// attemptSession and is projected into the terminal leg's reserved conflict
// channel; a crash that never reaches the terminal append therefore loses the
// only representation of "this prefix is known-truncated". These tests use a
// real file-backed SQLite observation journal and the production checkpoint
// sink (journalstore.NewObservationSink / NewObservationSinkWithOutbox) rather
// than an in-memory double.
//
// TestR5C2bCrashLeavesAcceptedPrefixWithoutDurableDegradedDisposition proves the
// crash state: the durable rows are exactly the accepted prefix, the outbox
// holds one pending relay entry per accepted observation, and nothing durable
// records the capture loss. TestR5C2bHealthyFileBackedPrefixRestartThenTerminalRetailCompletes
// proves the healthy counter-path: a bounded healthy prefix survives the same
// close/reopen and still terminalizes into a loss-less leg that retail accepts.

// r5c2bFileStore owns one file-backed journal connection so a test can close and
// reopen the same database file to model a process restart.
type r5c2bFileStore struct {
	store *journalstore.DurableStore
	sqlDB *sql.DB
}

// r5c2bOpenFileStore opens the file-backed observation journal with the steering
// reopen/crash DSN: WAL journaling plus synchronous=NORMAL keeps close/reopen
// recovery for committed transactions durable without a full fsync per commit,
// and busy_timeout keeps a concurrent reader from failing an immediate write.
func r5c2bOpenFileStore(t *testing.T, path, storeID string) *r5c2bFileStore {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", durableCheckpointSQLiteDSN(path))
	if err != nil {
		t.Fatalf("r5c2b open sqlite: %v", err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("r5c2b bun db: %v", err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID})
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("r5c2b durable store: %v", err)
	}
	return &r5c2bFileStore{store: store, sqlDB: sqlDB}
}

func (s *r5c2bFileStore) close(t *testing.T) {
	t.Helper()
	if s == nil {
		return
	}
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			t.Fatalf("r5c2b close store: %v", err)
		}
	}
	if s.sqlDB != nil {
		if err := s.sqlDB.Close(); err != nil {
			t.Fatalf("r5c2b close sql: %v", err)
		}
	}
}

// r5c2bFlakySink is the unavailable journal from the finding. It delegates the
// production checkpoint batch append to the real file-backed outbox sink until a
// test makes it unavailable, without fabricating durable state of its own.
type r5c2bFlakySink struct {
	mu        sync.Mutex
	delegate  metering.AtomicObservationSink
	available bool
	attempts  int
}

func (s *r5c2bFlakySink) Append(ctx context.Context, observation metering.Observation) error {
	return s.AppendObservations(ctx, []metering.Observation{observation})
}

func (s *r5c2bFlakySink) AppendObservations(ctx context.Context, observations []metering.Observation) error {
	s.mu.Lock()
	s.attempts++
	available := s.available
	delegate := s.delegate
	s.mu.Unlock()
	if !available || delegate == nil {
		return errors.New("r5c2b journal unavailable")
	}
	return delegate.AppendObservations(ctx, observations)
}

func (s *r5c2bFlakySink) setAvailable(available bool) {
	s.mu.Lock()
	s.available = available
	s.mu.Unlock()
}

// r5c2bProviderObservations drains one native provider draft per revision from
// the stock provider evidence buffer, so the admitted observations carry
// validated provider-native media + aggregate provider money identities.
func r5c2bProviderObservations(callID usageevidence.BillingCallID, sourceKey string, from, to uint64) []metering.Observation {
	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(r5cProviderIdentity(callID))
	var out []metering.Observation
	for revision := from; revision <= to; revision++ {
		buffer.Add(r5cProviderDraft(sourceKey, revision))
		out = append(out, buffer.DrainEconomicObservations()...)
	}
	return out
}

// TestR5C2bCrashLeavesAcceptedPrefixWithoutDurableDegradedDisposition builds the
// exact crash state in the finding. A provider-native prefix is admitted and
// flushed to the file-backed journal; the journal then becomes unavailable, an
// equal number of further observations fill both bounded queues, and the
// overflow becomes sticky in-memory capture loss. The attempt is discarded
// (crash) before any terminal append, the journal file is closed, reopened, and
// inspected.
//
// The durable rows must be exactly the accepted prefix: no overflow marker, no
// loss disposition, and none of the admitted-but-unflushed observations can
// have become durable. This is the gap R5-C2b records: after a crash the
// degraded completeness of the prefix is not recoverable from durable state.
func TestR5C2bCrashLeavesAcceptedPrefixWithoutDurableDegradedDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const (
		prefixCount = 8
		overflow    = 5
	)
	capacity := maxPreTerminalEconomicCheckpointPending + maxPreTerminalEconomicCheckpointDeferred

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "r5c2b-crash.sqlite")
	first := r5c2bOpenFileStore(t, path, r5cStoreID)

	outbox := journalstore.NewObservationSinkWithOutbox(first.store)
	atomicSink, ok := outbox.(metering.AtomicObservationSink)
	if !ok {
		t.Fatal("outbox checkpoint sink is not an atomic observation sink")
	}
	sink := &r5c2bFlakySink{delegate: atomicSink, available: true}
	attempt := &attemptSession{
		observationSink: sink,
		now:             func() time.Time { return time.Unix(1_700_300_500, 0).UTC() },
	}

	prefix := r5c2bProviderObservations(callID, "provider.r5c2b.prefix", 1, prefixCount)
	if len(prefix) != prefixCount {
		t.Fatalf("prefix observations=%d, want %d", len(prefix), prefixCount)
	}
	for _, observation := range prefix {
		if admission := attempt.rememberEconomicEvidenceOnce(execbackend.EconomicEvidence{Observation: observation}); admission.disposition != economicEvidenceAdmissionRetained {
			t.Fatalf("prefix admission=%v, want retained", admission.disposition)
		}
	}
	if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
		t.Fatalf("prefix flush: %v", err)
	}

	// The journal is now unavailable for every later flush; the next capacity
	// observations are admitted to the bounded queues, and the overflow is the
	// irreversible capture loss.
	sink.setAvailable(false)
	lost := r5c2bProviderObservations(callID, "provider.r5c2b.lost", 1, uint64(capacity+overflow))
	if len(lost) != capacity+overflow {
		t.Fatalf("lost drain=%d, want %d", len(lost), capacity+overflow)
	}
	for _, observation := range lost {
		attempt.rememberEconomicObservationOnce(observation)
	}
	loss := attempt.evidenceCaptureLossSnapshot()
	if !loss.present || loss.count != overflow || !loss.causes.has(evidenceCaptureLossCheckpointCapacity) {
		t.Fatalf("sticky loss=%+v, want present count=%d cause=checkpoint-capacity", loss, overflow)
	}
	if err := attempt.flushEconomicCheckpoints(ctx, true); err == nil {
		t.Fatal("flush against the unavailable journal unexpectedly succeeded")
	}

	// Crash before the terminal append: discard the attempt and close the file.
	first.close(t)

	restarted := r5c2bOpenFileStore(t, path, r5cStoreID)
	defer restarted.close(t)

	page, err := restarted.store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: r5cStoreID, SubjectKind: metering.SubjectBLeg, SubjectID: r5cProviderIdentity(callID).BLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable observations after restart: %v", err)
	}
	if len(page.Observations) != prefixCount {
		t.Fatalf("durable observations=%d, want the accepted prefix %d", len(page.Observations), prefixCount)
	}
	pending, err := restarted.store.ListPendingObservationOutbox(ctx, 500)
	if err != nil {
		t.Fatalf("list durable outbox after restart: %v", err)
	}
	if len(pending) != prefixCount {
		t.Fatalf("durable outbox entries=%d, want %d", len(pending), prefixCount)
	}
	for i, item := range pending {
		if item.Status != "pending" {
			t.Fatalf("outbox[%d] status=%q, want pending", i, item.Status)
		}
	}

	// The durable journal is exactly the accepted prefix: every row matches an
	// accepted observation, so no marker row and none of the admitted-but-
	// unflushed observations leaked into durable state. The capture loss that
	// prevented the prefix from being complete is now unrecoverable.
	wantIDs := make(map[string]struct{}, prefixCount)
	for _, observation := range prefix {
		wantIDs[observation.ID] = struct{}{}
	}
	for _, durable := range page.Observations {
		if _, ok := wantIDs[durable.ID]; !ok {
			t.Fatalf("durable observation %q is not part of the accepted prefix", durable.ID)
		}
	}
}

// TestR5C2bHealthyFileBackedPrefixRestartThenTerminalRetailCompletes is the
// non-vacuous counter-path. A healthy bounded provider progression is flushed to
// a real file-backed journal, the file is closed and reopened (restart), the
// durable prefix survives, and the later terminal append still produces exactly
// one loss-less leg that downstream retail selection completes. This proves the
// R5-C2b disposition is specific to the truncated prefix and does not freeze the
// healthy checkpoint/restart/terminal path.
func TestR5C2bHealthyFileBackedPrefixRestartThenTerminalRetailCompletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const revisions = 20

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "r5c2b-healthy.sqlite")
	first := r5c2bOpenFileStore(t, path, r5cStoreID)

	harness := r5cNewTerminalHarness(t, callID)
	attempt := harness.attempt()
	attempt.observationSink = journalstore.NewObservationSink(first.store)

	buffer := coremetering.NewProviderEvidenceBuffer()
	buffer.BindEconomicEvidence(r5cProviderIdentity(callID))
	source := &r5cProviderStream{buffer: buffer}
	for revision := uint64(1); revision <= revisions; revision++ {
		buffer.Add(r5cProviderDraft("provider.r5c2b.healthy", revision))
		drained := source.DrainEconomicObservations()
		if len(drained) != 1 {
			t.Fatalf("revision %d: drained=%d, want 1", revision, len(drained))
		}
		attempt.rememberEconomicObservationOnce(drained[0])
		if revision%preTerminalEconomicCheckpointBatch == 0 || revision == revisions {
			if err := attempt.flushEconomicCheckpoints(ctx, true); err != nil {
				t.Fatalf("revision %d: flush: %v", revision, err)
			}
		}
	}
	if loss := attempt.evidenceCaptureLossSnapshot(); loss.present {
		t.Fatalf("healthy progression manufactured capture loss: %+v", loss)
	}

	first.close(t)
	restarted := r5c2bOpenFileStore(t, path, r5cStoreID)
	defer restarted.close(t)

	page, err := restarted.store.ListObservations(ctx, journalstore.ObservationQuery{
		StoreID: r5cStoreID, SubjectKind: metering.SubjectBLeg, SubjectID: r5cProviderIdentity(callID).BLegID, Limit: 500,
	})
	if err != nil {
		t.Fatalf("list durable healthy prefix: %v", err)
	}
	if len(page.Observations) != revisions {
		t.Fatalf("durable healthy prefix=%d, want %d", len(page.Observations), revisions)
	}

	attempt.observationSink = journalstore.NewObservationSink(restarted.store)
	harness.terminalize(t)

	if len(harness.legs) != 1 {
		t.Fatalf("terminal appended legs=%d, want 1", len(harness.legs))
	}
	leg := harness.legs[0]
	for _, conflict := range leg.EvidenceConflicts {
		if strings.HasPrefix(conflict.IncomingCoverageReason, evidenceCaptureLossConflictReasonPrefix) {
			t.Fatalf("healthy restart+terminal acquired capture loss: %+v", conflict)
		}
	}
	if len(leg.Observations) != revisions {
		t.Fatalf("terminal leg observations=%d, want %d", len(leg.Observations), revisions)
	}
	// Retail completion over the recovered healthy leg is Enterprise (5.2
	// preservation: TestEnterprisePreservationRuntimeHealthyProgressionCompletesRetail
	// and ...DurableRestartRetainsFinalLocalMeasurement's marker-vs-control).
}
