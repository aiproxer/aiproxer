package runtime

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	featurehoststate "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// classificationStateProbe is a counting adapter around the real process-owned
// classification store. It is a *state-count* probe, not a snapshot reader: a
// durable classification row can only exist under a key the classifier asked to
// mutate (Promote, ClaimRemote, CompleteRemote), so the accepted-mutation keys
// form a conservative upper bound on the rows the classification feature owns.
// The bound is deliberately one-sided — an accepted no-op promotion (a store
// that already holds a positive for the key) is counted as a row — so a "want
// zero rows" assertion can never under-count. Load is counted separately and
// never allocates a row.
type classificationStateProbe struct {
	inner featurestate.Store

	mu        sync.Mutex
	loadErr   error
	writeErr  error
	loadKeys  []featurestate.Key
	rows      map[featurestate.Key]int
	mutations []string
}

var _ featurestate.Store = (*classificationStateProbe)(nil)

func newClassificationStateProbe(t *testing.T) *classificationStateProbe {
	t.Helper()
	store, err := featurehoststate.NewMemoryStore(featurehoststate.MemoryStoreConfig{}, featurehoststate.WithClock(func() time.Time {
		return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	}))
	if err != nil {
		t.Fatalf("new process-owned classification store: %v", err)
	}
	return &classificationStateProbe{inner: store, rows: map[featurestate.Key]int{}}
}

func (p *classificationStateProbe) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	p.mu.Lock()
	p.loadKeys = append(p.loadKeys, key)
	loadErr := p.loadErr
	p.mu.Unlock()
	if loadErr != nil {
		return featurestate.Record{}, false, loadErr
	}
	return p.inner.Load(ctx, key)
}

func (p *classificationStateProbe) Promote(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	p.recordAttempt("promote", key)
	p.mu.Lock()
	writeErr := p.writeErr
	p.mu.Unlock()
	if writeErr != nil {
		return featurestate.Record{}, false, writeErr
	}
	record, promoted, err := p.inner.Promote(ctx, key, proposal, now)
	if err == nil {
		p.recordAccepted("promote", key)
	}
	return record, promoted, err
}

func (p *classificationStateProbe) ClaimRemote(ctx context.Context, key featurestate.Key, now time.Time, maxAttempts uint32, leaseTTL, retryBackoff time.Duration) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	p.recordAttempt("claim_remote", key)
	p.mu.Lock()
	writeErr := p.writeErr
	p.mu.Unlock()
	if writeErr != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, writeErr
	}
	claim, record, ok, err := p.inner.ClaimRemote(ctx, key, now, maxAttempts, leaseTTL, retryBackoff)
	if err == nil {
		p.recordAccepted("claim_remote", key)
	}
	return claim, record, ok, err
}

func (p *classificationStateProbe) CompleteRemote(ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time) (featurestate.Record, error) {
	p.recordAttempt("complete_remote", claim.Key)
	p.mu.Lock()
	writeErr := p.writeErr
	p.mu.Unlock()
	if writeErr != nil {
		return featurestate.Record{}, writeErr
	}
	record, err := p.inner.CompleteRemote(ctx, claim, result, now)
	if err == nil {
		p.recordAccepted("complete_remote", claim.Key)
	}
	return record, err
}

// recordAttempt notes that the classification feature tried to own durable state
// under key. A row can only exist when a mutation reaches the store, so the
// accepted census below is what proves zero rows.
func (p *classificationStateProbe) recordAttempt(op string, key featurestate.Key) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutations = append(p.mutations, op+"@"+string(key.Kind)+":"+key.ID)
}

// recordAccepted notes that a mutation actually reached the process store, so a
// row now exists under key.
func (p *classificationStateProbe) recordAccepted(op string, key featurestate.Key) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows[key]++
}

// rowKeys returns the distinct classification state keys for which a durable
// mutation was accepted by the real process store. It is a conservative upper
// bound on the rows that exist, so zero entries proves zero rows exist.
// rowRecords returns the durable rows the probe observed the classifier touching,
// in a stable order, so a test can read back what was actually persisted instead
// of assuming a snapshot (requirements 6.9, 12.8).
func (p *classificationStateProbe) rowRecords() []featurestate.Key {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]featurestate.Key, 0, len(p.rows))
	for key := range p.rows {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (p *classificationStateProbe) rowKeys() []string {
	rows := p.rowRecords()
	out := make([]string, 0, len(rows))
	for _, key := range rows {
		out = append(out, string(key.Kind)+":"+key.ID)
	}
	sort.Strings(out)
	return out
}

func (p *classificationStateProbe) mutationLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.mutations...)
}

func (p *classificationStateProbe) loadedKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.loadKeys))
	for _, key := range p.loadKeys {
		out = append(out, string(key.Kind)+":"+key.ID)
	}
	return out
}

func (p *classificationStateProbe) failLoads(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loadErr = err
}

// failWrites makes every durable mutation fail before it reaches the real
// process store, modeling a write-side outage (quota, lock contention, lost
// connection) while keeping the census of rows the feature owns exact.
func (p *classificationStateProbe) failWrites(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeErr = err
}

// rowCount is the conservative upper bound on durable rows asserted by the
// isolation tests. It over-counts rather than under-counts, so zero is sound.
func (p *classificationStateProbe) rowCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.rows)
}

// probeStateAuthority resolves the probe as the process-owned state authority a
// generation-bound classifier is bound to.
type probeStateAuthority struct {
	store featurestate.Store
	err   error
}

func (a probeStateAuthority) ClassificationState() (featurestate.Store, error) {
	if a.err != nil {
		return nil, a.err
	}
	return a.store, nil
}

// countingClassifier records every classification request and delegates to the
// real standard-feature classifier, so the tests observe the production
// decision path instead of a stand-in.
type countingClassifier struct {
	mu    sync.Mutex
	calls int
	inner sessionclassification.Classifier
}

var _ sessionclassification.Classifier = (*countingClassifier)(nil)

func (c *countingClassifier) ID() string { return c.inner.ID() }

func (c *countingClassifier) Classify(ctx context.Context, in sessionclassification.Input) (session.Classification, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Classify(ctx, in)
}

func (c *countingClassifier) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// failingRemoteDecider is a hermetic stand-in for the Jev adapter: it performs
// no network and always fails with a bounded failure, which is the seam the
// executor-level fail-open proofs exercise (requirements 6.9, 12.8, 12.9).
type failingRemoteDecider struct {
	calls atomic.Int32
}

var _ featurestate.RemoteDecider = (*failingRemoteDecider)(nil)

func (d *failingRemoteDecider) Decide(context.Context, featurestate.RemoteInput) (featurestate.RemoteDecision, error) {
	d.calls.Add(1)
	return featurestate.RemoteDecision{}, errors.New("remote classification is unavailable in this hermetic fixture")
}

// newRealClassificationClassifier binds the real standard-feature classifier to
// the counting process store with the documented V1 default heuristic policy.
//
// A remote-capable mode receives a decider because NewClassifier refuses to build
// one without it (requirements 6.1, 6.10). The decider is a hermetic failing
// fake so no executor suite performs egress (requirement 12.9).
func newRealClassificationClassifier(t *testing.T, authority featurestate.StateAuthority, mode featurestate.Mode) *countingClassifier {
	t.Helper()
	cfg := featurestate.Config{Mode: mode}
	var remote featurestate.RemoteDecider
	if mode == featurestate.ModeJev || mode == featurestate.ModeHybrid {
		cfg.Remote = &featurestate.RemoteConfig{
			Provider:              "jev",
			APIKeyEnv:             "TYPESAFE_API_KEY",
			Timeout:               750 * time.Millisecond,
			MaxAttemptsPerSession: 1,
			LeaseTTL:              2 * time.Second,
			RetryBackoff:          0,
			PositiveThreshold:     0.9,
		}
		remote = &failingRemoteDecider{}
	}
	classifier, err := featurestate.NewClassifier(cfg, featurestate.ClassifierDeps{
		State:  authority,
		Remote: remote,
		Now:    func() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new real session classifier: %v", err)
	}
	return &countingClassifier{inner: classifier}
}

// codingHarnessCall is a turn whose bounded classification evidence is decisive
// twice over: a versioned high-confidence coding-harness identity (requirement
// 3.1) and a distinctive read/edit/OS-command tool cluster (requirement 3.3).
// Any code path that wrongly reaches the classifier on such a turn would both
// project a positive classification and write a durable row, which is what
// makes the isolation assertions below load-bearing.
func codingHarnessCall(clientSessionID, text string) *aipapi.Call {
	return &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: clientSessionID},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}},
		},
		Tools: []aipapi.ToolDef{
			{Name: "read_file"},
			{Name: "edit_file"},
			{Name: "bash"},
		},
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: "codex_cli_rs/0.42.0",
		},
	}
}

func classificationProbeExec(t *testing.T, probe *classificationStateProbe, classifier sessionclassification.Classifier) *Executor {
	t.Helper()
	ex := classificationExec(t)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace:     workspace.NewResolverChain([]aipworkspace.Resolver{classificationWorkspaceResolver{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{SessionClassifier: classifier}),
	})
	return ex
}

// TestDetachedAuxiliaryCallCreatesNoClassificationStateRow is the requirement 4.5
// proof named by design "Detached auxiliary calls": a proxy-owned detached
// auxiliary call runs under a private child A-leg and must not be treated as
// evidence of a new independent coding-agent client session.
//
// The parent client turn carries decisive coding evidence and is admitted
// through the canonical secure path, which promotes it and writes exactly one
// durable classification row under the proxy-owned secure-session key. The
// detached child then carries the very same decisive evidence. Because that
// evidence is sufficient to promote, the zero-row assertion below is only
// reachable if the detached preparation genuinely never opens a classification
// transaction — "no positive" would be trivially true for evidence that could
// not have promoted.
func TestDetachedAuxiliaryCallCreatesNoClassificationStateRow(t *testing.T) {
	probe := newClassificationStateProbe(t)
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	ex := classificationProbeExec(t, probe, real)

	parentCall := codingHarnessCall("client-parent", "refactor the repository")
	pr, parentCtx, cleanup, err := ex.prepareRequest(context.Background(), parentCall)
	if err != nil {
		t.Fatalf("parent prepareRequest: %v", err)
	}
	defer cleanup()
	if pr == nil || pr.identity == nil {
		t.Fatal("parent prepared request has no identity-bound turn")
	}
	parentSession := pr.identity.preSession.AuthoritativeSessionID
	parentALeg := pr.identity.aLeg.ALegID
	if parentSession == "" || parentALeg == "" {
		t.Fatalf("parent turn is not proxy-bound: session=%q a-leg=%q", parentSession, parentALeg)
	}
	if got := pr.identity.preSession.Classification; !got.IsCodingAgent() {
		t.Fatalf("parent classification = %+v, want a positive from the real classifier", got)
	}
	if rows := probe.rowKeys(); len(rows) != 1 || rows[0] != string(featurestate.ScopeSecureSession)+":"+parentSession {
		t.Fatalf("positive control: decisive coding evidence must write exactly one row under the proxy-owned secure session, got %v", rows)
	}
	parentMutations := len(probe.mutationLog())

	// The detached child replays the parent's decisive coding evidence.
	childCall := codingHarnessCall("client-detached-aux", "summarize the diff")
	childCtx := execctx.WithDetachedSession(parentCtx, execctx.DetachedSession{
		ParentSessionID: parentSession,
		ParentALegID:    parentALeg,
		ParentTraceID:   pr.identity.traceID,
	})
	childPR, childOutCtx, childCleanup, err := ex.prepareRequest(childCtx, childCall)
	if err != nil {
		t.Fatalf("detached auxiliary prepareRequest must be admitted: %v", err)
	}
	defer childCleanup()
	if childPR == nil || childPR.identity == nil {
		t.Fatal("detached prepared request has no identity-bound turn")
	}

	childALeg := childPR.identity.aLeg.ALegID
	if childALeg == "" || childALeg == parentALeg {
		t.Fatalf("detached child must own a private A-leg: got %q parent %q", childALeg, parentALeg)
	}
	if childPR.identity.preSession.AuthoritativeSessionID != "" {
		t.Fatalf("detached child must not inherit secure-session authority: %q", childPR.identity.preSession.AuthoritativeSessionID)
	}
	if got := childPR.identity.preSession.Classification; got != (session.Classification{}) {
		t.Fatalf("detached child classification = %+v, want the conservative unknown of design \"Detached auxiliary calls\"", got)
	}
	if views, ok := execctx.FromContext(childOutCtx); !ok || views.Session.Classification != (session.Classification{}) {
		t.Fatalf("detached child projected views leaked a classification: ok=%v view=%+v", ok, views.Session.Classification)
	}
	if views, ok := execctx.FromContext(childOutCtx); ok && views.Session.AuthoritativeSessionID != "" {
		t.Fatalf("detached child projected a secure-session authority: %+v", views.Session)
	}

	// The durable census: no state row may exist under the child A-leg key, and
	// no row at all may have been added by the detached turn.
	if rows := probe.rowKeys(); len(rows) != 1 || rows[0] != string(featurestate.ScopeSecureSession)+":"+parentSession {
		t.Fatalf("detached auxiliary call created classification state rows %v, want only the parent's secure-session row", rows)
	}
	if got := probe.rowCount(); got != 1 {
		t.Fatalf("detached auxiliary call changed the classification row count to %d, want 1", got)
	}
	if got := len(probe.mutationLog()); got != parentMutations {
		t.Fatalf("detached auxiliary call issued %d state mutations %v, want none", got-parentMutations, probe.mutationLog()[parentMutations:])
	}
	childKey := featurestate.Key{Kind: featurestate.ScopeALeg, ID: childALeg}
	for _, key := range probe.rowKeys() {
		if key == string(childKey.Kind)+":"+childKey.ID {
			t.Fatalf("classification state row created under the detached child A-leg key %s", key)
		}
	}
	for _, key := range probe.loadedKeys() {
		if key == string(childKey.Kind)+":"+childKey.ID {
			t.Fatalf("detached auxiliary call opened a classification transaction under its child A-leg key %s", key)
		}
	}
	if got := real.callCount(); got != 1 {
		t.Fatalf("classifier invoked %d times across parent + detached auxiliary turn, want exactly 1 (parent only)", got)
	}
}

// TestDetachedAuxiliaryCallCreatesNoRowWithoutCapturedParentLineage covers the
// remaining detached entry shape: an explicitly targeted A-leg with no captured
// parent lineage, which is the genuine external-ingress form of the detached
// preparation. It must still allocate a private child A-leg and still never open
// a classification transaction.
func TestDetachedAuxiliaryCallCreatesNoRowWithoutCapturedParentLineage(t *testing.T) {
	probe := newClassificationStateProbe(t)
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	ex := classificationProbeExec(t, probe, real)

	targeted := codingHarnessCall("", "extract")
	targeted.Session.ALegID = "explicitly-targeted-a-leg"
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{})
	pr, _, cleanup, err := ex.prepareRequest(ctx, targeted)
	if err != nil {
		t.Fatalf("detached prepareRequest without parent lineage: %v", err)
	}
	defer cleanup()
	if pr == nil || pr.identity == nil {
		t.Fatal("detached prepared request has no identity-bound turn")
	}
	childALeg := pr.identity.aLeg.ALegID
	if childALeg == "" || childALeg == "explicitly-targeted-a-leg" {
		t.Fatalf("detached child A-leg = %q, want a newly allocated private A-leg", childALeg)
	}
	if got := pr.identity.preSession.Classification; got != (session.Classification{}) {
		t.Fatalf("detached child classification = %+v, want unknown", got)
	}
	if got := probe.rowCount(); got != 0 {
		t.Fatalf("detached auxiliary call created classification rows %v, want zero", probe.rowKeys())
	}
	if got := len(probe.mutationLog()); got != 0 {
		t.Fatalf("detached auxiliary call issued state mutations %v, want none", probe.mutationLog())
	}
	if got := real.callCount(); got != 0 {
		t.Fatalf("classifier invoked %d times for a detached auxiliary call, want 0", got)
	}
}

// TestDetachedAuxiliaryStateCountIsIndependentOfParentPositiveKeepsParentRow
// proves the detached child is isolated from the parent's durable positive in
// both directions: it neither writes under the parent key nor erases it, and a
// second detached auxiliary call under a second private child A-leg still adds
// nothing. This is what keeps proxy-owned Jev/verifier/compression work from
// masquerading as, or perturbing, coding-client traffic.
func TestDetachedAuxiliaryStateCountIsIndependentOfParentPositiveKeepsParentRow(t *testing.T) {
	probe := newClassificationStateProbe(t)
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	ex := classificationProbeExec(t, probe, real)

	pr, parentCtx, cleanup, err := ex.prepareRequest(context.Background(), codingHarnessCall("client-parent", "refactor"))
	if err != nil {
		t.Fatalf("parent prepareRequest: %v", err)
	}
	defer cleanup()
	parentKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: pr.identity.preSession.AuthoritativeSessionID}

	for i := range 3 {
		childCtx := execctx.WithDetachedSession(parentCtx, execctx.DetachedSession{
			ParentSessionID: parentKey.ID,
			ParentALegID:    pr.identity.aLeg.ALegID,
		})
		childPR, _, childCleanup, err := ex.prepareRequest(childCtx, codingHarnessCall("client-detached-aux", "extract "+strings.Repeat("x", i+1)))
		if err != nil {
			t.Fatalf("detached auxiliary %d rejected: %v", i, err)
		}
		childALeg := childPR.identity.aLeg.ALegID
		childCleanup()
		if childALeg == "" {
			t.Fatalf("detached auxiliary %d has no private A-leg", i)
		}
	}

	if got := probe.rowCount(); got != 1 {
		t.Fatalf("classification row count = %d after three detached auxiliary calls, want 1 (parent only): %v", got, probe.rowKeys())
	}
	record, found, err := probe.inner.Load(context.Background(), parentKey)
	if err != nil {
		t.Fatalf("load parent classification state: %v", err)
	}
	if !found || !record.Classification.IsCodingAgent() {
		t.Fatalf("parent positive classification was disturbed by detached auxiliary work: found=%v record=%+v", found, record)
	}
	if got := real.callCount(); got != 1 {
		t.Fatalf("classifier invoked %d times, want exactly 1 (parent turn only)", got)
	}
}

// TestDetachedAuxiliaryCallKeepsRoutingSemantics proves requirement 4.6 for the
// detached shape: the absence of the classification transaction changes nothing
// about how the auxiliary call is routed or what the backend receives. The
// backend-observed call is compared field by field against the ingress call.
func TestDetachedAuxiliaryCallKeepsRoutingSemantics(t *testing.T) {
	probe := newClassificationStateProbe(t)
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	ex := classificationProbeExec(t, probe, real)

	var observed aipapi.Call
	ex.Backends = map[string]execbackend.Backend{
		"aux": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				observed = call
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(5)

	childCall := codingHarnessCall("client-detached-aux", "extract the summary")
	childCall.Route = aipapi.RouteIntent{Selector: "aux:model"}
	ingress := aipapi.CloneCall(*childCall)
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{ParentALegID: "parent-a-leg"})

	stream, err := ex.Execute(ctx, childCall)
	if err != nil {
		t.Fatalf("detached auxiliary execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("detached auxiliary stream: %v", err)
	}

	if observed.Route.Selector != "aux:model" {
		t.Fatalf("detached auxiliary route selector = %q, want the explicitly selected extractor route", observed.Route.Selector)
	}
	if observed.Invocation.Operation != ingress.Invocation.Operation {
		t.Fatalf("detached auxiliary operation rewritten: %v", observed.Invocation.Operation)
	}
	if observed.Invocation.ClientUserAgent != ingress.Invocation.ClientUserAgent {
		t.Fatalf("classification stage mutated the client User-Agent: %q", observed.Invocation.ClientUserAgent)
	}
	if len(observed.Tools) != len(ingress.Tools) {
		t.Fatalf("classification stage changed the tool set: %d want %d", len(observed.Tools), len(ingress.Tools))
	}
	for i := range observed.Tools {
		if observed.Tools[i].Name != ingress.Tools[i].Name {
			t.Fatalf("classification stage changed tool %d: %q want %q", i, observed.Tools[i].Name, ingress.Tools[i].Name)
		}
	}
	if !reflect.DeepEqual(observed.Options, ingress.Options) {
		t.Fatalf("classification stage changed the generation options: %+v want %+v", observed.Options, ingress.Options)
	}
	if got := probe.rowCount(); got != 0 {
		t.Fatalf("detached auxiliary call created classification rows %v, want zero", probe.rowKeys())
	}
	if got := real.callCount(); got != 0 {
		t.Fatalf("classifier invoked %d times for a detached auxiliary call, want 0", got)
	}
}

// TestDetachedAuxiliaryStateFailureStaysInert proves the durable-state failure
// seam cannot be reached from the detached path at all: with the process-owned
// state authority failing, a detached auxiliary call is still admitted and still
// writes nothing, because it never opens a classification transaction
// (requirements 4.4, 4.5).
func TestDetachedAuxiliaryStateFailureStaysInert(t *testing.T) {
	probe := newClassificationStateProbe(t)
	probe.failLoads(errors.New("classification state store unavailable"))
	real := newRealClassificationClassifier(t, probeStateAuthority{store: probe}, featurestate.ModeHeuristic)
	ex := classificationProbeExec(t, probe, real)

	childCall := codingHarnessCall("client-detached-aux", "extract")
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{ParentALegID: "parent-a-leg"})
	pr, _, cleanup, err := ex.prepareRequest(ctx, childCall)
	if err != nil {
		t.Fatalf("detached auxiliary must be admitted while durable state fails: %v", err)
	}
	defer cleanup()
	if pr == nil || pr.identity == nil {
		t.Fatal("detached prepared request has no identity-bound turn")
	}
	if got := pr.identity.preSession.Classification; got != (session.Classification{}) {
		t.Fatalf("detached child classification = %+v, want unknown", got)
	}
	if got := real.callCount(); got != 0 {
		t.Fatalf("classifier invoked %d times for a detached auxiliary call, want 0", got)
	}
	if got := probe.rowCount(); got != 0 {
		t.Fatalf("detached auxiliary call created classification rows %v, want zero", probe.rowKeys())
	}
	if got := probe.loadedKeys(); len(got) != 0 {
		t.Fatalf("detached auxiliary call read durable classification state %v, want none", got)
	}
}
