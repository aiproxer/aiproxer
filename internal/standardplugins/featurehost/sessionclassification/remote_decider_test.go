package sessionclassification_test

import (
	"context"
	"errors"
	"sync"
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

// countingRemoteDecider records every call and returns a scripted bounded answer
// or a scripted bounded failure. It is the hermetic stand-in for the Jev adapter
// wherever this package drives a real store and a real coordinator, so the
// lease proofs never perform egress (requirements 12.8, 12.9).
type countingRemoteDecider struct {
	mu       sync.Mutex
	calls    int
	inFlight int
	maxSeen  int
	answers  []deciderAnswer
	fallback deciderAnswer
	// entered receives while a call is in flight; release, when non-nil, parks
	// every call until it is closed. Together they expose the exact window in
	// which requirement 10.4 forbids holding a store lock.
	entered chan struct{}
	release <-chan struct{}
}

type deciderAnswer struct {
	decision featurestate.RemoteDecision
	err      error
}

var _ featurestate.RemoteDecider = (*countingRemoteDecider)(nil)

// boundedDeciderFailure is a scripted failure that carries its closed-vocabulary
// member through the optional featurestate.RemoteFailure contract, which is how
// the real adapter reports a failure without leaking error text into a label.
type boundedDeciderFailure struct {
	outcome featurestate.RemoteOutcome
	cause   error
}

func (f boundedDeciderFailure) Error() string { return "scripted jev failure" }
func (f boundedDeciderFailure) Unwrap() error { return f.cause }

func (f boundedDeciderFailure) Outcome() featurestate.RemoteOutcome { return f.outcome }

var _ featurestate.RemoteFailure = boundedDeciderFailure{}

func (d *countingRemoteDecider) Decide(_ context.Context, _ featurestate.RemoteInput) (featurestate.RemoteDecision, error) {
	d.mu.Lock()
	d.calls++
	d.inFlight++
	if d.inFlight > d.maxSeen {
		d.maxSeen = d.inFlight
	}
	answer := d.fallback
	if len(d.answers) > 0 {
		answer = d.answers[0]
		d.answers = d.answers[1:]
	}
	entered, release := d.entered, d.release
	d.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	d.mu.Lock()
	d.inFlight--
	d.mu.Unlock()
	return answer.decision, answer.err
}

func (d *countingRemoteDecider) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *countingRemoteDecider) maxConcurrent() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.maxSeen
}

// remoteFlowConfig builds a validated remote posture for the composed
// lease-acceptance proofs. The values are inside every bound the feature
// validates, and the lease TTL exceeds the hard timeout by the safety margin, so
// a lease can never outlive the call it authorises (requirements 6.7, 8.4).
func remoteFlowConfig(attempts int, backoff time.Duration) featurestate.Config {
	timeout := 200 * time.Millisecond
	return featurestate.Config{
		Mode: featurestate.ModeJev,
		Remote: &featurestate.RemoteConfig{
			Provider:              "jev",
			APIKeyEnv:             "AIP_FLOW_TEST_JEV_KEY",
			Timeout:               timeout,
			MaxAttemptsPerSession: attempts,
			LeaseTTL:              timeout + featurestate.RemoteLeaseSafetyMargin + time.Second,
			RetryBackoff:          backoff,
			PositiveThreshold:     0.9,
		},
	}
}

// ambiguousFlowInput is a turn no local rule accepts, so a remote-capable mode
// must consult the remote decider for it (requirements 6.3, 6.4).
func ambiguousFlowInput(sessionID string) sdkclassification.Input {
	return sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{
			ClientUserAgent: "openai-python/1.40.0",
			Operation:       aipapi.OperationOpenAIResponses,
		},
	}
}

// sharedStoreAuthority resolves one process-owned store for a generation, the
// same way the standard distribution's state holder does.
type sharedStoreAuthority struct {
	inner featurestate.Store
}

func (a sharedStoreAuthority) ClassificationState() (featurestate.Store, error) { return a.inner, nil }

// flowClassifier binds a real generation classifier to a real store.
func flowClassifier(t *testing.T, cfg featurestate.Config, inner featurestate.Store, decider featurestate.RemoteDecider) *featurestate.Classifier {
	t.Helper()
	classifier, err := featurestate.NewClassifier(cfg, featurestate.ClassifierDeps{
		State:  sharedStoreAuthority{inner: inner},
		Remote: decider,
		Now:    time.Now,
	})
	require.NoError(t, err)
	return classifier
}

func flowKey(sessionID string) featurestate.Key {
	return featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: sessionID}
}

// TestStoreScopeAdmitsOneLeaseForConcurrentAmbiguousTurns is the store-scope
// lease acceptance proof for requirements 6.6 and 12.5: N concurrent ambiguous
// turns of one logical session produce exactly one active lease in the store, one
// remote call, and no duplicated egress, and every turn completes without an
// error.
//
// The lease is enforced by the store rather than by the process, so the same
// property holds across two independent coordinators: the second half of the
// proof runs the identical fixture against two separately constructed coordinators
// over one shared store.
func TestStoreScopeAdmitsOneLeaseForConcurrentAmbiguousTurns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		coordinators func(t *testing.T, inner featurestate.Store) []featurestate.Store
	}{
		{
			name: "one coordinator",
			coordinators: func(t *testing.T, inner featurestate.Store) []featurestate.Store {
				t.Helper()
				return []featurestate.Store{mustCoordinator(t, inner)}
			},
		},
		{
			// Requirement 6.6 says the lease is permitted "within the configured
			// persistence/coordinator scope", so two coordinators over one store
			// must still admit exactly one lease: the guarantee is the durable
			// record, not a process lock.
			name: "two coordinators over one shared store",
			coordinators: func(t *testing.T, inner featurestate.Store) []featurestate.Store {
				t.Helper()
				return []featurestate.Store{mustCoordinator(t, inner), mustCoordinator(t, inner)}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			const turns = 24
			memoryStore, err := store.NewMemoryStore(store.MemoryStoreConfig{MaxEntries: 64, IdleTTL: time.Hour})
			require.NoError(t, err)
			stores := tc.coordinators(t, memoryStore)
			require.NotEmpty(t, stores)

			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			decider := &countingRemoteDecider{
				fallback: deciderAnswer{decision: featurestate.RemoteDecision{CodingProbability: 0.97}},
				entered:  entered,
				release:  release,
			}

			const sessionID = "sess-store-scope-concurrent"
			classifiers := make([]*featurestate.Classifier, 0, len(stores))
			for _, target := range stores {
				classifiers = append(classifiers, flowClassifier(t, remoteFlowConfig(1, 0), target, decider))
			}

			start := make(chan struct{})
			results := make(chan session.Classification, turns)
			errs := make(chan error, turns)
			var wg sync.WaitGroup
			for index := range turns {
				// Half the turns go through each coordinator, so the race crosses
				// the process-local boundary as well as the turn boundary.
				classifier := classifiers[index%len(classifiers)]
				wg.Go(func() {
					<-start
					got, err := classifier.Classify(context.Background(), ambiguousFlowInput(sessionID))
					if err != nil {
						errs <- err
						return
					}
					results <- got
				})
			}
			close(start)

			// Wait until the single granted attempt is genuinely in flight, then
			// release it so every other turn has already met the active lease.
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("no remote attempt entered the decider")
			}
			close(release)
			wg.Wait()
			close(results)
			close(errs)

			for err := range errs {
				t.Errorf("concurrent turn failed: %v", err)
			}
			coherent := 0
			for got := range results {
				if !got.IsCodingAgent() {
					continue
				}
				coherent++
				if got.Revision != 1 || got.Source != session.SourceRemote {
					t.Errorf("positive = %+v, want the remote positive at revision 1", got)
				}
			}
			require.Positive(t, coherent, "at least one turn must establish the positive")

			// Requirement 6.6: at most one active lease, one call, no duplicate
			// egress.
			assert.Equal(t, 1, decider.callCount(), "exactly one remote call for concurrent ambiguous turns")
			assert.Equal(t, 1, decider.maxConcurrent(), "remote calls must not overlap for one session")

			record, found, err := stores[0].Load(context.Background(), flowKey(sessionID))
			require.NoError(t, err)
			require.True(t, found, "the promoted session must persist a record")
			assert.Equal(t, uint32(1), record.RemoteAttempts, "exactly one attempt may be consumed for the session")
			assert.True(t, record.Classification.IsCodingAgent())
			assert.Equal(t, uint64(1), record.Classification.Revision)
			// Requirement 6.7: the lease is finished, not abandoned.
			assert.Empty(t, record.RemoteLeaseID, "the granted lease must be completed")
			assert.True(t, record.RemoteLeaseUntil.IsZero())
		})
	}
}

// mustCoordinator wraps one store in the standard process coordinator.
func mustCoordinator(t *testing.T, inner featurestate.Store) *store.Coordinator {
	t.Helper()
	coordinator, err := store.NewCoordinator(inner, store.CoordinatorConfig{})
	require.NoError(t, err)
	return coordinator
}

// TestDurableBunStoreAdmitsOneLeaseAcrossStoreInstances is the durable half of the
// same acceptance proof. The SQLite-backed store enforces the lease in the
// persistence scope, so two independently constructed store instances over one
// database still admit exactly one active lease (requirements 6.6, 12.10).
func TestDurableBunStoreAdmitsOneLeaseAcrossStoreInstances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDBWithConnections(t, sqliteTestDSN(t, "classification-store-scope.db"), 8)
	first, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, first.EnsureSchema(ctx))
	second, err := store.NewBunStore(bunDB)
	require.NoError(t, err)

	const (
		workers  = 16
		budget   = 1
		key      = "sess-bun-store-scope"
		leaseTTL = time.Minute
	)
	startAt := time.Now()
	start := make(chan struct{})
	type outcome struct {
		claim featurestate.RemoteClaim
		ok    bool
		err   error
	}
	results := make(chan outcome, workers)
	var wg sync.WaitGroup
	for index := range workers {
		target := featurestate.Store(first)
		if index%2 == 1 {
			target = second
		}
		wg.Go(func() {
			<-start
			claim, _, ok, err := target.ClaimRemote(ctx, flowKey(key), startAt, budget, leaseTTL, 0)
			results <- outcome{claim: claim, ok: ok, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for got := range results {
		require.NoError(t, got.err)
		if got.ok {
			winners++
			assert.NotEmpty(t, got.claim.LeaseID)
		}
	}
	assert.Equal(t, 1, winners, "the durable scope must admit exactly one active lease")

	record, found, err := first.Load(ctx, flowKey(key))
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, uint32(1), record.RemoteAttempts, "exactly one attempt may be consumed")
	assert.NotEmpty(t, record.RemoteLeaseID, "the winning lease identifier is active in the durable row")
	assert.True(t, record.RemoteLeaseUntil.After(startAt), "the active lease has a future deadline")
}

// TestDurableRemoteFailureFailsOpenThroughBunStore pins requirement 6.9 against
// the durable store: a remote failure leaves the session unknown, keeps the
// request, and persists no classification of any kind.
func TestDurableRemoteFailureFailsOpenThroughBunStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDBWithConnections(t, sqliteTestDSN(t, "classification-bun-failopen.db"), 4)
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))
	coordinator := mustCoordinator(t, classificationStore)

	const sessionID = "sess-bun-failopen"
	decider := &countingRemoteDecider{fallback: deciderAnswer{
		err: boundedDeciderFailure{outcome: featurestate.RemoteTimeout, cause: errors.New("scripted timeout")},
	}}
	classifier := flowClassifier(t, remoteFlowConfig(2, 0), coordinator, decider)

	got, err := classifier.Classify(ctx, ambiguousFlowInput(sessionID))
	require.NoError(t, err, "a remote failure must never fail the user request")
	assert.Equal(t, session.Classification{}, got)

	record, found, err := classificationStore.Load(ctx, flowKey(sessionID))
	require.NoError(t, err)
	require.True(t, found, "the failed attempt persists its bounded control state")
	assert.Equal(t, session.Classification{}, record.Classification, "a failure writes no classification at all")
	assert.Empty(t, record.RemoteLeaseID, "the failed attempt must complete its lease")

	// Requirements 6.9 and 12.8: a retryable failure consumed the whole finite
	// budget and then stopped. The budget is 2, so a third attempt would mean the
	// limit was not enforced against the durable scope.
	assert.Equal(t, 2, decider.callCount(), "a retryable failure must stop at the configured attempt budget")
	assert.Equal(t, uint32(2), record.RemoteAttempts, "the durable counter records every consumed attempt")
}

// TestBunStorePositiveMakesNoRemoteCall pins requirements 6.5 and 10.1 against
// the durable store: a session with an established positive performs no remote
// call and no further durable remote work, and a second turn reads it back.
func TestBunStorePositiveMakesNoRemoteCall(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDBWithConnections(t, sqliteTestDSN(t, "classification-bun-positive.db"), 4)
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))
	coordinator := mustCoordinator(t, classificationStore)

	const sessionID = "sess-bun-positive"
	first := &countingRemoteDecider{fallback: deciderAnswer{
		decision: featurestate.RemoteDecision{CodingProbability: 0.95},
	}}
	got, err := flowClassifier(t, remoteFlowConfig(1, 0), coordinator, first).Classify(ctx, ambiguousFlowInput(sessionID))
	require.NoError(t, err)
	require.True(t, got.IsCodingAgent())
	assert.Equal(t, session.SourceRemote, got.Source)
	assert.Equal(t, featurestate.EvidenceCodeRemoteAboveThreshold, got.Evidence)
	assert.Equal(t, 1, first.callCount())

	// A later turn of the same session, through an independent coordinator over
	// the same durable store, must make no further remote call.
	second := &countingRemoteDecider{fallback: deciderAnswer{
		decision: featurestate.RemoteDecision{CodingProbability: 0.1},
	}}
	other := mustCoordinator(t, classificationStore)
	later, err := flowClassifier(t, remoteFlowConfig(1, 0), other, second).Classify(ctx, ambiguousFlowInput(sessionID))
	require.NoError(t, err)
	assert.Equal(t, got, later, "the durable positive is projected unchanged")
	assert.Equal(t, 0, second.callCount(), "an established positive issues zero remote calls")
}
