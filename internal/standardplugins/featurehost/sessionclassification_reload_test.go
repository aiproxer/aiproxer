package featurehost

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// certificationHybridRemoteYAML is a valid remote-rule reload posture. It is
// built while composing the candidate generation (never at request time).
// Requirement 6.10 means the referenced credential must resolve during that
// composition step. The test therefore supplies a temporary value only for
// publication, then removes it before any remote-eligible turn (requirements
// 6.10, 7.1, 7.5, 12.9).
const certificationHybridRemoteYAML = "mode: hybrid\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n" +
	"  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n"

// certificationCountingTransport counts requests made through the package
// default HTTP client. It remains a useful auxiliary guard, but it is not the
// hermeticity proof for the Jev path: the remote adapter uses a dedicated
// client, so the proof is the missing credential, which refuses the attempt
// before any transport is used.
type certificationCountingTransport struct{ calls atomic.Int32 }

func (tr *certificationCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.calls.Add(1)
	return nil, errors.New("session classification reload must not perform an external request")
}

// certificationGuardGate is a secret guard that holds the turn between
// secret-guard processing and the classification stage.
//
// That position is deliberate: the canonical stage order is secret guard, then
// session classification, then submit. Parking a turn here makes it genuinely
// in-flight while a new generation is published, so the classification stage that
// follows is still the one the turn was admitted with.
type certificationGuardGate struct {
	id      string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func certificationNewGuardGate(id string) *certificationGuardGate {
	return &certificationGuardGate{id: id, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *certificationGuardGate) ID() string                           { return g.id }
func (g *certificationGuardGate) Order() int                           { return 0 }
func (g *certificationGuardGate) FailureMode() secretguard.FailureMode { return secretguard.FailOpen }

func (g *certificationGuardGate) Evaluate(
	context.Context,
	*aipapi.Call,
	secretguard.Meta,
	secretguard.Services,
) (secretguard.Decision, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

// certificationPlanesWithGate extends a compiled generation's plane set with the
// test-only secret guard that parks the turn before classification.
func certificationPlanesWithGate(t *testing.T, planes aipfeature.FrozenPlaneSet, gate secretguard.Guard) aipfeature.FrozenPlaneSet {
	t.Helper()
	contributions := planes.ToContributions()
	if err := aipfeature.Contribute(contributions, aipfeature.PlaneSecretGuards, gate.ID(), []secretguard.Guard{gate}); err != nil {
		t.Fatalf("contribute %q secret guard: %v", gate.ID(), err)
	}
	return contributions.Freeze()
}

// certificationClassify evaluates one bounded turn directly against a published
// generation's classifier plane value. It is the same call the generic stage
// makes, so it certifies the generation's immutable policy and the shared
// process state without inventing a decision path.
func certificationClassify(
	t *testing.T,
	classifier sdkclassification.Classifier,
	sessionID string,
	userAgent string,
) session.Classification {
	t.Helper()
	if classifier == nil {
		t.Fatal("no classifier plane value to evaluate")
	}
	got, err := classifier.Classify(context.Background(), sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: sessionID},
		Evidence: sdkclassification.Evidence{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: userAgent,
		},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	return got
}

// TestSessionClassificationReloadKeepsProcessStateAndDurableRows certifies
// requirements 8.5, 8.6, 8.7, 8.8 and 2.10 against one real process and one real
// on-disk database: reloading heuristic rules, then remote rules, then disabling,
// then re-enabling must change the generation's policy only.
//
// Every step asserts three separate things, because they fail independently:
// the process-owned coordinator is the same object (no state was recreated), the
// durable row is byte-identical (no state was erased), and the reloaded
// generation's own policy is genuinely different (the reload was not a no-op).
func TestSessionClassificationReloadKeepsProcessStateAndDurableRows(t *testing.T) {
	// Serial: this case observes the default HTTP transport and controls one
	// referenced credential for the whole reload sequence. The credential starts
	// absent. It is supplied only long enough to publish the remote-rule
	// generation, then removed before any remote-eligible classification, so the
	// suite cannot reach an external service.
	t.Setenv("TYPESAFE_API_KEY", "")
	originalTransport := http.DefaultTransport
	transport := &certificationCountingTransport{}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-reload.db"))
	database := certificationOpenSQLite(t, dsn)
	registry := prometheus.NewRegistry()
	rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: database, MetricsRegistry: registry})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Errorf("close process: %v", err)
		}
	})

	// ---- baseline generation promotes a session ----
	baseline := certificationCompile(t, rt, true, "")
	if baseline.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	baseline.start(t)
	coordinator := rt.sessionClassification.Coordinator()
	if coordinator == nil {
		t.Fatal("enabled generation lifecycle did not initialize process classification state")
	}

	const promotedSession = "session-reload-promoted"
	promoted := certificationClassify(t, baseline.classifier, promotedSession, certificationPositiveUserAgent)
	if !promoted.IsCodingAgent() {
		t.Fatalf("baseline classification = %+v, want a positive promotion", promoted)
	}
	persisted, found := certificationLoadRow(t, database, promotedSession)
	if !found || persisted.Classification != promoted {
		t.Fatalf("durable row after promotion = %+v (found=%t), want %+v", persisted.Classification, found, promoted)
	}
	if got := certificationEvaluations(t, registry); got["heuristic/promoted"] != 1 {
		t.Fatalf("baseline evaluations = %v, want exactly one heuristic/promoted", got)
	}

	// ---- reload 1: changed heuristic rules ----
	reloadedHeuristic := certificationCompile(t, rt, true, certificationExcludeCodex)
	if reloadedHeuristic.classifier == nil {
		t.Fatal("reloaded generation published no classifier")
	}
	if reloadedHeuristic.classifier == baseline.classifier {
		t.Fatal("the reloaded generation reused the previous generation's classifier policy object")
	}
	reloadedHeuristic.start(t)
	assertReloadKeptProcessState(t, rt, coordinator, database, promotedSession, persisted)
	if got := certificationClassify(t, reloadedHeuristic.classifier, promotedSession, ""); got != promoted {
		t.Fatalf("reloaded generation lost the persisted positive: got %+v, want %+v", got, promoted)
	}
	// The reloaded policy is genuinely different: a new session carrying the now
	// excluded identity stays unknown and creates no durable state.
	const excludedSession = "session-reload-excluded"
	if got := certificationClassify(t, reloadedHeuristic.classifier, excludedSession, certificationPositiveUserAgent); got != (session.Classification{}) {
		t.Fatalf("excluded identity classified %+v under the reloaded rules, want unknown", got)
	}
	if _, found := certificationLoadRow(t, database, excludedSession); found {
		t.Fatal("an excluded identity created a durable classification row")
	}
	// The baseline generation's own policy is unchanged for a turn already
	// admitted to it (requirement 8.5).
	if got := certificationClassify(t, baseline.classifier, "session-reload-baseline-still-live", certificationPositiveUserAgent); !got.IsCodingAgent() {
		t.Fatalf("baseline generation policy changed by a reload: %+v", got)
	}

	// ---- reload 2: changed remote rules ----
	// Supply the referenced credential only for the composition step that
	// requirement 6.10 checks.
	t.Setenv("TYPESAFE_API_KEY", "reload-publication-only-token")
	reloadedRemote := certificationCompile(t, rt, true, certificationHybridRemoteYAML)
	if reloadedRemote.classifier == nil {
		t.Fatal("remote-rule reload published no classifier")
	}
	if reloadedRemote.classifier == reloadedHeuristic.classifier {
		t.Fatal("the remote-rule reload reused the previous generation's classifier policy object")
	}
	// Remove the credential immediately after publication. The following
	// remote-eligible turn must therefore exercise the documented
	// post-publication path: acquire a lease, refuse the remote attempt before
	// HTTP, complete the lease, and remain unknown with zero egress.
	t.Setenv("TYPESAFE_API_KEY", "")
	reloadedRemote.start(t)
	assertReloadKeptProcessState(t, rt, coordinator, database, promotedSession, persisted)
	// A restored positive is returned from durable state before any remote attempt
	// could be made (requirement 6.5: an established positive is never sent out).
	if got := certificationClassify(t, reloadedRemote.classifier, promotedSession, ""); got != promoted {
		t.Fatalf("remote-rule reload lost the persisted positive: got %+v, want %+v", got, promoted)
	}
	// The remote rules are live: an eligible unknown session takes a durable lease
	// and then fails open to unknown because the publication-only credential was
	// removed before this turn. That ordering refuses the attempt before HTTP, so
	// no egress can happen.
	const remoteEligibleSession = "session-reload-remote-eligible"
	if got := certificationClassify(t, reloadedRemote.classifier, remoteEligibleSession, certificationWeakUserAgent); got != (session.Classification{}) {
		t.Fatalf("remote-eligible unknown session classified %+v, want unknown without a remote positive", got)
	}
	operations := certificationStoreOperations(t, registry)
	if operations["remote_claim/applied"] != 1 {
		t.Fatalf("remote lease observations = %v, want exactly one applied claim proving the reloaded remote rules ran", operations)
	}
	completions := 0
	for key, value := range operations {
		if strings.HasPrefix(key, "remote_complete/") {
			completions += int(value)
		}
	}
	if completions != 1 {
		t.Fatalf("remote completion observations = %v, want the granted lease to be finished exactly once", operations)
	}
	// The durable row the lease allocated carries remote-control state only: no
	// durable classification may exist for a failed remote decision (requirements
	// 1.5, 6.8, 6.9).
	remoteRow, found := certificationLoadRow(t, database, remoteEligibleSession)
	if found && remoteRow.Classification != (session.Classification{}) {
		t.Fatalf("a failed remote decision persisted %+v, want no durable classification", remoteRow.Classification)
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("reload sequence performed %d external requests, want none", got)
	}
	assertReloadKeptProcessState(t, rt, coordinator, database, promotedSession, persisted)

	// ---- disable: the plane is withdrawn, nothing is deleted ----
	disabled := certificationCompile(t, rt, false, "")
	if disabled.classifier != nil {
		t.Fatalf("disabled generation published classifier %v", disabled.classifier)
	}
	// Production retires the outgoing generation after the new one is published,
	// so the retiring lifecycle's Stop is part of the disable window. It must
	// release only the generation's reference, never the durable rows.
	//
	// Every live generation is stopped before the assertions below, so the
	// process-owned owner count actually reaches zero and the LAST release is
	// exercised. Releasing only some generations would leave a reference alive,
	// and a coordinator disposed only on the final release would then slip past
	// this whole test (requirements 8.7, 8.8: shared process state must survive
	// every release and exist only while the process owns it).
	reloadedRemote.stop(t)
	reloadedHeuristic.stop(t)
	baseline.stop(t)
	assertReloadKeptProcessState(t, rt, coordinator, database, promotedSession, persisted)
	if got := aipfeature.Get(disabled.planes, aipfeature.PlaneSessionClassifier); got != nil {
		t.Fatalf("disabled generation classifier plane = %v, want none", got)
	}

	// ---- re-enable: the persisted positive is recoverable again ----
	reEnabled := certificationCompile(t, rt, true, "")
	if reEnabled.classifier == nil {
		t.Fatal("re-enabled generation published no classifier")
	}
	reEnabled.start(t)
	assertReloadKeptProcessState(t, rt, coordinator, database, promotedSession, persisted)
	if got := certificationClassify(t, reEnabled.classifier, promotedSession, ""); got != promoted {
		t.Fatalf("re-enabled classification = %+v, want the recovered durable positive %+v", got, promoted)
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("disable/re-enable sequence performed %d external requests, want none", got)
	}

	// The whole sequence restored the positive repeatedly without ever advancing
	// its revision or rewriting the row (requirements 2.6, 2.10).
	final, found := certificationLoadRow(t, database, promotedSession)
	if !found || final != persisted {
		t.Fatalf("durable row after the full reload sequence = %+v (found=%t), want it unchanged at %+v", final.Classification, found, persisted.Classification)
	}
}

// assertReloadKeptProcessState is the shared "reload changed policy, not state"
// assertion: the process-owned coordinator is unchanged and the durable row is
// byte-identical.
func assertReloadKeptProcessState(
	t *testing.T,
	rt *Runtime,
	coordinator *hostclassification.Coordinator,
	database *bun.DB,
	sessionID string,
	persisted featurestate.Record,
) {
	t.Helper()
	if got := rt.sessionClassification.Coordinator(); got != coordinator {
		t.Fatalf("process-owned classification coordinator changed across reload/disable: %p -> %p", coordinator, got)
	}
	row, found := certificationLoadRow(t, database, sessionID)
	if !found || row != persisted {
		t.Fatalf("durable row across reload/disable = %+v (found=%t), want it unchanged at %+v", row.Classification, found, persisted.Classification)
	}
}

// TestSessionClassificationInFlightTurnKeepsAdmittedGenerationPolicy certifies
// the in-flight half of requirement 8.5: a request admitted by one generation
// completes under that generation's immutable classifier policy even when a new
// generation is published while the request is in flight, while a request admitted
// afterwards uses the new policy.
//
// The turn is parked between secret-guard processing and the classification stage,
// so the reload happens while the request is genuinely mid-flight and the
// classification decision it receives can only come from its admitted generation.
func TestSessionClassificationInFlightTurnKeepsAdmittedGenerationPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-in-flight.db"))
	database := certificationOpenSQLite(t, dsn)
	rt, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: database})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Errorf("close process: %v", err)
		}
	})

	// Generation 1 admits a turn whose identity rule promotes.
	admitted := certificationCompile(t, rt, true, "")
	if admitted.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	admitted.start(t)

	gate := certificationNewGuardGate("certification-in-flight-gate")
	submitBarrier := certificationNewSubmitBarrier("certification-in-flight-submit")
	inFlightConsumer := &certificationConsumer{id: "certification-in-flight-consumer", barrier: submitBarrier}
	inFlightExecutor, inFlightOpens := certificationExecutor(t, database,
		certificationPlanesWithGate(t,
			certificationPlanesWithConsumer(t, admitted.planes, inFlightConsumer), gate),
		submitBarrier)

	inFlightSession, inFlightToken := certificationResumableSession(t, inFlightExecutor, "client-in-flight")

	type turnResult struct {
		err error
	}
	done := make(chan turnResult, 1)
	go func() {
		stream, err := inFlightExecutor.Execute(ctx,
			certificationCall("client-in-flight", inFlightToken, certificationPositiveUserAgent))
		if err == nil {
			_, err = aipapi.Collect(ctx, stream)
		}
		done <- turnResult{err: err}
	}()

	select {
	case <-gate.entered:
	case <-time.After(certificationBarrierTimeout):
		t.Fatal("the in-flight turn never reached the stage before classification")
	}

	// Publish the new generation while the request is in flight.
	reloaded := certificationCompile(t, rt, true, certificationExcludeCodex)
	if reloaded.classifier == nil {
		t.Fatal("reloaded generation published no classifier")
	}
	if reloaded.classifier == admitted.classifier {
		t.Fatal("the reloaded generation reused the in-flight generation's classifier policy object")
	}
	reloaded.start(t)
	if rt.sessionClassification.Coordinator() == nil {
		t.Fatal("reload destroyed the process-owned classification state")
	}

	afterSubmit := certificationNewSubmitBarrier("certification-after-reload-submit")
	afterReloadConsumer := &certificationConsumer{id: "certification-after-reload-consumer", barrier: afterSubmit}
	afterReloadExecutor, _ := certificationExecutor(t, database,
		certificationPlanesWithConsumer(t, reloaded.planes, afterReloadConsumer), afterSubmit)

	// Let the in-flight turn continue into its classification stage.
	close(gate.release)
	result := <-done
	if result.err != nil {
		t.Fatalf("the in-flight turn failed after the reload: %v", result.err)
	}
	if got := inFlightOpens.Load(); got != 1 {
		t.Fatalf("in-flight turn opened the backend %d times, want exactly 1", got)
	}

	// The in-flight turn completed under its admitted generation's policy.
	observed, sessions, timedOut := inFlightConsumer.saw()
	if timedOut {
		t.Fatal("the in-flight turn's consumer never observed the earlier submit stage")
	}
	if len(observed) != 1 || sessions[0] != inFlightSession {
		t.Fatalf("in-flight consumer observations = %v for sessions %v, want one observation for %q", observed, sessions, inFlightSession)
	}
	if !observed[0].IsCodingAgent() || observed[0].Source != session.SourceLocalIdentity {
		t.Fatalf("in-flight classification = %+v, want the admitted generation's identity-rule promotion", observed[0])
	}
	if submitObserved, _ := submitBarrier.saw(); len(submitObserved) != 1 || submitObserved[0] != observed[0] {
		t.Fatalf("in-flight submit-stage classification = %+v, want the same snapshot %+v", submitObserved, observed[0])
	}
	persisted, found := certificationLoadRow(t, database, inFlightSession)
	if !found || persisted.Classification != observed[0] {
		t.Fatalf("durable row for the in-flight session = %+v (found=%t), want the in-flight decision %+v", persisted.Classification, found, observed[0])
	}

	// A turn admitted after the reload uses the new policy instead: the same
	// identity is now excluded, so the reloaded generation must not promote it.
	afterSession, afterToken := certificationResumableSession(t, afterReloadExecutor, "client-after-reload")
	certificationServe(t, afterReloadExecutor,
		certificationCall("client-after-reload", afterToken, certificationPositiveUserAgent))
	afterObserved, afterSessions, afterTimedOut := afterReloadConsumer.saw()
	if afterTimedOut {
		t.Fatal("the post-reload turn's consumer never observed the earlier submit stage")
	}
	if len(afterObserved) != 1 || afterSessions[0] != afterSession {
		t.Fatalf("post-reload consumer observations = %v for sessions %v, want one observation for %q", afterObserved, afterSessions, afterSession)
	}
	if afterObserved[0] != (session.Classification{}) {
		t.Fatalf("post-reload classification = %+v, want unknown: the reloaded exclusion must apply to newly admitted turns", afterObserved[0])
	}
	if _, found := certificationLoadRow(t, database, afterSession); found {
		t.Fatal("an excluded post-reload turn created a durable classification row")
	}
}
