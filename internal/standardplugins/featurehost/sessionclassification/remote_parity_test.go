package sessionclassification_test

import (
	"context"
	"sync"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteClaimOutcome struct {
	claim  featurestate.RemoteClaim
	record featurestate.Record
	ok     bool
	err    error
}

func TestSessionClassificationRemoteLeaseContract_Memory(t *testing.T) {
	t.Parallel()

	memoryStore, err := store.NewMemoryStore(store.MemoryStoreConfig{MaxEntries: 32, IdleTTL: time.Hour})
	require.NoError(t, err)
	runSessionClassificationRemoteLeaseContract(t, func() featurestate.Store { return memoryStore })
}

func runSessionClassificationRemoteLeaseContract(t *testing.T, newStore func() featurestate.Store) {
	t.Helper()

	t.Run("concurrent claimers have one winner across store instances", func(t *testing.T) {
		const workers = 24
		ctx := context.Background()
		key := parityKey("remote-concurrent-claim")
		startAt := parityTime().Add(123 * time.Nanosecond)
		leaseTTL := time.Minute + 321*time.Nanosecond
		start := make(chan struct{})
		var wg sync.WaitGroup
		outcomes := make(chan remoteClaimOutcome, workers)
		for range workers {
			candidate := newStore()
			wg.Add(1)
			go func(candidate featurestate.Store) {
				defer wg.Done()
				<-start
				claim, record, ok, err := candidate.ClaimRemote(ctx, key, startAt, 3, leaseTTL, 0)
				outcomes <- remoteClaimOutcome{claim: claim, record: record, ok: ok, err: err}
			}(candidate)
		}
		close(start)
		wg.Wait()
		close(outcomes)

		var winner remoteClaimOutcome
		all := make([]remoteClaimOutcome, 0, workers)
		winners := 0
		for got := range outcomes {
			require.NoError(t, got.err)
			all = append(all, got)
			if got.ok {
				winners++
				winner = got
			}
			assert.Equal(t, uint32(1), got.record.RemoteAttempts)
		}
		require.Equal(t, 1, winners, "one atomic claim must win for the authoritative key")
		assert.NotEmpty(t, winner.claim.LeaseID)
		assert.LessOrEqual(t, len(winner.claim.LeaseID), featurestate.MaxRemoteLeaseIDBytes)
		assert.Equal(t, winner.claim.LeaseID, winner.record.RemoteLeaseID)
		assert.True(t, winner.record.RemoteLeaseUntil.Equal(time.Date(2026, time.September, 30, 12, 1, 0, 1000, time.UTC)),
			"an unaligned lease deadline must round upward to the shared microsecond boundary")
		for _, got := range all {
			assert.Equal(t, winner.record, got.record, "every concurrent caller must observe the winning lease")
		}
	})

	t.Run("abandoned lease expiry finite budget and stale token", func(t *testing.T) {
		ctx := context.Background()
		key := parityKey("remote-abandoned-lease")
		startAt := parityTime().Add(123 * time.Nanosecond)
		leaseTTL := 2*time.Second + 321*time.Nanosecond
		firstStore := newStore()
		first, firstRecord, ok, err := firstStore.ClaimRemote(ctx, key, startAt, 2, leaseTTL, 0)
		require.NoError(t, err)
		require.True(t, ok)
		leaseDeadline := ceilTestDeadline(startAt.Add(leaseTTL))
		require.True(t, leaseDeadline.Equal(time.Date(2026, time.September, 30, 12, 0, 2, 1000, time.UTC)))
		require.True(t, firstRecord.RemoteLeaseUntil.Equal(leaseDeadline))

		secondStore := newStore()
		if _, _, ok, err := secondStore.ClaimRemote(ctx, key, leaseDeadline.Add(-time.Nanosecond), 2, leaseTTL, 0); err != nil || ok {
			t.Fatalf("claim one nanosecond before the rounded lease deadline = (ok=%t, err=%v), want blocked", ok, err)
		}
		second, secondRecord, ok, err := secondStore.ClaimRemote(ctx, key, leaseDeadline, 2, leaseTTL, 0)
		require.NoError(t, err)
		require.True(t, ok, "the exact rounded lease deadline must be reclaimable")
		require.Equal(t, uint32(2), second.Attempt)
		require.Equal(t, uint32(2), secondRecord.RemoteAttempts)
		require.NotEqual(t, first.LeaseID, second.LeaseID,
			"independent BunStore instances must produce distinct opaque lease IDs")

		staleRecord, err := firstStore.CompleteRemote(ctx, first, featurestate.RemoteCompletion{Proposal: remoteContractRemoteProposal("remote.stale")}, leaseDeadline)
		require.ErrorIs(t, err, featurestate.ErrStaleRemoteClaim)
		require.Equal(t, secondRecord, staleRecord, "stale completion must return the current lease record")

		secondDeadline := secondRecord.RemoteLeaseUntil
		if _, err := secondStore.CompleteRemote(ctx, second, featurestate.RemoteCompletion{}, secondDeadline); !assert.ErrorIs(t, err, featurestate.ErrStaleRemoteClaim) {
			t.Fatalf("completion at exact lease expiry = %v, want stale claim", err)
		}
		_, afterBudget, ok, err := firstStore.ClaimRemote(ctx, key, secondDeadline, 2, leaseTTL, 0)
		require.NoError(t, err)
		require.False(t, ok, "an expired lease cannot exceed the finite attempt budget")
		require.Equal(t, uint32(2), afterBudget.RemoteAttempts)

		completionKey := parityKey("remote-completion-before-expiry")
		completionStore := newStore()
		completionClaim, completionLease, ok, err := completionStore.ClaimRemote(ctx, completionKey, startAt, 1, leaseTTL, 0)
		require.NoError(t, err)
		require.True(t, ok)
		completedBeforeExpiry, err := completionStore.CompleteRemote(ctx, completionClaim, featurestate.RemoteCompletion{}, completionLease.RemoteLeaseUntil.Add(-time.Nanosecond))
		require.NoError(t, err, "completion one nanosecond before the rounded lease deadline is valid")
		require.Empty(t, completedBeforeExpiry.RemoteLeaseID)
	})

	t.Run("completion anchors rounded backoff", func(t *testing.T) {
		ctx := context.Background()
		key := parityKey("remote-completion-backoff")
		startAt := parityTime().Add(123 * time.Nanosecond)
		claim, _, ok, err := newStore().ClaimRemote(ctx, key, startAt, 2, 10*time.Second, time.Second+321*time.Nanosecond)
		require.NoError(t, err)
		require.True(t, ok)

		completedAt := startAt.Add(2*time.Second + 456*time.Nanosecond)
		completed, err := newStore().CompleteRemote(ctx, claim, featurestate.RemoteCompletion{}, completedAt)
		require.NoError(t, err)
		expectedBackoff := ceilTestDeadline(completedAt.Add(claim.RetryBackoff))
		require.True(t, expectedBackoff.Equal(time.Date(2026, time.September, 30, 12, 0, 3, 1000, time.UTC)))
		require.True(t, completed.RemoteNextEligibleAt.Equal(expectedBackoff),
			"backoff must start at completion and use the shared rounded deadline")
		require.Empty(t, completed.Classification.Kind, "neutral completion must not persist a negative class")

		if _, _, ok, err := newStore().ClaimRemote(ctx, key, expectedBackoff.Add(-time.Nanosecond), 2, 10*time.Second, 0); err != nil || ok {
			t.Fatalf("claim one nanosecond before backoff deadline = (ok=%t, err=%v), want blocked", ok, err)
		}
		second, record, ok, err := newStore().ClaimRemote(ctx, key, expectedBackoff, 2, 10*time.Second, 0)
		require.NoError(t, err)
		require.True(t, ok, "the exact rounded backoff deadline must be eligible")
		require.Equal(t, uint32(2), second.Attempt)
		require.True(t, record.UpdatedAt.Equal(expectedBackoff))
	})

	t.Run("local promotion racing remote completion stays monotonic", func(t *testing.T) {
		ctx := context.Background()
		key := parityKey("remote-local-promotion-race")
		startAt := parityTime()
		claim, _, ok, err := newStore().ClaimRemote(ctx, key, startAt, 2, time.Minute, 0)
		require.NoError(t, err)
		require.True(t, ok)

		localStore := newStore()
		remoteStore := newStore()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var localRecord featurestate.Record
		var localPromoted bool
		var localErr error
		var remoteRecord featurestate.Record
		var remoteErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			localRecord, localPromoted, localErr = localStore.Promote(ctx, key, remoteContractLocalProposal(), startAt.Add(time.Second))
		}()
		go func() {
			defer wg.Done()
			<-start
			remoteRecord, remoteErr = remoteStore.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{Proposal: remoteContractRemoteProposal("remote.accepted")}, startAt.Add(time.Second))
		}()
		close(start)
		wg.Wait()
		require.NoError(t, localErr)
		if remoteErr != nil {
			require.ErrorIs(t, remoteErr, featurestate.ErrStaleRemoteClaim)
		}

		current, found, err := newStore().Load(ctx, key)
		require.NoError(t, err)
		require.True(t, found)
		require.True(t, current.Classification.IsCodingAgent())
		require.Equal(t, uint64(1), current.Classification.Revision)
		require.Empty(t, current.RemoteLeaseID)
		require.True(t, current.RemoteLeaseUntil.IsZero())
		require.True(t, current.RemoteNextEligibleAt.IsZero())
		if remoteErr == nil {
			require.Equal(t, remoteRecord, current)
			require.False(t, localPromoted, "the first positive transition must win")
			require.Equal(t, remoteContractRemoteProposal("remote.accepted"), current.Classification)
		} else {
			require.True(t, localPromoted)
			require.Equal(t, localRecord, current)
			require.Equal(t, remoteContractLocalProposal(), current.Classification)
			require.Equal(t, featurestate.ErrStaleRemoteClaim, remoteErr)
		}
	})

	t.Run("positive record cannot be claimed again", func(t *testing.T) {
		ctx := context.Background()
		key := parityKey("remote-after-positive")
		positive, promoted, err := newStore().Promote(ctx, key, remoteContractLocalProposal(), parityTime())
		require.NoError(t, err)
		require.True(t, promoted)
		_, current, ok, err := newStore().ClaimRemote(ctx, key, parityTime().Add(time.Second), 2, time.Minute, 0)
		require.NoError(t, err)
		require.False(t, ok)
		require.Equal(t, positive, current)
	})

	t.Run("invalid inputs do not consume attempts or mutate rows", func(t *testing.T) {
		ctx := context.Background()
		key := parityKey("remote-invalid-inputs")
		remoteStore := newStore()
		startAt := parityTime()
		var nilCtx context.Context
		invalidOptions := []struct {
			maxAttempts uint32
			leaseTTL    time.Duration
			backoff     time.Duration
		}{
			{maxAttempts: 0, leaseTTL: time.Second},
			{maxAttempts: 1, leaseTTL: featurestate.MaxRemoteLeaseTTL + time.Nanosecond},
			{maxAttempts: 1, leaseTTL: time.Second, backoff: -time.Nanosecond},
		}
		for _, bounds := range invalidOptions {
			if _, _, _, err := remoteStore.ClaimRemote(ctx, key, startAt, bounds.maxAttempts, bounds.leaseTTL, bounds.backoff); !assert.ErrorIs(t, err, featurestate.ErrInvalidRemoteOptions) {
				t.Fatalf("invalid remote bounds %+v error = %v, want invalid options", bounds, err)
			}
		}
		if _, found, err := remoteStore.Load(ctx, key); err != nil || found {
			t.Fatalf("invalid bounds allocated a row: found=%t err=%v", found, err)
		}

		claim, before, ok, err := remoteStore.ClaimRemote(ctx, key, startAt, 2, time.Minute, 0)
		require.NoError(t, err)
		require.True(t, ok)

		badToken := claim
		badToken.LeaseID += " invalid"
		if _, err := remoteStore.CompleteRemote(ctx, badToken, featurestate.RemoteCompletion{}, startAt.Add(time.Second)); !assert.ErrorIs(t, err, featurestate.ErrInvalidRemoteClaim) {
			t.Fatalf("invalid lease token error = %v, want invalid claim", err)
		}
		badBackoff := claim
		badBackoff.RetryBackoff = featurestate.MaxRemoteRetryBackoff + time.Nanosecond
		if _, err := remoteStore.CompleteRemote(ctx, badBackoff, featurestate.RemoteCompletion{}, startAt.Add(time.Second)); !assert.ErrorIs(t, err, featurestate.ErrInvalidRemoteClaim) {
			t.Fatalf("invalid retry backoff error = %v, want invalid claim", err)
		}
		if _, err := remoteStore.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{Proposal: remoteContractLocalProposal()}, startAt.Add(time.Second)); !assert.ErrorIs(t, err, featurestate.ErrInvalidProposal) {
			t.Fatalf("non-remote proposal error = %v, want invalid proposal", err)
		}
		if _, _, _, err := remoteStore.ClaimRemote(nilCtx, key, startAt, 2, time.Minute, 0); !assert.ErrorIs(t, err, featurestate.ErrInvalidStoreContext) {
			t.Fatalf("nil claim context error = %v, want invalid context", err)
		}
		if _, err := remoteStore.CompleteRemote(nilCtx, claim, featurestate.RemoteCompletion{}, startAt.Add(time.Second)); !assert.ErrorIs(t, err, featurestate.ErrInvalidStoreContext) {
			t.Fatalf("nil completion context error = %v, want invalid context", err)
		}

		after, found, err := remoteStore.Load(ctx, key)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, before, after, "validation errors must leave attempt and lease state unchanged")
	})
}

func ceilTestDeadline(deadline time.Time) time.Time {
	truncated := deadline.Truncate(time.Microsecond)
	if !truncated.Equal(deadline) {
		return truncated.Add(time.Microsecond)
	}
	return truncated
}

func remoteContractLocalProposal() session.Classification {
	return session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh, Evidence: "client.codex", Revision: 1,
	}
}

func remoteContractRemoteProposal(evidence session.EvidenceCode) session.Classification {
	return session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceRemote,
		Confidence: session.ConfidenceHigh, Evidence: evidence, Revision: 1,
	}
}
