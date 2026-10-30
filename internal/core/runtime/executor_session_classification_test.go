package runtime

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// sessionClassificationStageFile is the single production file that owns the
// canonical session-classification stage. The static guard below pins that it
// reads bounded invocation metadata only.
const sessionClassificationStageFile = "executor_session_classification.go"

func codingAgentSessionClassification(evidence session.EvidenceCode, revision uint64) session.Classification {
	return session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   evidence,
		Revision:   revision,
	}
}

// spySessionClassifier records every bounded input it is handed so tests can
// assert both invocation count and the exact projected evidence.
type spySessionClassifier struct {
	id    string
	mu    *sync.Mutex
	seen  *[]sessionclassification.Input
	class func(context.Context, sessionclassification.Input) (session.Classification, error)
}

func newSpySessionClassifier(
	id string,
	class func(context.Context, sessionclassification.Input) (session.Classification, error),
) *spySessionClassifier {
	mu := &sync.Mutex{}
	return &spySessionClassifier{
		id:    id,
		mu:    mu,
		seen:  &[]sessionclassification.Input{},
		class: class,
	}
}

func (c *spySessionClassifier) ID() string { return c.id }

func (c *spySessionClassifier) Classify(ctx context.Context, in sessionclassification.Input) (session.Classification, error) {
	c.mu.Lock()
	*c.seen = append(*c.seen, in)
	c.mu.Unlock()
	if c.class == nil {
		return session.Classification{}, nil
	}
	return c.class(ctx, in)
}

func (c *spySessionClassifier) inputs() []sessionclassification.Input {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sessionclassification.Input(nil), *c.seen...)
}

type orderTrace struct {
	mu    *sync.Mutex
	trace *[]string
}

func (o *orderTrace) append(event string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	*o.trace = append(*o.trace, event)
}

func (o *orderTrace) index(event string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, v := range *o.trace {
		if v == event {
			return i
		}
	}
	return -1
}

func (o *orderTrace) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), *o.trace...)
}

type orderingSecretGuardSpy struct {
	id  string
	ord *orderTrace
}

func (g orderingSecretGuardSpy) ID() string                           { return g.id }
func (g orderingSecretGuardSpy) Order() int                           { return 0 }
func (g orderingSecretGuardSpy) FailureMode() secretguard.FailureMode { return secretguard.FailOpen }

func (g orderingSecretGuardSpy) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	g.ord.append("SecretGuard")
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

type orderingToolCatalogSpy struct {
	id  string
	ord *orderTrace
}

func (f orderingToolCatalogSpy) ID() string                        { return f.id }
func (f orderingToolCatalogSpy) Order() int                        { return 0 }
func (f orderingToolCatalogSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (f orderingToolCatalogSpy) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	f.ord.append("ToolCatalog")
	return nil
}

type orderingSubmitHookSpy struct {
	id  string
	ord *orderTrace
}

func (h orderingSubmitHookSpy) ID() string                        { return h.id }
func (h orderingSubmitHookSpy) Order() int                        { return 0 }
func (h orderingSubmitHookSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (h orderingSubmitHookSpy) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	h.ord.append("SubmitHooks")
	return sdkhooks.SubmitDecision{}, nil
}

type classificationWorkspaceResolver struct{}

func (classificationWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ID: "workspace-classification"}, nil
}

func classificationExec(t *testing.T) *Executor {
	t.Helper()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2bua store: %v", err)
	}
	mgr, err := app.NewManager(
		memory.New(memory.Options{SimulateDurable: true}),
		app.NewRandGenerator(testFingerprintKey32(t)),
		b2bualineage.New(b2),
		app.ManagerConfig{FingerprintKey: testFingerprintKey32(t), StoreDurable: true},
	)
	if err != nil {
		t.Fatalf("secure session manager: %v", err)
	}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.Bus = hooks.New(hooks.Config{})
	return ex
}

func classificationCall() *aipapi.Call {
	return &aipapi.Call{
		Session:      aipapi.SessionRef{ClientSessionID: "c-classification"},
		Instructions: []aipapi.Message{{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("SENTINEL_INSTRUCTION_BODY")}}},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("SENTINEL_USER_MESSAGE_BODY"), aipapi.TextPart("refactor src/main.go please")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("SENTINEL_ASSISTANT_MESSAGE_BODY")}},
		},
		Tools: []aipapi.ToolDef{
			{Name: "read_file", Description: "SENTINEL_TOOL_DESCRIPTION", Parameters: []byte(`{"sentinel":"SENTINEL_TOOL_PARAMETERS"}`)},
			{Name: "bash", Description: "SENTINEL_TOOL_DESCRIPTION", Parameters: []byte(`{"sentinel":"SENTINEL_TOOL_PARAMETERS"}`)},
			{Name: "mystery_tool", Description: "SENTINEL_TOOL_DESCRIPTION", Parameters: []byte(`{"sentinel":"SENTINEL_TOOL_PARAMETERS"}`)},
		},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: "codex-cli/0.42.0 (accepted)",
		},
	}
}

// TestSessionClassificationStageOrdering pins the canonical stage order required
// by requirements 4.1/4.2 and design "Canonical Execution Integration": the stage
// runs after BeginTurn/A-leg binding and secret guard, and before the
// frontend-ingress checkpoint, request-authority admission and submit.
func TestSessionClassificationStageOrdering(t *testing.T) {
	var trace []string
	var mu sync.Mutex
	ord := &orderTrace{mu: &mu, trace: &trace}

	var ingressBefore, admittedBefore, boundViews, projectionRecorded bool
	classifier := newSpySessionClassifier("classification-order", func(callCtx context.Context, in sessionclassification.Input) (session.Classification, error) {
		mu.Lock()
		defer mu.Unlock()
		trace = append(trace, "SessionClassification")
		ingressBefore = meteringHolderFrom(callCtx) != nil
		admittedBefore = requestAuthorityFrom(callCtx) != nil
		boundViews = in.Session.AuthoritativeSessionID != "" &&
			in.Session.ALegID != "" &&
			in.Session.TurnID != "" &&
			in.Session.WorkspaceID == "workspace-classification" &&
			in.Workspace.ID == "workspace-classification"
		projectionRecorded = in.Session.Classification == (session.Classification{})
		return codingAgentSessionClassification("order.client_identity", 1), nil
	})

	ex := classificationExec(t)
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{orderingSubmitHookSpy{id: "classification-order-submit", ord: ord}}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SecretGuards:       []secretguard.Guard{orderingSecretGuardSpy{id: "classification-order-secret", ord: ord}},
			SessionClassifier:  classifier,
			ToolCatalogFilters: []toolcatalog.Filter{orderingToolCatalogSpy{id: "classification-order-catalog", ord: ord}},
		}),
	})

	pr, _, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()

	secretIdx := ord.index("SecretGuard")
	classIdx := ord.index("SessionClassification")
	submitIdx := ord.index("SubmitHooks")
	catalogIdx := ord.index("ToolCatalog")
	if secretIdx < 0 || classIdx < 0 || submitIdx < 0 || catalogIdx < 0 {
		t.Fatalf("missing canonical stage markers in trace %v", ord.snapshot())
	}
	if secretIdx >= classIdx {
		t.Fatalf("session classification at %d must run after secret guard at %d: %v", classIdx, secretIdx, ord.snapshot())
	}
	if classIdx >= submitIdx {
		t.Fatalf("session classification at %d must run before submit at %d: %v", classIdx, submitIdx, ord.snapshot())
	}
	if classIdx >= catalogIdx {
		t.Fatalf("session classification at %d must run before the tool catalog at %d: %v", classIdx, catalogIdx, ord.snapshot())
	}
	if ingressBefore {
		t.Error("session classification ran after the frontend-ingress checkpoint was captured")
	}
	if admittedBefore {
		t.Error("session classification ran after request-authority admission")
	}
	if !boundViews {
		t.Error("session classification did not receive the bound secure-session and workspace views")
	}
	if !projectionRecorded {
		t.Error("session classification did not start from the pre-classification session view")
	}
	if got := classifier.inputs(); len(got) != 1 {
		t.Fatalf("classifier invoked %d times per admitted client turn, want exactly 1", len(got))
	}
	if pr == nil || pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}
	if got := pr.identity.preSession.Classification; !got.IsCodingAgent() {
		t.Fatalf("preSession classification = %+v, want the validated positive projection", got)
	}
}

// TestSessionClassificationStageBuildsCanonicalEvidence proves the bounded
// evidence is derived only from the accepted client User-Agent, the canonical
// operation and tool-name category bits, over the bound session/workspace views.
func TestSessionClassificationStageBuildsCanonicalEvidence(t *testing.T) {
	classifier := newSpySessionClassifier("classification-evidence", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return codingAgentSessionClassification("evidence.client_identity", 3), nil
	})

	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: classifier}),
	})

	pr, _, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()

	inputs := classifier.inputs()
	if len(inputs) != 1 {
		t.Fatalf("classifier invoked %d times, want 1", len(inputs))
	}
	got := inputs[0]

	wantEvidence := sessionclassification.Evidence{
		Operation:       aipapi.OperationOpenAIResponses,
		ClientUserAgent: "codex-cli/0.42.0 (accepted)",
		ToolCategories: sessionclassification.ToolCategoryFileRead |
			sessionclassification.ToolCategoryOSCommand |
			sessionclassification.ToolCategoryUnknownSeen,
	}
	if got.Evidence != wantEvidence {
		t.Fatalf("evidence = %+v, want %+v", got.Evidence, wantEvidence)
	}
	if got.TraceID == "" {
		t.Error("classifier input has an empty trace ID")
	}
	if got.Session.Classification.IsCodingAgent() {
		t.Error("classifier input must start from the pre-classification session view")
	}
	if got.Session.AuthoritativeSessionID == "" || got.Session.ALegID == "" || got.Session.TurnID == "" {
		t.Fatalf("classifier input session view is not bound: %+v", got.Session)
	}
	if got.Workspace.ID != "workspace-classification" {
		t.Fatalf("classifier input workspace = %q, want the resolved secure-session workspace", got.Workspace.ID)
	}
	if pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}
	if got := pr.identity.preSession.Classification; got != codingAgentSessionClassification("evidence.client_identity", 3) {
		t.Fatalf("preSession classification = %+v, want the validated classifier projection", got)
	}
}

// TestSessionClassificationEvidenceIgnoresRequestContent proves no message,
// instruction, item, tool-argument or tool-description traversal happens while
// building classification evidence.
func TestSessionClassificationEvidenceIgnoresRequestContent(t *testing.T) {
	withContent := classificationCall()
	stripped := classificationCall()
	stripped.Instructions = nil
	stripped.Messages = nil
	stripped.Items = nil
	for i := range stripped.Tools {
		stripped.Tools[i].Description = ""
		stripped.Tools[i].Parameters = nil
	}

	withContentEvidence := sessionClassificationEvidence(withContent)
	strippedEvidence := sessionClassificationEvidence(stripped)
	if withContentEvidence != strippedEvidence {
		t.Fatalf("evidence differs with/without request content: %+v vs %+v", withContentEvidence, strippedEvidence)
	}

	want := sessionclassification.Evidence{
		Operation:       aipapi.OperationOpenAIResponses,
		ClientUserAgent: "codex-cli/0.42.0 (accepted)",
		ToolCategories: sessionclassification.ToolCategoryFileRead |
			sessionclassification.ToolCategoryOSCommand |
			sessionclassification.ToolCategoryUnknownSeen,
	}
	if strippedEvidence != want {
		t.Fatalf("evidence = %+v, want %+v", strippedEvidence, want)
	}

	for _, sentinel := range []string{
		"SENTINEL_USER_MESSAGE_BODY", "SENTINEL_INSTRUCTION_BODY", "SENTINEL_ASSISTANT_MESSAGE_BODY",
		"SENTINEL_TOOL_DESCRIPTION", "SENTINEL_TOOL_PARAMETERS", "src/main.go",
	} {
		if strings.Contains(withContentEvidence.ClientUserAgent, sentinel) {
			t.Fatalf("evidence carries request content %q", sentinel)
		}
	}
}

// TestSessionClassificationStageFileDoesNotTouchRequestContent is the static
// guard behind "do not scan Messages/Items/Instructions": the canonical
// classification stage file may only read bounded invocation metadata.
func TestSessionClassificationStageFileDoesNotTouchRequestContent(t *testing.T) {
	path := filepath.Join(".", sessionClassificationStageFile)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", sessionClassificationStageFile, err)
	}

	forbidden := map[string]bool{
		"Messages": true, "Items": true, "Instructions": true, "Parts": true,
		"Parameters": true, "Description": true, "SemanticExtensions": true,
	}
	found := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok && forbidden[sel.Sel.Name] {
			found[sel.Sel.Name] = true
		}
		return true
	})
	if len(found) != 0 {
		t.Fatalf("%s traverses request content selectors %v; classification evidence must come from bounded invocation metadata only", sessionClassificationStageFile, found)
	}
}

// TestSessionClassificationStagePreservesPriorPositive proves an established
// positive classification is immutable at this stage: the stage short-circuits
// at extensions.RunSessionClassificationStage, so a classifier that would
// propose a different valid classification never gets the chance to replace it.
//
// The injected classifier deliberately returns a WELL-FORMED positive different
// from the prior one. A classifier returning only an error or an invalid value
// would leave the observed value unchanged even if the short-circuit were
// removed (the validate gate turns those into no-ops), so those rows could
// never fail and were removed. The per-failure-class proof and the
// invocation-count half live in
// TestSessionClassificationFailurePreservesPersistedPositive and
// TestSessionClassificationEstablishedPositiveSkipsClassifier
// (executor_session_classification_failure_test.go, task 6.3).
func TestSessionClassificationStagePreservesPriorPositive(t *testing.T) {
	priorPositive := codingAgentSessionClassification("prior.persisted_state", 2)
	downgradeCandidate := codingAgentSessionClassification("classifier.would_overwrite", 9)
	classifier := newSpySessionClassifier("classification-preserve", func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return downgradeCandidate, nil
	})

	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: classifier}),
	})
	ibt := &identityBoundTurn{
		traceID:    "trace-classification-preserve",
		preSession: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "aleg-1", Classification: priorPositive},
		workspace:  aipworkspace.WorkspaceView{ID: "workspace-classification"},
	}

	ex.runSessionClassificationStage(context.Background(), classificationCall(), ibt)
	if ibt.preSession.Classification != priorPositive {
		t.Fatalf("classification = %+v, want the preserved prior positive %+v: an established positive is immutable", ibt.preSession.Classification, priorPositive)
	}
}

// TestSessionClassificationStageFailOpenWithoutPriorPositive proves a failing
// classifier leaves the session conservatively unknown without rejecting the
// request.
func TestSessionClassificationStageFailOpenWithoutPriorPositive(t *testing.T) {
	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: newSpySessionClassifier(
			"classification-fail-open",
			func(context.Context, sessionclassification.Input) (session.Classification, error) {
				return session.Classification{}, errors.New("state store unavailable")
			},
		)}),
	})
	ibt := &identityBoundTurn{
		traceID:    "trace-classification-fail-open",
		preSession: session.SessionView{AuthoritativeSessionID: "sess-1", ALegID: "aleg-1"},
		workspace:  aipworkspace.WorkspaceView{ID: "workspace-classification"},
	}

	ex.runSessionClassificationStage(context.Background(), classificationCall(), ibt)
	if ibt.preSession.Classification != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown", ibt.preSession.Classification)
	}
}

// TestSessionClassificationStageDisabledIsBehaviorNeutral proves that with the
// feature absent the request is still admitted and later consumers still run,
// with no classification-stage marker in the canonical order.
func TestSessionClassificationStageDisabledIsBehaviorNeutral(t *testing.T) {
	var trace []string
	var mu sync.Mutex
	ord := &orderTrace{mu: &mu, trace: &trace}

	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			ToolCatalogFilters: []toolcatalog.Filter{orderingToolCatalogSpy{id: "classification-disabled-catalog", ord: ord}},
		}),
	})

	pr, _, cleanup, err := ex.prepareRequest(context.Background(), classificationCall())
	if err != nil {
		t.Fatalf("prepareRequest with classification absent: %v", err)
	}
	defer cleanup()

	if ord.index("SessionClassification") >= 0 {
		t.Fatalf("classification stage ran while absent: %v", ord.snapshot())
	}
	if ord.index("ToolCatalog") < 0 {
		t.Fatalf("later consumer did not run while classification absent: %v", ord.snapshot())
	}
	if pr.identity == nil {
		t.Fatal("prepared request has no identity-bound turn")
	}
	if got := pr.identity.preSession.Classification; got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown while the feature is absent", got)
	}
}

// TestSessionClassificationEvidenceRejectsUnacceptedUserAgent proves generic core
// re-applies the canonical identity acceptance policy instead of forwarding an
// unbounded or control-bearing User-Agent value to the classifier.
func TestSessionClassificationEvidenceRejectsUnacceptedUserAgent(t *testing.T) {
	blank := classificationCall()
	blank.Invocation.ClientUserAgent = ""

	control := classificationCall()
	control.Invocation.ClientUserAgent = "codex\x00cli"

	oversized := classificationCall()
	oversized.Invocation.ClientUserAgent = strings.Repeat("u", 513)

	for name, call := range map[string]*aipapi.Call{"blank": blank, "control": control, "oversized": oversized} {
		if got := sessionClassificationEvidence(call).ClientUserAgent; got != "" {
			t.Errorf("%s user agent evidence = %q, want empty", name, got)
		}
	}
}
