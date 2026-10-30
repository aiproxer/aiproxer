package workstore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.3 RED/GREEN workstore contracts (requirements 8.1–8.5, 8.7, 8.9, 11.3–11.5;
// design D6, D9, D12, D14).

type phase43Store interface {
	AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error
	GetByWorkID(ctx context.Context, workID string) (terminalwork.WorkRecord, error)
	GetBySourceKey(ctx context.Context, key terminalwork.SourceKey) (terminalwork.WorkRecord, error)
	PromotePending(ctx context.Context, cmd workstore.PromotePendingCommand) error
	ClaimDue(ctx context.Context, cmd workstore.ClaimDueCommand) ([]terminalwork.WorkRecord, error)
	RenewClaim(ctx context.Context, cmd workstore.RenewClaimCommand) error
	Complete(ctx context.Context, cmd workstore.CompleteCommand) error
	ScheduleRetry(ctx context.Context, cmd workstore.ScheduleRetryCommand) error
	Quarantine(ctx context.Context, cmd workstore.QuarantineCommand) error
	List(ctx context.Context, q workstore.Query) (workstore.Page, error)
}

type phase43Adapter struct {
	name     string
	open     func(t *testing.T, storeID string) phase43Store
	openPeer func(t *testing.T, storeID string) phase43Store
	reopen   func(t *testing.T, storeID string) phase43Store
	uniqueID func(prefix string) string
}

func validSource(key string) terminalwork.SourceKey {
	return terminalwork.SourceKey{IdentityVersion: 1, Key: key}
}

func sampleRecord(workID, sourceKey, provider string, kind sdk.WorkKind) terminalwork.WorkRecord {
	return terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      validSource(sourceKey),
		PayloadVersion: 1,
		Kind:           kind,
		State:          sdk.WorkStateIntent,
		ProviderID:     provider,
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: "req-1",
			AttemptID: "att-1",
			TraceID:   "tr-1",
		},
		Versions: terminalwork.BoundVersions{
			GenerationID: "g1",
			ProviderID:   provider,
			RatingID:     "r1",
		},
		Payload: []byte(`{"handle":"h1"}`),
	}
}

func TestPhase43_MemoryWorkStoreContracts(t *testing.T) {
	t.Parallel()
	shared := workstore.NewMemoryState()
	runPhase43WorkStoreContracts(t, phase43Adapter{
		name: "memory",
		open: func(t *testing.T, storeID string) phase43Store {
			t.Helper()
			s, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: storeID, State: shared})
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		openPeer: func(t *testing.T, storeID string) phase43Store {
			t.Helper()
			s, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: storeID, State: shared})
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	})
}

func TestPhase43_SQLiteWorkStoreContracts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "phase43-terminal-work.db")
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := sqlDB.ExecContext(context.Background(), `PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := workstore.Migrate(ctx, bunDB); err != nil {
		t.Fatal(err)
	}
	open := func(t *testing.T, storeID string) phase43Store {
		t.Helper()
		store, err := workstore.OpenStore(ctx, bunDB, workstore.DurableConfig{StoreID: storeID})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	runPhase43WorkStoreContracts(t, phase43Adapter{
		name:     "sqlite",
		open:     open,
		openPeer: open,
		reopen:   open,
	})
}

func runPhase43WorkStoreContracts(t *testing.T, a phase43Adapter) {
	t.Helper()
	a = withPhase43UniqueStoreIDs(a)
	parallelOK := a.openPeer == nil
	run := func(name string, fn func(t *testing.T, a phase43Adapter)) {
		t.Run(name, func(t *testing.T) {
			if parallelOK {
				t.Parallel()
			}
			fn(t, a)
		})
	}
	run("intent_idempotent_replay_and_conflict", phase43IntentIdempotentAndConflict)
	run("intent_survives_restart", phase43IntentSurvivesRestart)
	run("store_id_isolation", phase43StoreIDIsolation)
	run("claim_due_and_complete", phase43ClaimDueAndComplete)
	run("claim_renew_extends_lease", phase43ClaimRenewExtendsLease)
	run("claim_command_rejects_empty_owner_and_nonpositive_ttl", phase43ClaimCommandValidation)
	run("claim_contention_two_handles", phase43ClaimContentionTwoHandles)
	run("append_intent_unique_race", phase43AppendIntentUniqueRace)
	run("retry_schedule_and_reclaim", phase43RetryScheduleAndReclaim)
	run("renew_claim_error_taxonomy", phase43RenewClaimErrorTaxonomy)
	run("promote_pending_replay_idempotent", phase43PromotePendingReplayIdempotent)
	run("quarantine_permanent", phase43QuarantinePermanent)
	run("bounded_query_rejects_broad_scan", phase43BoundedQueryRejectsBroad)
	run("bounded_query_by_state_and_request", phase43BoundedQueryByStateAndRequest)
}

func withPhase43UniqueStoreIDs(a phase43Adapter) phase43Adapter {
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
	wrap := func(open func(t *testing.T, storeID string) phase43Store) func(t *testing.T, storeID string) phase43Store {
		if open == nil {
			return nil
		}
		return func(t *testing.T, storeID string) phase43Store {
			t.Helper()
			return open(t, resolve(storeID))
		}
	}
	a.open = wrap(a.open)
	a.openPeer = wrap(a.openPeer)
	a.reopen = wrap(a.reopen)
	return a
}

func phase43IntentIdempotentAndConflict(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-intent")
	rec := sampleRecord("w-1", "sk-1", "prov-a", sdk.WorkKindSettleRequestProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := store.GetBySourceKey(ctx, rec.SourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkID != rec.WorkID || got.State != sdk.WorkStateIntent {
		t.Fatalf("got=%+v", got)
	}

	conflict := rec
	conflict.Payload = []byte(`{"handle":"h2"}`)
	if err := store.AppendIntent(ctx, conflict); !errors.Is(err, workstore.ErrIdentityCollision) {
		t.Fatalf("conflict got %v", err)
	}
}

func phase43IntentSurvivesRestart(t *testing.T, a phase43Adapter) {
	t.Helper()
	if a.reopen == nil {
		t.Skip("restart not supported")
	}
	ctx := context.Background()
	storeID := "store-restart"
	rec := sampleRecord("w-restart", "sk-restart", "prov-a", sdk.WorkKindAppendFact)
	rec.FactID = "fact-1"
	if err := a.open(t, storeID).AppendIntent(ctx, rec); err != nil {
		t.Fatalf("append: %v", err)
	}
	reopened := a.reopen(t, storeID)
	got, err := reopened.GetByWorkID(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if !terminalwork.SameIntentReplay(rec, got) {
		t.Fatalf("restart lost intent: got=%+v", got)
	}
	if err := reopened.AppendIntent(ctx, rec); err != nil {
		t.Fatalf("ambiguous replay after restart: %v", err)
	}
}

func phase43StoreIDIsolation(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	aStore := a.open(t, "store-a")
	rec := sampleRecord("w-a", "sk-a", "prov-a", sdk.WorkKindReleaseRequestProvider)
	if err := aStore.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	bStore := a.open(t, "store-b")
	if a.openPeer != nil {
		bStore = a.openPeer(t, "store-b")
	}
	_, err := bStore.GetBySourceKey(ctx, rec.SourceKey)
	if !errors.Is(err, workstore.ErrNotFound) {
		t.Fatalf("cross-store leak got %v", err)
	}
}

func phase43ClaimDueAndComplete(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-claim")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-claim", "sk-claim", "prov-a", sdk.WorkKindSettleAttemptProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Limit:   1,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].State != sdk.WorkStateClaimed {
		t.Fatalf("claimed=%+v", claimed)
	}
	if err := store.Complete(ctx, workstore.CompleteCommand{
		WorkID:          rec.WorkID,
		ExpectedOwnerID: "worker-1",
		Now:             now,
	}); err != nil {
		t.Fatal(err)
	}
	done, err := store.GetByWorkID(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != sdk.WorkStateCompleted {
		t.Fatalf("state=%q", done.State)
	}
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatalf("completed replay must stay idempotent: %v", err)
	}
}

func phase43ClaimRenewExtendsLease(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-renew")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-renew", "sk-renew", "prov-a", sdk.WorkKindSettleAttemptProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Limit:   1,
		Now:     now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim err=%v len=%d", err, len(claimed))
	}
	got, err := store.GetByWorkID(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	wantExpires := now.Add(time.Minute)
	if !got.Lease.ExpiresAt.Equal(wantExpires) {
		t.Fatalf("ExpiresAt=%v want %v", got.Lease.ExpiresAt, wantExpires)
	}
	renewAt := now.Add(30 * time.Second)
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  rec.WorkID,
		OwnerID: "worker-1",
		TTL:     2 * time.Minute,
		Now:     renewAt,
	}); err != nil {
		t.Fatalf("renew: %v", err)
	}
	renewed, err := store.GetByWorkID(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	wantRenewed := renewAt.Add(2 * time.Minute)
	if !renewed.Lease.ExpiresAt.Equal(wantRenewed) {
		t.Fatalf("renewed ExpiresAt=%v want %v", renewed.Lease.ExpiresAt, wantRenewed)
	}
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  rec.WorkID,
		OwnerID: "worker-2",
		TTL:     time.Minute,
		Now:     renewAt,
	}); !errors.Is(err, workstore.ErrConflict) {
		t.Fatalf("wrong owner renew got %v want ErrConflict", err)
	}
}

func phase43ClaimCommandValidation(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-claim-cmd")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-claim-cmd", "sk-claim-cmd", "prov-a", sdk.WorkKindSettleRequestProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	_, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "",
		TTL:     time.Minute,
		Limit:   1,
		Now:     now,
	})
	if !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("empty owner got %v want ErrInvalid", err)
	}
	_, err = store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-1",
		TTL:     0,
		Limit:   1,
		Now:     now,
	})
	if !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("zero ttl got %v want ErrInvalid", err)
	}
	_, err = store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-1",
		TTL:     -time.Second,
		Limit:   1,
		Now:     now,
	})
	if !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("negative ttl got %v want ErrInvalid", err)
	}
	_, err = store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Limit:   -1,
		Now:     now,
	})
	if !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("negative limit got %v want ErrInvalid", err)
	}
	got, err := store.GetByWorkID(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sdk.WorkStatePending {
		t.Fatalf("invalid claim must not mutate state=%q", got.State)
	}
}

func phase43RenewClaimErrorTaxonomy(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-renew-tax")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  "missing-work",
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Now:     now,
	}); !errors.Is(err, workstore.ErrNotFound) {
		t.Fatalf("missing got %v want ErrNotFound", err)
	}
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  "missing-work",
		OwnerID: "",
		TTL:     time.Minute,
		Now:     now,
	}); !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("empty owner got %v want ErrInvalid", err)
	}
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  "missing-work",
		OwnerID: "worker-1",
		TTL:     0,
		Now:     now,
	}); !errors.Is(err, sdk.ErrInvalid) {
		t.Fatalf("zero ttl got %v want ErrInvalid", err)
	}

	rec := sampleRecord("w-renew-tax", "sk-renew-tax", "prov-a", sdk.WorkKindSettleAttemptProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  rec.WorkID,
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Now:     now,
	}); !errors.Is(err, sdk.ErrInvalidTransition) {
		t.Fatalf("renew from intent got %v want ErrInvalidTransition", err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Limit:   1,
		Now:     now,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim err=%v len=%d", err, len(claimed))
	}
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  rec.WorkID,
		OwnerID: "worker-2",
		TTL:     time.Minute,
		Now:     now.Add(10 * time.Second),
	}); !errors.Is(err, workstore.ErrConflict) {
		t.Fatalf("wrong owner got %v want ErrConflict", err)
	}
	if err := store.RenewClaim(ctx, workstore.RenewClaimCommand{
		WorkID:  rec.WorkID,
		OwnerID: "worker-1",
		TTL:     time.Minute,
		Now:     now.Add(2 * time.Minute),
	}); !errors.Is(err, sdk.ErrClaimLeaseHeld) {
		t.Fatalf("expired renew got %v want ErrClaimLeaseHeld", err)
	}
}

func phase43PromotePendingReplayIdempotent(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-promote")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-promote", "sk-promote", "prov-a", sdk.WorkKindReleaseRequestProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now.Add(time.Second)}); err != nil {
		t.Fatalf("replay promote: %v", err)
	}
	got, err := store.GetByWorkID(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != sdk.WorkStatePending {
		t.Fatalf("state=%q", got.State)
	}
}

func phase43AppendIntentUniqueRace(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	storeID := "store-race"
	base := a.open(t, storeID)
	handles := []phase43Store{base}
	if a.openPeer != nil {
		handles = append(handles, a.openPeer(t, storeID))
	}
	rec := sampleRecord("w-race", "sk-race", "prov-a", sdk.WorkKindSettleRequestProvider)

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			store := handles[i%len(handles)]
			errs[i] = retryAppendIntentBusy(func() error {
				return store.AppendIntent(ctx, rec)
			})
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	got, err := base.GetBySourceKey(ctx, rec.SourceKey)
	if err != nil {
		t.Fatal(err)
	}
	if !terminalwork.SameIntentReplay(rec, got) {
		t.Fatal("stored record mismatch")
	}
	page, err := base.List(ctx, workstore.Query{
		State:     sdk.WorkStateIntent,
		RequestID: rec.Lifecycle.RequestID,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("rows=%d want 1", len(page.Records))
	}

	conflict := rec
	conflict.Payload = []byte(`{"handle":"other"}`)
	if err := base.AppendIntent(ctx, conflict); !errors.Is(err, workstore.ErrIdentityCollision) {
		t.Fatalf("conflicting payload got %v want ErrIdentityCollision", err)
	}
}

func retryAppendIntentBusy(fn func() error) error {
	const budget = 10 * time.Second
	deadline := time.Now().Add(budget)
	backoff := 10 * time.Millisecond
	for {
		err := fn()
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(backoff)
		if backoff < 250*time.Millisecond {
			backoff *= 2
		}
	}
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}

func phase43ClaimContentionTwoHandles(t *testing.T, a phase43Adapter) {
	t.Helper()
	if a.openPeer == nil {
		t.Skip("peer handle not supported")
	}
	ctx := context.Background()
	storeID := "store-contention"
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	aHandle := a.open(t, storeID)
	bHandle := a.openPeer(t, storeID)
	for i := 1; i <= 3; i++ {
		rec := sampleRecord(fmt.Sprintf("w-c%d", i), fmt.Sprintf("sk-c%d", i), "prov-a", sdk.WorkKindReleaseAttemptProvider)
		if err := aHandle.AppendIntent(ctx, rec); err != nil {
			t.Fatal(err)
		}
		if err := aHandle.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
			t.Fatal(err)
		}
	}
	claimedA, err := aHandle.ClaimDue(ctx, workstore.ClaimDueCommand{OwnerID: "worker-a", TTL: time.Minute, Limit: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	claimedB, err := bHandle.ClaimDue(ctx, workstore.ClaimDueCommand{OwnerID: "worker-b", TTL: time.Minute, Limit: 2, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for _, rec := range append(claimedA, claimedB...) {
		if _, ok := seen[rec.WorkID]; ok {
			t.Fatalf("duplicate claim for %q", rec.WorkID)
		}
		seen[rec.WorkID] = struct{}{}
	}
	if len(seen) != 3 {
		t.Fatalf("claimed=%d want 3", len(seen))
	}
}

func phase43RetryScheduleAndReclaim(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-retry")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-retry", "sk-retry", "prov-a", sdk.WorkKindCompensateProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{OwnerID: "worker-1", TTL: time.Minute, Limit: 1, Now: now})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %v len=%d", err, len(claimed))
	}
	sched := terminalwork.RetrySchedule{Initial: time.Second, Multiplier: 2, Max: 8 * time.Second}
	if err := store.ScheduleRetry(ctx, workstore.ScheduleRetryCommand{
		WorkID:          rec.WorkID,
		ExpectedOwnerID: "worker-1",
		Schedule:        sched,
		Err:             terminalwork.BoundedError{Code: "ambiguous", Permanent: false},
		Now:             now,
	}); err != nil {
		t.Fatal(err)
	}
	beforeDue, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{OwnerID: "worker-2", TTL: time.Minute, Limit: 1, Now: now.Add(500 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeDue) != 0 {
		t.Fatalf("claimed before due: %+v", beforeDue)
	}
	afterDue, err := store.ClaimDue(ctx, workstore.ClaimDueCommand{OwnerID: "worker-2", TTL: time.Minute, Limit: 1, Now: now.Add(time.Second)})
	if err != nil || len(afterDue) != 1 {
		t.Fatalf("reclaim after due: err=%v len=%d", err, len(afterDue))
	}
}

func phase43QuarantinePermanent(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-quarantine")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	rec := sampleRecord("w-q", "sk-q", "prov-a", sdk.WorkKindSettleRequestProvider)
	if err := store.AppendIntent(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(ctx, workstore.PromotePendingCommand{WorkID: rec.WorkID, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Quarantine(ctx, workstore.QuarantineCommand{
		WorkID: rec.WorkID,
		Err:    terminalwork.BoundedError{Code: "malformed", Permanent: true, Message: "bad payload"},
		Now:    now,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(ctx, workstore.Query{State: sdk.WorkStateQuarantined, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].Error.Code != "malformed" {
		t.Fatalf("page=%+v", page)
	}
}

func phase43BoundedQueryRejectsBroad(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-broad")
	_, err := store.List(ctx, workstore.Query{Limit: 10})
	if !errors.Is(err, workstore.ErrQueryTooBroad) {
		t.Fatalf("got %v want ErrQueryTooBroad", err)
	}
}

func phase43BoundedQueryByStateAndRequest(t *testing.T, a phase43Adapter) {
	t.Helper()
	ctx := context.Background()
	store := a.open(t, "store-query")
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	match := sampleRecord("w-match", "sk-match", "prov-a", sdk.WorkKindSettleRequestProvider)
	match.Lifecycle.RequestID = "req-match"
	if err := store.AppendIntent(ctx, match); err != nil {
		t.Fatal(err)
	}
	other := sampleRecord("w-other", "sk-other", "prov-b", sdk.WorkKindSettleRequestProvider)
	other.Lifecycle.RequestID = "req-other"
	if err := store.AppendIntent(ctx, other); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(ctx, workstore.Query{
		State:     sdk.WorkStateIntent,
		RequestID: "req-match",
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].WorkID != match.WorkID {
		t.Fatalf("page=%+v", page)
	}
	_ = now
}
