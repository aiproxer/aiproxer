package sessionclassification_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task 9.2 concurrent-convergence and monotonicity certification.
//
// The revision semantics pinned here were read from the implementation rather than
// assumed:
//
//   - featurestate.Store.Promote rewrites proposal.Revision to 1 unconditionally in
//     both memory.go and bun.go, and only the FIRST accepted proposal for a key
//     becomes the stored positive ("first-positive-wins").
//   - BunStore's promoteSQL guards its upsert with `WHERE
//     session_classification.kind = ''`, and completeRemotePositiveSQL guards the
//     remote completion with the same `AND kind = ''`.
//   - MemoryStore's Promote returns the current record unchanged when
//     current.Classification.IsCodingAgent(), and its CompleteRemote only writes a
//     proposal when the row is not already positive.
//
// So classification_revision is NOT a counter that advances once per accepted
// promotion. It is the constant 1 the store assigns at the single accepted
// first-positive transition, and it never changes afterwards. The tests below pin
// exactly that, plus the absence of any source rewrite, evidence rewrite, or
// negative row.

// convergenceRemoteConfig is the valid remote posture the certified hybrid turns
// run under. One attempt and no backoff keep each race to a single lease.
func convergenceRemoteConfig() *featurestate.RemoteConfig {
	return &featurestate.RemoteConfig{
		Provider:              "jev",
		APIKeyEnv:             "TYPESAFE_API_KEY",
		Timeout:               750 * time.Millisecond,
		MaxAttemptsPerSession: 1,
		LeaseTTL:              2 * time.Second,
		RetryBackoff:          0,
		PositiveThreshold:     0.90,
	}
}

// convergenceDecider answers above threshold without any network access. The
// classifier, not this fake, owns every threshold and promotion decision.
type convergenceDecider struct {
	calls atomic.Int64
}

func (d *convergenceDecider) Decide(context.Context, featurestate.RemoteInput) (featurestate.RemoteDecision, error) {
	d.calls.Add(1)
	return featurestate.RemoteDecision{CodingProbability: 0.99}, nil
}

// convergenceAuthority is the process-owned state authority the classifier resolves.
// It hands out the very coordinator under test, so the certified chain is
// Classifier -> Coordinator -> real store with no seam in between.
type convergenceAuthority struct {
	state featurestate.Store
}

func (a convergenceAuthority) ClassificationState() (featurestate.Store, error) {
	return a.state, nil
}

// convergenceObservation is one record the underlying store returned. Capturing
// every one of them is what lets the test ask "did any caller ever see two
// different positives?" instead of only inspecting the final state.
type convergenceObservation struct {
	key    featurestate.Key
	record featurestate.Record
}

// convergenceBarrierStore wraps a real store and holds Promote and CompleteRemote at
// a barrier until the test releases them.
//
// The barrier is the instrument that makes the race deterministic: both writes are
// proven outstanding at the same time, so the winner is decided by the store's own
// monotonicity guards rather than by goroutine scheduling luck. Everything below the
// barrier is the production store.
type convergenceBarrierStore struct {
	inner featurestate.Store

	promoteEntered  chan struct{}
	completeEntered chan struct{}
	release         chan struct{}

	promoteCalls  atomic.Int64
	completeCalls atomic.Int64

	mu           sync.Mutex
	observations []convergenceObservation
}

func newConvergenceBarrierStore(inner featurestate.Store, barrier bool) *convergenceBarrierStore {
	wrapped := &convergenceBarrierStore{inner: inner}
	if barrier {
		wrapped.promoteEntered = make(chan struct{})
		wrapped.completeEntered = make(chan struct{})
		wrapped.release = make(chan struct{})
	}
	return wrapped
}

func (s *convergenceBarrierStore) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	record, found, err := s.inner.Load(ctx, key)
	s.observe(key, record)
	return record, found, err
}

func (s *convergenceBarrierStore) Promote(
	ctx context.Context,
	key featurestate.Key,
	proposal session.Classification,
	now time.Time,
) (featurestate.Record, bool, error) {
	s.promoteCalls.Add(1)
	s.awaitRelease(ctx, s.promoteEntered)
	record, promoted, err := s.inner.Promote(ctx, key, proposal, now)
	s.observe(key, record)
	return record, promoted, err
}

func (s *convergenceBarrierStore) ClaimRemote(
	ctx context.Context,
	key featurestate.Key,
	now time.Time,
	maxAttempts uint32,
	leaseTTL, retryBackoff time.Duration,
) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	claim, record, ok, err := s.inner.ClaimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
	s.observe(key, record)
	return claim, record, ok, err
}

func (s *convergenceBarrierStore) CompleteRemote(
	ctx context.Context,
	claim featurestate.RemoteClaim,
	result featurestate.RemoteCompletion,
	now time.Time,
) (featurestate.Record, error) {
	s.completeCalls.Add(1)
	s.awaitRelease(ctx, s.completeEntered)
	record, err := s.inner.CompleteRemote(ctx, claim, result, now)
	s.observe(claim.Key, record)
	return record, err
}

func (s *convergenceBarrierStore) awaitRelease(ctx context.Context, entered chan struct{}) {
	if entered == nil {
		return
	}
	select {
	case <-entered:
	default:
		close(entered)
	}
	select {
	case <-s.release:
	case <-ctx.Done():
	}
}

func (s *convergenceBarrierStore) observe(key featurestate.Key, record featurestate.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = append(s.observations, convergenceObservation{key: key, record: record})
}

// distinctPositives returns every distinct coding_agent snapshot any caller ever
// observed for key. A weakened monotonicity guard shows up here as two or more
// entries, before the final-state assertion could ever notice.
func (s *convergenceBarrierStore) distinctPositives(key featurestate.Key) map[session.Classification]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[session.Classification]int{}
	for _, observation := range s.observations {
		if observation.key == key && observation.record.Classification.IsCodingAgent() {
			out[observation.record.Classification]++
		}
	}
	return out
}

// observationsOutsideKey counts positive records that escaped their authority key,
// which would mean a store or coordinator leaked state across authorities.
func (s *convergenceBarrierStore) observationsOutsideKey(key featurestate.Key) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, observation := range s.observations {
		if observation.key != key && observation.record.Classification.IsCodingAgent() {
			count++
		}
	}
	return count
}

func (s *convergenceBarrierStore) waitPromoteEntered(t *testing.T) {
	t.Helper()
	if s.promoteEntered == nil {
		return
	}
	waitBarrierSignal(t, s.promoteEntered, "the local promotion to reach the store barrier")
}

func (s *convergenceBarrierStore) waitCompleteEntered(t *testing.T) {
	t.Helper()
	if s.completeEntered == nil {
		return
	}
	waitBarrierSignal(t, s.completeEntered, "the remote completion to reach the store barrier")
}

func (s *convergenceBarrierStore) openBarrier() {
	if s.release == nil {
		return
	}
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

func waitBarrierSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// convergenceDecisiveLocalInput is a turn the hybrid generation promotes from
// decisive local evidence alone, so it never reaches the remote phase.
func convergenceDecisiveLocalInput(key featurestate.Key) sdkclassification.Input {
	return convergenceInput(key, "codex_cli_rs/1.2.3")
}

// convergenceAmbiguousInput is a turn no local rule accepts, so the hybrid
// generation must reach the remote phase and can promote from the remote answer.
func convergenceAmbiguousInput(key featurestate.Key) sdkclassification.Input {
	return convergenceInput(key, "openai-python/1.40.0")
}

func convergenceInput(key featurestate.Key, userAgent string) sdkclassification.Input {
	return sdkclassification.Input{
		Session: session.SessionView{
			AuthoritativeSessionID: key.ID,
			ALegID:                 key.ID + "/a-leg",
			// Both racing turns carry the SAME client-supplied hint. Neither may use
			// it, so they share key because of proxy-owned authority alone.
			ClientSessionHint: "convergence-shared-client-hint",
		},
		Evidence: sdkclassification.Evidence{
			ClientUserAgent: userAgent,
			Operation:       aipapi.OperationOpenAIResponses,
		},
	}
}

func newConvergenceChain(t *testing.T, durable featurestate.Store) (*featurestate.Classifier, *convergenceBarrierStore) {
	t.Helper()
	coordinator, err := store.NewCoordinator(durable, store.CoordinatorConfig{
		CacheCapacity: 64,
		MaxInflight:   64,
		IdleTTL:       time.Hour,
	})
	require.NoError(t, err)
	barriered := newConvergenceBarrierStore(coordinator, true)
	classifier, err := featurestate.NewClassifier(featurestate.Config{
		Mode:   featurestate.ModeHybrid,
		Remote: convergenceRemoteConfig(),
	}, featurestate.ClassifierDeps{
		State:  convergenceAuthority{state: barriered},
		Remote: &convergenceDecider{},
	})
	require.NoError(t, err)
	return classifier, barriered
}

type convergenceOutcome struct {
	classification session.Classification
	err            error
}

// raceLocalAndRemotePositives drives one decisive local turn and one ambiguous
// remote turn against the same authority and releases both store writes together.
func raceLocalAndRemotePositives(
	t *testing.T,
	classifier *featurestate.Classifier,
	barriered *convergenceBarrierStore,
	key featurestate.Key,
) (local, remote convergenceOutcome) {
	t.Helper()
	ctx := context.Background()

	remoteDone := make(chan convergenceOutcome, 1)
	go func() {
		got, err := classifier.Classify(ctx, convergenceAmbiguousInput(key))
		remoteDone <- convergenceOutcome{classification: got, err: err}
	}()
	barriered.waitCompleteEntered(t)

	localDone := make(chan convergenceOutcome, 1)
	go func() {
		got, err := classifier.Classify(ctx, convergenceDecisiveLocalInput(key))
		localDone <- convergenceOutcome{classification: got, err: err}
	}()
	barriered.waitPromoteEntered(t)

	barriered.openBarrier()

	local = <-localDone
	remote = <-remoteDone
	return local, remote
}

// assertConvergedPositive is the shared verdict for every concurrent promotion shape:
// exactly one stored positive at revision 1, with no caller ever having observed a
// second source or evidence value.
func assertConvergedPositive(
	t *testing.T,
	barriered *convergenceBarrierStore,
	key featurestate.Key,
	reader featurestate.Store,
	local, remote convergenceOutcome,
) session.Classification {
	t.Helper()
	require.NoError(t, local.err, "the local turn failed")
	require.NoError(t, remote.err, "the remote turn failed")
	assert.Equal(t, int64(1), barriered.promoteCalls.Load(),
		"the racing local turn must issue exactly one durable promotion")
	assert.Equal(t, int64(1), barriered.completeCalls.Load(),
		"the racing remote turn must issue exactly one durable completion")

	positives := barriered.distinctPositives(key)
	require.Len(t, positives, 1,
		"concurrent local and remote positives must converge on ONE stored source/evidence snapshot, observed %+v", positives)
	var converged session.Classification
	for classification := range positives {
		converged = classification
	}
	assert.Equal(t, session.KindCodingAgent, converged.Kind)
	assert.Equal(t, session.ConfidenceHigh, converged.Confidence)
	assert.Equal(t, uint64(1), converged.Revision,
		"the store assigns revision 1 at the single accepted transition and never advances it")
	require.NotEmpty(t, converged.Evidence)
	require.NotEmpty(t, string(converged.Source))

	// Both racing callers must project that same snapshot. Requirement 1.4 makes an
	// established positive immutable for the whole logical session, so the loser
	// observes the winner rather than reporting unknown or its own losing proposal.
	assert.Equal(t, converged, local.classification, "the local turn observed a different classification than the converged one")
	assert.Equal(t, converged, remote.classification, "the remote turn observed a different classification than the converged one")

	stored, found, err := reader.Load(context.Background(), key)
	require.NoError(t, err)
	require.True(t, found, "the racing turns left no durable row")
	assert.Equal(t, converged, stored.Classification)
	assert.Equal(t, uint64(1), stored.Classification.Revision)
	assert.Empty(t, stored.RemoteLeaseID, "an accepted transition must leave no dangling remote lease")
	assert.True(t, stored.RemoteLeaseUntil.IsZero())
	assert.True(t, stored.RemoteNextEligibleAt.IsZero())
	assert.Zero(t, barriered.observationsOutsideKey(key), "a positive escaped its authority key")
	return converged
}

// convergenceStores is the store the race runs through plus an independent reader
// over the same state.
//
// The reader matters: the verdict must come from committed state rather than from
// the instance the race mutated. The durable topology opens a second store value
// over the same database, so it observes only what the race actually committed. The
// in-process store has no second view of its own state, so it reads through the same
// instance; that difference is deliberate and is why the census of every record the
// store returned, not just the final Load, is the primary evidence.
type convergenceStores struct {
	store  featurestate.Store
	reader featurestate.Store
}

// convergenceTopology names one store topology and builds one isolated store pair per
// subtest, so parallel subtests never share keys or rows.
type convergenceTopology struct {
	name  string
	setup func(t *testing.T) convergenceStores
}

func convergenceStoreTopologies(t *testing.T) []convergenceTopology {
	t.Helper()
	topologies := []convergenceTopology{{
		name: "memory",
		setup: func(t *testing.T) convergenceStores {
			t.Helper()
			memory, err := store.NewMemoryStore(store.MemoryStoreConfig{MaxEntries: 64, IdleTTL: time.Hour})
			require.NoError(t, err)
			return convergenceStores{store: memory, reader: memory}
		},
	}}

	// One database shared by every durable subtest, but each subtest gets its own
	// store values and its own key prefix, so nothing collides.
	_, database := openSQLiteBunDBWithConnections(t, sqliteTestDSN(t, "classification-convergence.db"), 8)
	seed, err := store.NewBunStore(database)
	require.NoError(t, err)
	require.NoError(t, seed.EnsureSchema(context.Background()))
	topologies = append(topologies, convergenceTopology{
		name: "sqlite",
		setup: func(t *testing.T) convergenceStores {
			t.Helper()
			// BunStore holds no connection or session state, so a fresh value over the
			// same database arbitrates in the database and the shared connection pool,
			// not in a process-local mutex.
			underRace, err := store.NewBunStore(database)
			require.NoError(t, err)
			reader, err := store.NewBunStore(database)
			require.NoError(t, err)
			return convergenceStores{store: underRace, reader: reader}
		},
	})
	return topologies
}

// TestConcurrentLocalAndRemotePromotionsConvergeOnOneRevision is the requirement
// 2.5/2.6/12.5 headline: concurrent turns that would each promote, one from
// decisive local evidence and one from an above-threshold remote decision, converge
// on exactly one stored coding_agent revision with no source or evidence flapping.
func TestConcurrentLocalAndRemotePromotionsConvergeOnOneRevision(t *testing.T) {
	t.Parallel()

	for _, topology := range convergenceStoreTopologies(t) {
		t.Run(topology.name, func(t *testing.T) {
			t.Parallel()
			key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "convergence/" + topology.name}
			stores := topology.setup(t)
			classifier, barriered := newConvergenceChain(t, stores.store)
			local, remote := raceLocalAndRemotePositives(t, classifier, barriered, key)
			converged := assertConvergedPositive(t, barriered, key, stores.reader, local, remote)
			assert.True(t,
				converged.Source == session.SourceLocalIdentity || converged.Source == session.SourceRemote,
				"the winner must be one of the two racing sources, got %q", converged.Source)
			assert.Equal(t, session.KindCodingAgent, local.classification.Kind)
			assert.Equal(t, session.KindCodingAgent, remote.classification.Kind)
		})
	}
}

// TestConcurrentPromotionsNeverRewriteAnEstablishedPositive covers requirement 1.4's
// concurrent half on a row that already holds a positive.
//
// Three kinds of racing write target the established positive: replayed local
// promotions carrying DIFFERENT evidence, fresh remote claims, and remote completions
// holding a lease taken BEFORE the positive existed. None may rewrite the source,
// the evidence, the confidence band, the revision, or the row's timestamp.
func TestConcurrentPromotionsNeverRewriteAnEstablishedPositive(t *testing.T) {
	t.Parallel()

	for _, topology := range convergenceStoreTopologies(t) {
		t.Run(topology.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "established/" + topology.name}
			stores := topology.setup(t)
			durable := stores.store

			// A lease claimed while the row is still unknown, then a promotion that
			// must clear it. The resulting claim token is the strongest downgrade
			// instrument available: it is a genuine remote lease whose completion
			// carries a DIFFERENT, positive, remote-sourced proposal.
			orphanClaim, _, granted, err := durable.ClaimRemote(ctx, key, testStoreTime(),
				featurestate.MaxRemoteAttemptsPerSession, time.Minute, 0)
			require.NoError(t, err)
			require.True(t, granted)

			established, promoted, err := durable.Promote(ctx, key, convergenceLocalProposal(), testStoreTime().Add(time.Second))
			require.NoError(t, err)
			require.True(t, promoted)
			require.Equal(t, uint64(1), established.Classification.Revision)
			require.Empty(t, established.RemoteLeaseID, "an accepted promotion must clear the pre-existing lease")

			coordinator, err := store.NewCoordinator(durable, store.CoordinatorConfig{
				CacheCapacity: 64, MaxInflight: 64, IdleTTL: time.Hour,
			})
			require.NoError(t, err)

			const replays = 8
			const staleCompletions = 4
			const freshClaims = 4
			start := make(chan struct{})
			var wg sync.WaitGroup
			var mu sync.Mutex
			distinct := map[session.Classification]int{}
			acceptedStale := 0
			grantedClaims := 0

			record := func(classification session.Classification) {
				mu.Lock()
				defer mu.Unlock()
				distinct[classification]++
			}

			for i := range replays {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					<-start
					proposal := convergenceLocalProposal()
					proposal.Source = session.SourceLocalTooling
					proposal.Evidence = session.EvidenceCode(fmt.Sprintf("replay.tooling_%d", index))
					got, promotedNow, err := coordinator.Promote(ctx, key, proposal, testStoreTime().Add(2*time.Second))
					if err != nil {
						t.Errorf("replayed promotion: %v", err)
						return
					}
					if promotedNow {
						t.Error("a replayed positive was accepted as a second transition")
					}
					record(got.Classification)
				}(i)
			}
			for range staleCompletions {
				wg.Go(func() {
					<-start
					_, err := coordinator.CompleteRemote(ctx, orphanClaim, featurestate.RemoteCompletion{
						Proposal: convergenceRemoteProposal("remote.stale_replay"),
					}, testStoreTime().Add(2*time.Second))
					mu.Lock()
					if err == nil {
						acceptedStale++
					}
					mu.Unlock()
				})
			}
			for i := range freshClaims {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					<-start
					_, got, ok, err := coordinator.ClaimRemote(ctx, key,
						testStoreTime().Add(time.Duration(index)*time.Hour), featurestate.MaxRemoteAttemptsPerSession, time.Minute, 0)
					if err != nil {
						t.Errorf("remote claim: %v", err)
						return
					}
					mu.Lock()
					if ok {
						grantedClaims++
					}
					mu.Unlock()
					record(got.Classification)
				}(i)
			}
			close(start)
			wg.Wait()

			mu.Lock()
			distinctCount := len(distinct)
			for classification := range distinct {
				assert.Equal(t, established.Classification, classification,
					"a concurrent write rewrote the established classification")
			}
			acceptedCompletions := acceptedStale
			grantedNew := grantedClaims
			mu.Unlock()
			assert.Zero(t, acceptedCompletions,
				"a remote completion holding a pre-positive lease must not be accepted")
			assert.Zero(t, grantedNew,
				"a positive row must refuse a fresh remote claim, so no remote write can target it")
			assert.Equal(t, 1, distinctCount,
				"concurrent replays produced %d distinct classifications, want one", distinctCount)

			stored, found, err := stores.reader.Load(ctx, key)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, established, stored,
				"a concurrent replay or stale remote completion must not rewrite the durable row")
			assert.Equal(t, uint64(1), stored.Classification.Revision,
				"a replay must not advance the classification revision")
		})
	}
}

// TestConcurrentBelowThresholdCompletionsNeverDowngradeOrPersistNegative covers
// requirement 1.4 and 1.5's concurrent half using the only instrument that can write
// to an unknown row: a below-threshold remote completion.
//
// The race runs in two phases. Phase one issues the below-threshold completions
// alone against a genuinely unknown row, proving the negative instrument is live:
// the completions are accepted, and they persist NO classification. Phase two races
// the same live tokens against concurrent positives, proving the mixed outcome
// converges on one positive at revision 1 with no negative state and no
// re-sourcing.
func TestConcurrentBelowThresholdCompletionsNeverDowngradeOrPersistNegative(t *testing.T) {
	t.Parallel()

	for _, topology := range convergenceStoreTopologies(t) {
		t.Run(topology.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "below-threshold/" + topology.name}
			stores := topology.setup(t)
			durable := stores.store

			// Take several leases sequentially, each within the store's finite attempt
			// budget, so a below-threshold completion has a live token to write with
			// rather than being rejected on staleness alone.
			const leaseCount = featurestate.MaxRemoteAttemptsPerSession
			claims := make([]featurestate.RemoteClaim, 0, leaseCount)
			for i := range leaseCount {
				at := testStoreTime().Add(time.Duration(i) * time.Minute)
				claim, _, ok, err := durable.ClaimRemote(ctx, key, at, leaseCount, 30*time.Second, 0)
				require.NoError(t, err)
				require.True(t, ok, "attempt %d was refused", i)
				claims = append(claims, claim)
			}

			coordinator, err := store.NewCoordinator(durable, store.CoordinatorConfig{
				CacheCapacity: 64, MaxInflight: 64, IdleTTL: time.Hour,
			})
			require.NoError(t, err)

			// Phase one proves the negative instrument is genuinely live. With no
			// positive racing it, the completions below are accepted against a
			// genuinely unknown row, so the later assertion that a mixed race produces
			// no negative state is a real measurement and not an artefact of every
			// negative being rejected on staleness.
			negativeResult := raceBelowThresholdCompletions(ctx, t, coordinator, claims)
			require.Positive(t, negativeResult.accepted,
				"the below-threshold completions must have been genuinely accepted, or the race proved nothing")
			assert.Zero(t, negativeResult.nonZeroNonPositive,
				"a below-threshold remote completion persisted a negative classification state")

			unknown, found, err := stores.reader.Load(ctx, key)
			require.NoError(t, err)
			require.True(t, found, "an accepted negative completion must still leave its lease row behind")
			assert.Equal(t, session.Classification{}, unknown.Classification,
				"an accepted below-threshold completion must leave the session unknown, not negatively classified")
			assert.Equal(t, uint64(0), unknown.Classification.Revision)

			// Phase two is the mixed race. It MUST run against a fresh key holding its
			// own live lease: phase one consumed every token in `claims` AND exhausted
			// the per-session attempt budget, so replaying those tokens deterministically
			// rejected all of them on staleness and the race measured nothing. A separate
			// key gives the negative a genuinely accepted window to lose the race in.
			mixedKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "mixed/" + topology.name}
			claimAt0 := testStoreTime()
			mixedClaim, _, granted, err := durable.ClaimRemote(ctx, mixedKey, claimAt0, 1, 30*time.Second, 0)
			require.NoError(t, err)
			require.True(t, granted, "the mixed-race key must be granted a live lease")

			mixedResult := raceMixedPositivesAndNegatives(ctx, t, coordinator, mixedKey, mixedClaim)
			// Without this the assertions below would pass on an all-rejected race,
			// which is exactly what phase one accidentally measured.
			require.Positive(t, mixedResult.accepted,
				"the mixed race rejected every below-threshold completion on staleness, so it proved nothing")
			assert.Zero(t, mixedResult.nonZeroNonPositive,
				"a concurrent below-threshold completion persisted a negative classification state")
			require.Len(t, mixedResult.positives, 1,
				"a concurrent mixed race produced %d distinct positives, want one: %+v",
				len(mixedResult.positives), mixedResult.positives)
			for classification := range mixedResult.positives {
				assert.Equal(t, uint64(1), classification.Revision)
				assert.Equal(t, session.ConfidenceHigh, classification.Confidence)
				assert.Equal(t, session.KindCodingAgent, classification.Kind)
			}

			stored, found, err := stores.reader.Load(ctx, mixedKey)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, stored.Classification.IsCodingAgent(),
				"the mixed race must end on a positive, never a negative or unknown row")
			assert.Equal(t, uint64(1), stored.Classification.Revision,
				"a mixed race must produce exactly the single accepted first-positive revision")
			assert.Equal(t, session.SourceLocalIdentity, stored.Classification.Source)
			for classification := range mixedResult.positives {
				assert.Equal(t, stored.Classification, classification,
					"the stored row must be the single value every racing caller observed")
			}
			assert.Empty(t, stored.RemoteLeaseID)
		})
	}
}

// negativeRaceOutcome is what a below-threshold race actually observed, so the test
// can distinguish "rejected" from "accepted and harmless".
type negativeRaceOutcome struct {
	accepted           int
	rejected           int
	nonZeroNonPositive int
	positives          map[session.Classification]int
}

// raceBelowThresholdCompletions issues every live lease as a below-threshold
// completion at once. A below-threshold remote answer is represented by the zero
// RemoteCompletion, so this is the feature's only writable negative path.
func raceBelowThresholdCompletions(
	ctx context.Context,
	t *testing.T,
	coordinator *store.Coordinator,
	claims []featurestate.RemoteClaim,
) negativeRaceOutcome {
	t.Helper()
	start := make(chan struct{})
	outcomes := make(chan negativeRaceOutcome, len(claims))
	var wg sync.WaitGroup
	for i, claim := range claims {
		wg.Add(1)
		go func(index int, held featurestate.RemoteClaim) {
			defer wg.Done()
			<-start
			// Each completion runs well inside its own lease window, so the store
			// evaluates the classification guard rather than the expiry guard.
			at := claimAt(index)
			got, err := coordinator.CompleteRemote(ctx, held, featurestate.RemoteCompletion{}, at)
			outcome := negativeRaceOutcome{positives: map[session.Classification]int{}}
			if err != nil {
				outcome.rejected++
			} else {
				outcome.accepted++
			}
			if got.Classification.IsCodingAgent() {
				outcome.positives[got.Classification]++
			} else if got.Classification != (session.Classification{}) {
				outcome.nonZeroNonPositive++
			}
			outcomes <- outcome
		}(i, claim)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	return mergeNegativeOutcomes(outcomes)
}

// raceMixedPositivesAndNegatives races the same negative completions against
// concurrent positives, each carrying distinct evidence, so the single-winner
// invariant is tested rather than trivially satisfied.
func raceMixedPositivesAndNegatives(
	ctx context.Context,
	t *testing.T,
	coordinator *store.Coordinator,
	key featurestate.Key,
	claim featurestate.RemoteClaim,
) negativeRaceOutcome {
	t.Helper()
	const positives = 8

	// The below-threshold completion is released first and MUST be accepted, so
	// the scenario starts from a row a negative completion has genuinely written
	// to. Releasing the negatives and the eight positives from a single barrier
	// lets the positives win every time, which measures nothing (the earlier
	// version of this test did exactly that and was vacuous).
	negativeDone := make(chan negativeRaceOutcome, 1)
	go func(held featurestate.RemoteClaim) {
		got, err := coordinator.CompleteRemote(ctx, held, featurestate.RemoteCompletion{}, claimAt(0))
		outcome := negativeRaceOutcome{positives: map[session.Classification]int{}}
		if err == nil {
			outcome.accepted++
		} else {
			outcome.rejected++
		}
		if got.Classification.IsCodingAgent() {
			outcome.positives[got.Classification]++
		} else if got.Classification != (session.Classification{}) {
			outcome.nonZeroNonPositive++
		}
		negativeDone <- outcome
	}(claim)

	// Only once the negative has been recorded does the positive race start, so
	// the ordering is deterministic and the negative's acceptance is a fact
	// rather than a coin flip.
	results := make(chan negativeRaceOutcome, positives+1)
	first := <-negativeDone
	results <- first

	var wg sync.WaitGroup
	for i := range positives {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			proposal := convergenceLocalProposal()
			proposal.Evidence = session.EvidenceCode(fmt.Sprintf("mixed.positive_%d", index))
			got, _, err := coordinator.Promote(ctx, key, proposal,
				testStoreTime().Add(10*time.Minute+time.Duration(index)*time.Second))
			if err != nil {
				t.Errorf("mixed positive promotion: %v", err)
				results <- negativeRaceOutcome{positives: map[session.Classification]int{}}
				return
			}
			outcome := negativeRaceOutcome{positives: map[session.Classification]int{}}
			if got.Classification.IsCodingAgent() {
				outcome.positives[got.Classification]++
			} else if got.Classification != (session.Classification{}) {
				outcome.nonZeroNonPositive++
			}
			results <- outcome
		}(i)
	}
	wg.Wait()
	close(results)
	return mergeNegativeOutcomes(results)
}

func mergeNegativeOutcomes(results <-chan negativeRaceOutcome) negativeRaceOutcome {
	merged := negativeRaceOutcome{positives: map[session.Classification]int{}}
	for result := range results {
		merged.accepted += result.accepted
		merged.rejected += result.rejected
		merged.nonZeroNonPositive += result.nonZeroNonPositive
		for classification, count := range result.positives {
			merged.positives[classification] += count
		}
	}
	return merged
}

// claimAt is the moment a completion for lease index runs: strictly inside that
// lease's own window, and strictly before every later lease's window, so a
// completion is judged on its classification rather than on expiry.
func claimAt(index int) time.Time {
	return testStoreTime().Add(time.Duration(index) * time.Minute).Add(time.Second)
}

func convergenceLocalProposal() session.Classification {
	return session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh, Evidence: "client_family.codex", Revision: 1,
	}
}

func convergenceRemoteProposal(evidence session.EvidenceCode) session.Classification {
	return session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceRemote,
		Confidence: session.ConfidenceHigh, Evidence: evidence, Revision: 1,
	}
}

// coalescingSignalContext signals when a caller first inspects its context, which
// is the observable moment a coordinator flight waiter has committed to waiting.
//
// It is the instrument that makes the coalescing test non-vacuous: the leader's store
// write stays blocked until every waiter has provably joined, so a waiter that
// reached the store instead of the flight would be counted, not merely raced.
type coalescingSignalContext struct {
	context.Context
	signaled chan struct{}
	once     sync.Once
}

func newCoalescingSignalContext(parent context.Context) *coalescingSignalContext {
	return &coalescingSignalContext{Context: parent, signaled: make(chan struct{})}
}

func (c *coalescingSignalContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.signaled) })
	return c.Context.Done()
}

// TestCoordinatorCoalescesConcurrentPromotionsToOneDurableWrite is the requirement
// 12.5 single-flight half, measured in durable writes.
//
// Without flight coalescing, every concurrent same-key promotion reaches the store
// and races there instead of sharing one operation. The gate keeps the leader's store
// write blocked until all waiters have provably joined its flight, so the store call
// count is an exact measure of coalescing rather than a scheduling accident.
func TestCoordinatorCoalescesConcurrentPromotionsToOneDurableWrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	durable, err := store.NewMemoryStore(store.MemoryStoreConfig{MaxEntries: 16, IdleTTL: time.Hour})
	require.NoError(t, err)
	gate := newConvergenceBarrierStore(durable, true)
	coordinator, err := store.NewCoordinator(gate, store.CoordinatorConfig{
		CacheCapacity: 16, MaxInflight: 16, IdleTTL: time.Hour,
	})
	require.NoError(t, err)
	defer gate.openBarrier()

	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "coalesce/one-authority"}

	// The leader blocks inside the store's Promote while the waiters pile up on its
	// flight. Its proposal is deliberately not the one a waiter would send, so a
	// coalesced waiter that mistakenly took over would be visible in the evidence.
	leaderDone := make(chan convergenceOutcome, 1)
	go func() {
		got, _, err := coordinator.Promote(ctx, key, convergenceLocalProposal(), testStoreTime())
		leaderDone <- convergenceOutcome{classification: got.Classification, err: err}
	}()
	gate.waitPromoteEntered(t)

	const waiters = 8
	// One result per waiter, joined by a WaitGroup so the assertion below cannot read
	// a partially filled channel.
	waiterDone := make(chan convergenceOutcome, waiters)
	var waiterWG sync.WaitGroup
	waiterCtx := make([]*coalescingSignalContext, waiters)
	for i := range waiters {
		waiterCtx[i] = newCoalescingSignalContext(ctx)
		waiterWG.Add(1)
		go func(index int, requestCtx context.Context) {
			defer waiterWG.Done()
			proposal := convergenceLocalProposal()
			proposal.Source = session.SourceLocalTooling
			proposal.Evidence = session.EvidenceCode(fmt.Sprintf("coalesce.waiter_%d", index))
			got, _, err := coordinator.Promote(requestCtx, key, proposal, testStoreTime())
			waiterDone <- convergenceOutcome{classification: got.Classification, err: err}
		}(i, waiterCtx[i])
	}
	// Every waiter has now inspected its context, so each has either joined the
	// leader's flight or reached the store on its own.
	for i, requestCtx := range waiterCtx {
		select {
		case <-requestCtx.signaled:
		case <-time.After(20 * time.Second):
			t.Fatalf("waiter %d never entered the coordinator", i)
		}
	}

	assert.Equal(t, int64(1), gate.promoteCalls.Load(),
		"while the leader holds the store, no waiter may issue its own durable write")
	gate.openBarrier()

	waiterWG.Wait()
	close(waiterDone)

	results := make([]convergenceOutcome, 0, waiters+1)
	for result := range waiterDone {
		require.NotEqual(t, session.Classification{}, result.classification,
			"a coalescing waiter received no classification at all")
		results = append(results, result)
	}
	require.Len(t, results, waiters, "every coalescing waiter must report exactly one result")
	results = append(results, <-leaderDone)

	distinct := map[session.Classification]int{}
	for _, result := range results {
		require.NoError(t, result.err)
		if result.classification != (session.Classification{}) {
			distinct[result.classification]++
		}
	}
	require.Len(t, distinct, 1,
		"coalesced same-key promotions must all observe the single winner, observed %+v", distinct)
	for classification := range distinct {
		assert.Equal(t, uint64(1), classification.Revision)
		assert.Equal(t, session.SourceLocalIdentity, classification.Source,
			"the leader's proposal owns the transition; a waiter must not replace it")
		assert.Equal(t, session.EvidenceCode("client_family.codex"), classification.Evidence)
	}
	assert.Equal(t, int64(1), gate.promoteCalls.Load(),
		"flight coalescing must collapse concurrent same-key promotions into ONE durable write")

	stored, found, err := durable.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint64(1), stored.Classification.Revision)
}

// TestConcurrentDistinctAuthoritiesNeverInterfereUnderLoad combines requirement 2.5
// with requirement 2.3: several authorities, each running its own local/remote race
// at the same time, must each converge independently.
//
// A coalescing or store bug that leaked state across keys would show up as an
// authority observing another's evidence code.
func TestConcurrentDistinctAuthoritiesNeverInterfereUnderLoad(t *testing.T) {
	t.Parallel()

	for _, topology := range convergenceStoreTopologies(t) {
		t.Run(topology.name, func(t *testing.T) {
			t.Parallel()
			stores := topology.setup(t)
			coordinator, err := store.NewCoordinator(stores.store, store.CoordinatorConfig{
				CacheCapacity: 64, MaxInflight: 64, IdleTTL: time.Hour,
			})
			require.NoError(t, err)

			const authorities = 8
			keys := make([]featurestate.Key, 0, authorities)
			for i := range authorities {
				// Same client hint for every authority, distinct proxy-owned
				// identifiers: the requirement 2.3 shape under concurrency.
				keys = append(keys, featurestate.Key{
					Kind: featurestate.ScopeSecureSession,
					ID:   fmt.Sprintf("load/%s/authority-%d", topology.name, i),
				})
			}

			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, key := range keys {
				wg.Add(1)
				go func(index int, authority featurestate.Key) {
					defer wg.Done()
					<-start
					for round := range 3 {
						proposal := convergenceLocalProposal()
						proposal.Evidence = session.EvidenceCode(fmt.Sprintf("load.authority_%d_round_%d", index, round))
						if _, _, err := coordinator.Promote(context.Background(), authority, proposal, testStoreTime()); err != nil {
							t.Errorf("authority %d round %d promotion: %v", index, round, err)
							return
						}
					}
				}(i, key)
			}
			close(start)
			wg.Wait()

			seen := make([]session.EvidenceCode, 0, authorities)
			for i, key := range keys {
				record, found, err := stores.reader.Load(context.Background(), key)
				require.NoError(t, err)
				require.True(t, found, "authority %d has no row of its own", i)
				require.True(t, record.Classification.IsCodingAgent())
				assert.Equal(t, uint64(1), record.Classification.Revision)
				// First-positive-wins: round 0 owns the row, rounds 1 and 2 are replays.
				assert.Equal(t, session.EvidenceCode(fmt.Sprintf("load.authority_%d_round_0", i)), record.Classification.Evidence,
					"authority %d observed another authority's or another round's evidence", i)
				seen = append(seen, record.Classification.Evidence)
			}
			unique := map[session.EvidenceCode]bool{}
			for _, evidence := range seen {
				assert.False(t, unique[evidence], "two authorities share the stored evidence %q", evidence)
				unique[evidence] = true
			}
		})
	}
}
