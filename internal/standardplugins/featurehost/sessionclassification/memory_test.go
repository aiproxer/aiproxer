package sessionclassification

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestMemoryStoreLoadMissingDoesNotAllocate(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 1, IdleTTL: time.Minute}, nil)
	ctx := context.Background()
	missing := keyForTest(t, "missing")
	if _, found, err := store.Load(ctx, missing); err != nil || found {
		t.Fatalf("Load(missing) = (found=%t, err=%v), want false, nil", found, err)
	}

	first := keyForTest(t, "first")
	_, record, ok, err := store.ClaimRemote(ctx, first, testTime(), 2, time.Minute, 0)
	if err != nil || !ok || record.RemoteAttempts != 1 {
		t.Fatalf("first claim = (ok=%t, record=%+v, err=%v), want allocated attempt 1", ok, record, err)
	}

	second := keyForTest(t, "second")
	_, _, _, err = store.ClaimRemote(ctx, second, testTime(), 2, time.Minute, 0)
	if !errors.Is(err, featurestate.ErrStoreCapacity) {
		t.Fatalf("second claim error = %v, want capacity error", err)
	}
	if _, found, err := store.Load(ctx, second); err != nil || found {
		t.Fatalf("capacity-rejected key was retained: found=%t err=%v", found, err)
	}
}

func TestMemoryStorePromoteFirstPositiveWinsAtStoreRevisionOne(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 4, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	key := keyForTest(t, "promote")
	first := localProposal(session.SourceLocalIdentity, "client.codex", 91)
	got, promoted, err := store.Promote(ctx, key, first, testTime())
	if err != nil || !promoted {
		t.Fatalf("first Promote() = (promoted=%t, record=%+v, err=%v), want promotion", promoted, got, err)
	}
	if got.Classification.Revision != 1 || got.Classification.Evidence != first.Evidence {
		t.Fatalf("first stored classification = %+v, want store-owned revision 1 and original evidence", got.Classification)
	}

	second := localProposal(session.SourceLocalTooling, "tooling.distinct_cluster", 7)
	again, promoted, err := store.Promote(ctx, key, second, testTime().Add(time.Second))
	if err != nil || promoted {
		t.Fatalf("repeat Promote() = (promoted=%t, err=%v), want idempotent no-op", promoted, err)
	}
	if again != got {
		t.Fatalf("repeat changed first record: got=%+v first=%+v", again, got)
	}
	if _, _, err := store.Promote(ctx, key, session.Classification{}, testTime().Add(2*time.Second)); !errors.Is(err, featurestate.ErrInvalidProposal) {
		t.Fatalf("weak proposal after promotion error = %v, want invalid proposal", err)
	}
	stillFirst, found, err := store.Load(ctx, key)
	if err != nil || !found || stillFirst != got {
		t.Fatalf("weak proposal rewrote positive state: got=%+v found=%t err=%v", stillFirst, found, err)
	}

	unknownKey := keyForTest(t, "unknown-proposal")
	if _, _, err := store.Promote(ctx, unknownKey, session.Classification{}, testTime()); !errors.Is(err, featurestate.ErrInvalidProposal) {
		t.Fatalf("unknown proposal error = %v, want invalid proposal", err)
	}
	if _, found, err := store.Load(ctx, unknownKey); err != nil || found {
		t.Fatalf("unknown proposal allocated a record: found=%t err=%v", found, err)
	}

	invalidKey := keyForTest(t, "invalid-proposal")
	invalid := session.Classification{Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity, Confidence: session.ConfidenceHigh, Evidence: "unsafe evidence", Revision: 100}
	if _, _, err := store.Promote(ctx, invalidKey, invalid, testTime()); !errors.Is(err, featurestate.ErrInvalidProposal) {
		t.Fatalf("malformed proposal error = %v, want invalid proposal", err)
	}
	if _, found, err := store.Load(ctx, invalidKey); err != nil || found {
		t.Fatalf("malformed proposal allocated a record: found=%t err=%v", found, err)
	}
}

func TestMemoryStoreUsesScopeKindAsPartOfAuthority(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 4, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	secure := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "same"}
	aLeg := featurestate.Key{Kind: featurestate.ScopeALeg, ID: "same"}
	if _, _, err := store.Promote(ctx, secure, localProposal(session.SourceLocalIdentity, "client.secure", 1), testTime()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Promote(ctx, aLeg, localProposal(session.SourceLocalTooling, "tooling.aleg", 1), testTime()); err != nil {
		t.Fatal(err)
	}
	secureRecord, secureFound, err := store.Load(ctx, secure)
	if err != nil || !secureFound || secureRecord.Classification.Evidence != "client.secure" {
		t.Fatalf("secure scope record = (%+v, %t, %v)", secureRecord, secureFound, err)
	}
	aLegRecord, aLegFound, err := store.Load(ctx, aLeg)
	if err != nil || !aLegFound || aLegRecord.Classification.Evidence != "tooling.aleg" {
		t.Fatalf("A-leg scope record = (%+v, %t, %v)", aLegRecord, aLegFound, err)
	}
}

func TestMemoryStoreRemoteClaimCompletionBackoffAndBudget(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 4, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	key := keyForTest(t, "remote")
	start := testTime()
	backoff := 10 * time.Second
	claim, claimed, ok, err := store.ClaimRemote(ctx, key, start, 2, 5*time.Second, backoff)
	if err != nil || !ok {
		t.Fatalf("ClaimRemote() = (ok=%t, err=%v), want claim", ok, err)
	}
	if claim.LeaseID == "" || len(claim.LeaseID) > featurestate.MaxRemoteLeaseIDBytes || claim.Attempt != 1 || claimed.RemoteAttempts != 1 {
		t.Fatalf("claim/control state is not bounded or attempt was not committed first: claim=%+v record=%+v", claim, claimed)
	}
	if claimed.RemoteLeaseID != claim.LeaseID || !claimed.RemoteLeaseUntil.Equal(start.Add(5*time.Second)) || !claimed.RemoteNextEligibleAt.IsZero() {
		t.Fatalf("initial claim record = %+v, want active lease and no failure backoff", claimed)
	}

	if _, _, ok, err := store.ClaimRemote(ctx, key, start.Add(time.Second), 2, 5*time.Second, backoff); err != nil || ok {
		t.Fatalf("concurrent active claim = (ok=%t, err=%v), want blocked", ok, err)
	}

	completedAt := start.Add(2 * time.Second)
	completed, err := store.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{}, completedAt)
	if err != nil {
		t.Fatalf("neutral completion: %v", err)
	}
	if completed.Classification != (session.Classification{}) || completed.RemoteAttempts != 1 || completed.RemoteLeaseID != "" || !completed.RemoteLeaseUntil.IsZero() {
		t.Fatalf("neutral completion wrote classification or lost attempt state: %+v", completed)
	}
	eligibleAt := completedAt.Add(backoff)
	if !completed.RemoteNextEligibleAt.Equal(eligibleAt) {
		t.Fatalf("retry eligibility = %v, want completion time plus backoff %v", completed.RemoteNextEligibleAt, eligibleAt)
	}
	if _, _, ok, err := store.ClaimRemote(ctx, key, eligibleAt.Add(-time.Nanosecond), 2, 5*time.Second, backoff); err != nil || ok {
		t.Fatalf("claim before exact backoff boundary = (ok=%t, err=%v), want blocked", ok, err)
	}
	secondClaim, secondRecord, ok, err := store.ClaimRemote(ctx, key, eligibleAt, 2, 5*time.Second, backoff)
	if err != nil || !ok || secondClaim.Attempt != 2 || secondRecord.RemoteAttempts != 2 || secondClaim.LeaseID == claim.LeaseID {
		t.Fatalf("claim at exact backoff boundary = (claim=%+v, record=%+v, ok=%t, err=%v), want attempt 2", secondClaim, secondRecord, ok, err)
	}

	if _, err := store.CompleteRemote(ctx, secondClaim, featurestate.RemoteCompletion{}, eligibleAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, afterBudget, ok, err := store.ClaimRemote(ctx, key, eligibleAt.Add(backoff+time.Second), 2, 5*time.Second, backoff)
	if err != nil || ok || afterBudget.RemoteAttempts != 2 {
		t.Fatalf("exhausted budget claim = (ok=%t record=%+v err=%v), want no new attempt", ok, afterBudget, err)
	}
}

func TestMemoryStoreAbandonedLeaseCanBeReclaimedAndOldCompletionIsStale(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Second}, nil)
	ctx := context.Background()
	key := keyForTest(t, "abandoned")
	start := testTime()
	first, _, ok, err := store.ClaimRemote(ctx, key, start, 2, time.Second, 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("first claim = (ok=%t, err=%v)", ok, err)
	}
	if _, _, ok, err := store.ClaimRemote(ctx, key, start.Add(time.Second-time.Nanosecond), 2, time.Second, 5*time.Second); err != nil || ok {
		t.Fatalf("claim before lease expiry = (ok=%t, err=%v), want blocked", ok, err)
	}
	second, secondRecord, ok, err := store.ClaimRemote(ctx, key, start.Add(time.Second), 2, time.Second, 5*time.Second)
	if err != nil || !ok || second.Attempt != 2 || secondRecord.RemoteAttempts != 2 {
		t.Fatalf("claim at lease expiry = (claim=%+v record=%+v ok=%t err=%v), want attempt 2", second, secondRecord, ok, err)
	}

	before := secondRecord
	if _, err := store.CompleteRemote(ctx, first, featurestate.RemoteCompletion{Proposal: remoteProposal("remote.stale", 1)}, start.Add(time.Second)); !errors.Is(err, featurestate.ErrStaleRemoteClaim) {
		t.Fatalf("stale completion error = %v, want stale claim", err)
	}
	after, found, err := store.Load(ctx, key)
	if err != nil || !found || after != before {
		t.Fatalf("stale completion mutated active record: before=%+v after=%+v found=%t err=%v", before, after, found, err)
	}
}

func TestMemoryStoreRejectsCompletionAtExactLeaseExpiry(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	key := keyForTest(t, "expiry-boundary")
	start := testTime()
	claim, claimed, ok, err := store.ClaimRemote(ctx, key, start, 2, time.Second, 0)
	if err != nil || !ok {
		t.Fatalf("ClaimRemote() = (ok=%t, err=%v)", ok, err)
	}

	got, err := store.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{Proposal: remoteProposal("remote.late", 3)}, start.Add(time.Second))
	if !errors.Is(err, featurestate.ErrStaleRemoteClaim) {
		t.Fatalf("completion at lease expiry error = %v, want stale claim", err)
	}
	if got != claimed {
		t.Fatalf("expired completion mutated record: got=%+v claimed=%+v", got, claimed)
	}
}

func TestMemoryStorePositiveLocalDecisionInvalidatesRemoteClaim(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	key := keyForTest(t, "concurrent-positive")
	claim, _, ok, err := store.ClaimRemote(ctx, key, testTime(), 3, time.Minute, 0)
	if err != nil || !ok {
		t.Fatalf("ClaimRemote() = (ok=%t, err=%v)", ok, err)
	}

	local := localProposal(session.SourceLocalIdentity, "client.codex", 5)
	winner, promoted, err := store.Promote(ctx, key, local, testTime().Add(time.Second))
	if err != nil || !promoted || winner.Classification.Revision != 1 {
		t.Fatalf("local promotion = (record=%+v promoted=%t err=%v)", winner, promoted, err)
	}
	if _, err := store.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{Proposal: remoteProposal("remote.other", 9)}, testTime().Add(2*time.Second)); !errors.Is(err, featurestate.ErrStaleRemoteClaim) {
		t.Fatalf("completion after local win error = %v, want stale claim", err)
	}
	after, found, err := store.Load(ctx, key)
	if err != nil || !found || after.Classification != winner.Classification || after.RemoteAttempts != 1 {
		t.Fatalf("remote completion replaced local winner or reset attempts: %+v found=%t err=%v", after, found, err)
	}
}

func TestMemoryStoreRemotePositiveCompletionUsesCanonicalRevisionOne(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	key := keyForTest(t, "remote-positive")
	claim, _, ok, err := store.ClaimRemote(ctx, key, testTime(), 1, time.Minute, 0)
	if err != nil || !ok {
		t.Fatalf("ClaimRemote() = (ok=%t, err=%v)", ok, err)
	}
	proposal := remoteProposal("remote.positive", 444)
	got, err := store.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{Proposal: proposal}, testTime().Add(time.Second))
	if err != nil {
		t.Fatalf("positive completion: %v", err)
	}
	if got.Classification.Revision != 1 || got.Classification.Evidence != proposal.Evidence || got.RemoteAttempts != 1 || got.RemoteLeaseID != "" {
		t.Fatalf("positive completion record = %+v, want revision 1 with retained attempt and cleared lease", got)
	}
	if _, _, ok, err := store.ClaimRemote(ctx, key, testTime().Add(2*time.Second), 1, time.Minute, 0); err != nil || ok {
		t.Fatalf("positive session allowed another remote claim: ok=%t err=%v", ok, err)
	}
}

func TestMemoryStoreKeepsPositiveAndSpentUnknownRecordsUnderIdleCapacityPressure(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Second}, nil)
	ctx := context.Background()
	start := testTime()
	positiveKey := keyForTest(t, "positive-pinned")
	positive, promoted, err := store.Promote(ctx, positiveKey, localProposal(session.SourceLocalIdentity, "client.pinned", 3), start)
	if err != nil || !promoted {
		t.Fatalf("positive promotion = (promoted=%t err=%v)", promoted, err)
	}

	unknownKey := keyForTest(t, "attempted-unknown-pinned")
	claim, _, ok, err := store.ClaimRemote(ctx, unknownKey, start, 2, time.Second, 0)
	if err != nil || !ok {
		t.Fatalf("unknown claim = (ok=%t err=%v)", ok, err)
	}
	if _, err := store.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{}, start); err != nil {
		t.Fatal(err)
	}

	late := start.Add(24 * time.Hour)
	newKey := keyForTest(t, "new-after-idle-pressure")
	if _, _, _, err := store.ClaimRemote(ctx, newKey, late, 2, time.Second, 0); !errors.Is(err, featurestate.ErrStoreCapacity) {
		t.Fatalf("new key under pressure error = %v, want fail-open capacity rejection", err)
	}
	gotPositive, found, err := store.Load(ctx, positiveKey)
	if err != nil || !found || gotPositive != positive {
		t.Fatalf("idle cleanup downgraded positive record: got=%+v found=%t err=%v", gotPositive, found, err)
	}
	gotUnknown, found, err := store.Load(ctx, unknownKey)
	if err != nil || !found || gotUnknown.RemoteAttempts != 1 || gotUnknown.Classification != (session.Classification{}) {
		t.Fatalf("idle cleanup erased used-attempt unknown record: got=%+v found=%t err=%v", gotUnknown, found, err)
	}
	secondClaim, secondRecord, ok, err := store.ClaimRemote(ctx, unknownKey, late, 2, time.Second, 0)
	if err != nil || !ok || secondClaim.Attempt != 2 || secondRecord.RemoteAttempts != 2 {
		t.Fatalf("retained remote budget = (claim=%+v record=%+v ok=%t err=%v), want second attempt", secondClaim, secondRecord, ok, err)
	}
}

func TestMemoryStoreLazilyCleansOnlyIdleUnknownZeroAttemptEntries(t *testing.T) {
	t.Parallel()

	cleanupTime := testTime().Add(time.Minute)
	store, err := NewMemoryStore(
		MemoryStoreConfig{MaxEntries: 1, IdleTTL: time.Minute},
		WithClock(func() time.Time { return cleanupTime }),
	)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	key := keyForTest(t, "unused-placeholder")
	store.mu.Lock()
	if err := store.insertRecordLocked(featurestate.Record{Key: key, UpdatedAt: testTime()}); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.mu.Unlock()

	if _, found, err := store.Load(context.Background(), key); err != nil || found {
		t.Fatalf("expired zero-attempt unknown = (found=%t err=%v), want lazily removed", found, err)
	}
	_, record, ok, err := store.ClaimRemote(context.Background(), keyForTest(t, "replacement"), testTime().Add(time.Minute), 1, time.Minute, 0)
	if err != nil || !ok || record.RemoteAttempts != 1 {
		t.Fatalf("new key after idle cleanup = (ok=%t record=%+v err=%v)", ok, record, err)
	}
}

func TestMemoryStoreRunsNonceGenerationOutsideSharedLock(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Hour}, func() (string, error) {
		close(entered)
		<-release
		return "blocked-nonce", nil
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	claimDone := make(chan error, 1)
	claimKey := keyForTest(t, "nonce-key")
	go func() {
		_, _, _, err := store.ClaimRemote(context.Background(), claimKey, testTime(), 1, time.Minute, 0)
		claimDone <- err
	}()
	<-entered

	loadDone := make(chan error, 1)
	unrelatedKey := keyForTest(t, "unrelated-key")
	go func() {
		_, _, err := store.Load(context.Background(), unrelatedKey)
		loadDone <- err
	}()
	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatalf("unrelated Load() while nonce is blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated Load() waited behind nonce generation")
	}

	releaseOnce.Do(func() { close(release) })
	if err := <-claimDone; err != nil {
		t.Fatalf("ClaimRemote() after nonce release: %v", err)
	}
}

func TestMemoryStoreRunsClockOutsideSharedLock(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	store, err := NewMemoryStore(
		MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Hour},
		WithClock(func() time.Time {
			close(entered)
			<-release
			return testTime()
		}),
	)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	loadDone := make(chan error, 1)
	loadKey := keyForTest(t, "clock-key")
	go func() {
		_, _, err := store.Load(context.Background(), loadKey)
		loadDone <- err
	}()
	<-entered

	promoteDone := make(chan error, 1)
	promoteKey := keyForTest(t, "other-key")
	go func() {
		_, _, err := store.Promote(context.Background(), promoteKey, localProposal(session.SourceLocalIdentity, "client.other", 1), testTime())
		promoteDone <- err
	}()
	select {
	case err := <-promoteDone:
		if err != nil {
			t.Fatalf("unrelated Promote() while clock is blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated Promote() waited behind the injected clock")
	}

	releaseOnce.Do(func() { close(release) })
	if err := <-loadDone; err != nil {
		t.Fatalf("Load() after clock release: %v", err)
	}
}

func TestMemoryStoreRejectsUnboundedStoreAndRemoteOptions(t *testing.T) {
	t.Parallel()

	invalidConfigs := []MemoryStoreConfig{
		{MaxEntries: -1},
		{MaxEntries: maxMemoryStoreEntries + 1},
		{IdleTTL: -time.Second},
		{IdleTTL: maxMemoryStoreIdleTTL + time.Nanosecond},
	}
	for _, config := range invalidConfigs {
		if _, err := NewMemoryStore(config); !errors.Is(err, featurestate.ErrInvalidStoreConfig) {
			t.Errorf("NewMemoryStore(%+v) error = %v, want invalid bounds", config, err)
		}
	}

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 2, IdleTTL: time.Hour}, nil)
	key := keyForTest(t, "bad-remote-bounds")
	invalid := []struct {
		maxAttempts uint32
		leaseTTL    time.Duration
		backoff     time.Duration
	}{
		{maxAttempts: 0, leaseTTL: time.Second},
		{maxAttempts: featurestate.MaxRemoteAttemptsPerSession + 1, leaseTTL: time.Second},
		{maxAttempts: 1, leaseTTL: 0},
		{maxAttempts: 1, leaseTTL: featurestate.MaxRemoteLeaseTTL + time.Nanosecond},
		{maxAttempts: 1, leaseTTL: time.Second, backoff: -time.Nanosecond},
		{maxAttempts: 1, leaseTTL: time.Second, backoff: featurestate.MaxRemoteRetryBackoff + time.Nanosecond},
	}
	for _, bounds := range invalid {
		if _, _, _, err := store.ClaimRemote(context.Background(), key, testTime(), bounds.maxAttempts, bounds.leaseTTL, bounds.backoff); !errors.Is(err, featurestate.ErrInvalidRemoteOptions) {
			t.Errorf("ClaimRemote(%+v) error = %v, want invalid bounds", bounds, err)
		}
	}
	if _, found, err := store.Load(context.Background(), key); err != nil || found {
		t.Fatalf("invalid remote bounds allocated state: found=%t err=%v", found, err)
	}
}

func TestMemoryStoreConcurrentPromotionHasOneWinner(t *testing.T) {
	t.Parallel()

	store := newTestStore(t, MemoryStoreConfig{MaxEntries: 4, IdleTTL: time.Hour}, nil)
	ctx := context.Background()
	key := keyForTest(t, "concurrent")
	const workers = 32
	var wg sync.WaitGroup
	var winners atomic.Int32
	errCh := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			proposal := localProposal(session.SourceLocalIdentity, session.EvidenceCode(fmt.Sprintf("client.evidence_%d", i)), uint64(i+1))
			got, promoted, err := store.Promote(ctx, key, proposal, testTime())
			if err != nil {
				errCh <- err
				return
			}
			if promoted {
				winners.Add(1)
			}
			if got.Classification.Revision != 1 || !got.Classification.IsCodingAgent() {
				errCh <- fmt.Errorf("concurrent record is not one valid revision-1 positive: %+v", got.Classification)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("concurrent promotions had %d winners, want exactly one", got)
	}
}

func newTestStore(t *testing.T, config MemoryStoreConfig, nonce func() (string, error)) *MemoryStore {
	t.Helper()
	options := []MemoryStoreOption(nil)
	if nonce != nil {
		options = append(options, WithLeaseNonceGenerator(nonce))
	}
	store, err := NewMemoryStore(config, options...)
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	return store
}

func keyForTest(t *testing.T, id string) featurestate.Key {
	t.Helper()
	key, err := featurestate.ResolveKey(session.SessionView{AuthoritativeSessionID: id})
	if err != nil {
		t.Fatalf("ResolveKey(%q): %v", id, err)
	}
	return key
}

func localProposal(source session.ClassificationSource, evidence session.EvidenceCode, revision uint64) session.Classification {
	return session.Classification{Kind: session.KindCodingAgent, Source: source, Confidence: session.ConfidenceHigh, Evidence: evidence, Revision: revision}
}

func remoteProposal(evidence session.EvidenceCode, revision uint64) session.Classification {
	return localProposal(session.SourceRemote, evidence, revision)
}

func testTime() time.Time {
	return time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
}
