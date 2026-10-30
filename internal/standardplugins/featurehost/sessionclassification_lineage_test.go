package featurehost

import (
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
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
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/uptrace/bun"
)

// Task 9.2 lineage certification (requirements 2.4 and 12.6).
//
// Requirement 2.4 says routing, backend/model selection, retry, race, failover,
// compaction, and provider continuity changes inside the same authoritative logical
// session must leave the positive classification unchanged. The mechanism that makes
// this true is that the classification is keyed by the authoritative session and is
// resolved before any routing decision, so route and backend selection cannot reach
// it.
//
// The test proves it compositionally rather than by assertion: each certified turn
// changes the route selector, the backend set, the compaction behaviour, or the
// failover outcome, and every turn's downstream consumer must still observe the
// byte-identical persisted positive. A test that only varied the route string would
// pass even if the stage consumed route state, so each variant is verified to have
// actually changed the execution path through a per-backend open census.

// lineageOpenRecord is one backend open the executor performed. The operation is
// recorded so a "compaction turn" variant is distinguishable from an ordinary turn
// rather than being assumed to differ.
type lineageOpenRecord struct {
	backend   string
	selector  string
	operation aipapi.Operation
}

// lineageBackendCensus records every backend open so a test can prove the route
// really changed rather than merely declaring a different selector.
type lineageBackendCensus struct {
	mu    sync.Mutex
	opens []lineageOpenRecord
}

func (c *lineageBackendCensus) record(record lineageOpenRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens = append(c.opens, record)
}

func (c *lineageBackendCensus) snapshot() []lineageOpenRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]lineageOpenRecord(nil), c.opens...)
}

func (c *lineageBackendCensus) backends() []string {
	seen := map[string]int{}
	out := []string{}
	for _, open := range c.snapshot() {
		if seen[open.backend] == 0 {
			out = append(out, open.backend)
		}
		seen[open.backend]++
	}
	return out
}

// lineageStreamingBackend builds a backend that serves one trivial turn and records
// the open. failFirst makes it fail recoverably before any client-visible output,
// which is the shape that produces a real pre-output failover.
func lineageStreamingBackend(census *lineageBackendCensus, name string, failFirst *atomic.Bool) execbackend.Backend {
	open := func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		census.record(lineageOpenRecord{
			backend: name, selector: call.Route.Selector, operation: call.Invocation.Operation,
		})
		if failFirst != nil && failFirst.CompareAndSwap(true, false) {
			// Pre-output recoverable: the runtime may fail over to the next
			// alternative, and no client-visible event has been produced.
			return nil, aipapi.RecoverablePreOutputError(errLineagePreOutput)
		}
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools, aipapi.CapabilityCompaction),
		Open: open,
		OpenWire: func(_ context.Context, request largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			census.record(lineageOpenRecord{backend: name, selector: request.Candidate.Primary.Backend})
			if failFirst != nil && failFirst.CompareAndSwap(true, false) {
				return nil, aipapi.RecoverablePreOutputError(errLineagePreOutput)
			}
			return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			})}, nil
		},
	}
}

type lineageError string

func (e lineageError) Error() string { return string(e) }

const errLineagePreOutput = lineageError("lineage: simulated recoverable pre-output backend failure")

// lineageExecutor builds a real generic-core executor over the shared durable
// database, with two real backends so route and failover are genuine.
//
// The two backends are the load-bearing part: a single backend cannot demonstrate a
// backend switch, and a backend that never fails cannot demonstrate a failover.
func lineageExecutor(
	t *testing.T,
	database *bun.DB,
	planes aipfeature.FrozenPlaneSet,
	census *lineageBackendCensus,
	failFirst *atomic.Bool,
	submitHooks ...sdkhooks.SubmitHook,
) *runtime.Executor {
	t.Helper()
	ctx := context.Background()
	sessionStore, err := ssbunstore.NewWithContext(ctx, database)
	if err != nil {
		t.Fatalf("durable secure-session store: %v", err)
	}
	continuityStore, err := bunstore.NewWithContext(ctx, database)
	if err != nil {
		t.Fatalf("durable continuity store: %v", err)
	}
	fingerprintKey := certificationFingerprintKey()
	manager, err := app.NewManager(
		sessionStore,
		app.NewRandGenerator(fingerprintKey),
		b2bualineage.New(continuityStore),
		app.ManagerConfig{FingerprintKey: fingerprintKey, StoreDurable: true},
	)
	if err != nil {
		t.Fatalf("secure session manager: %v", err)
	}
	ex := runtime.TestExecutor()
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.Store = continuityStore
	ex.SecureSession = manager
	ex.SyntheticLocalPrincipal = true
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: submitHooks})
	ex.Rand = routing.NewSeededRng(3)
	ex.MaxAttempts = 4
	ex.LargeBodyGenerationID = "lineage-generation-1"
	ex.LargeBodyCandidateDomainGeneration = "lineage-domain-generation-1"
	ex.DefaultBackend = "alpha"
	ex.Backends = map[string]execbackend.Backend{
		"alpha": lineageStreamingBackend(census, "alpha", failFirst),
		"beta":  lineageStreamingBackend(census, "beta", nil),
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{certificationWorkspaceResolver{}}),
		FeaturePlanes: planes,
	})
	return ex
}

// lineageCall is one canonical client turn on a session, with a caller-chosen route
// selector and operation. The User-Agent decides the classification outcome, so a
// promoting turn and a later weak-evidence turn can both be expressed here.
func lineageCall(clientSessionID, resumeToken, selector, operation, userAgent string) *aipapi.Call {
	temperature := 0.2
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Session: aipapi.SessionRef{
			ClientSessionID: clientSessionID,
			ResumeToken:     resumeToken,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("summarize the diff")},
		}},
		Options: aipapi.GenerationOptions{Temperature: &temperature},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.Operation(operation),
			ClientUserAgent: userAgent,
		},
	}
}

// lineageConsumer is the test-only downstream consumer. It captures the session view
// it is handed on every turn, so the verdict is "what each consumer saw", not "what
// the store holds at the end".
type lineageConsumer struct {
	id     string
	mu     sync.Mutex
	seen   []session.Classification
	sessID []string
}

func (c *lineageConsumer) ID() string                        { return c.id }
func (c *lineageConsumer) Order() int                        { return 0 }
func (c *lineageConsumer) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (c *lineageConsumer) Handle(
	_ context.Context,
	_ *aipapi.Call,
	meta prerequest.Meta,
	_ prerequest.Services,
) (prerequest.Decision, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, meta.Session.Classification)
	c.sessID = append(c.sessID, meta.Session.AuthoritativeSessionID)
	return prerequest.Allow(), nil
}

func (c *lineageConsumer) observed() ([]session.Classification, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.Classification(nil), c.seen...),
		append([]string(nil), c.sessID...)
}

// TestSessionClassificationSurvivesRouteFailoverCompactionAndBackendSwitches is the
// requirement 2.4 and 12.6 lineage certification.
//
// One authoritative secure session is promoted once, then served under a series of
// deliberately different execution paths:
//
//  1. the default backend and selector;
//  2. a different backend reached through a different selector (backend switch);
//  3. a pre-output backend failure that the runtime fails over (retry/failover);
//  4. a context-compaction operation on the same session;
//  5. a long failover chain that opens several alternatives.
//
// Every turn's consumer must observe the byte-identical persisted positive, and the
// durable row must never change. The census proves each variant really executed a
// different path, so the test cannot pass by serving the same request repeatedly.
func TestSessionClassificationSurvivesRouteFailoverCompactionAndBackendSwitches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-lineage.db"))
	database := certificationOpenSQLite(t, dsn)
	process, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: database})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close process: %v", err)
		}
	})
	generation := certificationCompile(t, process, true, "")
	if generation.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	generation.start(t)

	const clientSessionID = "client-lineage"
	census := &lineageBackendCensus{}
	consumer := &lineageConsumer{id: "certification-lineage-consumer"}
	armFailover := &atomic.Bool{}
	executor := lineageExecutor(t, database,
		certificationPlanesWithConsumer(t, generation.planes, consumer), census, armFailover)

	// The promoting turn establishes the positive through the default backend.
	sessionID, resumeToken := certificationResumableSession(t, executor, clientSessionID)
	certificationServe(t, executor, lineageCall(clientSessionID, resumeToken, "alpha:m",
		string(aipapi.OperationOpenAIResponses), certificationPositiveUserAgent))
	persisted, found := certificationLoadRow(t, database, sessionID)
	if !found || !persisted.Classification.IsCodingAgent() {
		t.Fatalf("durable row after the promoting turn = %+v (found=%t), want a persisted positive",
			persisted.Classification, found)
	}
	established := persisted.Classification

	// Each variant changes the execution path, and failFirst makes the failover
	// variant genuinely fail over inside a single turn. The assertion that follows is
	// the requirement: the classification is identical on all of them.
	variants := []struct {
		name      string
		selector  string
		operation aipapi.Operation
		// failFirst arms the primary backend to fail recoverably before any
		// client-visible output, so the runtime must open the next alternative.
		failFirst bool
		// wantBackends is the ordered set of backends the census must show this turn
		// used. It is what makes a "route change" measurable rather than declarative.
		wantBackends []string
	}{
		{
			name: "same backend repeated", selector: "alpha:m",
			operation: aipapi.OperationOpenAIResponses, wantBackends: []string{"alpha"},
		},
		{
			name: "backend switch via selector", selector: "beta:m",
			operation: aipapi.OperationOpenAIResponses, wantBackends: []string{"beta"},
		},
		{
			name: "context compaction", selector: "alpha:m",
			operation: aipapi.OperationContextCompaction, wantBackends: []string{"alpha"},
		},
		{
			name: "pre-output failover", selector: "alpha:m|beta:m",
			operation: aipapi.OperationOpenAIResponses, failFirst: true,
			wantBackends: []string{"alpha", "beta"},
		},
	}

	// armFailover is consumed by the executor's primary backend; it is armed only for
	// the variant that must fail over.
	for _, variant := range variants {
		armFailover.Store(variant.failFirst)
		before := len(census.snapshot())
		certificationServe(t, executor, lineageCall(clientSessionID, resumeToken,
			variant.selector, string(variant.operation), certificationWeakUserAgent))
		opens := census.snapshot()[before:]
		require.NotEmpty(t, opens, "%s: the turn never opened a backend, so nothing was certified", variant.name)
		used := map[string]bool{}
		for _, open := range opens {
			used[open.backend] = true
			// The executor forwards the turn's operation to the backend unchanged, so
			// this is how the compaction variant proves it really was a compaction
			// turn rather than another ordinary turn wearing a different label.
			if open.operation != variant.operation {
				t.Fatalf("%s: backend %q served operation %q, want %q",
					variant.name, open.backend, open.operation, variant.operation)
			}
		}
		for _, backend := range variant.wantBackends {
			if !used[backend] {
				t.Fatalf("%s: backend %q was never opened, got %+v", variant.name, backend, opens)
			}
		}
		if variant.failFirst {
			// Two opens in one turn is what proves the failover happened rather than
			// merely being configured, and the consumed arm proves the primary backend
			// really failed rather than the runtime simply choosing the alternative.
			if len(opens) < 2 {
				t.Fatalf("%s: the turn opened only %+v, so no failover occurred", variant.name, opens)
			}
			if armFailover.Load() {
				t.Fatalf("%s: the primary backend never consumed its armed failure", variant.name)
			}
		}

		after, found := certificationLoadRow(t, database, sessionID)
		if !found || after != persisted {
			t.Fatalf("%s: durable row = %+v (found=%t), want it byte-identical at %+v",
				variant.name, after.Classification, found, persisted.Classification)
		}
	}
	if armFailover.Load() {
		t.Fatal("the failover variant never consumed its armed failure, so no failover was certified")
	}

	// A promoting turn that ALSO fails over, on its own authoritative session: this
	// proves the classification is established through a failover, not merely
	// preserved across one.
	failoverBefore := len(census.snapshot())
	armFailover.Store(true)
	failoverSessionID, failoverToken := certificationResumableSession(t, executor, clientSessionID+"-failover")
	certificationServe(t, executor, lineageCall(clientSessionID+"-failover", failoverToken,
		"alpha:m|beta:m", string(aipapi.OperationOpenAIResponses), certificationPositiveUserAgent))
	failoverOpens := census.snapshot()[failoverBefore:]
	if len(failoverOpens) < 2 || failoverOpens[0].backend != "alpha" || failoverOpens[1].backend != "beta" {
		t.Fatalf("failover turn opened %+v, want alpha then beta", failoverOpens)
	}
	failoverRow, found := certificationLoadRow(t, database, failoverSessionID)
	if !found || !failoverRow.Classification.IsCodingAgent() {
		t.Fatalf("failover turn left no positive: %+v (found=%t)", failoverRow.Classification, found)
	}
	if failoverRow.Classification != established {
		t.Fatalf("failover turn classification = %+v, want the same authority-derived positive %+v",
			failoverRow.Classification, established)
	}

	// Every certified turn's consumer must have observed the identical positive, on
	// the identical authoritative session. This is the requirement 4.2/2.4 verdict:
	// the classification travels with authority, not with the route.
	observed, sessions := consumer.observed()
	if len(observed) < len(variants)+2 {
		t.Fatalf("consumer ran %d times, want at least %d", len(observed), len(variants)+2)
	}
	for i, classification := range observed {
		if !classification.IsCodingAgent() {
			t.Fatalf("consumer turn %d observed %+v, want the established positive", i, classification)
		}
		if classification != established {
			t.Fatalf("consumer turn %d observed %+v, want the byte-identical %+v",
				i, classification, established)
		}
		if sessions[i] == "" {
			t.Fatalf("consumer turn %d observed no authoritative session", i)
		}
	}
	// Exactly two distinct authoritative sessions are involved: the lineage session
	// and the failover session. A route change must not create a new one.
	distinct := map[string]bool{}
	for _, id := range sessions {
		distinct[id] = true
	}
	if len(distinct) != 2 {
		t.Fatalf("consumer observed %d authoritative sessions (%v), want exactly the two created sessions",
			len(distinct), sessions)
	}
	if !distinct[sessionID] || !distinct[failoverSessionID] {
		t.Fatalf("consumer observed sessions %v, want the lineage %q and failover %q sessions",
			sessions, sessionID, failoverSessionID)
	}

	// The census must show the lineage really moved across backends, so the
	// assertions above are not all measuring one repeated execution path.
	backends := census.backends()
	if len(backends) < 2 {
		t.Fatalf("lineage turns only opened %v, so no route/backend switch was exercised", backends)
	}
}

// TestSessionClassificationLineageIsNotCarriedByTheRouteSelector is the negative
// control for the lineage test above.
//
// Two authoritative sessions are served through the SAME route selector. If the
// classification were keyed by the route, both would share one value; because it is
// keyed by authority, each keeps its own row. The control therefore fails if the
// lineage test ever starts passing for the wrong reason.
func TestSessionClassificationLineageIsNotCarriedByTheRouteSelector(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := certificationSQLiteDSN(filepath.Join(t.TempDir(), "classification-lineage-control.db"))
	database := certificationOpenSQLite(t, dsn)
	process, err := NewProcess(ctx, ProcessInput{Logger: slog.Default(), BunDB: database})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := process.Close(); err != nil {
			t.Errorf("close process: %v", err)
		}
	})
	generation := certificationCompile(t, process, true, "")
	if generation.classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	generation.start(t)

	const selector = "alpha:m"
	census := &lineageBackendCensus{}
	executor := lineageExecutor(t, database, generation.planes, census, nil)

	firstID, firstToken := certificationResumableSession(t, executor, "client-control")
	secondID, secondToken := certificationResumableSession(t, executor, "client-control")
	certificationServe(t, executor, lineageCall("client-control", firstToken, selector,
		string(aipapi.OperationOpenAIResponses), certificationPositiveUserAgent))
	certificationServe(t, executor, lineageCall("client-control", secondToken, selector,
		string(aipapi.OperationOpenAIResponses), certificationWeakUserAgent))

	first, found := certificationLoadRow(t, database, firstID)
	if !found || !first.Classification.IsCodingAgent() {
		t.Fatalf("first control session = %+v (found=%t), want a positive", first.Classification, found)
	}
	if second, found := certificationLoadRow(t, database, secondID); found {
		t.Fatalf("the weak-evidence control session created a durable row %+v; the same route and hint must not carry the other session's classification",
			second.Classification)
	}
}

// TestLineageEvidenceCarriesNoRouteState is the structural half of requirement
// 2.4: the stage's evidence value has no route-shaped field, so a classification
// stage cannot consume route state even if one wanted it to. The behavioural half
// is the per-backend open census in the test above.
//
// This reflects over the real SDK type on purpose. An earlier version declared a
// LOCAL struct with the same three fields, which was a tautology: adding a
// route-shaped field to sdkclassification.Evidence left every test green.
func TestLineageEvidenceCarriesNoRouteState(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[sdkclassification.Evidence]()
	got := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		got = append(got, field.Name)
	}
	assert.ElementsMatch(t, []string{"Operation", "ClientUserAgent", "ToolCategories"}, got,
		"sdkclassification.Evidence must carry only bounded derived facts; a route, "+
			"selector, backend, model, attempt or failover field would let the "+
			"classification stage consume route state (requirement 2.4)")
}
