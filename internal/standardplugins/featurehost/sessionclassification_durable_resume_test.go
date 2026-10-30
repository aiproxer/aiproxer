package featurehost

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	ssbunstore "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/bunstore"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	hostclassification "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// Task 9.1 certification harness.
//
// Every helper here composes the real standard-distribution surfaces: a real
// featurehost process over a real on-disk SQLite database, a real compiled
// generation (classifier plane + generation lifecycle), and a real generic-core
// executor whose classification stage and downstream consumers are the
// production ones. Nothing here re-implements a production decision.
//
// The helpers take testing.TB rather than *testing.T so Task 9.3's hot-path
// benchmarks drive the SAME harness instead of duplicating it. The signatures were
// widened only; no helper body or assertion changed.

// certificationWorkspaceID is the workspace every certified turn resolves.
const certificationWorkspaceID = "workspace-session-classification-9-1"

// certificationPositiveUserAgent is an accepted high-confidence coding-harness
// identity (the codex family), and it carries no tools, so only the client
// identity rule can promote it and a bounded exclusion prefix can suppress it.
const certificationPositiveUserAgent = "codex_cli_rs/1.2.3"

// certificationWeakUserAgent is a generic SDK identity with no decisive evidence,
// so a turn carrying it can only observe a classification restored from state.
const certificationWeakUserAgent = "OpenAI/JS 4.0.0"

// certificationExcludeCodex is the reloaded heuristic rule under test: it
// suppresses the prospective codex identity match for a newly admitted turn.
const certificationExcludeCodex = "heuristic:\n  ignored_user_agent_prefixes: [codex_cli_rs]\n"

// certificationBarrierTimeout bounds the ordering handshake so a missing
// classification stage fails the test instead of hanging the suite.
const certificationBarrierTimeout = 10 * time.Second

type certificationWorkspaceResolver struct{}

func (certificationWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ID: certificationWorkspaceID}, nil
}

// certificationYAML decodes a feature config payload the same way the operator
// configuration path does.
func certificationYAML(tb testing.TB, document string) yaml.Node {
	tb.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(document), &node); err != nil {
		tb.Fatalf("decode feature config: %v", err)
	}
	if len(node.Content) == 0 {
		return node
	}
	return *node.Content[0]
}

// certificationRegistration builds the canonical outer-enabled (or
// outer-disabled) session-classification registration.
func certificationRegistration(tb testing.TB, enabled bool, configYAML string) aipsdk.Registration {
	tb.Helper()
	registration := aipsdk.Registration{
		ID:          featurestate.ID,
		FactoryKind: featurestate.ID,
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     enabled,
	}
	if configYAML != "" {
		registration.Config = aipsdk.ConfigPayload{Node: certificationYAML(tb, configYAML)}
	}
	return registration
}

// certificationSQLiteDSN is a real on-disk SQLite database inside the test's
// temporary directory. An in-memory database cannot prove durability across a
// close/reopen, which is the whole point of this certification.
func certificationSQLiteDSN(path string) string {
	return "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

// certificationOpenSQLite opens (and, for the first call, creates) the durable
// database. A restart may close the returned handle early to reopen it
// deliberately; a cleanup closes it otherwise. Close is idempotent, and the
// cleanup is registered before any caller's process cleanup, so it runs after
// the process closes and before TempDir removal (Windows cannot delete an open
// database file).
func certificationOpenSQLite(tb testing.TB, dsn string) *bun.DB {
	tb.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		tb.Fatalf("open sqlite: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		_ = sqlDB.Close()
		tb.Fatalf("new bun db: %v", err)
	}
	tb.Cleanup(func() { _ = bunDB.Close() })
	return bunDB
}

// certificationGeneration is one compiled generation: the published classifier
// plane value, the plane set an executor snapshot can be built from, and the
// generation lifecycles whose Start initializes the shared process state.
type certificationGeneration struct {
	classifier sdkclassification.Classifier
	planes     aipfeature.FrozenPlaneSet
	lifecycles []aipplugin.Lifecycle
}

// certificationCompile compiles one generation from the session-classification
// registration alone, so the test observes exactly the surface a reload swaps.
func certificationCompile(tb testing.TB, rt *Runtime, enabled bool, configYAML string) certificationGeneration {
	tb.Helper()
	out, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations: []aipsdk.Registration{certificationRegistration(tb, enabled, configYAML)},
	})
	if err != nil {
		tb.Fatalf("CompileGeneration(enabled=%t, config=%q): %v", enabled, configYAML, err)
	}
	return certificationGeneration{
		classifier: aipfeature.Get(out.Planes, aipfeature.PlaneSessionClassifier),
		planes:     out.Planes,
		lifecycles: out.Lifecycles,
	}
}

func (g certificationGeneration) start(tb testing.TB) {
	tb.Helper()
	for _, lifecycle := range g.lifecycles {
		if err := lifecycle.Start(context.Background()); err != nil {
			tb.Fatalf("generation lifecycle Start: %v", err)
		}
	}
}

func (g certificationGeneration) stop(tb testing.TB) {
	tb.Helper()
	for _, lifecycle := range g.lifecycles {
		if err := lifecycle.Stop(context.Background()); err != nil {
			tb.Fatalf("generation lifecycle Stop: %v", err)
		}
	}
}

// certificationSubmitBarrier is a submit hook: the first consumer the canonical
// lane runs after the classification stage. It captures the submit evidence view
// and publishes a one-shot "the earlier submit stage already ran" signal.
//
// That signal is what makes the downstream ordering observable instead of
// inferred. The later consumer waits for it, so this test can distinguish
// "classification was projected before both consumers" from "classification ran
// after them" (their captures would be unknown) and from "the earlier stage never
// ran" (an explicit barrier timeout).
type certificationSubmitBarrier struct {
	id string

	observed chan struct{}
	once     sync.Once

	mu       sync.Mutex
	captured []session.Classification
	sessions []string
}

func certificationNewSubmitBarrier(id string) *certificationSubmitBarrier {
	return &certificationSubmitBarrier{id: id, observed: make(chan struct{})}
}

func (b *certificationSubmitBarrier) ID() string                        { return b.id }
func (b *certificationSubmitBarrier) Order() int                        { return 0 }
func (b *certificationSubmitBarrier) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (b *certificationSubmitBarrier) Handle(
	ctx context.Context,
	_ *aipapi.Call,
	_ *sdkhooks.SubmitMeta,
) (sdkhooks.SubmitDecision, error) {
	var (
		view  session.SessionView
		found bool
	)
	if evidence := extensions.DecisionEvidenceFromContext(ctx); evidence != nil {
		view, found = evidence.Views.Session, true
	}
	b.mu.Lock()
	if found {
		b.captured = append(b.captured, view.Classification)
		b.sessions = append(b.sessions, view.AuthoritativeSessionID)
	}
	b.mu.Unlock()
	b.once.Do(func() { close(b.observed) })
	return sdkhooks.SubmitDecision{}, nil
}

func (b *certificationSubmitBarrier) saw() ([]session.Classification, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]session.Classification(nil), b.captured...),
		append([]string(nil), b.sessions...)
}

// certificationConsumer is a test-only downstream feature consumer. It is not a
// production consumer: session classification gates no existing feature until a
// feature opts in. It exists to observe the same-turn session view a real
// consumer would receive, and to prove the classification was already there when
// it ran.
type certificationConsumer struct {
	id       string
	barrier  *certificationSubmitBarrier
	observed []session.Classification
	sessions []string
	timedOut bool
	mu       sync.Mutex
}

func (c *certificationConsumer) ID() string                        { return c.id }
func (c *certificationConsumer) Order() int                        { return 0 }
func (c *certificationConsumer) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (c *certificationConsumer) Handle(
	_ context.Context,
	_ *aipapi.Call,
	meta prerequest.Meta,
	_ prerequest.Services,
) (prerequest.Decision, error) {
	// Capture before waiting: if classification had not already been projected,
	// this value is the unknown projection and the assertion fails.
	c.mu.Lock()
	c.observed = append(c.observed, meta.Session.Classification)
	c.sessions = append(c.sessions, meta.Session.AuthoritativeSessionID)
	c.mu.Unlock()
	if c.barrier != nil {
		select {
		case <-c.barrier.observed:
		case <-time.After(certificationBarrierTimeout):
			c.mu.Lock()
			c.timedOut = true
			c.mu.Unlock()
		}
	}
	return prerequest.Allow(), nil
}

func (c *certificationConsumer) saw() ([]session.Classification, []string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.Classification(nil), c.observed...),
		append([]string(nil), c.sessions...),
		c.timedOut
}

// certificationPlanesWithConsumer extends a compiled generation's plane set with
// the test-only downstream consumer, so one executor snapshot carries the real
// classifier and an observable consumer.
func certificationPlanesWithConsumer(tb testing.TB, planes aipfeature.FrozenPlaneSet, consumers ...prerequest.Handler) aipfeature.FrozenPlaneSet {
	tb.Helper()
	contributions := planes.ToContributions()
	if len(consumers) > 0 {
		if err := aipfeature.Contribute(contributions, aipfeature.PlanePreRequestHandlers,
			consumers[0].ID(), consumers); err != nil {
			tb.Fatalf("contribute %q consumer: %v", consumers[0].ID(), err)
		}
	}
	return contributions.Freeze()
}

// certificationExecutor builds the generic-core executor the runtime uses over a
// real durable topology: the secure-session store and the A-leg continuity store
// are both Bun-backed on the same shared database the featurehost borrows, so a
// rebuilt executor is a genuine cold start rather than a warm in-process stub.
// One counting backend makes sure every certified turn is actually served, and an
// optional submit hook installs the ordering barrier.
func certificationExecutor(
	tb testing.TB,
	database *bun.DB,
	planes aipfeature.FrozenPlaneSet,
	submitHooks ...sdkhooks.SubmitHook,
) (*runtime.Executor, *atomic.Int32) {
	tb.Helper()
	return certificationExecutorWithWorkspace(tb, database, planes, certificationWorkspaceResolver{}, submitHooks...)
}

// certificationExecutorWithWorkspace is the shared executor factory. It exists so
// an acceptance fixture whose workspace view is not the default one (the
// cross-harness evidence matrix supplies per-row project markers) reuses this
// durable topology instead of duplicating it.
func certificationExecutorWithWorkspace(
	tb testing.TB,
	database *bun.DB,
	planes aipfeature.FrozenPlaneSet,
	resolver aipworkspace.Resolver,
	submitHooks ...sdkhooks.SubmitHook,
) (*runtime.Executor, *atomic.Int32) {
	tb.Helper()
	ctx := context.Background()
	sessionStore, err := ssbunstore.NewWithContext(ctx, database)
	if err != nil {
		tb.Fatalf("durable secure-session store: %v", err)
	}
	continuityStore, err := continuitybunstore.NewWithContext(ctx, database)
	if err != nil {
		tb.Fatalf("durable continuity store: %v", err)
	}
	fingerprintKey := certificationFingerprintKey()
	manager, err := app.NewManager(
		sessionStore,
		app.NewRandGenerator(fingerprintKey),
		b2bualineage.New(continuityStore),
		app.ManagerConfig{FingerprintKey: fingerprintKey, StoreDurable: true},
	)
	if err != nil {
		tb.Fatalf("secure session manager: %v", err)
	}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = continuityStore
	ex.SecureSession = manager
	ex.SyntheticLocalPrincipal = true
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: submitHooks})
	ex.Rand = routing.NewSeededRng(3)
	ex.LargeBodyGenerationID = "certification-generation-1"
	ex.LargeBodyCandidateDomainGeneration = "certification-domain-generation-1"
	ex.DefaultBackend = "only"
	ex.Backends = map[string]execbackend.Backend{
		"only": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
			OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		},
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{resolver}),
		FeaturePlanes: planes,
	})
	return ex, &opens
}

// certificationEvaluations reads the feature-owned bounded evaluation counters
// the process collector registered. Counting the production observation is how the
// test proves the real classifier evaluated the turn exactly once and restored
// from state instead of promoting again.
func certificationEvaluations(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather feature metrics: %v", err)
	}
	out := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "aip_session_classification_evaluations_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			var mode, outcome string
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "mode":
					mode = label.GetValue()
				case "outcome":
					outcome = label.GetValue()
				}
			}
			out[mode+"/"+outcome] += metric.GetCounter().GetValue()
		}
	}
	return out
}

// certificationStoreOperations reads the bounded durable-operation counters.
func certificationStoreOperations(t *testing.T, registry *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather feature metrics: %v", err)
	}
	out := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "aip_session_classification_store_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			var operation, outcome string
			for _, label := range metric.GetLabel() {
				switch label.GetName() {
				case "operation":
					operation = label.GetValue()
				case "outcome":
					outcome = label.GetValue()
				}
			}
			out[operation+"/"+outcome] += metric.GetCounter().GetValue()
		}
	}
	return out
}

func certificationFingerprintKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

// certificationCall is one canonical client turn. The User-Agent selects the
// classification outcome; no tools are carried, so the identity rule is the only
// possible promotion path.
func certificationCall(clientSessionID, resumeToken, userAgent string) *aipapi.Call {
	return certificationCallWith(clientSessionID, resumeToken, userAgent, "summarize the diff", "")
}

// certificationCallWith is certificationCall with the client prompt and route
// model supplied. The cross-harness evidence matrix declares a weakPrompt and a
// modelName per fixture, and those signals must actually reach the turn: a
// technical-chat negative row delivered with a generic "summarize the diff"
// prompt certifies nothing, because no classifier that promoted on the row's
// declared signal could ever be observed promoting.
func certificationCallWith(clientSessionID, resumeToken, userAgent, prompt, model string) *aipapi.Call {
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "only:model"},
		Session: aipapi.SessionRef{
			ClientSessionID: clientSessionID,
			ResumeToken:     resumeToken,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(prompt)},
		}},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: userAgent,
		},
	}
	_ = model // the canonical classifier input is deliberately model-free (requirement 3.4);
	// the matrix carries a model name inside weakPrompt, which IS delivered above.
	return call
}

// certificationServe runs one canonical client turn end to end.
func certificationServe(tb testing.TB, ex *runtime.Executor, call *aipapi.Call) {
	tb.Helper()
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		tb.Fatalf("Execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		tb.Fatalf("collect turn: %v", err)
	}
}

// certificationResumableSession creates a proxy-owned secure session and
// returns its authoritative SessionID together with the resume token a later
// process uses to continue it. Creating it through the exported preparation seam
// is what makes the following turns genuine resumes rather than second turns.
func certificationResumableSession(
	tb testing.TB,
	ex *runtime.Executor,
	clientSessionID string,
) (sessionID string, resumeToken string) {
	tb.Helper()
	ctx := context.Background()
	prep, err := ex.PrepareSecureSession(ctx, runtime.SecureSessionPrepInput{
		TraceID: "certification-" + clientSessionID,
		Session: largebody.SessionInput{
			ClientSessionID:     clientSessionID,
			NewSessionRequested: true,
		},
	})
	if err != nil {
		tb.Fatalf("PrepareSecureSession: %v", err)
	}
	begin, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		tb.Fatalf("ExecuteBeginTurn: %v", err)
	}
	if !begin.IsNew {
		tb.Fatal("session setup did not create a new secure session")
	}
	if _, _, err := prep.ResolveALeg(prep.Context(), begin.Record.ALegID); err != nil {
		tb.Fatalf("ResolveALeg: %v", err)
	}
	resumeToken = prep.ResponseCarrier(begin).ResumeToken.Reveal()
	sessionID = string(begin.Record.SessionID)
	if sessionID == "" || resumeToken == "" {
		tb.Fatalf("session setup produced session=%q resume-token-present=%t", sessionID, resumeToken != "")
	}
	return sessionID, resumeToken
}

// certificationLoadRow reads the durable classification row through an
// independent store instance, so every assertion observes committed database
// state rather than a process cache.
func certificationLoadRow(tb testing.TB, database *bun.DB, sessionID string) (featurestate.Record, bool) {
	tb.Helper()
	store, err := hostclassification.NewBunStore(database)
	if err != nil {
		tb.Fatalf("new classification store: %v", err)
	}
	record, found, err := store.Load(context.Background(), certificationKey(sessionID))
	if err != nil {
		tb.Fatalf("load durable classification row: %v", err)
	}
	return record, found
}

func certificationKey(sessionID string) featurestate.Key {
	return featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: sessionID}
}

// TestSessionClassificationDurableResumeRestoresBeforeDownstreamConsumer is the
// load-bearing requirement 2.7/2.10/4.2/12.6 certification: a durably resumable
// session's persisted positive classification is restored after the durable store
// is closed and reopened, and the restored value is already available to a
// downstream consumer on that same turn.
//
// Ordering is proved with an observable barrier rather than a final state: the
// consumer captures the session view it is handed and then waits for the
// classification stage's completion signal, so "classification ran after the
// consumer" fails on the captured value and "classification never ran" fails on
// the barrier timeout.
//
// The restore itself is proved rather than assumed: the resumed turn carries a
// generic SDK identity with no decisive evidence, so local evaluation cannot
// promote it, and a control turn under the reloaded generation proves that the
// same weak evidence stays unknown for an unrelated session.
func TestSessionClassificationDurableResumeRestoresBeforeDownstreamConsumer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-durable-resume.db"))

	// ---- first process: a new session is promoted through the canonical lane ----
	firstDB := certificationOpenSQLite(t, dsn)
	firstProcess, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: firstDB})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	first := certificationCompile(t, firstProcess, true, "")
	if first.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	first.start(t)

	executor, opens := certificationExecutor(t, firstDB, certificationPlanesWithConsumer(t, first.planes))

	sessionID, resumeToken := certificationResumableSession(t, executor, "client-durable-resume")
	if _, found := certificationLoadRow(t, firstDB, sessionID); found {
		t.Fatal("a session with no classification turn already owns a durable row")
	}

	certificationServe(t, executor, certificationCall("client-durable-resume", resumeToken, certificationPositiveUserAgent))
	if got := opens.Load(); got != 1 {
		t.Fatalf("promoting turn opened the backend %d times, want exactly 1", got)
	}
	persisted, found := certificationLoadRow(t, firstDB, sessionID)
	if !found || !persisted.Classification.IsCodingAgent() {
		t.Fatalf("durable row after the promoting turn = %+v (found=%t), want a persisted coding_agent", persisted.Classification, found)
	}
	persistedPositive := persisted.Classification
	if persistedPositive.Source != session.SourceLocalIdentity || persistedPositive.Evidence != "client_family.codex" {
		t.Fatalf("persisted classification = %+v, want the local identity rule's evidence code", persistedPositive)
	}
	if persistedPositive.Revision != 1 {
		t.Fatalf("persisted revision = %d, want the store-assigned first revision 1", persistedPositive.Revision)
	}

	// ---- restart: stop the generation, close the process, close the database ----
	first.stop(t)
	if err := firstProcess.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	// ---- second process over the same on-disk database ----
	secondDB := certificationOpenSQLite(t, dsn)
	secondRegistry := prometheus.NewRegistry()
	secondProcess, err := NewProcess(ctx, ProcessInput{
		Logger:          slog.Default(),
		BunDB:           secondDB,
		MetricsRegistry: secondRegistry,
	})
	if err != nil {
		t.Fatalf("NewProcess after restart: %v", err)
	}
	t.Cleanup(func() {
		if err := secondProcess.Close(); err != nil {
			t.Errorf("close second process: %v", err)
		}
	})
	second := certificationCompile(t, secondProcess, true, "")
	if second.classifier == nil {
		t.Fatal("re-enabled generation published no classifier")
	}
	second.start(t)
	if secondProcess.sessionClassification.Coordinator() == firstProcess.sessionClassification.Coordinator() {
		t.Fatal("the restarted process reused the closed process coordinator")
	}

	// The reopened database really is the same durable store: the row written by
	// the closed process is readable again through an independent store.
	reopened, found := certificationLoadRow(t, secondDB, sessionID)
	if !found || reopened.Classification != persistedPositive || !reopened.UpdatedAt.Equal(persisted.UpdatedAt) {
		t.Fatalf("reopened durable row = %+v (found=%t), want the byte-identical %+v", reopened.Classification, found, persistedPositive)
	}

	// ---- resumed turn under the restarted generation ----
	submitBarrier := certificationNewSubmitBarrier("certification-durable-resume-submit")
	consumer := &certificationConsumer{id: "certification-durable-resume-consumer", barrier: submitBarrier}
	resumedExecutor, resumedOpens := certificationExecutor(t, secondDB,
		certificationPlanesWithConsumer(t, second.planes, consumer), submitBarrier)
	certificationServe(t, resumedExecutor,
		certificationCall("client-durable-resume", resumeToken, certificationWeakUserAgent))
	if got := resumedOpens.Load(); got != 1 {
		t.Fatalf("resumed turn opened the backend %d times, want exactly 1", got)
	}

	// The production observation proves the real classifier ran exactly once on
	// the resumed turn and restored from durable state rather than promoting it
	// again (requirement 2.6/2.7: a restore must not advance the revision).
	if got := certificationEvaluations(t, secondRegistry); got["heuristic/restored"] != 1 || len(got) != 1 {
		t.Fatalf("classification evaluations after the resumed turn = %v, want exactly one heuristic/restored", got)
	}
	if got := certificationStoreOperations(t, secondRegistry); got["load/hit"] != 1 {
		t.Fatalf("durable load observations after the resumed turn = %v, want exactly one load hit", got)
	}

	observed, sessions, timedOut := consumer.saw()
	if timedOut {
		t.Fatal("the downstream consumer ran before the earlier submit stage, so the ordering barrier never released")
	}
	if len(observed) != 1 {
		t.Fatalf("downstream consumer ran %d times on the resumed turn, want exactly 1", len(observed))
	}
	if sessions[0] != sessionID {
		t.Fatalf("consumer observed authoritative session %q, want the resumed %q", sessions[0], sessionID)
	}
	if observed[0] != persistedPositive {
		t.Fatalf("consumer classification = %+v, want the restored durable positive %+v before it ran", observed[0], persistedPositive)
	}
	submitObserved, submitSessions := submitBarrier.saw()
	if len(submitObserved) != 1 || submitObserved[0] != persistedPositive {
		t.Fatalf("submit-stage classification = %+v, want the restored durable positive %+v before it ran", submitObserved, persistedPositive)
	}
	if submitSessions[0] != sessionID {
		t.Fatalf("submit stage observed session %q, want the resumed %q", submitSessions[0], sessionID)
	}

	// Restoring must not rewrite the durable row.
	afterRestore, found := certificationLoadRow(t, secondDB, sessionID)
	if !found || afterRestore != persisted {
		t.Fatalf("durable row after restore = %+v (found=%t), want it unchanged at %+v", afterRestore.Classification, found, persisted)
	}

	// Control: the same weak evidence under the restarted generation stays unknown
	// for an unrelated authoritative session, so the restored positive above can
	// only have come from durable state.
	controlSubmit := certificationNewSubmitBarrier("certification-durable-resume-control-submit")
	controlConsumer := &certificationConsumer{id: "certification-durable-resume-control", barrier: controlSubmit}
	controlExecutor, _ := certificationExecutor(t, secondDB,
		certificationPlanesWithConsumer(t, second.planes, controlConsumer), controlSubmit)
	controlSession, controlToken := certificationResumableSession(t, controlExecutor, "client-durable-resume-control")
	certificationServe(t, controlExecutor,
		certificationCall("client-durable-resume-control", controlToken, certificationWeakUserAgent))
	controlObserved, _, controlTimedOut := controlConsumer.saw()
	if controlTimedOut {
		t.Fatal("control turn's consumer never observed the earlier submit stage")
	}
	if len(controlObserved) != 1 || controlObserved[0] != (session.Classification{}) {
		t.Fatalf("control classification = %+v, want unknown: weak evidence must not promote an unrelated session", controlObserved)
	}
	if _, found := certificationLoadRow(t, secondDB, controlSession); found {
		t.Fatal("an unknown control session created a durable classification row")
	}
}

// TestSessionClassificationDisabledGenerationLeavesDurableResumeIntact pins the
// requirement 8.7 half that the durable-resume path depends on: a disabled
// generation withdraws the classifier, and the reopened process still restores
// the persisted positive once the feature is enabled again (requirement 8.8).
//
// It is deliberately composed across a real close/reopen so the disable window
// cannot be satisfied by a process cache.
func TestSessionClassificationDisabledGenerationLeavesDurableResumeIntact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-disable-resume.db"))

	firstDB := certificationOpenSQLite(t, dsn)
	firstProcess, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: firstDB})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	first := certificationCompile(t, firstProcess, true, "")
	first.start(t)
	executor, _ := certificationExecutor(t, firstDB, certificationPlanesWithConsumer(t, first.planes))
	sessionID, resumeToken := certificationResumableSession(t, executor, "client-disable-resume")
	certificationServe(t, executor,
		certificationCall("client-disable-resume", resumeToken, certificationPositiveUserAgent))
	persisted, found := certificationLoadRow(t, firstDB, sessionID)
	if !found || !persisted.Classification.IsCodingAgent() {
		t.Fatalf("durable row before the disable = %+v (found=%t), want a persisted positive", persisted.Classification, found)
	}
	first.stop(t)
	if err := firstProcess.Close(); err != nil {
		t.Fatalf("close first process: %v", err)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatalf("close first database: %v", err)
	}

	secondDB := certificationOpenSQLite(t, dsn)
	secondProcess, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: secondDB})
	if err != nil {
		t.Fatalf("NewProcess after restart: %v", err)
	}
	t.Cleanup(func() {
		if err := secondProcess.Close(); err != nil {
			t.Errorf("close second process: %v", err)
		}
	})

	// Disabled generation: no classifier plane and no classification lifecycle are
	// published at all, measured against a generation with no registration.
	disabled := certificationCompile(t, secondProcess, false, "")
	if disabled.classifier != nil {
		t.Fatalf("disabled generation published classifier %v", disabled.classifier)
	}
	baselineOut, err := secondProcess.CompileGeneration(context.Background(), GenerationInput{})
	if err != nil {
		t.Fatalf("CompileGeneration without any registration: %v", err)
	}
	if got, want := len(disabled.lifecycles), len(baselineOut.Lifecycles); got != want {
		t.Fatalf("disabled generation lifecycles = %d, want the %d of a generation without the feature", got, want)
	}
	// The withdraw must not have touched the durable row.
	afterDisable, found := certificationLoadRow(t, secondDB, sessionID)
	if !found || afterDisable != persisted {
		t.Fatalf("durable row after disable = %+v (found=%t), want it unchanged at %+v", afterDisable.Classification, found, persisted.Classification)
	}

	// Re-enabled generation: the persisted positive is recoverable.
	reEnabled := certificationCompile(t, secondProcess, true, "")
	if reEnabled.classifier == nil {
		t.Fatal("re-enabled generation published no classifier")
	}
	reEnabled.start(t)
	submitBarrier := certificationNewSubmitBarrier("certification-disable-resume-submit")
	consumer := &certificationConsumer{id: "certification-disable-resume-consumer", barrier: submitBarrier}
	resumedExecutor, _ := certificationExecutor(t, secondDB,
		certificationPlanesWithConsumer(t, reEnabled.planes, consumer), submitBarrier)
	certificationServe(t, resumedExecutor,
		certificationCall("client-disable-resume", resumeToken, certificationWeakUserAgent))

	observed, sessions, timedOut := consumer.saw()
	if timedOut {
		t.Fatal("the re-enabled generation's consumer ran before the earlier submit stage")
	}
	if len(observed) != 1 || observed[0] != persisted.Classification {
		t.Fatalf("re-enabled classification = %+v, want the recovered durable positive %+v", observed, persisted.Classification)
	}
	if sessions[0] != sessionID {
		t.Fatalf("re-enabled consumer observed session %q, want %q", sessions[0], sessionID)
	}
	if submitObserved, _ := submitBarrier.saw(); len(submitObserved) != 1 || submitObserved[0] != persisted.Classification {
		t.Fatalf("re-enabled submit-stage classification = %+v, want %+v", submitObserved, persisted.Classification)
	}
}
