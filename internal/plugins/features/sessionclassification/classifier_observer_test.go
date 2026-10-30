package sessionclassification_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// recordingObserver captures every bounded observation the classifier emits so a
// test can prove how many were emitted and which bounded fields they carried.
type recordingObserver struct {
	mu          sync.Mutex
	evaluations []sessionclassification.EvaluationObservation
	transitions []sessionclassification.TransitionObservation
	remotes     []sessionclassification.RemoteObservation
	stores      []sessionclassification.StoreObservation
}

var _ sessionclassification.Observer = (*recordingObserver)(nil)

func (r *recordingObserver) ObserveEvaluation(observation sessionclassification.EvaluationObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evaluations = append(r.evaluations, observation)
}

func (r *recordingObserver) ObserveTransition(observation sessionclassification.TransitionObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitions = append(r.transitions, observation)
}

func (r *recordingObserver) ObserveRemote(observation sessionclassification.RemoteObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remotes = append(r.remotes, observation)
}

func (r *recordingObserver) ObserveStore(observation sessionclassification.StoreObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stores = append(r.stores, observation)
}

func (r *recordingObserver) snapshot() ([]sessionclassification.EvaluationObservation, []sessionclassification.TransitionObservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionclassification.EvaluationObservation(nil), r.evaluations...),
		append([]sessionclassification.TransitionObservation(nil), r.transitions...)
}

func (r *recordingObserver) remoteCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.remotes)
}

func observedClassifier(
	t *testing.T,
	cfg sessionclassification.Config,
	state sessionclassification.StateAuthority,
	observer sessionclassification.Observer,
) *sessionclassification.Classifier {
	t.Helper()
	return observedRemoteClassifier(t, cfg, state, observer, belowThresholdDecider())
}

// belowThresholdDecider answers every remote attempt below the configured
// threshold, so a remote-capable fixture reaches its bounded unknown outcome
// deterministically and without any network (requirements 6.8, 12.9).
func belowThresholdDecider() *countingDecider {
	return &countingDecider{fallback: scriptedAnswer{
		decision: sessionclassification.RemoteDecision{CodingProbability: 0.1},
	}}
}

// observedRemoteClassifier builds a generation classifier with an explicit
// decider. A remote-capable mode is refused a nil decider by NewClassifier, so
// every jev or hybrid fixture passes one deliberately.
func observedRemoteClassifier(
	t *testing.T,
	cfg sessionclassification.Config,
	state sessionclassification.StateAuthority,
	observer sessionclassification.Observer,
	decider sessionclassification.RemoteDecider,
) *sessionclassification.Classifier {
	t.Helper()
	if cfg.Mode != sessionclassification.ModeJev && cfg.Mode != sessionclassification.ModeHybrid {
		decider = nil
	}
	classifier, err := sessionclassification.NewClassifier(cfg, sessionclassification.ClassifierDeps{
		State:    state,
		Remote:   decider,
		Observer: observer,
		Now:      fixedNow(),
	})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	return classifier
}

// TestClassifyEmitsExactlyOneBoundedTransitionPerPositiveSession covers
// requirement 9.1: the first accepted promotion emits one observation carrying
// source, confidence band, decisive evidence code, and revision, and every later
// turn of the same logical session emits none.
func TestClassifyEmitsExactlyOneBoundedTransitionPerPositiveSession(t *testing.T) {
	t.Parallel()

	const secretSessionID = "sess-observer-secret-2f19c0de"
	observer := &recordingObserver{}
	store := newFakeStore()
	classifier := observedClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, &fakeAuthority{store: store}, observer)
	in := codingInput(secretSessionID, "codex_cli_rs/1.2.3")

	want := sessionclassification.TransitionObservation{
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	const turns = 5
	for turn := range turns {
		got, err := classifier.Classify(t.Context(), in)
		if err != nil {
			t.Fatalf("turn %d Classify: %v", turn, err)
		}
		if !got.IsCodingAgent() {
			t.Fatalf("turn %d classification = %+v, want the accepted positive", turn, got)
		}
		if turn == 0 && want.Snapshot() != got {
			t.Fatalf("first projection = %+v, want the bounded positive %+v", got, want.Snapshot())
		}
	}

	evaluations, transitions := observer.snapshot()
	if len(transitions) != 1 {
		t.Fatalf("transition observations over %d turns = %d, want exactly one per positive session", turns, len(transitions))
	}
	if transitions[0] != want {
		t.Fatalf("transition observation = %+v, want %+v", transitions[0], want)
	}
	// Requirement 9.5: the observation projects the same bounded snapshot the
	// classifier returned, without any feature-private classifier state.
	snapshot := transitions[0].Snapshot()
	if snapshot.Kind != session.KindCodingAgent || snapshot.Source != session.SourceLocalIdentity ||
		snapshot.Confidence != session.ConfidenceHigh || snapshot.Evidence != "client_family.codex" || snapshot.Revision != 1 {
		t.Fatalf("transition projection = %+v, want the bounded positive snapshot", snapshot)
	}

	if evaluations[0] != (sessionclassification.EvaluationObservation{Mode: sessionclassification.ModeHeuristic, Outcome: sessionclassification.EvaluationPromoted}) {
		t.Fatalf("first evaluation = %+v, want a heuristic-mode promoted outcome", evaluations[0])
	}
	if len(evaluations) != turns {
		t.Fatalf("evaluation observations = %d, want one per turn", len(evaluations))
	}
	for _, observation := range evaluations[1:] {
		if observation.Outcome != sessionclassification.EvaluationRestored {
			t.Fatalf("replayed turn outcome = %q, want %q", observation.Outcome, sessionclassification.EvaluationRestored)
		}
	}
	if count := observer.remoteCount(); count != 0 {
		t.Fatalf("remote observations = %d, want zero while no remote decider is wired", count)
	}
}

// TestClassifyEmitsBoundedOutcomeDiagnosticsWithoutTransition covers
// requirements 9.2, 6.3, and 6.4: with no transition the observation vocabulary
// stays inside the closed outcome set and never carries identities or prompt
// excerpts, and a remote-capable turn reports its bounded outcome only after the
// configured remote decision was actually attempted. A turn whose prospective
// local match was excluded makes no remote attempt at all.
func TestClassifyEmitsBoundedOutcomeDiagnosticsWithoutTransition(t *testing.T) {
	t.Parallel()

	const (
		secretSessionID = "sess-secret-2f19c0de"
		secretALegID    = "a-leg-secret-2f19c0de"
		secretUserAgent = "codex_cli_rs/9.9.9-secret"
		secretMarker    = "private-repository-go.mod"
	)

	cases := []struct {
		name            string
		cfg             sessionclassification.Config
		state           sessionclassification.StateAuthority
		in              sdkclassification.Input
		decider         *countingDecider
		want            sessionclassification.EvaluationOutcome
		wantErr         bool
		wantRemoteCalls int
	}{
		{
			name:  "no decisive evidence stays unknown",
			cfg:   sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			state: &fakeAuthority{store: newFakeStore()},
			in: sdkclassification.Input{
				Session: session.SessionView{AuthoritativeSessionID: secretSessionID},
				Evidence: sdkclassification.Evidence{
					ClientUserAgent: "Mozilla/5.0 (X11; Linux x86_64)",
					Operation:       aipapi.OperationOpenAIResponses,
				},
				Workspace: workspace.WorkspaceView{Markers: []string{secretMarker}},
			},
			want: sessionclassification.EvaluationUnknown,
		},
		{
			name: "configured exclusion is reported as excluded",
			cfg: sessionclassification.Config{
				Mode:      sessionclassification.ModeHeuristic,
				Heuristic: sessionclassification.HeuristicConfig{IgnoredUserAgentPrefixes: []string{"codex_cli_rs"}},
			},
			state: &fakeAuthority{store: newFakeStore()},
			in: sdkclassification.Input{
				Session:  session.SessionView{AuthoritativeSessionID: secretSessionID},
				Evidence: sdkclassification.Evidence{ClientUserAgent: secretUserAgent},
			},
			want: sessionclassification.EvaluationExcluded,
		},
		{
			// Requirement 6.3: a jev turn's decisive local evidence is not enough;
			// the configured remote decision is. A below-threshold remote answer
			// leaves the session unknown and reports the bare-unknown outcome,
			// because a remote decision was taken and simply did not promote.
			name: "remote-required mode with decisive local evidence reports unknown after a below-threshold decision",
			cfg: sessionclassification.Config{
				Mode:   sessionclassification.ModeJev,
				Remote: validTestRemoteConfig(),
			},
			state: &fakeAuthority{store: newFakeStore()},
			in: sdkclassification.Input{
				Session: session.SessionView{ALegID: secretALegID},
				Evidence: sdkclassification.Evidence{
					ClientUserAgent: "codex_cli_rs/1.2.3",
					Operation:       aipapi.OperationOpenAIResponses,
				},
			},
			decider:         belowThresholdDecider(),
			want:            sessionclassification.EvaluationUnknown,
			wantRemoteCalls: 1,
		},
		{
			// Requirement 6.4: a hybrid turn that local evaluation left unknown is
			// remote-eligible, so a below-threshold remote decision leaves the
			// session unknown rather than promoting it.
			name: "hybrid mode with no decisive local evidence reports unknown after a below-threshold decision",
			cfg: sessionclassification.Config{
				Mode:   sessionclassification.ModeHybrid,
				Remote: validTestRemoteConfig(),
			},
			state: &fakeAuthority{store: newFakeStore()},
			in: sdkclassification.Input{
				Session: session.SessionView{AuthoritativeSessionID: secretSessionID},
				Evidence: sdkclassification.Evidence{
					ClientUserAgent: "Mozilla/5.0 (X11; Linux x86_64)",
					Operation:       aipapi.OperationOpenAIResponses,
				},
				Workspace: workspace.WorkspaceView{Markers: []string{secretMarker}},
			},
			decider:         belowThresholdDecider(),
			want:            sessionclassification.EvaluationUnknown,
			wantRemoteCalls: 1,
		},
		{
			// Requirement 6.4: an exclusion still takes precedence for a hybrid
			// turn because the prospective local match was suppressed.
			name: "hybrid mode with a suppressed identity reports excluded",
			cfg: sessionclassification.Config{
				Mode:      sessionclassification.ModeHybrid,
				Remote:    validTestRemoteConfig(),
				Heuristic: sessionclassification.HeuristicConfig{IgnoredUserAgentPrefixes: []string{"codex_cli_rs"}},
			},
			state: &fakeAuthority{store: newFakeStore()},
			in: sdkclassification.Input{
				Session:  session.SessionView{AuthoritativeSessionID: secretSessionID},
				Evidence: sdkclassification.Evidence{ClientUserAgent: secretUserAgent},
			},
			// A decider is wired, but an excluded identity must never reach it.
			decider:         belowThresholdDecider(),
			want:            sessionclassification.EvaluationExcluded,
			wantRemoteCalls: 0,
		},
		{
			name:  "turn without proxy authority reports no_authority",
			cfg:   sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			state: &fakeAuthority{store: newFakeStore()},
			in: sdkclassification.Input{
				Session:  session.SessionView{ClientSessionHint: secretSessionID},
				Evidence: sdkclassification.Evidence{ClientUserAgent: secretUserAgent},
			},
			want: sessionclassification.EvaluationNoAuthority,
		},
		{
			name:    "unavailable process state reports the bounded fail-open outcome",
			cfg:     sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			state:   &fakeAuthority{err: errors.New("state holder closed")},
			in:      codingInput(secretSessionID, "codex_cli_rs/1.2.3"),
			want:    sessionclassification.EvaluationStateUnavailable,
			wantErr: true,
		},
		{
			name:    "durable load failure reports the bounded fail-open outcome",
			cfg:     sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			state:   &fakeAuthority{store: failedLoadStore()},
			in:      codingInput(secretSessionID, "codex_cli_rs/1.2.3"),
			want:    sessionclassification.EvaluationStateUnavailable,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			observer := &recordingObserver{}
			classifier := observedRemoteClassifier(t, tc.cfg, tc.state, observer, tc.decider)
			_, err := classifier.Classify(t.Context(), tc.in)
			if tc.wantErr != (err != nil) {
				t.Fatalf("Classify error = %v, want error=%t", err, tc.wantErr)
			}
			evaluations, transitions := observer.snapshot()
			if len(evaluations) != 1 {
				t.Fatalf("evaluation observations = %d, want exactly one bounded diagnostic", len(evaluations))
			}
			if len(transitions) != 0 {
				t.Fatalf("transition observations = %d, want none without a positive transition", len(transitions))
			}
			observation := evaluations[0]
			if observation.Outcome != tc.want {
				t.Fatalf("evaluation outcome = %q, want %q", observation.Outcome, tc.want)
			}
			if !sessionclassification.ValidEvaluationObservation(observation) {
				t.Fatalf("evaluation observation %+v is outside the closed vocabulary", observation)
			}
			assertObservationCarriesNoSecret(t, observation, secretSessionID, secretALegID, secretUserAgent, secretMarker)
			if tc.decider != nil {
				if calls := tc.decider.callCount(); calls != tc.wantRemoteCalls {
					t.Fatalf("remote decisions = %d, want %d for this outcome", calls, tc.wantRemoteCalls)
				}
			}
			if count := observer.remoteCount(); count != tc.wantRemoteCalls {
				t.Fatalf("remote observations = %d, want %d for this outcome", count, tc.wantRemoteCalls)
			}
		})
	}
}

// staticAuthority satisfies the state-authority contract with a fixed store.
type staticAuthority struct{ store sessionclassification.Store }

func (a staticAuthority) ClassificationState() (sessionclassification.Store, error) {
	return a.store, nil
}

// alreadyPromotedStore models a store that coalesced this turn's attempt with a
// concurrent turn's: the authoritative read still reports the session unknown
// while the promotion path returns the winner's positive record with
// promoted=false. That is exactly what the real process coordinator does for a
// coalesced-flight waiter and for its cached-positive short-circuit, so it pins
// the first-positive-ownership rule without depending on thread timing.
type alreadyPromotedStore struct {
	winner sessionclassification.Record
}

var _ sessionclassification.Store = (*alreadyPromotedStore)(nil)

func newAlreadyPromotedStore(winner session.Classification) *alreadyPromotedStore {
	return &alreadyPromotedStore{
		winner: sessionclassification.Record{Classification: winner, UpdatedAt: time.Now()},
	}
}

func (s *alreadyPromotedStore) Load(context.Context, sessionclassification.Key) (sessionclassification.Record, bool, error) {
	// The authoritative read happened before the winning turn's write landed.
	return sessionclassification.Record{}, false, nil
}

func (s *alreadyPromotedStore) Promote(_ context.Context, key sessionclassification.Key, _ session.Classification, _ time.Time) (sessionclassification.Record, bool, error) {
	record := s.winner
	record.Key = key
	return record, false, nil
}

func (*alreadyPromotedStore) ClaimRemote(context.Context, sessionclassification.Key, time.Time, uint32, time.Duration, time.Duration) (sessionclassification.RemoteClaim, sessionclassification.Record, bool, error) {
	return sessionclassification.RemoteClaim{}, sessionclassification.Record{}, false, errors.New("remote claims are not part of the local classifier")
}

func (*alreadyPromotedStore) CompleteRemote(context.Context, sessionclassification.RemoteClaim, sessionclassification.RemoteCompletion, time.Time) (sessionclassification.Record, error) {
	return sessionclassification.Record{}, errors.New("remote completion is not part of the local classifier")
}

// TestClassifySuppressesOnlyTheObservationWhenAnotherTurnOwnsThePositive is the
// unit-level rule behind requirement 9.1: a store that reports promoted=false
// while holding a positive means a concurrent turn established the first
// positive. Requirement 1.4 still requires this turn to project that positive;
// only the transition observation is suppressed.
func TestClassifySuppressesOnlyTheObservationWhenAnotherTurnOwnsThePositive(t *testing.T) {
	t.Parallel()

	winner := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	observer := &recordingObserver{}
	store := newAlreadyPromotedStore(winner)
	classifier := observedClassifier(t,
		sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
		staticAuthority{store: store},
		observer,
	)

	got, err := classifier.Classify(t.Context(), codingInput("sess-loser", "codex_cli_rs/1.2.3"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != winner {
		t.Fatalf("losing turn classification = %+v, want the winner's positive %+v", got, winner)
	}
	evaluations, transitions := observer.snapshot()
	if len(transitions) != 0 {
		t.Fatalf("losing turn emitted %d transition observations, want none (transitions=%+v)", len(transitions), transitions)
	}
	if len(evaluations) != 1 || evaluations[0].Outcome != sessionclassification.EvaluationRestored {
		t.Fatalf("losing turn observations = %+v, want one bounded restored outcome", evaluations)
	}
}

// TestClassifyObservesTheRejectedContextPath keeps every early return
// observable: a nil or canceled context is a caller-side rejection, but it must
// still be reported through a bounded outcome rather than silently unobserved.
func TestClassifyObservesTheRejectedContextPath(t *testing.T) {
	t.Parallel()

	for _, ctx := range []context.Context{nil, canceledContext(t)} {
		observer := &recordingObserver{}
		classifier := observedClassifier(t,
			sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			&fakeAuthority{store: newFakeStore()},
			observer,
		)
		got, err := classifier.Classify(ctx, codingInput("sess-rejected-ctx", "codex_cli_rs/1.2.3"))
		if err == nil {
			t.Fatalf("Classify with %T context returned no error", ctx)
		}
		if got != (session.Classification{}) {
			t.Fatalf("classification = %+v, want unknown", got)
		}
		evaluations, transitions := observer.snapshot()
		if len(evaluations) != 1 || evaluations[0].Outcome != sessionclassification.EvaluationStateUnavailable {
			t.Fatalf("rejected-context observations = %+v, want one bounded state_unavailable outcome", evaluations)
		}
		if evaluations[0].Mode != sessionclassification.ModeHeuristic {
			t.Fatalf("rejected-context mode = %q, want the configured generation mode", evaluations[0].Mode)
		}
		if len(transitions) != 0 {
			t.Fatalf("rejected context emitted %d transition observations, want none", len(transitions))
		}
	}
}

func canceledContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func failedLoadStore() *fakeStore {
	store := newFakeStore()
	store.loadErr = errors.New("durable load failed")
	return store
}

// TestClassifyWithPriorPositiveObservesPreservedWithoutStateWork proves a warm
// positive records a bounded preserved outcome and still performs no
// process-state work (requirements 9.2, 10.1).
func TestClassifyWithPriorPositiveObservesPreservedWithoutStateWork(t *testing.T) {
	t.Parallel()

	observer := &recordingObserver{}
	authority := &fakeAuthority{store: newFakeStore()}
	classifier := observedClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, authority, observer)
	in := codingInput("sess-warm", "codex_cli_rs/1.2.3")
	in.Session.Classification = session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tooling.distinct_coding_cluster",
		Revision:   9,
	}
	if _, err := classifier.Classify(t.Context(), in); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	evaluations, transitions := observer.snapshot()
	if len(evaluations) != 1 || evaluations[0].Outcome != sessionclassification.EvaluationPreserved {
		t.Fatalf("warm positive observations = %+v, want one preserved outcome", evaluations)
	}
	if len(transitions) != 0 {
		t.Fatalf("warm positive emitted %d transition observations, want none", len(transitions))
	}
	if calls := authority.callCount(); calls != 0 {
		t.Fatalf("warm positive resolved process state %d times, want zero", calls)
	}
}

// promoteOutcomeStore models the two promotion-write outcomes a classifier must
// not mistake for an accepted positive: a durable write failure, and a store that
// reports a promotion while holding no positive record.
type promoteOutcomeStore struct {
	*fakeStore
	promoteErr error
	promoted   bool
}

var _ sessionclassification.Store = (*promoteOutcomeStore)(nil)

func (s *promoteOutcomeStore) Promote(context.Context, sessionclassification.Key, session.Classification, time.Time) (sessionclassification.Record, bool, error) {
	if s.promoteErr != nil {
		return sessionclassification.Record{}, false, s.promoteErr
	}
	return sessionclassification.Record{}, s.promoted, nil
}

// TestClassifyPromotionWriteWithoutAcceptedPositiveNeverFabricatesOne proves the
// classifier reports a promotion only when this turn's own durable write accepted
// a positive: a failing write fails open with ErrStateUnavailable and a
// state_unavailable observation, and a store that reports a promotion while
// holding no positive leaves the session unknown with a restored evaluation and
// no transition observation (requirements 4.4, 1.2, 1.5, 9.1).
func TestClassifyPromotionWriteWithoutAcceptedPositiveNeverFabricatesOne(t *testing.T) {
	t.Parallel()

	promoteFailure := errors.New("durable promotion write failed")
	cases := []struct {
		name        string
		store       sessionclassification.Store
		wantErr     bool
		wantOutcome sessionclassification.EvaluationOutcome
	}{
		{
			name:        "durable promotion write failure fails open",
			store:       &promoteOutcomeStore{fakeStore: newFakeStore(), promoteErr: promoteFailure},
			wantErr:     true,
			wantOutcome: sessionclassification.EvaluationStateUnavailable,
		},
		{
			name:        "reported promotion without a positive stays unknown",
			store:       &promoteOutcomeStore{fakeStore: newFakeStore(), promoted: true},
			wantOutcome: sessionclassification.EvaluationRestored,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			observer := &recordingObserver{}
			classifier := observedClassifier(t,
				sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
				staticAuthority{store: tc.store},
				observer,
			)

			got, err := classifier.Classify(t.Context(), codingInput("sess-promote-write", "codex_cli_rs/1.2.3"))
			if tc.wantErr != (err != nil) {
				t.Fatalf("Classify error = %v, want error=%t", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, sessionclassification.ErrStateUnavailable) {
				t.Fatalf("Classify error = %v, want %v", err, sessionclassification.ErrStateUnavailable)
			}
			if got != (session.Classification{}) {
				t.Fatalf("classification = %+v, want unknown", got)
			}
			evaluations, transitions := observer.snapshot()
			if len(evaluations) != 1 || evaluations[0].Outcome != tc.wantOutcome {
				t.Fatalf("evaluations = %+v, want one bounded %q outcome", evaluations, tc.wantOutcome)
			}
			if len(transitions) != 0 {
				t.Fatalf("transitions = %+v, want none without an accepted positive", transitions)
			}
		})
	}
}

// TestObserverIsOptionalAndAddsNoHotPathDependency keeps the classifier usable
// without any observer and proves the added dependency is a bounded interface.
func TestObserverIsOptionalAndAddsNoHotPathDependency(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	got, err := classifier.Classify(t.Context(), codingInput("sess-no-observer", "codex_cli_rs/1.2.3"))
	if err != nil {
		t.Fatalf("Classify without an observer: %v", err)
	}
	if !got.IsCodingAgent() {
		t.Fatalf("classification without an observer = %+v, want the accepted positive", got)
	}

	field, ok := reflect.TypeFor[sessionclassification.ClassifierDeps]().FieldByName("Observer")
	if !ok {
		t.Fatal("ClassifierDeps has no bounded Observer dependency")
	}
	if field.Type.Kind() != reflect.Interface || field.Type.NumMethod() != 4 {
		t.Fatalf("ClassifierDeps.Observer is %s with %d methods, want a bounded four-method interface", field.Type, field.Type.NumMethod())
	}
}

func assertObservationCarriesNoSecret(t *testing.T, observation sessionclassification.EvaluationObservation, secrets ...string) {
	t.Helper()
	value := reflect.ValueOf(observation)
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.String {
			continue
		}
		rendered := field.String()
		for _, secret := range secrets {
			if secret != "" && strings.Contains(rendered, secret) {
				t.Fatalf("observation field %s retained hostile value %q: %q", typ.Field(i).Name, secret, rendered)
			}
		}
	}
}
