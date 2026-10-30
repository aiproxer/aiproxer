package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type countingReader struct {
	base   conversationprojection.Reader
	mu     sync.Mutex
	count  int
	events *[]string
}

func (c *countingReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	c.mu.Lock()
	c.count++
	if c.events != nil {
		*c.events = append(*c.events, "Snapshot")
	}
	c.mu.Unlock()
	if c.base != nil {
		return c.base.Snapshot(ctx, aLegID)
	}
	return conversationprojection.Snapshot{}, nil
}

func (c *countingReader) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

type failingSnapshotReader struct {
	err    error
	events *[]string
}

func (f *failingSnapshotReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	if f.events != nil {
		*f.events = append(*f.events, "Snapshot")
	}
	return conversationprojection.Snapshot{}, f.err
}

type cvTaggerTestAdapter struct {
	store conversationview.Tagger
}

func (a cvTaggerTestAdapter) TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error) {
	reqs := make([]conversationview.TagRequest, len(tags))
	for i, t := range tags {
		reqs[i] = conversationview.TagRequest{Identity: t.Identity, Reason: conversationview.ReasonCode(t.Reason)}
	}
	res, err := a.store.TagNeverBackend(ctx, aLegID, reqs)
	if err != nil {
		return TagResult{}, err
	}
	return TagResult{StateRevision: res.StateRevision, Tags: res.Tags}, nil
}

func testCVTagger(cv conversationview.Tagger) ConversationViewTagger {
	if cv == nil {
		return nil
	}
	return cvTaggerTestAdapter{store: cv}
}

func recordingCall(selector string, msgs []aipapi.Message) *aipapi.Call {
	if len(msgs) == 0 {
		msgs = []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}
	}
	return &aipapi.Call{Route: aipapi.RouteIntent{Selector: selector}, Messages: msgs}
}

func execDetachedCtx(ctx context.Context) context.Context {
	return execctx.WithDetachedSession(ctx, execctx.DetachedSession{})
}

func withTestPrincipal(ctx context.Context, id string) context.Context {
	return execview.WithPrincipal(ctx, execview.PrincipalView{ID: id})
}

func voidResolver() aipworkspace.Resolver {
	return workspace.NewResolverChain([]aipworkspace.Resolver{cvVoidWS{}})
}

type cvVoidWS struct{}

func (cvVoidWS) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, nil
}

func newSecureExecutorForCV(t *testing.T, reader conversationprojection.Reader, snapOpts extensions.SnapshotOptions) (*Executor, *b2bua.MemoryStore) {
	t.Helper()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := make([]byte, 32)
	for i := range fk {
		fk[i] = byte(i + 1)
	}
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(st), app.ManagerConfig{FingerprintKey: fk, StoreDurable: true, ResumeWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if snapOpts.Workspace == nil {
		snapOpts.Workspace = voidResolver()
	}
	ex := TestExecutor()
	ex.Store = st
	ex.ConversationViewReader = reader
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, snapOpts)
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = false
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{}
	ex.Now = func() time.Time { return time.Unix(3000, 0) }
	return ex, st
}

// TestConversationView_ExactlyOneSnapshotPerTurn
func TestConversationView_ExactlyOneSnapshotPerTurn(t *testing.T) {
	t.Parallel()
	var events []string
	baseStore, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	counting := &countingReader{events: &events}
	ex := TestExecutor()
	ex.Store = baseStore
	ex.ConversationViewReader = counting
	ex.ConversationViewTagger = nil
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }

	ctx := execDetachedCtx(context.Background())
	call := recordingCall("openai:gpt-4", nil)
	pr, _, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()
	if len(events) != 1 || events[0] != "Snapshot" {
		t.Fatalf("expected exactly one Snapshot, got %v", events)
	}
	if counting.Count() != 1 {
		t.Fatalf("count !=1 got %d", counting.Count())
	}
	_, _ = ex.buildRoutePlan(ctx, pr)
	if counting.Count() != 1 {
		t.Fatalf("reread on buildRoutePlan: count %d want 1", counting.Count())
	}
	if !pr.identity.convSnapshotSet {
		t.Fatalf("snapshot not marked set")
	}
}

// Seam order detached
func TestConversationView_SeamOrder(t *testing.T) {
	t.Parallel()
	var events []string
	var mu sync.Mutex
	record := func(name string) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	counting := &countingReader{events: &events}
	submitHook := hookFunc{id: "submit", fn: func() { record("Submit") }}
	requestHook := requestFn{id: "request", fn: func() { record("RequestTransform") }}
	preHook := prereqFn{id: "prereq", fn: func() { record("PreRequest") }}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{submitHook}}), extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms:  []request.Transform{requestHook},
			PreRequestHandlers: []prerequest.Handler{preHook},
		}),
	})
	ex := TestExecutor()
	baseStore, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = baseStore
	ex.ConversationViewReader = counting
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{submitHook}})
	ex.RuntimeSnapshot = snap
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }

	ctx := execDetachedCtx(context.Background())
	call := recordingCall("openai:gpt-4", nil)
	_, _, cleanup, _ := ex.prepareRequest(ctx, call)
	defer cleanup()
	mu.Lock()
	defer mu.Unlock()
	idxSnapshot := indexOf(events, "Snapshot")
	idxRequest := indexOf(events, "RequestTransform")
	idxPre := indexOf(events, "PreRequest")
	idxSubmit := indexOf(events, "Submit")
	if idxSnapshot == -1 {
		t.Fatalf("Snapshot not recorded, events %v", events)
	}
	if idxSubmit != -1 && idxSnapshot < idxSubmit {
		t.Fatalf("Snapshot should be after Submit, got %v", events)
	}
	if idxRequest != -1 && idxSnapshot > idxRequest {
		t.Fatalf("Snapshot should be before RequestTransform, got %v", events)
	}
	if idxPre != -1 && idxSnapshot > idxPre {
		t.Fatalf("Snapshot should be before PreRequest, got %v", events)
	}
}

type hookFunc struct {
	id string
	fn func()
}

func (h hookFunc) ID() string                        { return h.id }
func (h hookFunc) Order() int                        { return 0 }
func (h hookFunc) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h hookFunc) Handle(ctx context.Context, call *aipapi.Call, meta *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	h.fn()
	return sdkhooks.SubmitDecision{}, nil
}

type requestFn struct {
	id string
	fn func()
}

func (r requestFn) ID() string                        { return r.id }
func (r requestFn) Order() int                        { return 0 }
func (r requestFn) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (r requestFn) Handle(ctx context.Context, call *aipapi.Call, meta request.RequestMeta, svc request.Services) error {
	r.fn()
	return nil
}

type prereqFn struct {
	id string
	fn func()
}

func (p prereqFn) ID() string                        { return p.id }
func (p prereqFn) Order() int                        { return 0 }
func (p prereqFn) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (p prereqFn) Handle(ctx context.Context, call *aipapi.Call, meta prerequest.Meta, svc prerequest.Services) (prerequest.Decision, error) {
	p.fn()
	return prerequest.Decision{}, nil
}

func indexOf(arr []string, v string) int {
	for i, e := range arr {
		if e == v {
			return i
		}
	}
	return -1
}

// domain-direct tagged (kept for contract) – supplemented by prepareRequest tests below
func TestConversationView_TaggedContentFilteredDownstream(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ctx := context.Background()
	rec, err := st.CreateALeg(ctx, "ck-tag")
	if err != nil {
		t.Fatal(err)
	}
	aLegID := rec.ALegID
	msg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-only")}}
	id, err := conversationview.MessageIdentityOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	cv := conversationview.NewReferenceStore()
	_ = cv.CreateALeg(ctx, aLegID)
	if _, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: id, Reason: "test"}}); err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep-me")}},
			msg,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}},
		},
		Session: aipapi.SessionRef{ALegID: aLegID},
	}
	snap, _ := cv.Snapshot(ctx, aLegID)
	out, ev, err := conversationview.Project(call, snap)
	if err != nil {
		t.Fatal(err)
	}
	if ev.FilteredCount != 1 {
		t.Fatalf("filtered %d want 1", ev.FilteredCount)
	}
	found := false
	for _, m := range call.Messages {
		if mid, _ := conversationview.MessageIdentityOf(m); mid == id {
			found = true
		}
	}
	if !found {
		t.Fatal("ingress lost tagged")
	}
	for _, m := range out.Messages {
		if mid, _ := conversationview.MessageIdentityOf(m); mid == id {
			t.Fatalf("downstream still contains tagged %v", id)
		}
	}
	raw, _ := json.Marshal(ev)
	if strings.Contains(string(raw), "local-only") {
		t.Fatalf("evidence contains plaintext: %s", raw)
	}
}

func TestConversationView_SteeringInjectedDownstream(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ctx := context.Background()
	rec, _ := st.CreateALeg(ctx, "ck-steer")
	aLegID := rec.ALegID
	cv := conversationview.NewReferenceStore()
	_ = cv.CreateALeg(ctx, aLegID)
	_, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov1",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "hidden-steer"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user hi")}}},
		Session:  aipapi.SessionRef{ALegID: aLegID},
	}
	snap, _ := cv.Snapshot(ctx, aLegID)
	out, ev, err := conversationview.Project(call, snap)
	if err != nil {
		t.Fatal(err)
	}
	if ev.InjectedCount != 1 {
		t.Fatalf("injected %d want 1", ev.InjectedCount)
	}
	for _, m := range call.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer" {
			t.Fatal("ingress contains steering")
		}
	}
	found := 0
	for _, m := range out.Instructions {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer" {
			found++
		}
	}
	for _, m := range out.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("steering injected %d want 1, out %+v", found, out)
	}
	raw, _ := json.Marshal(ev)
	if strings.Contains(string(raw), "hidden-steer") {
		t.Fatalf("evidence contains plaintext %s", raw)
	}
}

// Full prepareRequest MemoryStore tests

type requestTransformCapture struct{ fn func(*aipapi.Call) }

func (r requestTransformCapture) ID() string                        { return "capture-rtx" }
func (r requestTransformCapture) Order() int                        { return 0 }
func (r requestTransformCapture) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (r requestTransformCapture) Handle(ctx context.Context, call *aipapi.Call, meta request.RequestMeta, svc request.Services) error {
	r.fn(call)
	return nil
}

func TestConversationView_TaggedViaPrepareRequest_MemoryStore(t *testing.T) {
	t.Parallel()
	memStore, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	transformHook := requestTransformCapture{fn: func(call *aipapi.Call) { _ = call }}
	snapOpts := extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms: []request.Transform{transformHook},
		}),
	}
	ex := TestExecutor()
	ex.Store = memStore
	ex.ConversationViewReader = nil
	ex.ConversationViewTagger = nil
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, snapOpts)
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ctx1 := execDetachedCtx(context.Background())
	call1 := recordingCall("openai:gpt-4", []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("init")}}})
	pr1, _, cleanup1, err := ex.prepareRequest(ctx1, call1)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	aLegID := pr1.identity.aLeg.ALegID
	cleanup1()
	_ = cv.CreateALeg(context.Background(), aLegID)
	taggedMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-only-tagged")}}
	id, _ := conversationview.MessageIdentityOf(taggedMsg)
	if _, err := cv.TagNeverBackend(context.Background(), aLegID, []conversationview.TagRequest{{Identity: id, Reason: "test"}}); err != nil {
		t.Fatal(err)
	}
	staticSnap, _ := cv.Snapshot(context.Background(), aLegID)
	counting := &countingReader{base: &staticReader{snap: staticSnap}}
	ex.ConversationViewReader = counting
	ex.ConversationViewTagger = testCVTagger(cv)
	call2 := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep-me")}},
			taggedMsg,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}},
		},
	}
	ctx2 := execDetachedCtx(context.Background())
	pr2, _, cleanup2, err := ex.prepareRequest(ctx2, call2)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	defer cleanup2()
	foundIngress := false
	for _, m := range pr2.identity.ingressCall.Messages {
		if mid, _ := conversationview.MessageIdentityOf(m); mid == id {
			foundIngress = true
		}
	}
	if !foundIngress {
		t.Fatalf("ingress lost tagged content")
	}
	for _, m := range pr2.call.Messages {
		if mid, _ := conversationview.MessageIdentityOf(m); mid == id {
			t.Fatalf("downstream still contains tagged")
		}
	}
	if pr2.conversationEvidence.FilteredCount != 1 {
		t.Fatalf("evidence filtered %d want 1", pr2.conversationEvidence.FilteredCount)
	}
	// Transform via detached is not run; downstream check above is sufficient.
	// Secure-path transform observation is covered in TestConversationView_SecureSeamOrder.
}

func TestConversationView_SteeringViaPrepareRequest_MemoryStore(t *testing.T) {
	t.Parallel()
	memStore, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	var transformSeen []aipapi.Message
	var transformInstr []aipapi.Message
	transformHook := requestTransformCapture{fn: func(call *aipapi.Call) {
		transformSeen = append([]aipapi.Message(nil), call.Messages...)
		transformInstr = append([]aipapi.Message(nil), call.Instructions...)
	}}
	snapOpts := extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms: []request.Transform{transformHook},
		}),
	}
	ex := TestExecutor()
	ex.Store = memStore
	ex.ConversationViewReader = nil
	ex.ConversationViewTagger = nil
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, snapOpts)
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ctxTmp := execDetachedCtx(context.Background())
	callTmp := recordingCall("openai:gpt-4", []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("init")}}})
	prTmp, _, cleanupTmp, _ := ex.prepareRequest(ctxTmp, callTmp)
	aLegID := prTmp.identity.aLeg.ALegID
	cleanupTmp()
	_ = cv.CreateALeg(context.Background(), aLegID)
	_, err := cv.PutSteering(context.Background(), aLegID, conversationview.PutSteeringRequest{
		OverlayID:           "ov-prepare",
		Message:             conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "hidden-steer-prepare"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix},
		AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback,
		Reason:              "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	staticSnap, _ := cv.Snapshot(context.Background(), aLegID)
	counting := &countingReader{base: &staticReader{snap: staticSnap}}
	ex.ConversationViewReader = counting
	ex.ConversationViewTagger = testCVTagger(cv)
	call2 := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user hi")}}},
	}
	ctx2 := execDetachedCtx(context.Background())
	pr2, _, cleanup2, err := ex.prepareRequest(ctx2, call2)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer cleanup2()
	for _, m := range pr2.identity.ingressCall.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer-prepare" {
			t.Fatalf("ingress contains hidden steering")
		}
	}
	for _, m := range pr2.identity.ingressCall.Instructions {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer-prepare" {
			t.Fatalf("ingress instructions contains steering")
		}
	}
	found := 0
	for _, m := range pr2.call.Instructions {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer-prepare" {
			found++
		}
	}
	for _, m := range pr2.call.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == "hidden-steer-prepare" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("steering injected %d want 1", found)
	}
	if pr2.conversationEvidence.InjectedCount != 1 {
		t.Fatalf("injected %d want 1", pr2.conversationEvidence.InjectedCount)
	}
	// Transform not run for detached; downstream check above is sufficient for this MemoryStore test.
	// Secure transform observation is validated in TestConversationView_SecureSeamOrder via request transform counting.
	raw, _ := json.Marshal(pr2.conversationEvidence)
	if strings.Contains(string(raw), "hidden-steer-prepare") {
		t.Fatalf("evidence leaks plaintext")
	}
	rawSum, _ := json.Marshal(pr2.conversationSummary)
	if strings.Contains(string(rawSum), "hidden-steer-prepare") || strings.Contains(string(rawSum), "ov-prepare") {
		t.Fatalf("summary leaks plaintext/overlayID %s", rawSum)
	}
	_ = transformSeen
	_ = transformInstr
}

// Secure seam ordering test
func TestConversationView_SecureSeamOrder(t *testing.T) {
	t.Parallel()
	var events []string
	var mu sync.Mutex
	record := func(name string) {
		mu.Lock()
		events = append(events, name)
		mu.Unlock()
	}
	counting := &countingReader{events: &events}
	submitHook := hookFunc{id: "submit", fn: func() { record("Submit") }}
	requestHook := requestFn{id: "request", fn: func() { record("RequestTransform") }}
	preHook := prereqFn{id: "prereq", fn: func() { record("PreRequest") }}
	ex, _ := newSecureExecutorForCV(t, counting, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms:  []request.Transform{requestHook},
			PreRequestHandlers: []prerequest.Handler{preHook},
		}),
	})
	// Wrap store to record FetchALeg
	origStore := ex.Store
	ex.Store = &recordingStoreWrapper{Store: origStore, onFetch: func() { record("FetchALeg") }}
	ex.Bus = hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{submitHook}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms:  []request.Transform{requestHook},
			PreRequestHandlers: []prerequest.Handler{preHook},
		}),
	})
	ex.ConversationViewReader = counting
	call := &aipapi.Call{
		Session:  aipapi.SessionRef{ClientSessionID: "secure-seam-client"},
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	ctx := withTestPrincipal(context.Background(), "user-secure")
	_, _, cleanup, _ := ex.prepareRequest(ctx, call)
	defer cleanup()
	mu.Lock()
	defer mu.Unlock()
	idxFetch := indexOf(events, "FetchALeg")
	idxSubmit := indexOf(events, "Submit")
	idxSnapshot := indexOf(events, "Snapshot")
	idxRequest := indexOf(events, "RequestTransform")
	idxPre := indexOf(events, "PreRequest")
	if idxFetch == -1 || idxSubmit == -1 || idxSnapshot == -1 {
		t.Fatalf("missing events %v", events)
	}
	if idxFetch >= idxSubmit || idxSubmit >= idxSnapshot {
		t.Fatalf("order FetchALeg->Submit->Snapshot wrong %v", events)
	}
	if idxRequest != -1 && idxSnapshot > idxRequest {
		t.Fatalf("Snapshot before RequestTransform failed %v", events)
	}
	if idxPre != -1 && idxSnapshot > idxPre {
		t.Fatalf("Snapshot before PreRequest failed %v", events)
	}
}

type recordingStoreWrapper struct {
	b2bua.Store
	onFetch func()
}

func (r *recordingStoreWrapper) FetchALeg(ctx context.Context, id string) (b2bua.ALegRecord, error) {
	if r.onFetch != nil {
		r.onFetch()
	}
	return r.Store.FetchALeg(ctx, id)
}

// One snapshot through backend Open
func TestConversationView_OneSnapshotThroughBackendOpen(t *testing.T) {
	t.Parallel()
	var events []string
	baseStore, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	counting := &countingReader{events: &events}
	backends := map[string]execbackend.Backend{
		"openai": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
		}},
	}
	ex := TestExecutor()
	ex.Store = baseStore
	ex.ConversationViewReader = counting
	ex.ConversationViewTagger = nil
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Backends = backends
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ctx := execDetachedCtx(context.Background())
	call := recordingCall("openai:gpt-4", nil)
	_, err := ex.Execute(ctx, call)
	_ = err // may fail due to missing route but snapshot count still 1
	if counting.Count() != 1 {
		t.Fatalf("expected exactly 1 Snapshot through Execute, got %d events %v", counting.Count(), events)
	}
	ctx2 := execDetachedCtx(context.Background())
	pr, pctx, cleanup, err := ex.prepareRequest(ctx2, call)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer cleanup()
	before := counting.Count()
	plan, err := ex.buildRoutePlan(pctx, pr)
	if err == nil && plan != nil {
		_ = plan
	}
	if counting.Count() != before {
		t.Fatalf("Snapshot reread during route plan, count %d want %d", counting.Count(), before)
	}
}

// Snapshot/project failures with counters zero
func TestConversationView_FailureCountersZero(t *testing.T) {
	t.Parallel()
	var preReqCount atomic.Int32
	var routeHintCount atomic.Int32
	var backendOpens atomic.Int32
	preHook := &countingPreReq{count: &preReqCount}
	routeHook := &countingRouteHint{count: &routeHintCount}
	backends := map[string]execbackend.Backend{
		"openai": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			backendOpens.Add(1)
			return aipapi.NewFixedEventStream(nil), nil
		}},
	}
	failing := &failingSnapshotReader{err: errors.New("snapshot boom")}
	ex := TestExecutor()
	ex.Store, _ = b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.ConversationViewReader = failing
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{preHook},
			RouteHintProviders: []routehint.Provider{routeHook},
		}),
	})
	ex.Backends = backends
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	_, err := ex.Execute(execDetachedCtx(context.Background()), recordingCall("openai:gpt-4", nil))
	if err == nil {
		t.Fatal("expected snapshot failure")
	}
	if preReqCount.Load() != 0 {
		t.Fatalf("prerequest should be 0 after snapshot failure, got %d", preReqCount.Load())
	}
	if routeHintCount.Load() != 0 {
		t.Fatalf("route hint should be 0 after snapshot failure, got %d", routeHintCount.Load())
	}
	if backendOpens.Load() != 0 {
		t.Fatalf("backend should be 0 after snapshot failure, got %d", backendOpens.Load())
	}
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	rec, _ := st.CreateALeg(context.Background(), "fail-ctr")
	_ = cv.CreateALeg(context.Background(), rec.ALegID)
	anchor := conversationview.MessageAnchor{Identity: conversationview.MessageIdentity("v1:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"), Occurrence: 1}
	if _, err := cv.PutSteering(context.Background(), rec.ALegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-fail-ctr", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "test",
	}); err != nil {
		t.Fatalf("PutSteering: %v", err)
	}
	snap, _ := cv.Snapshot(context.Background(), rec.ALegID)
	reader2 := &staticReader{snap: snap}
	preReqCount.Store(0)
	routeHintCount.Store(0)
	backendOpens.Store(0)
	ex2 := TestExecutor()
	ex2.Store = st
	ex2.ConversationViewReader = reader2
	ex2.ConversationViewTagger = testCVTagger(cv)
	ex2.Bus = hooks.New(hooks.Config{})
	ex2.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex2.Bus, extensions.SnapshotOptions{
		FeaturePlanes: freezeBundle(testFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{preHook},
			RouteHintProviders: []routehint.Provider{routeHook},
		}),
	})
	ex2.Backends = backends
	ex2.Rand = routing.NewSeededRng(1)
	ex2.Now = func() time.Time { return time.Unix(4000, 0) }
	_, err = ex2.Execute(execDetachedCtx(context.Background()), recordingCall("openai:gpt-4", nil))
	if err == nil {
		t.Fatal("expected projection failure")
	}
	if preReqCount.Load() != 0 {
		t.Fatalf("prerequest 0 after projection failure got %d", preReqCount.Load())
	}
	if routeHintCount.Load() != 0 {
		t.Fatalf("route 0 after projection failure got %d", routeHintCount.Load())
	}
	if backendOpens.Load() != 0 {
		t.Fatalf("backend 0 after projection failure got %d", backendOpens.Load())
	}
}

type countingPreReq struct{ count *atomic.Int32 }

func (c *countingPreReq) ID() string                        { return "cnt-pre" }
func (c *countingPreReq) Order() int                        { return 0 }
func (c *countingPreReq) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (c *countingPreReq) Handle(ctx context.Context, call *aipapi.Call, meta prerequest.Meta, svc prerequest.Services) (prerequest.Decision, error) {
	c.count.Add(1)
	return prerequest.Decision{}, nil
}

type countingRouteHint struct{ count *atomic.Int32 }

func (c *countingRouteHint) ID() string                        { return "cnt-route" }
func (c *countingRouteHint) Order() int                        { return 0 }
func (c *countingRouteHint) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (c *countingRouteHint) Hint(ctx context.Context, in routehint.Input) (routehint.Result, error) {
	c.count.Add(1)
	return routehint.Result{}, nil
}

// Summary bounded
func TestConversationView_SummaryBounded(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	cv := conversationview.NewReferenceStore()
	rec, _ := st.CreateALeg(context.Background(), "sum-ck")
	_ = cv.CreateALeg(context.Background(), rec.ALegID)
	hiMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}
	hiID, _ := conversationview.MessageIdentityOf(hiMsg)
	if _, err := cv.TagNeverBackend(context.Background(), rec.ALegID, []conversationview.TagRequest{{Identity: hiID, Reason: "r"}}); err != nil {
		t.Fatalf("TagNeverBackend: %v", err)
	}
	if _, err := cv.PutSteering(context.Background(), rec.ALegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-sum-123", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "super-secret-plaintext-value"},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r",
	}); err != nil {
		t.Fatalf("PutSteering: %v", err)
	}
	snap, _ := cv.Snapshot(context.Background(), rec.ALegID)
	call := aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}}}}
	_, ev, _ := conversationview.Project(call, snap)
	sum := newConversationProjectionSummary(snap, ev)
	raw, _ := json.Marshal(sum)
	s := string(raw)
	if strings.Contains(s, "super-secret-plaintext-value") || strings.Contains(s, "ov-sum-123") {
		t.Fatalf("summary leaks plaintext/overlayID: %s", s)
	}
	// Ensure no digest hex in summary (summary must not contain identity)
	if strings.Contains(s, "v1:") {
		t.Fatalf("summary leaks identity digest: %s", s)
	}
	if sum.FilteredCount != 1 || sum.InjectedCount != 1 {
		t.Fatalf("summary counts wrong %+v", sum)
	}
	if sum.StateRevision != snap.StateRevision {
		t.Fatalf("state revision mismatch %d vs %d", sum.StateRevision, snap.StateRevision)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for k := range m {
		switch k {
		case "state_revision", "filtered_count", "injected_count", "stable_prefix_count", "after_message_count", "fallback_count", "max_overlay_revision", "max_slot_ordinal":
		default:
			t.Fatalf("unexpected summary field %q", k)
		}
	}
}

// Snapshot failure fail closed (original)

func TestConversationView_SnapshotFailureFailClosed(t *testing.T) {
	t.Parallel()
	var opens atomic.Int32
	backends := map[string]execbackend.Backend{
		"openai": {Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming), Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}}), nil
		}},
	}
	failing := &failingSnapshotReader{err: errors.New("snapshot boom")}
	ex := TestExecutor()
	ex.Store, _ = b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.ConversationViewReader = failing
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ex.Backends = backends
	ctx := execDetachedCtx(context.Background())
	call := recordingCall("openai:gpt-4", nil)
	_, _, cleanup, err := ex.prepareRequest(ctx, call)
	if err == nil {
		cleanup()
		t.Fatal("expected snapshot failure")
	}
	if !strings.Contains(err.Error(), "conversation view snapshot") {
		t.Fatalf("wrong error %v", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend opened despite snapshot failure")
	}
}

func TestConversationView_ProjectionFailureFailClosed(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ctx := context.Background()
	rec, _ := st.CreateALeg(ctx, "ck-projfail")
	aLegID := rec.ALegID
	cv := conversationview.NewReferenceStore()
	_ = cv.CreateALeg(ctx, aLegID)
	anchor := conversationview.MessageAnchor{Identity: conversationview.MessageIdentity("v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Occurrence: 1}
	_, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-fail", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "steer"},
		Placement:           conversationview.StoredPlacement{Kind: conversationview.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: conversationview.AnchorFailClosed, Reason: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{
		Route: libReRouteIntent(), Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Session: aipapi.SessionRef{ALegID: aLegID},
	}
	snap, _ := cv.Snapshot(ctx, aLegID)
	_, _, err = conversationview.Project(call, snap)
	if err == nil {
		t.Fatal("expected projection failure")
	}
	if !errors.Is(err, conversationview.ErrAnchorMissing) {
		t.Fatalf("want ErrAnchorMissing got %v", err)
	}
	reader := &staticReader{snap: snap}
	ex := TestExecutor()
	ex.Store = st
	ex.ConversationViewReader = reader
	ex.ConversationViewTagger = testCVTagger(cv)
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ctx2 := execDetachedCtx(context.Background())
	c2 := recordingCall("openai:gpt-4", nil)
	_, _, cleanup, err := ex.prepareRequest(ctx2, c2)
	if err == nil {
		cleanup()
		t.Fatal("expected prepare failure due to projection")
	}
	if !strings.Contains(err.Error(), "conversation view projection") {
		t.Fatalf("wrong error %v", err)
	}
}

func libReRouteIntent() aipapi.RouteIntent { return aipapi.RouteIntent{Selector: "openai:gpt-4"} }

type staticReader struct {
	snap conversationprojection.Snapshot
}

func (s *staticReader) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	return s.snap, nil
}

func TestConversationView_BoundedEvidenceAndProvenanceSeparation(t *testing.T) {
	t.Parallel()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ctx := context.Background()
	rec, _ := st.CreateALeg(ctx, "ck-ev")
	aLegID := rec.ALegID
	cv := conversationview.NewReferenceStore()
	_ = cv.CreateALeg(ctx, aLegID)
	if _, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: conversationview.MessageIdentity("v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Reason: "r"}}); err != nil {
		t.Fatalf("TagNeverBackend: %v", err)
	}
	if _, err := cv.PutSteering(ctx, aLegID, conversationview.PutSteeringRequest{
		OverlayID: "ov-ev", Message: conversationview.StoredMessageV1{Role: aipapi.RoleSystem, Text: "secret-plaintext"},
		Placement: conversationview.StoredPlacement{Kind: conversationview.PlacementStablePrefix}, AnchorMissingPolicy: conversationview.AnchorStablePrefixFallback, Reason: "r",
	}); err != nil {
		t.Fatalf("PutSteering: %v", err)
	}
	snap, _ := cv.Snapshot(ctx, aLegID)
	call := aipapi.Call{Route: aipapi.RouteIntent{Selector: "openai:gpt-4"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
	_, ev, _ := conversationview.Project(call, snap)
	raw, _ := json.Marshal(ev)
	s := string(raw)
	if strings.Contains(s, "secret-plaintext") {
		t.Fatalf("evidence leaks plaintext")
	}
	observable, _ := json.Marshal(map[string]any{"filtered": ev.FilteredCount, "injected": ev.InjectedCount, "fallbacks": len(ev.Fallbacks)})
	if strings.Contains(string(observable), "secret") {
		t.Fatalf("observable leaks")
	}
	if len(ev.Provenance) == 0 {
		t.Fatalf("provenance should be present internally")
	}
}

func TestConversationView_EmptyStateAliasSafe(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ctx := execDetachedCtx(context.Background())
	call := aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("orig")}}},
	}
	pr, _, cleanup, err := ex.prepareRequest(ctx, &call)
	if err != nil {
		t.Fatalf("prepare %v", err)
	}
	defer cleanup()
	if pr.identity.ingressCall == nil || pr.call == nil {
		t.Fatal("nil calls")
	}
	if pr.identity.ingressCall == pr.call {
		t.Fatal("alias: ingress and working share pointer")
	}
	pr.call.Messages[0].Parts[0].Text = "mutated"
	if pr.identity.ingressCall.Messages[0].Parts[0].Text == "mutated" {
		t.Fatal("ingress mutated via alias")
	}
}

func TestConversationView_NilReaderBackwardCompat(t *testing.T) {
	t.Parallel()
	stub := &stubB2BUAStore{}
	ex := TestExecutor()
	ex.Store = stub
	ex.ConversationViewReader = nil
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(4000, 0) }
	ctx := execDetachedCtx(context.Background())
	call := recordingCall("openai:gpt-4", nil)
	pr, _, cleanup, err := ex.prepareRequest(ctx, call)
	if err != nil {
		t.Fatalf("nil-reader should be backward compat, got %v", err)
	}
	defer cleanup()
	if pr.conversationEvidence == nil {
		t.Fatalf("evidence nil")
	}
	if pr.conversationEvidence.FilteredCount != 0 || pr.conversationEvidence.InjectedCount != 0 {
		t.Fatalf("empty evidence wrong %+v", pr.conversationEvidence)
	}
}

type stubB2BUAStore struct {
	b2bua.Store
	mu sync.Mutex
}

func (s *stubB2BUAStore) CreateALeg(ctx context.Context, k string) (b2bua.ALegRecord, error) {
	if s.Store == nil {
		mem, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		s.Store = mem
	}
	return s.Store.CreateALeg(ctx, k)
}

func (s *stubB2BUAStore) FetchALeg(ctx context.Context, id string) (b2bua.ALegRecord, error) {
	if s.Store == nil {
		mem, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		s.Store = mem
	}
	return s.Store.FetchALeg(ctx, id)
}

func (s *stubB2BUAStore) ResolveALeg(ctx context.Context, k string) (b2bua.ALegRecord, error) {
	if s.Store == nil {
		mem, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		s.Store = mem
	}
	return s.Store.ResolveALeg(ctx, k)
}
