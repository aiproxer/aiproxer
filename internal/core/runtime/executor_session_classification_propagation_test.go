package runtime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// classificationMatrix records what every later same-turn SessionView consumer
// observed for one client turn, keyed by consumer name. It is the same-turn
// metadata matrix named by design "Projection ordering": submit hook
// evidence/views, tool-catalog CatalogMeta, request RequestMeta, pre-request
// Meta, route-hint Input, and the execctx/session views attached afterwards.
type classificationMatrix struct {
	mu     *sync.Mutex
	seen   map[string]session.Classification
	sessID map[string]string
}

func newClassificationMatrix() *classificationMatrix {
	return &classificationMatrix{
		mu:     &sync.Mutex{},
		seen:   map[string]session.Classification{},
		sessID: map[string]string{},
	}
}

func (m *classificationMatrix) record(consumer string, view session.SessionView) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[consumer] = view.Classification
	m.sessID[consumer] = strings.TrimSpace(view.AuthoritativeSessionID)
}

func (m *classificationMatrix) get(consumer string) (session.Classification, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	got, ok := m.seen[consumer]
	return got, ok
}

func (m *classificationMatrix) authoritativeSessionID(consumer string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessID[consumer]
}

// recordContext projects the classification from the execctx aggregate view and
// from the public SDK session-view seam, which is what an operator-visible
// explanation surface reads (requirements 9.5, 1.6).
func (m *classificationMatrix) recordContext(ctx context.Context, consumer string) {
	views, ok := execctx.FromContext(ctx)
	if ok {
		m.record(consumer+"/execctx_views", views.Session)
	} else {
		m.record(consumer+"/execctx_views", session.SessionView{})
	}
	view, ok := session.SessionViewFromContext(ctx)
	if !ok {
		m.record(consumer+"/sdk_session_view", session.SessionView{})
		return
	}
	m.record(consumer+"/sdk_session_view", view)
}

// matrixSubmitHook is a submit-hook consumer that only records what the shared
// submit evidence view projects. It never classifies and never reads a store.
type matrixSubmitHook struct {
	id  string
	mtx *classificationMatrix
}

func (h matrixSubmitHook) ID() string                        { return h.id }
func (h matrixSubmitHook) Order() int                        { return 0 }
func (h matrixSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h matrixSubmitHook) Handle(ctx context.Context, _ *aipapi.Call, _ *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	ev := extensions.DecisionEvidenceFromContext(ctx)
	if ev == nil {
		return sdkhooks.SubmitDecision{}, nil
	}
	h.mtx.record("submit_hook_evidence", ev.Views.Session)
	return sdkhooks.SubmitDecision{}, nil
}

type matrixToolCatalogFilter struct {
	id  string
	mtx *classificationMatrix
}

func (f matrixToolCatalogFilter) ID() string                        { return f.id }
func (f matrixToolCatalogFilter) Order() int                        { return 0 }
func (f matrixToolCatalogFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f matrixToolCatalogFilter) Handle(_ context.Context, _ *aipapi.Call, meta toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	f.mtx.record("tool_catalog", meta.Session)
	return nil
}

type matrixRequestTransform struct {
	id  string
	mtx *classificationMatrix
}

func (t matrixRequestTransform) ID() string                        { return t.id }
func (t matrixRequestTransform) Order() int                        { return 0 }
func (t matrixRequestTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (t matrixRequestTransform) Handle(_ context.Context, _ *aipapi.Call, meta request.RequestMeta, _ request.Services) error {
	t.mtx.record("request_transform", meta.Session)
	return nil
}

type matrixPreRequestHandler struct {
	id  string
	mtx *classificationMatrix
}

func (h matrixPreRequestHandler) ID() string                        { return h.id }
func (h matrixPreRequestHandler) Order() int                        { return 0 }
func (h matrixPreRequestHandler) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h matrixPreRequestHandler) Handle(_ context.Context, _ *aipapi.Call, meta prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	h.mtx.record("pre_request", meta.Session)
	return prerequest.Allow(), nil
}

type matrixRouteHintProvider struct {
	id  string
	mtx *classificationMatrix
}

func (p matrixRouteHintProvider) ID() string                        { return p.id }
func (p matrixRouteHintProvider) Order() int                        { return 0 }
func (p matrixRouteHintProvider) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (p matrixRouteHintProvider) Hint(_ context.Context, in routehint.Input) (routehint.Result, error) {
	p.mtx.record("route_hint", in.Session)
	return routehint.Result{}, nil
}

// matrixSessionStartSink is the operator-visible session-start explanation sink.
// It runs after the turn's execctx view snapshot is attached and before any later
// re-projection, so it observes exactly what [execctx.ViewsFromSecureSubmit]
// produced for this turn.
type matrixSessionStartSink struct {
	mtx *classificationMatrix
}

func (matrixSessionStartSink) OnAuthDecision(context.Context, sdkauth.AuthDecisionEvent) error {
	return nil
}

func (s matrixSessionStartSink) OnSessionStart(ctx context.Context, _ sdkauth.SessionStartEvent) error {
	view, ok := session.SessionViewFromContext(ctx)
	if !ok {
		s.mtx.record("session_start_view", session.SessionView{})
		return nil
	}
	s.mtx.record("session_start_view", view)
	return nil
}

// wantMatrixConsumers is the exact set of same-turn SessionView consumers design
// "Projection ordering" requires to observe the one immutable classification.
var wantMatrixConsumers = []string{
	"submit_hook_evidence",
	"tool_catalog",
	"request_transform",
	"pre_request",
	"route_hint",
	"session_start_view",
	"later_execctx/execctx_views",
	"later_execctx/sdk_session_view",
}

func classificationMatrixExec(t *testing.T, mtx *classificationMatrix, classifier sessionclassification.Classifier) *Executor {
	t.Helper()
	ex := classificationExec(t)
	ex.Bus = hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{matrixSubmitHook{id: "matrix-submit", mtx: mtx}},
	})
	ex.AuthEvents = coreauth.NewEventDispatcher(matrixSessionStartSink{mtx: mtx}, coreauth.EventFailureBestEffort)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SessionClassifier:  classifier,
			ToolCatalogFilters: []toolcatalog.Filter{matrixToolCatalogFilter{id: "matrix-catalog", mtx: mtx}},
			RequestTransforms:  []request.Transform{matrixRequestTransform{id: "matrix-request", mtx: mtx}},
			PreRequestHandlers: []prerequest.Handler{matrixPreRequestHandler{id: "matrix-pre-request", mtx: mtx}},
			RouteHintProviders: []routehint.Provider{matrixRouteHintProvider{id: "matrix-route-hint", mtx: mtx}},
		}),
	})
	return ex
}

// TestSameTurnClassificationReachesAllLaterSessionViewConsumers is the same-turn
// metadata matrix required by requirements 4.2, 9.5 and 1.6: a positive
// classification decided on the first eligible turn must already be visible to
// the submit hook evidence view, the tool-catalog CatalogMeta, the request
// RequestMeta, the pre-request Meta, the route-hint Input and the later
// execctx/session views, all carrying one identical immutable snapshot produced
// by a single classifier invocation.
func TestSameTurnClassificationReachesAllLaterSessionViewConsumers(t *testing.T) {
	mtx := newClassificationMatrix()
	want := codingAgentSessionClassification("matrix.client_identity", 7)
	classifier := newSpySessionClassifier("classification-matrix", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return want, nil
	})

	ex := classificationMatrixExec(t, mtx, classifier)
	pr, outCtx, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()

	if pr == nil || pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}
	if got := pr.identity.preSession.Classification; got != want {
		t.Fatalf("preSession classification = %+v, want the projected positive %+v", got, want)
	}

	mtx.recordContext(outCtx, "later_execctx")

	for _, consumer := range wantMatrixConsumers {
		got, ok := mtx.get(consumer)
		if !ok {
			t.Fatalf("same-turn consumer %q did not observe any session view", consumer)
		}
		if got != want {
			t.Errorf("%s classification = %+v, want the same immutable snapshot %+v", consumer, got, want)
		}
		if !got.IsCodingAgent() {
			t.Errorf("%s cannot gate on a first-turn positive classification: %+v", consumer, got)
		}
		if mtx.authoritativeSessionID(consumer) == "" {
			t.Errorf("%s received an unbound authoritative session view", consumer)
		}
	}
	if got := len(classifier.inputs()); got != 1 {
		t.Fatalf("classifier invoked %d times per admitted client turn, want exactly 1", got)
	}
}

// TestSameTurnUnknownClassificationProjectsToEveryConsumer proves the views
// project whatever the single stage decided, and that an unknown result keeps
// every existing consumer behavior-neutral on the very same turn
// (requirements 1.2, 1.8).
func TestSameTurnUnknownClassificationProjectsToEveryConsumer(t *testing.T) {
	mtx := newClassificationMatrix()
	classifier := newSpySessionClassifier("classification-matrix-unknown", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return session.Classification{}, nil
	})

	ex := classificationMatrixExec(t, mtx, classifier)
	pr, outCtx, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest with an unknown classifier: %v", err)
	}
	defer cleanup()

	mtx.recordContext(outCtx, "later_execctx")

	if pr == nil || pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}
	for _, consumer := range wantMatrixConsumers {
		got, ok := mtx.get(consumer)
		if !ok {
			t.Fatalf("same-turn consumer %q did not run", consumer)
		}
		if got != (session.Classification{}) {
			t.Errorf("%s classification = %+v, want the conservative unknown projection", consumer, got)
		}
		if got.IsCodingAgent() {
			t.Errorf("%s exposed a positive classification that was never decided", consumer)
		}
	}
	if got := len(classifier.inputs()); got != 1 {
		t.Fatalf("classifier invoked %d times per admitted client turn, want exactly 1", got)
	}
}

// optInCodingAgentGate is the fake opt-in consumer used to prove first-turn
// positive gating is possible. It is test-only: no real feature gates on session
// classification until its own implementation opts in (requirement 1.8).
type optInCodingAgentGate struct {
	id       string
	mtx      *classificationMatrix
	gateHits *int
	mu       *sync.Mutex
}

func (g optInCodingAgentGate) ID() string                        { return g.id }
func (g optInCodingAgentGate) Order() int                        { return 0 }
func (g optInCodingAgentGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (g optInCodingAgentGate) Handle(_ context.Context, _ *aipapi.Call, meta prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	g.mtx.record("opt_in_consumer", meta.Session)
	if !meta.Session.Classification.IsCodingAgent() {
		return prerequest.Allow(), nil
	}
	g.mu.Lock()
	*g.gateHits++
	g.mu.Unlock()
	return prerequest.Deny("coding agent turn"), nil
}

func classificationOptInExec(t *testing.T, mtx *classificationMatrix, gate optInCodingAgentGate, classifier sessionclassification.Classifier) *Executor {
	t.Helper()
	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SessionClassifier:  classifier,
			PreRequestHandlers: []prerequest.Handler{gate},
		}),
	})
	return ex
}

// TestSameTurnOptInConsumerGatesOnFirstTurnPositive proves requirement 4.2 from
// the consumer side: a feature that opts into classification can gate on the
// very first eligible turn without waiting for a second user turn, reading the
// decision from the immutable snapshot alone.
func TestSameTurnOptInConsumerGatesOnFirstTurnPositive(t *testing.T) {
	mtx := newClassificationMatrix()
	var gateHits int
	var gateMu sync.Mutex
	gate := optInCodingAgentGate{
		id:       "opt-in-coding-agent-gate",
		mtx:      mtx,
		gateHits: &gateHits,
		mu:       &gateMu,
	}
	want := codingAgentSessionClassification("optin.client_identity", 1)
	classifier := newSpySessionClassifier("classification-opt-in", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return want, nil
	})

	ex := classificationOptInExec(t, mtx, gate, classifier)
	pr, _, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("opt-in consumer did not gate the positively classified first turn")
	}
	if !strings.Contains(err.Error(), "coding agent turn") {
		t.Fatalf("prepareRequest error = %v, want the opt-in consumer rejection", err)
	}
	if pr != nil {
		t.Fatal("a rejected turn must not produce a prepared request")
	}
	gateMu.Lock()
	hits := gateHits
	gateMu.Unlock()
	if hits != 1 {
		t.Fatalf("opt-in gate fired %d times on the first turn, want exactly 1", hits)
	}
	got, ok := mtx.get("opt_in_consumer")
	if !ok || got != want {
		t.Fatalf("opt-in consumer classification = %+v ok=%v, want %+v", got, ok, want)
	}
	if got := len(classifier.inputs()); got != 1 {
		t.Fatalf("classifier invoked %d times per admitted client turn, want exactly 1", got)
	}
}

// TestSameTurnOptInConsumerStaysNeutralWhileUnknown proves the opt-in consumer
// path is inert when no positive classification was decided, so existing
// behavior is preserved for every session the classifier does not promote.
func TestSameTurnOptInConsumerStaysNeutralWhileUnknown(t *testing.T) {
	mtx := newClassificationMatrix()
	var gateHits int
	var gateMu sync.Mutex
	gate := optInCodingAgentGate{
		id:       "opt-in-coding-agent-gate-unknown",
		mtx:      mtx,
		gateHits: &gateHits,
		mu:       &gateMu,
	}
	classifier := newSpySessionClassifier("classification-opt-in-unknown", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return session.Classification{}, nil
	})

	ex := classificationOptInExec(t, mtx, gate, classifier)
	pr, _, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest with unknown classification: %v", err)
	}
	defer cleanup()
	if pr == nil || pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}
	gateMu.Lock()
	hits := gateHits
	gateMu.Unlock()
	if hits != 0 {
		t.Fatalf("opt-in gate fired %d times without a positive classification, want 0", hits)
	}
	if got := len(classifier.inputs()); got != 1 {
		t.Fatalf("classifier invoked %d times per admitted client turn, want exactly 1", got)
	}
}

// TestSameTurnAbsentClassifierKeepsEveryConsumerUnknown proves requirement 11.7:
// with the classification feature removed the proxy still routes the turn and
// every later consumer simply sees the conservative unknown zero value, without
// any of them trying to classify on its own.
func TestSameTurnAbsentClassifierKeepsEveryConsumerUnknown(t *testing.T) {
	mtx := newClassificationMatrix()
	ex := classificationMatrixExec(t, mtx, nil)

	pr, outCtx, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest with the classifier absent: %v", err)
	}
	defer cleanup()
	if pr == nil || pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}

	mtx.recordContext(outCtx, "later_execctx")
	for _, consumer := range wantMatrixConsumers {
		got, ok := mtx.get(consumer)
		if !ok {
			t.Fatalf("same-turn consumer %q did not run while classification was absent", consumer)
		}
		if got != (session.Classification{}) {
			t.Errorf("%s classification = %+v, want unknown while the feature is absent", consumer, got)
		}
	}
}

// TestSameTurnPolicyDecisionExplainsClassification proves requirement 9.5: the
// operator-visible policy-decision explanation surface projects the bounded
// snapshot from the safe views alone, with no reach into feature-private
// classifier state.
func TestSameTurnPolicyDecisionExplainsClassification(t *testing.T) {
	want := codingAgentSessionClassification("policy.client_identity", 2)
	reqMeta := request.RequestMeta{
		TraceID: "trace-policy",
		Session: session.SessionView{
			AuthoritativeSessionID: "sess-1",
			ALegID:                 "aleg-1",
			Classification:         want,
		},
	}
	views := decisionViewsFromRequestMeta(reqMeta, execview.AttemptView{TraceID: "trace-policy"})
	if views.Session.Classification != want {
		t.Fatalf("decision views classification = %+v, want %+v", views.Session.Classification, want)
	}

	decisionCtx := extensions.BuildDecisionContext(views, "submit_request", "provider-1", extensions.DecisionContextOptions{})
	if decisionCtx.Session.Classification != want {
		t.Fatalf("policy decision context classification = %+v, want %+v", decisionCtx.Session.Classification, want)
	}
}

// TestSameTurnClassificationReachesAttemptViews proves the later per-candidate
// attempt view keeps the turn's classification in both projection branches: the
// full cloned session view and the reduced attempt-derived one (requirements
// 4.2, 1.6).
func TestSameTurnClassificationReachesAttemptViews(t *testing.T) {
	want := codingAgentSessionClassification("attempt.client_identity", 9)
	candidate := routing.AttemptCandidate{
		Key:     "backend:model",
		Primary: routing.Primary{Backend: "backend-1", Model: "model-1"},
	}
	attempt := aipapi.Call{Session: aipapi.SessionRef{ClientSessionID: "c-attempt"}}

	t.Run("reduced attempt view keeps classification", func(t *testing.T) {
		rf := requestFacts{recvTurnFacts: recvTurnFacts{
			recvViews:   execctx.Views{Session: session.SessionView{Classification: want}},
			recvViewsOK: true,
			aLegID:      "aleg-1",
		}}
		meta := TestExecutor().candidateAttemptMeta(context.Background(), rf, attempt, candidate, execbackend.Backend{})
		if meta.Session.Classification != want {
			t.Fatalf("attempt view classification = %+v, want %+v", meta.Session.Classification, want)
		}
	})

	t.Run("cloned session view keeps classification", func(t *testing.T) {
		rf := requestFacts{recvTurnFacts: recvTurnFacts{
			recvViews: execctx.Views{Session: session.SessionView{
				AuthoritativeSessionID: "sess-1",
				Classification:         want,
			}},
			recvViewsOK: true,
			aLegID:      "aleg-1",
		}}
		meta := TestExecutor().candidateAttemptMeta(context.Background(), rf, attempt, candidate, execbackend.Backend{})
		if meta.Session.Classification != want {
			t.Fatalf("attempt view classification = %+v, want %+v", meta.Session.Classification, want)
		}
	})

	t.Run("undecided turn stays unknown", func(t *testing.T) {
		rf := requestFacts{recvTurnFacts: recvTurnFacts{
			recvViews:   execctx.Views{Session: session.SessionView{}},
			recvViewsOK: true,
			aLegID:      "aleg-1",
		}}
		meta := TestExecutor().candidateAttemptMeta(context.Background(), rf, attempt, candidate, execbackend.Backend{})
		if meta.Session.Classification != (session.Classification{}) {
			t.Fatalf("attempt view classification = %+v, want unknown", meta.Session.Classification)
		}
	})
}

// sessionClassificationProjectionSites is the exact set of production files
// allowed to read the new SessionView.Classification scalar. Everything else in
// generic core, in the execctx view-copy helpers and in the public SDK must stay
// unaware of classification, which is what keeps every existing feature
// behavior-neutral (requirements 1.8, 11.7) and what proves later consumers
// cannot re-classify (requirement 1.6).
var sessionClassificationProjectionSites = map[string]bool{
	"executor_session_classification.go": true, // the single canonical stage
	"executor_prepare_secure.go":         true, // same-turn view construction
	"executor_attempt_transform.go":      true, // later attempt view construction
	"submit_views.go":                    true, // core view-copy helper
}

// sessionClassificationContractFiles are the files that own the SDK contract and
// the generated plane registry for classification. They define or wire the
// contract; they are not downstream consumers and therefore cannot gate on it.
var sessionClassificationContractFiles = map[string]bool{
	"classification.go":  true, // pkg/aipsdk/session: the snapshot itself
	"contracts.go":       true, // pkg/aipsdk/sessionclassification: the Classifier contract
	"plane_manifest.go":  true, // pkg/aipsdk/feature: hand-written plane descriptor
	"plane_generated.go": true, // pkg/aipsdk/feature: generated plane registry
}

// sessionClassificationContractImporters are the production files allowed to
// depend on the SDK classifier contract: the two generic-core classification
// stages (canonical and wire) plus the generated feature-plane registry. Any
// other importer could obtain a second, independent classification decision for
// the same turn.
var sessionClassificationContractImporters = map[string]bool{
	"executor_session_classification.go":      true,
	"executor_session_classification_wire.go": true,
	"plane_manifest.go":                       true,
	"plane_generated.go":                      true,
}

// TestSessionClassificationFieldHasNoExistingConsumer is the load-bearing static
// proof of requirement 1.8: within its scan roots, no existing generic-core
// consumer, no execctx view-copy/context helper and no public SDK view may read
// the classification field or call IsCodingAgent, so no existing feature can
// have started gating on it.
//
// Scan roots are internal/core/runtime, internal/core/execctx and pkg/aipsdk —
// the boundary of this task, "runtime meta construction + core view projection".
// This guard does NOT cover internal/core/extensions, which holds the sanctioned
// generic-core classification seam (extensions.RunSessionClassificationStage):
// that file legitimately reads the field and invokes the classifier, and it is
// the one place outside the two sanctioned stages where a second decision could
// be requested. Its production callers are executor_session_classification.go
// (canonical lane) and executor_session_classification_wire.go (wire lane); the
// per-turn classifier invocation counts in this package pin each to one call per
// admitted turn. A repo-wide guard across internal/plugins and
// internal/standardplugins belongs to task 12.1, not here.
func TestSessionClassificationFieldHasNoExistingConsumer(t *testing.T) {
	roots := []string{
		".",
		filepath.Join("..", "execctx"),
		filepath.Join("..", "..", "..", "pkg", "aipsdk"),
	}

	readers := map[string][]string{}
	classifierImporters := map[string]string{}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				return perr
			}
			base := filepath.Base(path)
			for _, imp := range file.Imports {
				imported := strings.Trim(imp.Path.Value, `"`)
				if strings.HasSuffix(imported, "pkg/aipsdk/sessionclassification") ||
					strings.HasSuffix(imported, "plugins/features/sessionclassification") {
					if classifierImporters[base] == "" {
						classifierImporters[base] = imported
					}
				}
			}
			ast.Inspect(file, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.SelectorExpr:
					if typed.Sel.Name == "Classification" {
						readers[base] = append(readers[base], "selector .Classification")
					}
				case *ast.Ident:
					if typed.Name == "IsCodingAgent" {
						readers[base] = append(readers[base], "call IsCodingAgent")
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}

	for file, hits := range readers {
		if sessionClassificationProjectionSites[file] || sessionClassificationContractFiles[file] {
			continue
		}
		t.Errorf("production file %q reads the session classification (%s) but is not an approved projection site; "+
			"existing features must stay behavior-neutral until they opt in (requirements 1.8, 11.7)", file, strings.Join(hits, ", "))
	}
	for file := range sessionClassificationProjectionSites {
		if _, ok := readers[file]; !ok {
			t.Errorf("approved classification projection site %q no longer touches the field; update the allowlist deliberately", file)
		}
	}

	for file, imported := range classifierImporters {
		if !sessionClassificationContractImporters[file] {
			t.Errorf("production file %q depends on the classifier contract %q; only the two sanctioned classification stages (canonical and wire) may classify", file, imported)
		}
	}
	for _, stage := range []string{"executor_session_classification.go", "executor_session_classification_wire.go"} {
		if _, ok := classifierImporters[stage]; !ok {
			t.Errorf("the sanctioned classification stage %q no longer depends on the SDK classifier contract", stage)
		}
	}
}
