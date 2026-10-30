package sessionclassification_test

import (
	"context"
	"errors"
	"fmt"
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

// fakeAuthority resolves one process-owned store per classifier and counts how
// often the classifier asked for it.
type fakeAuthority struct {
	mu    sync.Mutex
	store sessionclassification.Store
	err   error
	calls int
}

func (a *fakeAuthority) ClassificationState() (sessionclassification.Store, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return a.store, nil
}

func (a *fakeAuthority) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// fakeStore is a minimal monotonic store: it keeps the first positive record
// per authority key, enforces the same single-lease attempt budget the real
// stores enforce, and counts every operation without retaining raw evidence.
type fakeStore struct {
	mu       sync.Mutex
	records  map[sessionclassification.Key]sessionclassification.Record
	loads    int
	promote  int
	promoted []sessionclassification.Record
	loadErr  error

	claims      int
	granted     int
	completions int
	claimErr    error
	completeErr error
	leaseSeq    uint64
	maxActive   int
	active      int
}

var _ sessionclassification.Store = (*fakeStore)(nil)

func newFakeStore() *fakeStore {
	return &fakeStore{records: make(map[sessionclassification.Key]sessionclassification.Record)}
}

func (s *fakeStore) Load(_ context.Context, key sessionclassification.Key) (sessionclassification.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.loadErr != nil {
		return sessionclassification.Record{}, false, s.loadErr
	}
	record, ok := s.records[key]
	return record, ok, nil
}

func (s *fakeStore) Promote(_ context.Context, key sessionclassification.Key, proposal session.Classification, now time.Time) (sessionclassification.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok && existing.Classification.IsCodingAgent() {
		return existing, false, nil
	}
	proposal.Revision = 1
	record := sessionclassification.Record{Key: key, Classification: proposal, UpdatedAt: now}
	s.records[key] = record
	s.promote++
	s.promoted = append(s.promoted, record)
	return record, true, nil
}

// ClaimRemote mirrors the store contract every implementation shares: it
// consumes one finite attempt, refuses while another lease is active or while a
// completion backoff has not elapsed, and never rewrites a positive. The fake
// keeps the same counters the real stores expose so a test can prove how many
// leases and egress calls one turn produced.
func (s *fakeStore) ClaimRemote(_ context.Context, key sessionclassification.Key, now time.Time, maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration) (sessionclassification.RemoteClaim, sessionclassification.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	if s.claimErr != nil {
		return sessionclassification.RemoteClaim{}, sessionclassification.Record{}, false, s.claimErr
	}
	record, exists := s.records[key]
	switch {
	case exists && record.Classification.IsCodingAgent():
		return sessionclassification.RemoteClaim{}, record, false, nil
	case exists && record.RemoteLeaseID != "" && now.Before(record.RemoteLeaseUntil):
		return sessionclassification.RemoteClaim{}, record, false, nil
	case exists && record.RemoteAttempts >= maxAttempts:
		return sessionclassification.RemoteClaim{}, record, false, nil
	case exists && !record.RemoteNextEligibleAt.IsZero() && now.Before(record.RemoteNextEligibleAt):
		return sessionclassification.RemoteClaim{}, record, false, nil
	}
	s.leaseSeq++
	record.Key = key
	record.RemoteAttempts++
	record.RemoteLeaseID = fmt.Sprintf("fake-lease-%d", s.leaseSeq)
	record.RemoteLeaseUntil = sessionclassification.RoundRemoteDeadlineUpToMicrosecond(now.Add(leaseTTL))
	record.UpdatedAt = now
	s.records[key] = record
	s.granted++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	claim := sessionclassification.RemoteClaim{
		Key: key, LeaseID: record.RemoteLeaseID, Attempt: record.RemoteAttempts, RetryBackoff: retryBackoff,
	}
	return claim, record, true, nil
}

// CompleteRemote accepts only the currently held lease, clears it, and starts
// the bounded completion backoff for a non-promoting result.
func (s *fakeStore) CompleteRemote(_ context.Context, claim sessionclassification.RemoteClaim, result sessionclassification.RemoteCompletion, now time.Time) (sessionclassification.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions++
	if s.completeErr != nil {
		return sessionclassification.Record{}, s.completeErr
	}
	record, exists := s.records[claim.Key]
	if !exists || record.RemoteLeaseID != claim.LeaseID || record.RemoteAttempts != claim.Attempt || !now.Before(record.RemoteLeaseUntil) {
		return record, sessionclassification.ErrStaleRemoteClaim
	}
	s.active--
	record.RemoteLeaseID = ""
	record.RemoteLeaseUntil = time.Time{}
	switch {
	case result.Proposal != (session.Classification{}) && !record.Classification.IsCodingAgent():
		result.Proposal.Revision = 1
		record.Classification = result.Proposal
		record.RemoteNextEligibleAt = time.Time{}
	case record.Classification.IsCodingAgent() || claim.RetryBackoff == 0:
		record.RemoteNextEligibleAt = time.Time{}
	default:
		record.RemoteNextEligibleAt = sessionclassification.RoundRemoteDeadlineUpToMicrosecond(now.Add(claim.RetryBackoff))
	}
	record.UpdatedAt = now
	s.records[claim.Key] = record
	return record, nil
}

func (s *fakeStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads, s.promote
}

// leaseCounts reports how many lease claims were granted and how many leases
// were simultaneously active, which is what proves single-flight remote work.
func (s *fakeStore) leaseCounts() (granted int, completions int, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.granted, s.completions, s.maxActive
}

// claimAttempts reports how many times the store was asked for a lease, whether
// or not it granted one. A canceled turn must not ask again after its context
// ended, so this is what distinguishes a released wait from an immediate
// surrender (requirements 6.7, 6.9).
func (s *fakeStore) claimAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

func (s *fakeStore) stored(key sessionclassification.Key) (sessionclassification.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	return record, ok
}

func mustClassifier(t *testing.T, cfg sessionclassification.Config, store *fakeStore, now func() time.Time) *sessionclassification.Classifier {
	t.Helper()
	return mustRemoteClassifier(t, cfg, store, now, nil)
}

// mustRemoteClassifier builds a generation classifier bound to the given decider.
// A remote-capable mode without a decider is refused by NewClassifier, so every
// jev or hybrid fixture supplies one and the decider's call count is the
// load-bearing instrument of the remote proofs (requirements 6.1, 6.2, 6.3).
func mustRemoteClassifier(
	t *testing.T,
	cfg sessionclassification.Config,
	store *fakeStore,
	now func() time.Time,
	decider sessionclassification.RemoteDecider,
) *sessionclassification.Classifier {
	t.Helper()
	classifier, err := sessionclassification.NewClassifier(cfg, sessionclassification.ClassifierDeps{
		State:  &fakeAuthority{store: store},
		Remote: decider,
		Now:    now,
	})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	return classifier
}

// countingDecider is a fake RemoteDecider that records every call and returns a
// scripted bounded answer or bounded failure. It exists so a test can prove the
// exact number of remote calls a mode, a claim outcome, or a retry budget
// produces without any network (requirements 6.7, 12.8, 12.9).
//
// A scripted answer may also gate on a channel, which is how a test observes
// that the durable lease is published before the call and released after it
// returns (requirement 6.6).
type countingDecider struct {
	mu       sync.Mutex
	calls    int
	inputs   []sessionclassification.RemoteInput
	answers  []scriptedAnswer
	fallback scriptedAnswer
	// entered receives just before each scripted answer is returned, so a test
	// can observe the in-flight window of the remote call.
	entered chan struct{}
	// release, when non-nil, blocks every call until it is closed. The lease must
	// already be published while a call is parked here.
	release <-chan struct{}
	// blockOnContext makes every call block until the caller's context ends and
	// return its error, which is what a real adapter's in-flight HTTP call does.
	blockOnContext bool
	// malformedOnEnd returns the scripted answer instead of the context error
	// after blockOnContext released. It models an adapter that answers with a
	// determined failure in the same window the caller canceled, which is the case
	// where the two signals must be told apart.
	malformedOnEnd bool
}

type scriptedAnswer struct {
	decision sessionclassification.RemoteDecision
	err      error
}

var _ sessionclassification.RemoteDecider = (*countingDecider)(nil)

// boundedFailure is a scripted remote failure carrying a closed-vocabulary
// outcome through the optional featurestate.RemoteFailure contract, which is
// how an adapter reports its failure without leaking error text into a label.
type boundedFailure struct {
	outcome sessionclassification.RemoteOutcome
	cause   error
}

func (f boundedFailure) Error() string { return "scripted remote failure" }
func (f boundedFailure) Unwrap() error { return f.cause }

func (f boundedFailure) Outcome() sessionclassification.RemoteOutcome { return f.outcome }

var _ sessionclassification.RemoteFailure = boundedFailure{}

func (d *countingDecider) Decide(ctx context.Context, in sessionclassification.RemoteInput) (sessionclassification.RemoteDecision, error) {
	d.mu.Lock()
	d.calls++
	d.inputs = append(d.inputs, in)
	answer := d.fallback
	if len(d.answers) > 0 {
		answer = d.answers[0]
		d.answers = d.answers[1:]
	}
	entered, release, blockOnContext, malformedOnEnd := d.entered, d.release, d.blockOnContext, d.malformedOnEnd
	d.mu.Unlock()
	// The lease must already be published and no store lock held while this call
	// is in flight, so the bookkeeping lock is released before anything blocks.
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	switch {
	case blockOnContext:
		<-ctx.Done()
		if malformedOnEnd {
			return answer.decision, answer.err
		}
		return sessionclassification.RemoteDecision{}, ctx.Err()
	case release != nil:
		<-release
	}
	return answer.decision, answer.err
}

func (d *countingDecider) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *countingDecider) observedInputs() []sessionclassification.RemoteInput {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]sessionclassification.RemoteInput(nil), d.inputs...)
}

func fixedNow() func() time.Time {
	instant := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	return func() time.Time { return instant }
}

// realNow is the process clock. It is used where lease deadlines must actually
// elapse, because the store anchors its retry backoff at the timestamp the
// classifier passes, and only a moving clock can release that deadline
// (requirements 6.7, 12.8).
func realNow() func() time.Time { return time.Now }

// validTestRemoteConfig mirrors the documented operational values for the
// remote modes. The credential stays a referenced environment name.
func validTestRemoteConfig() *sessionclassification.RemoteConfig {
	return &sessionclassification.RemoteConfig{
		Provider:              "jev",
		APIKeyEnv:             "TYPESAFE_API_KEY",
		Timeout:               750 * time.Millisecond,
		MaxAttemptsPerSession: 1,
		LeaseTTL:              2 * time.Second,
		RetryBackoff:          0,
		PositiveThreshold:     0.90,
	}
}

func codingInput(sessionID, userAgent string) sdkclassification.Input {
	return sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{ClientUserAgent: userAgent},
	}
}

func TestNewClassifierRejectsUnusableConfiguration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  sessionclassification.Config
		deps sessionclassification.ClassifierDeps
	}{
		{
			name: "missing state authority",
			cfg:  sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			deps: sessionclassification.ClassifierDeps{},
		},
		{
			name: "unknown mode",
			cfg:  sessionclassification.Config{Mode: sessionclassification.Mode("telepathy")},
			deps: sessionclassification.ClassifierDeps{State: &fakeAuthority{store: newFakeStore()}},
		},
		{
			name: "remote settings without remote mode",
			cfg: sessionclassification.Config{
				Mode:   sessionclassification.ModeHeuristic,
				Remote: &sessionclassification.RemoteConfig{Provider: "jev"},
			},
			deps: sessionclassification.ClassifierDeps{State: &fakeAuthority{store: newFakeStore()}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classifier, err := sessionclassification.NewClassifier(tc.cfg, tc.deps)
			if err == nil {
				t.Fatalf("NewClassifier(%+v) = %+v, want error", tc.cfg, classifier)
			}
			if classifier != nil {
				t.Fatalf("NewClassifier returned a classifier together with error %v", err)
			}
		})
	}
}

func TestNewClassifierNormalizesOmittedModeToHeuristic(t *testing.T) {
	t.Parallel()

	classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{}, sessionclassification.ClassifierDeps{
		State: &fakeAuthority{store: newFakeStore()},
	})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	got, err := classifier.Classify(t.Context(), codingInput("sess-mode", "codex_cli_rs/1.2.3"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !got.IsCodingAgent() {
		t.Fatalf("omitted-mode classification = %+v, want heuristic promotion", got)
	}
}

// TestClassifierIDIsTheRegisteredFeatureName pins the operator-facing feature
// name as a literal instead of echoing the package constant the getter returns.
// The ID is the plugin name an operator writes under plugins.features and the key
// the standard distribution registers, so the contract is the value itself.
func TestClassifierIDIsTheRegisteredFeatureName(t *testing.T) {
	t.Parallel()

	const registeredFeatureName = "session-classification"
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, newFakeStore(), fixedNow())
	if got := classifier.ID(); got != registeredFeatureName {
		t.Fatalf("classifier ID = %q, want the registered feature name %q", got, registeredFeatureName)
	}
	var plane sdkclassification.Classifier = classifier
	id, err := sdkclassification.ClassifierIdentity(plane)
	if err != nil {
		t.Fatalf("ClassifierIdentity: %v", err)
	}
	if id != registeredFeatureName {
		t.Fatalf("ClassifierIdentity = %q, want the registered feature name %q", id, registeredFeatureName)
	}
}

func TestClassifierHoldsOnlyBoundedGenerationFields(t *testing.T) {
	t.Parallel()

	// Requirements 6.1/6.2/10.5: the concrete classifier holds only bounded
	// generation policy, a lazy state authority, at most the frozen remote port
	// its mode requires, a copy of its validated remote settings, a bounded
	// observation sink, and a clock. It holds no HTTP client, no endpoint, no
	// credential, no per-session map, and no goroutine or timer, so composing or
	// running a heuristic generation cannot reach an external service.
	typ := reflect.TypeFor[sessionclassification.Classifier]()
	allowed := map[string]reflect.Type{
		"cfg":           reflect.TypeFor[sessionclassification.Config](),
		"state":         reflect.TypeFor[sessionclassification.StateAuthority](),
		"remoteDecider": reflect.TypeFor[sessionclassification.RemoteDecider](),
		"remote":        reflect.TypeFor[sessionclassification.RemoteConfig](),
		"observer":      reflect.TypeFor[sessionclassification.Observer](),
		"now":           reflect.TypeFor[func() time.Time](),
	}
	if typ.NumField() != len(allowed) {
		t.Fatalf("classifier has %d fields, want exactly %d bounded fields", typ.NumField(), len(allowed))
	}
	for field := range typ.Fields() {
		want, ok := allowed[field.Name]
		if !ok {
			t.Errorf("classifier has unapproved field %q of type %s", field.Name, field.Type)
			continue
		}
		if field.Type != want {
			t.Errorf("classifier.%s has type %s, want %s", field.Name, field.Type, want)
		}
	}
}

func TestClassifyPromotesDecisiveIdentityOnceAndReplaysIdempotently(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	in := codingInput("sess-codex", "codex_cli_rs/1.2.3")

	first, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.codex",
		Revision:   1,
	}
	if first != want {
		t.Fatalf("first classification = %+v, want %+v", first, want)
	}

	second, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("replayed Classify: %v", err)
	}
	if second != first {
		t.Fatalf("replayed classification = %+v, want identical %+v", second, first)
	}
	if _, promotes := store.counts(); promotes != 1 {
		t.Fatalf("durable promotions = %d, want exactly one first-accepted promotion", promotes)
	}
	key := sessionclassification.Key{Kind: sessionclassification.ScopeSecureSession, ID: "sess-codex"}
	record, ok := store.stored(key)
	if !ok {
		t.Fatal("promotion did not persist under the proxy-authority key")
	}
	if record.Classification != want {
		t.Fatalf("persisted classification = %+v, want %+v", record.Classification, want)
	}
	if strings.Contains(string(record.Classification.Evidence), "codex_cli_rs") {
		t.Fatalf("persisted evidence retained the raw User-Agent: %q", record.Classification.Evidence)
	}
}

func TestClassifyKeepsPriorPositiveWithoutTouchingProcessState(t *testing.T) {
	t.Parallel()

	authority := &fakeAuthority{store: newFakeStore()}
	classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, sessionclassification.ClassifierDeps{State: authority, Now: fixedNow()})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	prior := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh,
		Evidence:   "tooling.distinct_coding_cluster",
		Revision:   7,
	}
	in := codingInput("sess-prior", "codex_cli_rs/1.2.3")
	in.Session.Classification = prior

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != prior {
		t.Fatalf("classification = %+v, want the preserved positive %+v", got, prior)
	}
	if calls := authority.callCount(); calls != 0 {
		t.Fatalf("warm positive resolved process state %d times, want zero", calls)
	}
}

func TestClassifyWithoutProxyAuthorityKeepsCurrentSnapshot(t *testing.T) {
	t.Parallel()

	authority := &fakeAuthority{store: newFakeStore()}
	classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, sessionclassification.ClassifierDeps{State: authority, Now: fixedNow()})
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	in := codingInput("", "codex_cli_rs/1.2.3")
	in.Session = session.SessionView{ClientSessionHint: "client-controlled-hint"}

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown without proxy authority", got)
	}
	if calls := authority.callCount(); calls != 0 {
		t.Fatalf("classifier consulted process state %d times without authority, want zero", calls)
	}
}

func TestClassifyUsesALegAuthorityFallback(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	in := sdkclassification.Input{
		Session:  session.SessionView{ALegID: "a-leg-42", ClientSessionHint: "shared-hint"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "roo-code/1.0.0"},
	}

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Source != session.SourceLocalIdentity || got.Evidence != "client_family.roo" {
		t.Fatalf("classification = %+v, want A-leg scoped roo identity promotion", got)
	}
	if _, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "a-leg-42"}); !ok {
		t.Fatal("A-leg promotion did not persist under the proxy-owned A-leg key")
	}
	if _, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "shared-hint"}); ok {
		t.Fatal("client-controlled session hint was used as a state authority")
	}
}

func TestClassifyModeControlsWhichLocalEvidenceMayPromote(t *testing.T) {
	t.Parallel()

	distinctCluster := sdkclassification.ToolCategoryFileRead |
		sdkclassification.ToolCategoryFileEdit |
		sdkclassification.ToolCategoryOSCommand

	cases := []struct {
		name        string
		mode        sessionclassification.Mode
		wantPromote bool
		wantSource  session.ClassificationSource
		wantCode    session.EvidenceCode
	}{
		{name: "heuristic promotes decisive local identity", mode: sessionclassification.ModeHeuristic, wantPromote: true, wantSource: session.SourceLocalIdentity, wantCode: "client_family.codex"},
		{name: "jev never promotes from local evidence alone", mode: sessionclassification.ModeJev, wantPromote: false},
		{name: "hybrid promotes decisive local identity", mode: sessionclassification.ModeHybrid, wantPromote: true, wantSource: session.SourceLocalIdentity, wantCode: "client_family.codex"},
		{name: "heuristic promotes the distinct coding cluster", mode: sessionclassification.ModeHeuristic, wantPromote: true, wantSource: session.SourceLocalTooling, wantCode: "tooling.distinct_coding_cluster"},
		{name: "hybrid promotes the distinct coding cluster", mode: sessionclassification.ModeHybrid, wantPromote: true, wantSource: session.SourceLocalTooling, wantCode: "tooling.distinct_coding_cluster"},
		{name: "jev keeps the distinct coding cluster unknown", mode: sessionclassification.ModeJev, wantPromote: false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			cfg := sessionclassification.Config{Mode: tc.mode}
			// The decider variable keeps its concrete type so the call count can be read
			// after the fixture, while a heuristic generation must receive a truly
			// nil interface: a typed nil would be a non-nil interface value and
			// would be refused as a network-capable dependency (requirement 6.2).
			decider := &countingDecider{fallback: scriptedAnswer{
				decision: sessionclassification.RemoteDecision{CodingProbability: 0.2},
			}}
			var bound sessionclassification.RemoteDecider
			if tc.mode == sessionclassification.ModeJev || tc.mode == sessionclassification.ModeHybrid {
				cfg.Remote = validTestRemoteConfig()
				bound = decider
			}
			classifier := mustRemoteClassifier(t, cfg, store, fixedNow(), bound)
			userAgent := ""
			if tc.wantSource == session.SourceLocalIdentity {
				userAgent = "codex_cli_rs/1.2.3"
			}
			in := sdkclassification.Input{
				Session: session.SessionView{ALegID: "a-leg-mode-" + string(rune('a'+i))},
				Evidence: sdkclassification.Evidence{
					ClientUserAgent: userAgent,
					ToolCategories:  distinctCluster,
					Operation:       aipapi.OperationOpenAIResponses,
				},
			}

			got, err := classifier.Classify(t.Context(), in)
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			// Requirement 6.2: a mode whose local evidence promotes makes no
			// remote call at all; requirement 6.3: a jev turn whose decisive local
			// evidence is not enough still consults the remote decider.
			wantCalls := 0
			if tc.mode == sessionclassification.ModeJev {
				wantCalls = 1
			} else if tc.mode == sessionclassification.ModeHybrid && !tc.wantPromote {
				wantCalls = 1
			}
			if calls := decider.callCount(); calls != wantCalls {
				t.Fatalf("remote calls = %d, want %d for mode %q", calls, wantCalls, tc.mode)
			}
			if !tc.wantPromote {
				if got != (session.Classification{}) {
					t.Fatalf("classification = %+v, want unknown", got)
				}
				if _, promotes := store.counts(); promotes != 0 {
					t.Fatalf("durable promotions = %d, want zero", promotes)
				}
				return
			}
			if !got.IsCodingAgent() || got.Source != tc.wantSource || got.Evidence != tc.wantCode || got.Revision != 1 {
				t.Fatalf("classification = %+v, want %s/%s at revision 1", got, tc.wantSource, tc.wantCode)
			}
		})
	}
}

func TestClassifyReturnsPersistedPositiveForLaterWeakTurns(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	strong := codingInput("sess-persist", "codex_cli_rs/1.2.3")
	if _, err := classifier.Classify(t.Context(), strong); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	// A later turn that shares the client hint but carries no decisive evidence
	// still projects the authoritative positive value.
	weak := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-persist", ClientSessionHint: "unrelated-hint"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "Mozilla/5.0"},
	}
	got, err := classifier.Classify(t.Context(), weak)
	if err != nil {
		t.Fatalf("later Classify: %v", err)
	}
	if !got.IsCodingAgent() || got.Evidence != "client_family.codex" {
		t.Fatalf("later classification = %+v, want the stored positive snapshot", got)
	}
}

func TestClassifyFailsOpenWhenProcessStateIsUnavailable(t *testing.T) {
	t.Parallel()

	loadFailure := errors.New("durable load failed")
	stateFailure := errors.New("state holder closed")
	failing := newFakeStore()
	cases := []struct {
		name      string
		authority *fakeAuthority
		storeErr  error
		want      session.Classification
	}{
		{name: "uninitialized state authority", authority: &fakeAuthority{err: stateFailure}},
		{name: "durable load failure", authority: &fakeAuthority{store: failing}, storeErr: loadFailure},
	}
	for _, tc := range cases {
		if tc.storeErr != nil {
			// The injected failure must be the one the authority's own store
			// reports, so the store under test is wired before the fixture runs.
			failing.loadErr = tc.storeErr
		}
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			classifier, err := sessionclassification.NewClassifier(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, sessionclassification.ClassifierDeps{State: tc.authority, Now: fixedNow()})
			if err != nil {
				t.Fatalf("NewClassifier: %v", err)
			}
			got, err := classifier.Classify(t.Context(), codingInput("sess-failopen", "codex_cli_rs/1.2.3"))
			if err == nil {
				t.Fatalf("Classify error = nil, want a bounded state failure")
			}
			if got != tc.want {
				t.Fatalf("classification = %+v, want unknown after fail-open", got)
			}
		})
	}
}

// TestClassifyKeepsAcceptedPositiveWhenStateIsUnavailable proves an accepted
// positive is never downgraded or replaced by a state failure: the state holder is
// wired to fail, and the turn still returns the accepted snapshot unchanged
// without resolving it (requirements 1.4, 6.5).
//
// The unavailable state holder is the point of the fixture. An earlier version
// injected a durable load failure instead, but the warm-positive short-circuit
// returns before any store call, so that fixture could never be reached and the
// test only repeated TestClassifyKeepsPriorPositiveWithoutTouchingProcessState.
func TestClassifyKeepsAcceptedPositiveWhenStateIsUnavailable(t *testing.T) {
	t.Parallel()

	authority := &fakeAuthority{err: errors.New("state holder closed")}
	classifier, err := sessionclassification.NewClassifier(
		sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
		sessionclassification.ClassifierDeps{State: authority, Now: fixedNow()},
	)
	if err != nil {
		t.Fatalf("NewClassifier: %v", err)
	}
	prior := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceRemote,
		Confidence: session.ConfidenceHigh,
		Evidence:   "client_family.opencode",
		Revision:   1,
	}
	in := codingInput("sess-remote", "codex_cli_rs/1.2.3")
	in.Session.Classification = prior

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != prior {
		t.Fatalf("classification = %+v, want the preserved prior positive %+v", got, prior)
	}
	if calls := authority.callCount(); calls != 0 {
		t.Fatalf("warm positive resolved process state %d times, want zero", calls)
	}
}

func TestClassifyRequiresContext(t *testing.T) {
	t.Parallel()

	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, newFakeStore(), fixedNow())
	got, err := classifier.Classify(nil, codingInput("sess-nilctx", "codex_cli_rs/1.2.3")) //nolint:staticcheck // The nil-context case is the behavior under test.
	if err == nil {
		t.Fatal("Classify with a nil context returned no error")
	}
	if got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown", got)
	}
}

func TestClassifyNeverConstructsDurableNegativeState(t *testing.T) {
	t.Parallel()

	store := newFakeStore()
	classifier := mustClassifier(t, sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, store, fixedNow())
	in := sdkclassification.Input{
		Session:   session.SessionView{ALegID: "a-leg-chat"},
		Evidence:  sdkclassification.Evidence{ClientUserAgent: "openai-python/1.40.0", ToolCategories: sdkclassification.ToolCategoryWebAccess},
		Workspace: workspace.WorkspaceView{Markers: []string{"go.mod"}},
	}

	got, err := classifier.Classify(t.Context(), in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("technical-chat classification = %+v, want unknown", got)
	}
	if _, ok := store.stored(sessionclassification.Key{Kind: sessionclassification.ScopeALeg, ID: "a-leg-chat"}); ok {
		t.Fatal("unknown evidence created a durable negative classification row")
	}
	if loads, promotes := store.counts(); promotes != 0 {
		t.Fatalf("durable promotions = %d, want zero", promotes)
	} else if loads != 1 {
		t.Fatalf("durable loads = %d, want exactly one authoritative load per unknown turn", loads)
	}
}

func TestClassifyHonorsGenerationScopedExclusions(t *testing.T) {
	t.Parallel()

	excluded := mustClassifier(t, sessionclassification.Config{
		Mode:      sessionclassification.ModeHeuristic,
		Heuristic: sessionclassification.HeuristicConfig{IgnoredUserAgentPrefixes: []string{"codex_cli_rs"}},
	}, newFakeStore(), fixedNow())
	got, err := excluded.Classify(t.Context(), codingInput("sess-excluded", "codex_cli_rs/1.2.3"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != (session.Classification{}) {
		t.Fatalf("excluded classification = %+v, want unknown", got)
	}
}
