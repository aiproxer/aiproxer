package runtime

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// classificationFailureCase is one entry of the task 6.3 failure matrix. Each
// case injects a failure at the seam that exists today — the public SDK
// Classifier contract or the standard classifier's process state authority —
// which is where a canceled context, a durable state failure, a remote-decision
// failure and an invalid result all surface. No remote adapter is required or
// built: the remote decider is owned by task 8.x.
//
// Every case returns a countingFailureClassifier, so each case can assert that
// its failure injection was actually reached. A case whose classifier is never
// consulted would otherwise pass vacuously.
type classificationFailureCase struct {
	name string
	// newClassifier builds the failing classifier together with the durable
	// state probe that records every classification state row.
	newClassifier func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe)
}

var errClassificationStateUnavailable = errors.New("classification durable state unavailable")

// countingFailureClassifier records how many classification requests reached
// the classifier. It is what makes each matrix case non-vacuous: a case that
// asserts a failure was handled must also prove the failure was injected.
type countingFailureClassifier struct {
	inner sessionclassification.Classifier

	mu    sync.Mutex
	calls int
}

var _ sessionclassification.Classifier = (*countingFailureClassifier)(nil)

func (c *countingFailureClassifier) ID() string { return c.inner.ID() }

func (c *countingFailureClassifier) Classify(ctx context.Context, in sessionclassification.Input) (session.Classification, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Classify(ctx, in)
}

func (c *countingFailureClassifier) invocationCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// newRealClassifierWithAuthority binds the real standard-feature classifier to
// an arbitrary process-state authority, so the durable-state failures below
// exercise the production decision path rather than a stand-in.
func newRealClassifierWithAuthority(t *testing.T, authority featurestate.StateAuthority) sessionclassification.Classifier {
	t.Helper()
	classifier, err := featurestate.NewClassifier(featurestate.Config{Mode: featurestate.ModeHeuristic}, featurestate.ClassifierDeps{
		State: authority,
	})
	if err != nil {
		t.Fatalf("new real session classifier: %v", err)
	}
	return &countingClassifier{inner: classifier}
}

// countFailure wraps classifier so a matrix case can assert how many times its
// failure injection was actually reached.
func countFailure(classifier sessionclassification.Classifier) *countingFailureClassifier {
	return &countingFailureClassifier{inner: classifier}
}

func classificationFailureCases() []classificationFailureCase {
	return []classificationFailureCase{
		{
			name: "remote decision failure",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				// The remote decider is deliberately unwired until task 8.3, so
				// its failure is expressed at the seam it will use: the
				// classifier surfaces the remote failure as an error and the
				// generic stage must fail open. The standalone-decoder shapes
				// requirement 12.8 calls out (timeout, 429, 5xx, malformed or
				// unmappable output) are all the same contract: a bounded error
				// and no positive.
				return countFailure(newSpySessionClassifier("classification-remote-failure", func(context.Context, sessionclassification.Input) (session.Classification, error) {
					return session.Classification{}, errors.New("remote classification decision failed")
				})), probe
			},
		},
		{
			name: "durable state failure",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				return countFailure(newRealClassifierWithAuthority(t, probeStateAuthority{err: errClassificationStateUnavailable})), probe
			},
		},
		{
			name: "durable state read failure",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				probe.failLoads(errClassificationStateUnavailable)
				return countFailure(newRealClassifierWithAuthority(t, probeStateAuthority{store: probe})), probe
			},
		},
		{
			name: "durable state write failure",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				probe.failWrites(errClassificationStateUnavailable)
				return countFailure(newRealClassifierWithAuthority(t, probeStateAuthority{store: probe})), probe
			},
		},
		{
			name: "canceled classification context",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				// Cancellation is observed by the classifier itself: a real
				// remote or durable path that sees its context already canceled
				// reports the cancellation rather than a decision. Returning
				// ctx.Err() unconditionally would be vacuous — this turn's
				// context is live, so ctx.Err() is nil and the case would
				// silently degrade into a plain unknown result. The dedicated
				// TestSessionClassificationCanceledContextSkipsClassifier pins
				// the actually-canceled context separately.
				return countFailure(newSpySessionClassifier("classification-canceled", func(ctx context.Context, _ sessionclassification.Input) (session.Classification, error) {
					if err := ctx.Err(); err != nil {
						return session.Classification{}, err
					}
					return session.Classification{}, context.DeadlineExceeded
				})), probe
			},
		},
		{
			name: "invalid positive classifier result",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				// A coding-agent kind without the bounded source, confidence
				// band, evidence code and revision cannot validate.
				return countFailure(newSpySessionClassifier("classification-invalid-positive", func(context.Context, sessionclassification.Input) (session.Classification, error) {
					return session.Classification{Kind: session.KindCodingAgent}, nil
				})), probe
			},
		},
		{
			name: "invalid unknown classifier result",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				// A malformed unknown is not the all-zero value, so it must be
				// rejected rather than silently treated as unknown.
				return countFailure(newSpySessionClassifier("classification-invalid-unknown", func(context.Context, sessionclassification.Input) (session.Classification, error) {
					return session.Classification{Kind: session.KindUnknown, Revision: 3}, nil
				})), probe
			},
		},
		{
			name: "unsupported classifier kind",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				return countFailure(newSpySessionClassifier("classification-invalid-kind", func(context.Context, sessionclassification.Input) (session.Classification, error) {
					return session.Classification{
						Kind:       session.Kind("autonomous_agent"),
						Source:     session.SourceLocalIdentity,
						Confidence: session.ConfidenceHigh,
						Evidence:   "invalid.kind",
						Revision:   1,
					}, nil
				})), probe
			},
		},
		{
			name: "classifier panic",
			newClassifier: func(t *testing.T) (*countingFailureClassifier, *classificationStateProbe) {
				t.Helper()
				probe := newClassificationStateProbe(t)
				return countFailure(newSpySessionClassifier("classification-panic", func(context.Context, sessionclassification.Input) (session.Classification, error) {
					panic("classifier panic sentinel")
				})), probe
			},
		},
	}
}

// TestSessionClassificationFailureMatrixFailsOpen proves the requirement 4.4
// matrix over the seams that exist today. For every failure the user request is
// still admitted, every later same-turn consumer still runs, the session stays
// conservatively unknown, and no durable classification row is created.
//
// Each case also asserts its failure injection was actually reached
// (invocationCount >= 1), so a case cannot pass by never consulting the
// classifier at all.
func TestSessionClassificationFailureMatrixFailsOpen(t *testing.T) {
	for _, tc := range classificationFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			classifier, probe := tc.newClassifier(t)
			mtx := newClassificationMatrix()
			ex := classificationMatrixExec(t, mtx, classifier)

			pr, _, cleanup, err := ex.prepareRequest(context.Background(), codingHarnessCall("client-failure", "refactor the repository"))
			if err != nil {
				t.Fatalf("classification failure %q must preserve the user request: %v", tc.name, err)
			}
			defer cleanup()
			if pr == nil || pr.identity == nil {
				t.Fatal("prepared request has no identity-bound turn")
			}
			if got := classifier.invocationCount(); got < 1 {
				t.Fatalf("case %q never reached its failure injection: classifier invoked %d times, want at least 1", tc.name, got)
			}
			if got := pr.identity.preSession.Classification; got != (session.Classification{}) {
				t.Fatalf("classification = %+v after %q, want the conservative unknown", got, tc.name)
			}
			for _, consumer := range wantMatrixConsumers {
				// This harness returns only the prepared turn, so the
				// later-attempt execctx views and the session-start sink are
				// not wired here. Their full eight-consumer positive path is
				// covered by TestSameTurnClassificationReachesAllLaterSessionViewConsumers.
				if strings.HasPrefix(consumer, "later_execctx/") || consumer == "session_start_view" {
					continue
				}
				if _, ok := mtx.get(consumer); !ok {
					t.Fatalf("consumer %q did not run after classification failure %q", consumer, tc.name)
				}
			}
			if got := probe.rowCount(); got != 0 {
				t.Fatalf("classification failure %q created %d durable rows %v, want zero", tc.name, got, probe.rowKeys())
			}
		})
	}
}

// TestSessionClassificationCanceledContextSkipsClassifier is the genuine
// requirement 4.4 "canceled classification context" coverage. The turn context
// really is canceled, so the stage must return before the classifier is
// consulted at all. The two assertions together distinguish this from a plain
// unknown result:
//
//   - the classifier is invoked exactly zero times (a live context invokes it
//     once), which pins the ctx.Err() != nil early return in
//     internal/core/extensions/session_classification.go;
//   - the projected classification is the all-zero unknown.
//
// It also pins the fail-open default when the only bound context is canceled,
// which is what a client disconnect mid-preparation looks like.
func TestSessionClassificationCanceledContextSkipsClassifier(t *testing.T) {
	cases := []struct {
		name        string
		newContext  func() (context.Context, context.CancelFunc)
		wantSkipped bool
	}{
		{
			name: "canceled before the stage runs",
			newContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantSkipped: true,
		},
		{
			name: "expired deadline before the stage runs",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Unix(0, 0))
			},
			wantSkipped: true,
		},
		{
			name: "live context control",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantSkipped: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := newSpySessionClassifier("classification-canceled-context", func(context.Context, sessionclassification.Input) (session.Classification, error) {
				return codingAgentSessionClassification("canceled.ctx", 1), nil
			})
			ex := classificationExec(t)
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
				Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
				FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: spy}),
			})
			ibt := &identityBoundTurn{
				traceID:    "trace-canceled-context",
				preSession: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "aleg-1"},
				workspace:  aipworkspace.WorkspaceView{ID: "workspace-classification"},
			}
			ctx, cancel := tc.newContext()
			defer cancel()

			ex.runSessionClassificationStage(ctx, codingHarnessCall("client-canceled", "refactor"), ibt)

			got := len(spy.inputs())
			if tc.wantSkipped {
				if got != 0 {
					t.Fatalf("classifier invoked %d times with an already-canceled context, want 0: a canceled classification context must be skipped, not evaluated", got)
				}
				if ibt.preSession.Classification != (session.Classification{}) {
					t.Fatalf("classification = %+v with a canceled context, want the conservative unknown", ibt.preSession.Classification)
				}
			} else {
				// Control: the same harness with a live context does evaluate.
				// Without this the zero-invocation assertion above could pass
				// because the classifier was simply never wired up.
				if got != 1 {
					t.Fatalf("classifier invoked %d times with a live context, want exactly 1; the canceled-context control is not exercising the same wiring", got)
				}
				if !ibt.preSession.Classification.IsCodingAgent() {
					t.Fatalf("control classification = %+v, want the projected positive so the canceled-context case is genuinely distinguished from a plain unknown", ibt.preSession.Classification)
				}
			}
		})
	}
}

// TestSessionClassificationCanceledContextPreservesPriorPositive composes
// cancellation with an established positive: a turn whose session view already
// carries a positive and whose context is canceled must keep the positive, and
// must still not consult the classifier.
//
// Note the scenario is deliberately over-determined: the established-positive
// short-circuit in extensions.RunSessionClassificationStage fires before the
// cancellation guard, so it is the short-circuit, not cancellation, that
// suppresses the classifier here. This test pins preservation (it fails if that
// short-circuit is removed); the cancellation guard itself is pinned by
// TestSessionClassificationCanceledContextSkipsClassifier.
func TestSessionClassificationCanceledContextPreservesPriorPositive(t *testing.T) {
	priorPositive := codingAgentSessionClassification("prior.persisted_state", 6)
	spy := newSpySessionClassifier("classification-canceled-prior", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		panic("a canceled context must never reach the classifier")
	})
	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: spy}),
	})
	ibt := &identityBoundTurn{
		traceID:    "trace-canceled-prior",
		preSession: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "aleg-1", Classification: priorPositive},
		workspace:  aipworkspace.WorkspaceView{ID: "workspace-classification"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ex.runSessionClassificationStage(ctx, codingHarnessCall("client-canceled", "refactor"), ibt)

	if ibt.preSession.Classification != priorPositive {
		t.Fatalf("classification = %+v with a canceled context, want the preserved prior positive %+v", ibt.preSession.Classification, priorPositive)
	}
	if got := len(spy.inputs()); got != 0 {
		t.Fatalf("classifier invoked %d times with a canceled context and an established positive, want 0", got)
	}
}

// TestSessionClassificationEstablishedPositiveSkipsClassifier pins the
// monotonicity short-circuit itself: a session view that already carries a valid
// positive is returned unchanged and the classifier is never consulted. This is
// a deliberate property (requirement 1.4, and requirement 6.5/10.1 which forbid
// any further remote or state work for an established positive), and it is the
// reason a *failure* cannot be injected on that path — see
// TestSessionClassificationFailurePreservesPersistedPositive for the composed
// version that actually runs each failure class.
//
// The positive-preservation claim of the short-circuit is already covered by
// TestSessionClassificationStagePreservesPriorPositive in
// executor_session_classification_test.go (committed in task 6.1); this test
// adds the invocation-count half that that occurrence lacks.
func TestSessionClassificationEstablishedPositiveSkipsClassifier(t *testing.T) {
	priorPositive := codingAgentSessionClassification("prior.persisted_state", 4)

	for _, tc := range classificationFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			classifier, _ := tc.newClassifier(t)
			ex := classificationExec(t)
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
				Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
				FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: classifier}),
			})
			ibt := &identityBoundTurn{
				traceID:    "trace-classification-established-" + tc.name,
				preSession: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "aleg-1", Classification: priorPositive},
				workspace:  aipworkspace.WorkspaceView{ID: "workspace-classification"},
			}

			ex.runSessionClassificationStage(context.Background(), codingHarnessCall("client-failure", "refactor"), ibt)

			if got := ibt.preSession.Classification; got != priorPositive {
				t.Fatalf("classification = %+v with an established positive, want it unchanged: %+v", got, priorPositive)
			}
			if got := classifier.invocationCount(); got != 0 {
				t.Fatalf("classifier invoked %d times for an already-positive session, want 0: an established positive is immutable for the whole logical session", got)
			}
		})
	}
}

// TestSessionClassificationFailurePreservesPersistedPositive is the composed
// monotonicity proof for requirements 1.4 and 4.4 across all nine failure
// classes. Each case really runs its failure injection:
//
//  1. turn 1 promotes the session through the real standard-feature classifier
//     against the real process-owned store, producing a durable positive;
//  2. that positive is read back out of durable state — never fabricated — so the
//     scenario starts from a genuinely persisted positive;
//  3. turn 2 is a later turn of the same authoritative session whose session view
//     is still unknown, exactly as production sees it. Restoring the positive is
//     therefore the classifier's job, so the classifier IS invoked and each
//     failure injection genuinely fires;
//  4. the durable row must still hold the persisted positive.
//
// The earlier occurrence pre-set preSession.Classification, which makes
// extensions.RunSessionClassificationStage short-circuit before the classifier
// runs, so no failure class was ever exercised. This composition reaches the
// classifier instead.
//
// Each failing classifier is constructed with its own empty state probe, so under
// this wiring it cannot read the row turn 1 wrote: the turn-2 projection is
// always the conservative unknown. That is correct behavior — the failing
// classifier fails before it can restore, which requirement 4.4's "unless a
// prior positive classification is already available" permits, because the
// turn's own session view is unknown at stage entry. The assertion below still
// accepts a restored positive so the test stays valid if a future case wires the
// failing classifier to the same store.
//
// What must hold either way is that the positive is never lost: the durable row
// survives byte-identical, and a subsequent healthy turn restores it from
// durable state under deliberately weak evidence, so the restore cannot have
// come from that turn's own evidence.
func TestSessionClassificationFailurePreservesPersistedPositive(t *testing.T) {
	for _, tc := range classificationFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			probe := newClassificationStateProbe(t)

			// Turn 1: promote through the real classifier and real store.
			healthy := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
			ex := classificationProbeExec(t, probe, healthy)
			first, _, cleanupFirst, err := ex.prepareRequest(context.Background(), codingHarnessCall("client-monotonic", "refactor the repository"))
			if err != nil {
				t.Fatalf("first turn must be admitted: %v", err)
			}
			defer cleanupFirst()
			if got := first.identity.preSession.Classification; !got.IsCodingAgent() {
				t.Fatalf("first turn classification = %+v, want a positive promotion", got)
			}
			sessionID := first.identity.preSession.AuthoritativeSessionID
			if got := healthy.callCount(); got != 1 {
				t.Fatalf("classifier invoked %d times on the first turn, want exactly 1", got)
			}

			// Read the positive back out of durable state rather than building
			// it by hand.
			key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: sessionID}
			record, found, err := probe.inner.Load(context.Background(), key)
			if err != nil {
				t.Fatalf("load durable classification state: %v", err)
			}
			if !found || !record.Classification.IsCodingAgent() {
				t.Fatalf("durable positive missing after the first turn: found=%v record=%+v", found, record)
			}
			persistedPositive := record.Classification

			// Turn 2: same proxy-owned session, later turn, unknown session view,
			// failing classifier installed.
			failing, _ := tc.newClassifier(t)
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
				Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
				FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: failing}),
			})
			second := &identityBoundTurn{
				traceID: "trace-monotonic-" + tc.name,
				preSession: session.SessionView{
					AuthoritativeSessionID: sessionID,
					ALegID:                 first.identity.aLeg.ALegID,
				},
				workspace: aipworkspace.WorkspaceView{ID: "workspace-classification"},
			}
			ex.runSessionClassificationStage(context.Background(), codingHarnessCall("client-monotonic", "summarize the diff"), second)

			if got := failing.invocationCount(); got < 1 {
				t.Fatalf("case %q never reached its failure injection: classifier invoked %d times on the persisted-positive turn, want at least 1", tc.name, got)
			}
			projected := second.preSession.Classification
			if projected != persistedPositive && projected != (session.Classification{}) {
				t.Fatalf("turn-2 classification = %+v after %q, want either the persisted positive %+v or the conservative unknown", projected, tc.name, persistedPositive)
			}

			// The load-bearing assertion: whatever the turn projected, the
			// persisted positive must survive untouched (requirement 1.4).
			after, found, err := probe.inner.Load(context.Background(), key)
			if err != nil {
				t.Fatalf("reload durable classification state: %v", err)
			}
			if !found || after.Classification != persistedPositive {
				t.Fatalf("durable positive disturbed by failure %q: before=%+v after=%+v found=%v", tc.name, persistedPositive, after.Classification, found)
			}

			// A later healthy turn must still restore the positive, proving the
			// failure never downgraded the session (requirement 2.7 restore).
			restored := &identityBoundTurn{
				traceID:    "trace-monotonic-restore-" + tc.name,
				preSession: session.SessionView{AuthoritativeSessionID: sessionID, ALegID: first.identity.aLeg.ALegID},
				workspace:  aipworkspace.WorkspaceView{ID: "workspace-classification"},
			}
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
				Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
				FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)}),
			})
			// Deliberately weak evidence: the restore must come from durable
			// state, not from this turn's own decisive evidence.
			ex.runSessionClassificationStage(context.Background(), ordinaryTechnicalChatCall("client-monotonic"), restored)
			if got := restored.preSession.Classification; got != persistedPositive {
				t.Fatalf("restored classification = %+v after failure %q, want the persisted positive %+v", got, tc.name, persistedPositive)
			}
		})
	}
}

// TestSessionClassificationFailureDoesNotRejectOrReclassifyAfterAdmission proves
// requirement 4.4 on the full execution path: a classification failure is a
// pre-submit no-op, so the turn reaches the backend exactly once with its
// ingress evidence and route unchanged, and no durable state is created.
func TestSessionClassificationFailureDoesNotRejectOrReclassifyAfterAdmission(t *testing.T) {
	for _, tc := range classificationFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			classifier, probe := tc.newClassifier(t)
			ex := classificationProbeExec(t, probe, classifier)
			var opens atomic.Int32
			var observed aipapi.Call
			ex.Backends = map[string]execbackend.Backend{
				"only": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
					Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						opens.Add(1)
						observed = call
						return aipapi.NewFixedEventStream([]aipapi.Event{
							{Kind: aipapi.EventResponseStarted},
							{Kind: aipapi.EventResponseFinished},
						}), nil
					},
				},
			}
			ex.Rand = routing.NewSeededRng(3)

			call := codingHarnessCall("client-failure", "refactor the repository")
			call.Route = aipapi.RouteIntent{Selector: "only:model"}
			ingress := aipapi.CloneCall(*call)

			stream, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("classification failure %q must not reject the turn: %v", tc.name, err)
			}
			if _, err := aipapi.Collect(context.Background(), stream); err != nil {
				t.Fatalf("classification failure %q must not fail the turn: %v", tc.name, err)
			}
			if got := opens.Load(); got != 1 {
				t.Fatalf("backend opened %d times after classification failure %q, want exactly 1", got, tc.name)
			}
			if observed.Invocation.ClientUserAgent != ingress.Invocation.ClientUserAgent {
				t.Fatalf("classification failure %q changed the client User-Agent", tc.name)
			}
			if len(observed.Tools) != len(ingress.Tools) {
				t.Fatalf("classification failure %q changed the tool set", tc.name)
			}
			if observed.Route.Selector != ingress.Route.Selector {
				t.Fatalf("classification failure %q changed the route selector: %q", tc.name, observed.Route.Selector)
			}
			if got := probe.rowCount(); got != 0 {
				t.Fatalf("classification failure %q created %d durable rows %v, want zero", tc.name, got, probe.rowKeys())
			}
		})
	}
}

// ordinaryTechnicalChatCall is a turn with a generic SDK User-Agent and no
// decisive tool cluster, so local evaluation finds no decisive evidence.
func ordinaryTechnicalChatCall(clientSessionID string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: clientSessionID},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("explain this code fence")}},
		},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: "OpenAI/JS 4.0.0",
		},
	}
}

// TestSessionClassificationRemoteFailureFailsOpenToUnknown expresses
// requirement 6.9 end to end through the real executor: the generation holds a
// wired but hermetic failing decider, so a remote decision is attempted, fails,
// and must leave the session unknown while the request is preserved.
//
// In jev mode the real classifier must never promote from local evidence at all
// (requirement 6.3), even for a decisively coding-shaped turn. In hybrid mode
// decisive local evidence may promote first (requirement 6.4), and that positive
// must be local because a failing remote decision promotes nothing. Both modes
// must reach the classifier exactly once, and only a still-unknown turn may
// consume the finite per-session attempt budget (requirement 6.7).
func TestSessionClassificationRemoteFailureFailsOpenToUnknown(t *testing.T) {
	cases := []struct {
		mode featurestate.Mode
		call func() *aipapi.Call
		// wantPromotion records whether this mode is permitted to promote
		// locally, per requirements 6.3 (jev forbids it) and 6.4 (hybrid
		// allows decisive local evidence to promote first).
		wantPromotion bool
	}{
		{mode: featurestate.ModeJev, call: func() *aipapi.Call { return codingHarnessCall("client-jev", "refactor the repository") }},
		{mode: featurestate.ModeHybrid, call: func() *aipapi.Call { return codingHarnessCall("client-hybrid", "refactor the repository") }, wantPromotion: true},
		{mode: featurestate.ModeJev, call: func() *aipapi.Call { return ordinaryTechnicalChatCall("client-jev-chat") }},
		{mode: featurestate.ModeHybrid, call: func() *aipapi.Call { return ordinaryTechnicalChatCall("client-hybrid-chat") }},
	}

	for _, tc := range cases {
		name := string(tc.mode)
		if tc.wantPromotion {
			name += "/decisive local evidence"
		} else if tc.call().Tools != nil {
			name += "/decisive local evidence"
		} else {
			name += "/ordinary technical chat"
		}
		t.Run(name, func(t *testing.T) {
			probe := newClassificationStateProbe(t)
			real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, tc.mode)
			ex := classificationProbeExec(t, probe, real)

			pr, _, cleanup, err := ex.prepareRequest(context.Background(), tc.call())
			if err != nil {
				t.Fatalf("%s mode must preserve the user request: %v", tc.mode, err)
			}
			defer cleanup()

			got := pr.identity.preSession.Classification
			if tc.wantPromotion {
				// Requirement 6.4: hybrid may promote from decisive local
				// evidence, and that positive must stay local because a failing
				// remote decision promotes nothing (requirement 6.8).
				if !got.IsCodingAgent() {
					t.Fatalf("%s classification = %+v, want a local positive from decisive evidence", tc.mode, got)
				}
				if got.Source == session.SourceRemote {
					t.Fatalf("%s produced a remote-sourced classification %+v from a failing remote decision", tc.mode, got)
				}
			} else if got != (session.Classification{}) {
				t.Fatalf("%s mode classification = %+v, want unknown: without a remote positive neither local evidence nor weak evidence may promote", tc.mode, got)
			}
			if got := real.callCount(); got != 1 {
				t.Fatalf("%s mode classifier invoked %d times, want exactly 1", tc.mode, got)
			}

			claims, completions := 0, 0
			for _, op := range probe.mutationLog() {
				switch {
				case strings.HasPrefix(op, "claim_remote@"):
					claims++
				case strings.HasPrefix(op, "complete_remote@"):
					completions++
				}
			}
			// Requirement 6.7: exactly one leased attempt happens, and only for a
			// turn local evaluation left unknown. A decisive local hybrid turn
			// promotes before any remote claim (requirement 6.4).
			wantClaims := 1
			if tc.wantPromotion {
				wantClaims = 0
			}
			if claims != wantClaims {
				t.Fatalf("%s mode performed %d remote claims, want %d", tc.mode, claims, wantClaims)
			}
			// Requirement 6.7: every granted lease is finished after the network
			// call, so an abandoned lease cannot block later turns.
			if completions != wantClaims {
				t.Fatalf("%s mode performed %d remote completions, want %d", tc.mode, completions, wantClaims)
			}
			if !tc.wantPromotion {
				// Requirement 6.9: the failed attempt leaves attempt state, never a
				// durable classification of any kind.
				for _, key := range probe.rowRecords() {
					record, found, loadErr := probe.Load(context.Background(), key)
					if loadErr != nil {
						t.Fatalf("read back durable state for %+v: %v", key, loadErr)
					}
					if !found {
						continue
					}
					if record.Classification != (session.Classification{}) {
						t.Fatalf("%s mode persisted %+v after a remote failure, want no durable classification", tc.mode, record.Classification)
					}
				}
			}
		})
	}
}

// classificationFailAfterCommittedStream emits prefix events and then fails
// with a recoverable-shaped error, so the post-commit gate can be exercised
// while classification is failing.
type classificationFailAfterCommittedStream struct {
	events []aipapi.Event
	i      int
	fail   error
}

func (s *classificationFailAfterCommittedStream) Recv(context.Context) (aipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}

func (*classificationFailAfterCommittedStream) Close() error { return nil }

func (*classificationFailAfterCommittedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// classificationCommitGateCall is the base call used by the post-commit gate.
func classificationCommitGateCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

// newClassificationCommitGateExecutor builds the dual-candidate harness used for
// the post-commit rule proof, with a failing classifier in the classifier plane.
// The returned spy lets the caller pin the per-turn invocation count.
func newClassificationCommitGateExecutor(t *testing.T, primary, secondary execbackend.Backend) (*Executor, *spySessionClassifier) {
	t.Helper()
	ex := classificationExec(t)
	ex.MaxAttempts = 3
	ex.Rand = routing.NewSeededRng(11)
	ex.Backends = map[string]execbackend.Backend{"primary": primary, "secondary": secondary}
	classifier := newSpySessionClassifier("classification-post-commit", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return session.Classification{}, errors.New("remote classification decision failed")
	})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: classifier}),
	})
	return ex, classifier
}

// classificationCountedBackend wraps an Open function so candidate opens can be
// counted per backend.
func classificationCountedBackend(opens *atomic.Int64, open func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return open(ctx, call, cand)
		},
	}
}

func classificationSecondarySuccessBackend(opens *atomic.Int64) execbackend.Backend {
	return classificationCountedBackend(opens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventMessageStarted},
			{Kind: aipapi.EventTextDelta, Delta: "secondary-ok"},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	})
}

// classificationDrainUntilError drains stream and returns only the committed
// output events plus the terminal error.
func classificationDrainUntilError(t *testing.T, stream aipapi.EventStream) (committed []aipapi.Event, err error) {
	t.Helper()
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			return committed, rerr
		}
		if aipapi.OutputCommitted(ev) {
			committed = append(committed, ev)
		}
	}
}

// assertClassificationPostOutputNonRecoverable asserts the repository invariant
// that a post-commit failure is surfaced as a non-recoverable upstream failure
// rather than a retryable pre-output error.
func assertClassificationPostOutputNonRecoverable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a terminal error after committed output")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-commit error must not remain recoverable pre-output: %v", err)
	}
	var uf *aipapi.UpstreamFailureError
	if !errors.As(err, &uf) {
		t.Fatalf("want an UpstreamFailureError classification, got %T %v", err, err)
	}
	if uf.Phase != aipapi.PhasePostOutput || uf.Recoverable {
		t.Fatalf("want PhasePostOutput non-recoverable, got phase=%q recoverable=%v", uf.Phase, uf.Recoverable)
	}
}

// TestSessionClassificationFailurePreservesPostCommitNoFailoverRule is the
// requirement 11.8 immunity proof. The repository invariant is that no retry or
// failover may open a new candidate after the first downstream content event.
// The same recoverable-shaped error must still open the secondary candidate when
// nothing has committed, and must still be refused when a content delta has
// already been delivered, no matter how the classification stage failed.
//
// It also pins the invocation count: classification is evaluated exactly once
// per turn, so a failing classifier is never re-evaluated on a retry attempt and
// can never add an attempt of its own.
func TestSessionClassificationFailurePreservesPostCommitNoFailoverRule(t *testing.T) {
	t.Run("pre-output recoverable error still fails over", func(t *testing.T) {
		var primaryOpens, secondaryOpens atomic.Int64
		ex, classifier := newClassificationCommitGateExecutor(
			t,
			classificationCountedBackend(&primaryOpens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &classificationFailAfterCommittedStream{
					events: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}},
					fail:   aipapi.RecoverablePreOutputError(errors.New("pre-output-temp")),
				}, nil
			}),
			classificationSecondarySuccessBackend(&secondaryOpens),
		)

		stream, err := ex.Execute(context.Background(), classificationCommitGateCall("primary:m|secondary:m"))
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		defer func() { _ = stream.Close() }()
		var sawSecondaryText bool
		for {
			ev, rerr := stream.Recv(context.Background())
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				if aipapi.IsRecoverablePreOutput(rerr) {
					continue
				}
				t.Fatalf("unexpected recv error during pre-output failover: %v", rerr)
			}
			if ev.Kind == aipapi.EventTextDelta && ev.Delta == "secondary-ok" {
				sawSecondaryText = true
			}
		}
		if !sawSecondaryText {
			t.Fatal("a failing classifier must not change pre-output failover semantics")
		}
		if primaryOpens.Load() == 0 || secondaryOpens.Load() == 0 {
			t.Fatalf("pre-output failover did not happen: primary=%d secondary=%d", primaryOpens.Load(), secondaryOpens.Load())
		}
		if got := len(classifier.inputs()); got != 1 {
			t.Fatalf("classifier invoked %d times across a failover, want exactly 1 per turn", got)
		}
	})

	t.Run("post-commit recoverable error never fails over", func(t *testing.T) {
		var primaryOpens, secondaryOpens atomic.Int64
		ex, classifier := newClassificationCommitGateExecutor(
			t,
			classificationCountedBackend(&primaryOpens, func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &classificationFailAfterCommittedStream{
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "committed-visible"},
					},
					fail: aipapi.RecoverablePreOutputError(errors.New("would-retry-if-uncommitted")),
				}, nil
			}),
			classificationSecondarySuccessBackend(&secondaryOpens),
		)

		stream, err := ex.Execute(context.Background(), classificationCommitGateCall("primary:m|secondary:m"))
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		defer func() { _ = stream.Close() }()
		committed, rerr := classificationDrainUntilError(t, stream)
		if len(committed) == 0 || committed[0].Kind != aipapi.EventTextDelta || committed[0].Delta != "committed-visible" {
			t.Fatalf("first committed event must be a delivered TextDelta; got %#v", committed)
		}
		assertClassificationPostOutputNonRecoverable(t, rerr)
		if primaryOpens.Load() != 1 {
			t.Fatalf("primary opens=%d want 1", primaryOpens.Load())
		}
		if secondaryOpens.Load() != 0 {
			t.Fatalf("a failing classifier introduced post-commit failover: secondary opens=%d want 0", secondaryOpens.Load())
		}
		if got := len(classifier.inputs()); got != 1 {
			t.Fatalf("classifier invoked %d times, want exactly 1 per turn", got)
		}
	})
}

// classificationStageForbiddenCallSites names the files that participate in
// retry, failover, terminal recovery or detached preparation. The classification
// stage must never be invoked from any of them.
var classificationStageForbiddenCallSites = []string{
	"recovery_controller.go",
	"turn_terminal.go",
	"terminal_decision_continuation.go",
	"executor_open_attempt.go",
	"executor_attempt_transform.go",
	"executor_prepare_detached.go",
	"executor_prepare_request.go",
}

// classificationStageCallSite is one production caller of the canonical
// classification stage.
type classificationStageCallSite struct {
	file string
	line int
}

// classificationStageProductionCallSites finds every production call site of the
// canonical classification stage, so the structural half of requirement 11.8 can
// be proven: the stage is reached only from secure preparation and therefore
// cannot participate in a post-commit recovery, replay or failover decision.
func classificationStageProductionCallSites(t *testing.T) []classificationStageCallSite {
	t.Helper()
	var sites []classificationStageCallSite
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob runtime sources: %v", err)
	}
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "runSessionClassificationStage" {
				return true
			}
			sites = append(sites, classificationStageCallSite{file: path, line: fset.Position(call.Pos()).Line})
			return true
		})
	}
	sortCallSites(sites)
	return sites
}

func sortCallSites(sites []classificationStageCallSite) {
	for i := 1; i < len(sites); i++ {
		for j := i; j > 0; j-- {
			if sites[j].file < sites[j-1].file || (sites[j].file == sites[j-1].file && sites[j].line < sites[j-1].line) {
				sites[j], sites[j-1] = sites[j-1], sites[j]
				continue
			}
			break
		}
	}
}

// TestSessionClassificationFailureDoesNotEnablePostCommitReplayFallback is the
// structural half of requirement 11.8: the classification stage is invoked from
// exactly one production site, inside secure preparation, is never reachable
// from a retry/failover/detached file, and returns nothing so it can never
// reject a request from a recovery path.
func TestSessionClassificationFailureDoesNotEnablePostCommitReplayFallback(t *testing.T) {
	const callerFile = "executor_prepare_secure.go"

	sites := classificationStageProductionCallSites(t)

	// The stage must not be reachable from any recovery, retry, failover,
	// terminal or detached-preparation file. Checked before the exact-count
	// assertion so a forbidden site always reports the actionable message.
	for _, forbidden := range classificationStageForbiddenCallSites {
		for _, site := range sites {
			if site.file == forbidden {
				t.Fatalf("session classification stage is invoked from %s:%d, a retry/failover/detached path", site.file, site.line)
			}
		}
	}
	if len(sites) != 1 {
		t.Fatalf("session classification stage has %d production call sites %+v, want exactly 1", len(sites), sites)
	}
	if sites[0].file != callerFile {
		t.Fatalf("session classification stage is invoked from %s:%d, want %s", sites[0].file, sites[0].line, callerFile)
	}
}

// TestSessionClassificationStageReturnsNoError is the structural proof that the
// stage cannot reject a request: its declared result type is the empty tuple, so
// an absent plane, a canceled context, a state failure, a remote failure and an
// invalid result are all compile-time unable to become a request error.
func TestSessionClassificationStageReturnsNoError(t *testing.T) {
	path := filepath.Join(".", sessionClassificationStageFile)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", sessionClassificationStageFile, err)
	}
	var found bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runSessionClassificationStage" || fn.Recv == nil {
			continue
		}
		found = true
		if fn.Type.Results != nil && len(fn.Type.Results.List) != 0 {
			t.Fatalf("%s declares %d result values; the stage must return nothing so it cannot reject a request", sessionClassificationStageFile, len(fn.Type.Results.List))
		}
	}
	if !found {
		t.Fatalf("%s no longer declares runSessionClassificationStage", sessionClassificationStageFile)
	}
}

// TestSessionClassificationUntrustedClientIdentityIsNotPromotedAuthority is the
// requirements 7.6/7.7 proof. A client-supplied session hint that claims a
// coding-agent identity, combined with metadata that names a coding harness,
// must still classify as ordinary traffic when no accepted coding-harness
// identity rule matches; and the client hint must never become the state key, so
// two clients sharing a hint keep isolated state.
func TestSessionClassificationUntrustedClientIdentityIsNotPromotedAuthority(t *testing.T) {
	t.Run("claimed identity in client metadata does not promote", func(t *testing.T) {
		probe := newClassificationStateProbe(t)
		real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
		ex := classificationProbeExec(t, probe, real)

		call := codingHarnessCall("claimed-identity", "hello")
		// A generic SDK User-Agent plus an explicitly self-claimed agent name.
		call.Invocation.ClientUserAgent = "OpenAI/JS 4.0.0"
		call.Tools = nil
		call.Session.Metadata = map[string]string{
			"agent":  "codex_cli_rs",
			"client": "claude-code",
		}

		pr, _, cleanup, err := ex.prepareRequest(context.Background(), call)
		if err != nil {
			t.Fatalf("prepareRequest: %v", err)
		}
		defer cleanup()
		if got := pr.identity.preSession.Classification; got != (session.Classification{}) {
			t.Fatalf("classification = %+v from self-claimed client metadata, want unknown", got)
		}
		if got := probe.rowCount(); got != 0 {
			t.Fatalf("self-claimed client identity created %d durable rows %v, want zero", got, probe.rowKeys())
		}
	})

	t.Run("shared client hint keeps isolated state", func(t *testing.T) {
		probe := newClassificationStateProbe(t)
		real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
		ex := classificationProbeExec(t, probe, real)

		pr1, _, cleanup1, err := ex.prepareRequest(context.Background(), codingHarnessCall("shared-client-hint", "refactor"))
		if err != nil {
			t.Fatalf("first prepareRequest: %v", err)
		}
		defer cleanup1()
		pr2, _, cleanup2, err := ex.prepareRequest(context.Background(), codingHarnessCall("shared-client-hint", "summarize"))
		if err != nil {
			t.Fatalf("second prepareRequest: %v", err)
		}
		defer cleanup2()

		session1 := pr1.identity.preSession.AuthoritativeSessionID
		session2 := pr2.identity.preSession.AuthoritativeSessionID
		if session1 == "" || session2 == "" || session1 == session2 {
			t.Fatalf("proxy-owned sessions are not distinct for one shared client hint: %q %q", session1, session2)
		}
		if got := probe.rowKeys(); len(got) != 2 {
			t.Fatalf("classification rows = %v, want one secure-session row per proxy-owned session", got)
		}
		for _, key := range probe.rowKeys() {
			if strings.Contains(key, "shared-client-hint") {
				t.Fatalf("classification state was keyed by the client-controlled hint: %s", key)
			}
			if !strings.HasPrefix(key, string(featurestate.ScopeSecureSession)+":") {
				t.Fatalf("classification state key is not a proxy-owned secure session: %s", key)
			}
		}
	})
}

// TestSessionClassificationFailureKeepsRecoveryCommitSemantics is the
// recovery/commit regression suite requirement: with classification failing at
// the concrete standard-feature decision path, a pre-output failover still
// happens, both attempts are still recorded under the turn's A-leg, and no
// classification state is created.
func TestSessionClassificationFailureKeepsRecoveryCommitSemantics(t *testing.T) {
	probe := newClassificationStateProbe(t)
	// The real standard-feature classifier runs the real promotion path; the
	// durable write fails, so the production classifier reports a bounded
	// state-unavailable error. Isolation is therefore proven for the concrete
	// decision path, not only for a test stand-in.
	probe.failWrites(errClassificationStateUnavailable)
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	ex := classificationProbeExec(t, probe, real)
	ex.MaxAttempts = 2
	ex.Rand = routing.NewSeededRng(9)

	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"fail": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return nil, aipapi.RecoverablePreOutputError(errors.New("pre-output-temp"))
			},
		},
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := codingHarnessCall("client-recovery", "refactor the repository")
	call.Route = aipapi.RouteIntent{Selector: "fail:model^good:model"}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute with failing classification: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect with failing classification: %v", err)
	}
	if got := opens.Load(); got != 2 {
		t.Fatalf("backend opened %d times, want 2 (pre-output failover unchanged)", got)
	}
	if got := probe.rowCount(); got != 0 {
		t.Fatalf("failed durable promotion created %d durable rows %v, want zero", got, probe.rowKeys())
	}
	// The real classifier must actually have attempted the durable write, so
	// the failure above is the real write-side path and not a no-op.
	if got := len(probe.mutationLog()); got == 0 {
		t.Fatal("the real classifier never attempted a durable promotion, so the failure path was not exercised")
	}
	if got := real.callCount(); got != 1 {
		t.Fatalf("classifier invoked %d times across a recovery, want exactly 1 per turn", got)
	}
	childALeg := call.Session.ALegID
	if childALeg == "" {
		t.Fatal("no A-leg recorded for the recovered turn")
	}
	records, err := ex.Store.LoadAttempts(context.Background(), childALeg)
	if err != nil {
		t.Fatalf("load attempts: %v", err)
	}
	if len(records) != 2 || records[0].Outcome == aipapi.AttemptSuccess || records[1].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("attempt lineage was not preserved with failing classification: %#v", records)
	}
}
