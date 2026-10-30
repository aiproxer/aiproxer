package runtime

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// wireSessionClassificationStageFile is the single production file that owns
// the wire-path session-classification stage. The static guard below pins that
// it never names the canonical request package at all: the entire reason this
// feature needs a wire stage is that classification runs without
// reconstructing a aipapi.Call.
const wireSessionClassificationStageFile = "executor_session_classification_wire.go"

// wireClassificationWorkspaceID is the resolved workspace every wire
// classification fixture observes. PrepareSecureSession resolves it for the
// canonical and the wire lane alike, so it is the parity anchor between the two
// stage entry points.
const wireClassificationWorkspaceID = "workspace-wire-classification"

// wireClassificationUserAgent is the accepted client identity a certified
// frontend proof compiler captures (requirements 5.2, 5.3).
const wireClassificationUserAgent = "codex-cli/0.42.0 (accepted)"

// wireClassificationEvidence is the bounded evidence a certified frontend proof
// compiles for a coding-agent turn: the canonical operation, the accepted
// client identity, and the fixed tool-category bits the canonical tool-name
// classifier derives from read_file/apply_patch/bash. It carries no transcript,
// header bag or tool list.
var wireClassificationEvidence = sessionclassification.Evidence{
	Operation:       aipapi.OperationOpenAIChatCompletions,
	ClientUserAgent: wireClassificationUserAgent,
	ToolCategories: sessionclassification.ToolCategoryFileRead |
		sessionclassification.ToolCategoryFileEdit |
		sessionclassification.ToolCategoryOSCommand,
}

type wireClassificationWorkspaceResolver struct{}

func (wireClassificationWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{ID: wireClassificationWorkspaceID}, nil
}

// wireClassificationRecorder captures everything the wire stage hands the
// classifier plus which wire-lane steps had already run, so the ordering claim
// is measured from inside the classifier rather than assumed.
type wireClassificationRecorder struct {
	mu             sync.Mutex
	inputs         []sessionclassification.Input
	afterIngress   bool
	afterAuthority bool
}

func (r *wireClassificationRecorder) record(callCtx context.Context, in sessionclassification.Input) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, in)
	r.afterIngress = meteringHolderFrom(callCtx) != nil
	r.afterAuthority = requestAuthorityFrom(callCtx) != nil
}

func (r *wireClassificationRecorder) seen() []sessionclassification.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sessionclassification.Input(nil), r.inputs...)
}

func (r *wireClassificationRecorder) consumerOrder() (afterIngress, afterAuthority bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.afterIngress, r.afterAuthority
}

// wireClassificationClassifier is a stateful classifier keyed by proxy-owned
// authority: it promotes once on decisive evidence and returns the stored
// positive for every later turn. That makes the wire stage's load-bearing
// effect - the monotonic session-state transition - observable even though the
// wire lane has no SessionView consumer for the projection.
type wireClassificationClassifier struct {
	id    string
	rec   *wireClassificationRecorder
	class func(sessionclassification.Input) (session.Classification, error)

	mu    sync.Mutex
	state map[string]session.Classification
	calls atomic.Int32
}

func newWireClassificationClassifier(
	id string,
	rec *wireClassificationRecorder,
	class func(sessionclassification.Input) (session.Classification, error),
) *wireClassificationClassifier {
	return &wireClassificationClassifier{id: id, rec: rec, class: class, state: map[string]session.Classification{}}
}

func (c *wireClassificationClassifier) ID() string { return c.id }

func (c *wireClassificationClassifier) Classify(ctx context.Context, in sessionclassification.Input) (session.Classification, error) {
	c.calls.Add(1)
	c.rec.record(ctx, in)
	key := in.Session.AuthoritativeSessionID + "|" + in.Session.ALegID
	c.mu.Lock()
	defer c.mu.Unlock()
	if current := c.state[key]; current.IsCodingAgent() {
		return current, nil
	}
	if c.class == nil {
		return session.Classification{}, nil
	}
	result, err := c.class(in)
	if err != nil {
		return session.Classification{}, err
	}
	if result.IsCodingAgent() {
		c.state[key] = result
	}
	return result, nil
}

func (c *wireClassificationClassifier) positiveFor(sessionID, aLegID string) (session.Classification, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	got, ok := c.state[sessionID+"|"+aLegID]
	return got, ok
}

func (c *wireClassificationClassifier) callCount() int { return int(c.calls.Load()) }

// promoteOnDecisiveEvidence mirrors the stock local rule shape at the fidelity
// the wire lane can observe: a high-confidence accepted client identity alone
// promotes, and absent evidence never demotes the stored positive.
func promoteOnDecisiveEvidence(in sessionclassification.Input) (session.Classification, error) {
	if in.Evidence.ClientUserAgent == wireClassificationUserAgent {
		return codingAgentSessionClassification("wire.client_identity", 7), nil
	}
	return session.Classification{}, nil
}

// wireClassificationExecutor builds a wire-capable executor whose single
// backend counts OpenWire calls, so "no post-commit canonical fallback and no
// second ordinary execution" is measured rather than assumed.
//
// A non-nil RequestCoordinator is installed deliberately: without one,
// admitRequestAuthorityOnce returns the context unchanged and never installs
// requestAuthorityFrom, which would make any "ran before request authority"
// assertion a permanent tautology. With it, the ordering barrier is real.
func wireClassificationExecutor(t *testing.T, bundle testFeatureBundle) (*Executor, *testCountingMetricsSink, *atomic.Int32) {
	t.Helper()
	ex, metrics, _ := setupTestExecutor(t)
	var opens atomic.Int32
	ex.Backends = map[string]execbackend.Backend{
		"default": {
			OpenWire: func(context.Context, largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				})}, nil
			},
		},
	}
	// A single quota slot makes admitRequestAuthorityOnce install requestAuthorityFrom,
	// so the ordering barrier below observes a real authority marker instead of a
	// permanently false one.
	ex.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "wire-classification-slot", Class: authoritycoord.PriorityQuotaBudgetRate,
			Provider: &settleRecordingRequestProvider{id: "wire-classification"},
			Strength: authority.StrengthRequired,
		}},
	}
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{wireClassificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(bundle),
	})
	return ex, metrics, &opens
}

// wireClassificationProof builds the assessed proof a certified frontend hands
// the wire lane: bounded classification evidence plus session input, with no
// canonical Call anywhere (requirements 5.2, 5.5).
func wireClassificationProof(evidence sessionclassification.Evidence) largebody.Proof {
	return largebody.Proof{
		ProfileID: "openai-chat",
		Operation: aipapi.OperationOpenAIChatCompletions,
		Delivery:  aipapi.DeliveryModeStreaming,
		Identity:  largebody.NewIdentityDigest([32]byte{7, 7, 7, 7}),
		Session: largebody.SessionInput{
			ClientSessionID:     "client-sess-wire-classification",
			NewSessionRequested: true,
		},
		ClassificationEvidence: evidence,
	}
}

func wireClassificationBoundSession() session.SessionView {
	return session.SessionView{
		AuthoritativeSessionID: "sess-wire-classification",
		ClientSessionHint:      "client-sess-wire-classification",
		ALegID:                 "aleg-wire-classification",
		TurnID:                 "turn-wire-classification",
		WorkspaceID:            wireClassificationWorkspaceID,
	}
}

// TestWireSessionClassificationStageRunsAfterBindingWithProofEvidence is the
// load-bearing behavioural proof for the wire stage: ExecuteLargeBody must run
// the generic classification stage exactly once, after post-commit
// secure-session/A-leg/workspace binding and before the frontend-ingress
// checkpoint and request-authority admission, and it must hand the classifier
// the proof's bounded evidence rather than a reconstructed canonical request
// (requirements 4.1, 4.6, 5.2, 5.7, 12.7).
func TestWireSessionClassificationStageRunsAfterBindingWithProofEvidence(t *testing.T) {
	rec := &wireClassificationRecorder{}
	classifier := newWireClassificationClassifier("wire-classification", rec, promoteOnDecisiveEvidence)
	ex, _, opens := wireClassificationExecutor(t, testFeatureBundle{SessionClassifier: classifier})

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	proof := wireClassificationProof(wireClassificationEvidence)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-wire-classification"})
	ctx = largebody.ContextWithWireProof(ctx, proof, "req-wire-classification")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("ExecuteLargeBody: %v", err)
	}
	if res.Facts.RequestID != "req-wire-classification" {
		t.Fatalf("wire RequestID = %q, want the proof-derived wire identity", res.Facts.RequestID)
	}

	inputs := rec.seen()
	if len(inputs) != 1 {
		t.Fatalf("classifier invoked %d times per committed wire turn, want exactly 1", len(inputs))
	}
	got := inputs[0]

	if got.Evidence != wireClassificationEvidence {
		t.Fatalf("wire evidence = %+v, want the proof evidence %+v", got.Evidence, wireClassificationEvidence)
	}
	if got.Session.AuthoritativeSessionID == "" || got.Session.ALegID == "" || got.Session.TurnID == "" {
		t.Fatalf("wire classification ran before authority binding: session view %+v", got.Session)
	}
	if got.Session.WorkspaceID != wireClassificationWorkspaceID {
		t.Fatalf("bound session workspace = %q, want the resolved workspace %q",
			got.Session.WorkspaceID, wireClassificationWorkspaceID)
	}
	if got.Workspace.ID != wireClassificationWorkspaceID {
		t.Fatalf("wire workspace = %q, want the resolved secure-session workspace %q",
			got.Workspace.ID, wireClassificationWorkspaceID)
	}
	if got.TraceID == "" {
		t.Fatal("wire classifier input has an empty trace ID")
	}
	if got.Session.Classification.IsCodingAgent() {
		t.Fatal("wire classifier input must start from the pre-classification session view")
	}
	if afterIngress, afterAuthority := rec.consumerOrder(); afterIngress || afterAuthority {
		t.Fatalf("wire classification ran after a later wire consumer (ingress=%t authority=%t)", afterIngress, afterAuthority)
	}

	positive, ok := classifier.positiveFor(got.Session.AuthoritativeSessionID, got.Session.ALegID)
	if !ok || !positive.IsCodingAgent() {
		t.Fatalf("wire classification did not promote the bound authority: %+v (present=%t)", positive, ok)
	}
	if opens.Load() != 1 {
		t.Fatalf("wire backend opened %d times, want exactly 1 committed execution", opens.Load())
	}
}

// TestWireSessionClassificationStageFailsOpenWithoutCanonicalFallback pins
// requirements 5.7 and 4.4 on the wire lane: an erroring, invalid-output or
// evidence-starved classifier must leave the single committed wire execution
// untouched and must never ask for canonical materialization after the commit
// barrier.
func TestWireSessionClassificationStageFailsOpenWithoutCanonicalFallback(t *testing.T) {
	cases := []struct {
		name  string
		class func(sessionclassification.Input) (session.Classification, error)
	}{
		{
			name: "classifier error",
			class: func(sessionclassification.Input) (session.Classification, error) {
				return session.Classification{}, errors.New("classification state store unavailable")
			},
		},
		{
			name: "invalid classification output",
			class: func(sessionclassification.Input) (session.Classification, error) {
				return session.Classification{Kind: session.KindCodingAgent}, nil
			},
		},
		{
			name: "absent wire evidence stays unknown",
			class: func(in sessionclassification.Input) (session.Classification, error) {
				if in.Evidence.IsZero() {
					return session.Classification{}, nil
				}
				return codingAgentSessionClassification("wire.unexpected", 1), nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &wireClassificationRecorder{}
			classifier := newWireClassificationClassifier("wire-fail-open", rec, tc.class)
			ex, metrics, opens := wireClassificationExecutor(t, testFeatureBundle{SessionClassifier: classifier})

			src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
			proof := wireClassificationProof(sessionclassification.Evidence{})
			acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

			ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-wire-fail-open"})
			ctx = largebody.ContextWithWireProof(ctx, proof, "req-wire-fail-open")

			res, err := ex.ExecuteLargeBody(ctx, acc, src)
			if err != nil {
				t.Fatalf("classification failure rejected the committed wire turn: %v", err)
			}
			if res.Facts.RequestID != "req-wire-fail-open" {
				t.Fatalf("wire RequestID = %q, want the proof-derived wire identity: a canonical fallback would have re-derived it",
					res.Facts.RequestID)
			}
			if classifier.callCount() != 1 {
				t.Fatalf("classifier call count = %d, want 1", classifier.callCount())
			}
			if opens.Load() != 1 {
				t.Fatalf("wire backend opened %d times, want exactly 1 (no second execution, no canonical fallback)", opens.Load())
			}
			if got := metrics.newCalls.Load() + metrics.resumeCalls.Load(); got != 1 {
				t.Fatalf("BeginTurn committed %d times, want exactly 1", got)
			}
		})
	}
}

// recordingWireClassifier records every bounded input the wire stage hands the
// classifier while delegating the decision to the real process-owned classifier,
// so a test observes both what the stage passed and what production persisted.
type recordingWireClassifier struct {
	rec   *wireClassificationRecorder
	inner sessionclassification.Classifier
}

func (c recordingWireClassifier) ID() string { return c.inner.ID() }

func (c recordingWireClassifier) Classify(ctx context.Context, in sessionclassification.Input) (session.Classification, error) {
	c.rec.record(ctx, in)
	return c.inner.Classify(ctx, in)
}

// TestWireSessionClassificationStagePreservesPriorPositiveAcrossTurns proves the
// wire lane reuses the shared generic runner against the real process-owned
// classifier: the first decisive wire turn promotes the bound authority in the
// shared store, and a later wire turn with absent evidence re-observes that same
// stored positive without advancing or downgrading its revision
// (requirements 1.4, 2.6, 5.4).
func TestWireSessionClassificationStagePreservesPriorPositiveAcrossTurns(t *testing.T) {
	probe := newClassificationStateProbe(t)
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	rec := &wireClassificationRecorder{}
	ex := classificationProbeExec(t, probe, recordingWireClassifier{rec: rec, inner: real})

	bound := wireClassificationBoundSession()
	turns := []struct {
		name     string
		evidence sessionclassification.Evidence
	}{
		{name: "decisive first turn", evidence: wireClassificationEvidence},
		{name: "absent evidence later turn", evidence: sessionclassification.Evidence{}},
	}
	for i, turn := range turns {
		ex.runWireSessionClassificationStage(context.Background(), wireSessionClassificationInput{
			TraceID:   "trace-wire-monotonic",
			Session:   bound,
			Workspace: aipworkspace.WorkspaceView{ID: wireClassificationWorkspaceID},
			Evidence:  turn.evidence,
		})
		if got := real.callCount(); got != i+1 {
			t.Fatalf("%s: classifier invoked %d times, want one evaluation per admitted wire turn", turn.name, got)
		}
	}
	inputs := rec.seen()
	if len(inputs) != 2 {
		t.Fatalf("classifier observed %d inputs, want one per wire turn", len(inputs))
	}
	if inputs[0].Evidence != wireClassificationEvidence {
		t.Fatalf("first wire turn evidence = %+v, want the proof evidence", inputs[0].Evidence)
	}
	if !inputs[1].Evidence.IsZero() {
		t.Fatalf("second wire turn evidence = %+v, want absent evidence", inputs[1].Evidence)
	}

	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: bound.AuthoritativeSessionID}
	record, found, err := probe.inner.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("load wire classification state: %v", err)
	}
	if !found || !record.Classification.IsCodingAgent() {
		t.Fatalf("wire classification lost its positive across turns: found=%v record=%+v", found, record)
	}
	if record.Classification.Revision != 1 {
		t.Fatalf("wire classification revision = %d, want the established 1 (no re-promotion, no downgrade)", record.Classification.Revision)
	}
	if got := len(probe.rowKeys()); got != 1 {
		t.Fatalf("wire classification durable rows = %d, want exactly the one promoted row: %v", got, probe.rowKeys())
	}
}

// TestWireSessionClassificationStageAbsentPlaneIsNoop proves requirement 4.7 and
// design "If the plane is absent, this is a no-op": with no classifier plane
// contributed, the committed wire execution is unchanged.
func TestWireSessionClassificationStageAbsentPlaneIsNoop(t *testing.T) {
	ex, _, opens := wireClassificationExecutor(t, testFeatureBundle{})

	src := newTestSource(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	proof := wireClassificationProof(wireClassificationEvidence)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-wire-absent"})
	ctx = largebody.ContextWithWireProof(ctx, proof, "req-wire-absent")

	res, err := ex.ExecuteLargeBody(ctx, acc, src)
	if err != nil {
		t.Fatalf("wire execution with the classifier plane absent: %v", err)
	}
	if res.Facts.RequestID != "req-wire-absent" {
		t.Fatalf("wire RequestID = %q, want the proof-derived wire identity", res.Facts.RequestID)
	}
	if opens.Load() != 1 {
		t.Fatalf("wire backend opened %d times, want exactly 1", opens.Load())
	}
}

// TestWireSessionClassificationStageGuardsMissingExecutorState proves the stage
// is total: a nil receiver and an executor with no published generation
// snapshot are both no-ops that must not panic or reach a classifier.
func TestWireSessionClassificationStageGuardsMissingExecutorState(t *testing.T) {
	in := wireSessionClassificationInput{
		TraceID:   "trace-wire-guards",
		Session:   wireClassificationBoundSession(),
		Workspace: aipworkspace.WorkspaceView{ID: wireClassificationWorkspaceID},
		Evidence:  wireClassificationEvidence,
	}

	var nilExecutor *Executor
	nilExecutor.runWireSessionClassificationStage(context.Background(), in)

	// No published generation snapshot: the stage must return before it could
	// reach any classifier. The scenario below proves this is a real early
	// return rather than a permanent no-op.
	orphan := &Executor{}
	orphan.runWireSessionClassificationStage(context.Background(), in)

	// Once a snapshot exists the stage must actually reach the classifier, so
	// the guard above is a real early return and not a permanent no-op.
	rec := &wireClassificationRecorder{}
	orphan.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(nil, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{wireClassificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: newWireClassificationClassifier("wire-guards", rec, nil)}),
	})
	orphan.runWireSessionClassificationStage(context.Background(), in)
	if len(rec.seen()) != 1 {
		t.Fatalf("wire classification reached the classifier %d times with a published snapshot, want 1", len(rec.seen()))
	}
}

// TestWireSessionClassificationStageLogsOnlyBoundedFailures proves the stage
// wires the executor logger into the shared runner, so a classification failure
// surfaces as a bounded failure code and never as raw classifier error text
// (requirements 4.4, 7.5, 9.2).
func TestWireSessionClassificationStageLogsOnlyBoundedFailures(t *testing.T) {
	const secret = "raw client token: bearer abc123"
	var out strings.Builder
	logger := slog.New(slog.NewTextHandler(&out, nil))

	rec := &wireClassificationRecorder{}
	classifier := newWireClassificationClassifier("wire-log", rec, func(sessionclassification.Input) (session.Classification, error) {
		return session.Classification{}, errors.New(secret)
	})
	ex, _, _ := wireClassificationExecutor(t, testFeatureBundle{SessionClassifier: classifier})
	ex.Log = logger

	ex.runWireSessionClassificationStage(context.Background(), wireSessionClassificationInput{
		TraceID:   "trace-wire-log",
		Session:   wireClassificationBoundSession(),
		Workspace: aipworkspace.WorkspaceView{ID: wireClassificationWorkspaceID},
		Evidence:  wireClassificationEvidence,
	})

	logged := out.String()
	if logged == "" {
		t.Fatal("wire classification failure produced no bounded diagnostic")
	}
	if !strings.Contains(logged, "failure_code=classifier_error") {
		t.Fatalf("wire classification log = %q, want a bounded classifier_error failure code", logged)
	}
	if strings.Contains(logged, secret) {
		t.Fatalf("wire classification log leaked raw classifier error text: %q", logged)
	}
}

// TestCanonicalAndWireSessionClassificationShareInputAndOutcome is the
// canonical/wire differential for requirement 12.7 and design "canonical and
// wire evidence/outcome are differential-equal": both stage entry points hand
// the shared runner a byte-identical bounded Input and therefore produce the
// same classification outcome for the same evidence.
func TestCanonicalAndWireSessionClassificationShareInputAndOutcome(t *testing.T) {
	rec := &wireClassificationRecorder{}
	classifier := newWireClassificationClassifier("parity", rec, promoteOnDecisiveEvidence)

	canonicalCall := classificationCall()
	canonicalCall.Invocation.Operation = wireClassificationEvidence.Operation
	canonicalCall.Invocation.ClientUserAgent = wireClassificationUserAgent
	canonicalCall.Tools = []aipapi.ToolDef{{Name: "read_file"}, {Name: "apply_patch"}, {Name: "bash"}}
	if got := sessionClassificationEvidence(canonicalCall); got != wireClassificationEvidence {
		t.Fatalf("canonical derivation = %+v, want the wire proof evidence %+v", got, wireClassificationEvidence)
	}

	bound := wireClassificationBoundSession()
	ex, _, _ := wireClassificationExecutor(t, testFeatureBundle{SessionClassifier: classifier})

	canonicalIBT := &identityBoundTurn{
		traceID:    "trace-parity",
		preSession: bound,
		workspace:  aipworkspace.WorkspaceView{ID: wireClassificationWorkspaceID},
	}
	ex.runSessionClassificationStage(context.Background(), canonicalCall, canonicalIBT)
	ex.runWireSessionClassificationStage(context.Background(), wireSessionClassificationInput{
		TraceID:   "trace-parity",
		Session:   bound,
		Workspace: aipworkspace.WorkspaceView{ID: wireClassificationWorkspaceID},
		Evidence:  wireClassificationEvidence,
	})

	inputs := rec.seen()
	if len(inputs) != 2 {
		t.Fatalf("classifier observed %d inputs, want one per stage entry point", len(inputs))
	}
	if !reflect.DeepEqual(inputs[0], inputs[1]) {
		t.Fatalf("canonical and wire inputs differ:\n canonical %+v\n wire      %+v", inputs[0], inputs[1])
	}
	if inputs[0].Evidence != inputs[1].Evidence {
		t.Fatalf("canonical evidence %+v != wire evidence %+v", inputs[0].Evidence, inputs[1].Evidence)
	}
	canonicalOutcome := canonicalIBT.preSession.Classification
	wireOutcome, ok := classifier.positiveFor(bound.AuthoritativeSessionID, bound.ALegID)
	if !ok {
		t.Fatal("wire stage produced no classification outcome for the bound authority")
	}
	if canonicalOutcome != wireOutcome {
		t.Fatalf("canonical outcome %+v != wire outcome %+v", canonicalOutcome, wireOutcome)
	}
	if !canonicalOutcome.IsCodingAgent() {
		t.Fatalf("parity outcome = %+v, want a positive coding_agent classification", canonicalOutcome)
	}
}

// TestWireSessionClassificationStageFileNeverNamesCanonicalRequest is the
// static guard for the task's hard invariant: the wire stage production file
// must not reference the canonical request package at all, so it cannot
// reconstruct a aipapi.Call (requirements 5.5, 12.7).
func TestWireSessionClassificationStageFileNeverNamesCanonicalRequest(t *testing.T) {
	path := filepath.Join(".", wireSessionClassificationStageFile)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", wireSessionClassificationStageFile, err)
	}

	for _, imp := range file.Imports {
		if strings.Contains(imp.Path.Value, "pkg/aipapi") {
			t.Fatalf("%s imports the canonical request package %s; the wire stage must consume bounded proof facts only",
				wireSessionClassificationStageFile, imp.Path.Value)
		}
	}
	forbidden := map[string]bool{
		"Messages": true, "Items": true, "Instructions": true, "Parts": true,
		"Parameters": true, "Description": true, "SemanticExtensions": true,
		"Call": true, "Tools": true, "Invocation": true,
	}
	found := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if forbidden[sel.Sel.Name] {
			found[sel.Sel.Name] = true
		}
		return true
	})
	if len(found) != 0 {
		t.Fatalf("%s reads canonical request selectors %v; wire classification must consume bounded proof evidence only",
			wireSessionClassificationStageFile, found)
	}
}
