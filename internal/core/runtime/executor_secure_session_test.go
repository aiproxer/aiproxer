package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func testFingerprintKey32(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func testSecureManager(t *testing.T, st app.Store, b2 b2bua.Store) *app.Manager {
	t.Helper()
	m, err := app.NewManager(st, app.NewRandGenerator(testFingerprintKey32(t)), b2bualineage.New(b2), app.ManagerConfig{
		FingerprintKey: testFingerprintKey32(t),
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// setSecureSessionDenialMapper applies the production aipapi session-denial shape for tests that
// exercise the secure prepare path (composition root wires the same in runtimebundle).
func setSecureSessionDenialMapper(ex *Executor) *Executor {
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	return ex
}

type voidWS struct{}

func (voidWS) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, nil
}

type errWorkspaceResolver struct{}

func (errWorkspaceResolver) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, errors.New("workspace resolver unavailable")
}

type countSubmitHook struct{ n *atomic.Int32 }

func (c *countSubmitHook) ID() string                        { return "count_submit" }
func (c *countSubmitHook) Order() int                        { return 0 }
func (c *countSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (c *countSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	if c.n != nil {
		c.n.Add(1)
	}
	return sdkhooks.SubmitDecision{}, nil
}

func TestPrincipalRefFromScope_usesScopeTenant(t *testing.T) {
	t.Parallel()

	ref := principalRefFromScope(
		execview.PrincipalView{
			ID:     "user-1",
			Claims: map[string]string{"issuer": "issuer-1", "tenant": "legacy-tenant"},
		},
		scope.PrincipalScopeView{TenantID: scope.Known("scope-tenant")},
	)
	if ref.ID != "user-1" {
		t.Fatalf("ID: got %q want user-1", ref.ID)
	}
	if ref.Issuer != "issuer-1" {
		t.Fatalf("Issuer: got %q want issuer-1", ref.Issuer)
	}
	if ref.Tenant != "scope-tenant" {
		t.Fatalf("Tenant: got %q want scope-tenant", ref.Tenant)
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_newSession_replacesForgedALeg(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1700, 0) }
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "hint",
			ALegID:          "forged-aleg-should-not-stick",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, baseline, aLeg, _, outCtx, err := ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if aLeg.ALegID == "" || aLeg.ALegID == "forged-aleg-should-not-stick" {
		t.Fatalf("unexpected a-leg %q", aLeg.ALegID)
	}
	if call.Session.ALegID != aLeg.ALegID {
		t.Fatalf("call session aleg: want %q got %q", aLeg.ALegID, call.Session.ALegID)
	}
	if baseline.Session.ResumeToken != "" {
		t.Fatalf("baseline must not carry resume token to backends, got %q", baseline.Session.ResumeToken)
	}
	if call.Session.ResumeToken == "" {
		t.Fatal("expected issued resume token on client call for new secure session")
	}
	attempt := aipapi.CloneCall(baseline)
	if attempt.Session.ResumeToken != "" {
		t.Fatalf("backend attempt must not include resume bearer, got %q", attempt.Session.ResumeToken)
	}
	if attempt.Session.AuthoritativeSessionID != call.Session.AuthoritativeSessionID {
		t.Fatalf("attempt sid %q vs call sid %q", attempt.Session.AuthoritativeSessionID, call.Session.AuthoritativeSessionID)
	}
	v, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected views")
	}
	if v.Session.AuthoritativeSessionID == "" || v.Session.AuthoritativeSessionID == "forged-aleg-should-not-stick" {
		t.Fatalf("authoritative session id: %q", v.Session.AuthoritativeSessionID)
	}
	if v.Session.TurnID == "" {
		t.Fatal("expected turn id on session view")
	}
	st, ok := execctx.SecureSessionTurnFromContext(outCtx)
	if !ok || st.SessionID == "" || st.TurnID == "" {
		t.Fatalf("secure turn binding: ok=%v sid=%q tid=%q", ok, st.SessionID, st.TurnID)
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_requireWorkspaceID_denies(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionRequireWorkspaceID = true
	ex.Now = func() time.Time { return time.Unix(1900, 0) }
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "hint"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	var sd *aipapi.SessionDenialError
	if !errors.As(err, &sd) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
	if sd.Code() != aipapi.SessionDeniedPolicyUnavailable {
		t.Fatalf("want policy unavailable, got %v", sd.Code())
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_workspaceFailClosed_skipsSubmitHooks(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	var submitCount atomic.Int32
	bus := hooks.New(hooks.Config{SubmitHooks: []sdkhooks.SubmitHook{&countSubmitHook{n: &submitCount}}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewStrictChain([]aipworkspace.Resolver{errWorkspaceResolver{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionWorkspaceResolveFailClosed = true
	ex.Now = func() time.Time { return time.Unix(1950, 0) }
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "hint"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
	if submitCount.Load() != 0 {
		t.Fatalf("submit hooks should not run after fail-closed workspace, count=%d", submitCount.Load())
	}
}

func TestExecutor_prepareSubmitAndALeg_secure_invalidResumeIsDenial(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(1800, 0) }
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-z"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "hint",
			ResumeToken:     "not-a-valid-resume-token",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial, got %T %v", err, err)
	}
}

func TestExecutor_secureSession_failoverTwoOpens_memoryLatestTraceReflectsSecondModel(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	clock := time.Unix(2000, 0).UTC()
	var capturedAuthoritative string
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return clock }
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("temp"))
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if v, ok := execctx.FromContext(ctx); ok && v.Session.AuthoritativeSessionID != "" {
					capturedAuthoritative = v.Session.AuthoritativeSessionID
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-trace-2"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "hint-trace-2"},
		Route:   aipapi.RouteIntent{Selector: "bad:g1|ok:g2"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if capturedAuthoritative == "" {
		t.Fatal("expected authoritative session id captured from backend open context")
	}
	rec, err := memSS.LoadByID(context.Background(), domain.SessionID(capturedAuthoritative))
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.LatestAttemptTrace.ResolvedModel; got != "g2" {
		t.Fatalf("latest trace resolved model: want g2 got %q", got)
	}
	if rec.LatestAttemptTrace.AttemptSeq != 2 {
		t.Fatalf("expected second b-leg seq on successful open, got %d", rec.LatestAttemptTrace.AttemptSeq)
	}
}

func TestExecutor_secureSession_Open_recordsOutcomeMemory(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	var capturedAuthoritative string
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(2100, 0) }
	ex.Rand = routing.NewSeededRng(3)
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				if v, ok := execctx.FromContext(ctx); ok {
					capturedAuthoritative = v.Session.AuthoritativeSessionID
				}
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-outcome"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "hint-o"},
		Route:   aipapi.RouteIntent{Selector: "ok:model-x"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if capturedAuthoritative == "" {
		t.Fatal("expected authoritative session id")
	}
	rec, err := memSS.LoadByID(context.Background(), domain.SessionID(capturedAuthoritative))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.LatestAttemptOutcome.Success || rec.LatestAttemptOutcome.SurfaceState != domain.SurfaceSurfaced {
		t.Fatalf("outcome: %#v", rec.LatestAttemptOutcome)
	}
}

func TestExecutor_prepareSubmitAndALeg_syntheticLocalPrincipalWhenEnabled(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.Now = func() time.Time { return time.Unix(2200, 0) }
	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "c1",
			ALegID:          "forged-aleg",
			ContinuityKey:   "forged-ck",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, outCtx, err := ex.prepareSubmitAndALeg(context.Background(), ex.Bus, call)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected views")
	}
	if v.Principal.ID != syntheticLocalPrincipalID {
		t.Fatalf("principal id: want %q got %q", syntheticLocalPrincipalID, v.Principal.ID)
	}
	if v.Principal.Claims["issuer"] != syntheticLocalPrincipalIssuer {
		t.Fatalf("issuer: want %q got %q", syntheticLocalPrincipalIssuer, v.Principal.Claims["issuer"])
	}
	if v.Session.ALegID == "" || v.Session.ALegID == "forged-aleg" {
		t.Fatalf("unexpected aleg %q", v.Session.ALegID)
	}
}

func TestExecutor_prepareSubmitAndALeg_missingPrincipalWithoutSynthetic(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = false
	ex.Now = func() time.Time { return time.Unix(2210, 0) }
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "c1"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(context.Background(), ex.Bus, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial got %T %v", err, err)
	}
}

func TestExecutor_Execute_unauthenticatedSyntheticPrincipal_reachesBackend(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	var opens atomic.Int32
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.Now = func() time.Time { return time.Unix(2300, 0) }
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ctx := context.Background()
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "unsynth"},
		Route:   aipapi.RouteIntent{Selector: "ok:g"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	if opens.Load() != 1 {
		t.Fatalf("backend opens: want 1 got %d", opens.Load())
	}
}

func TestExecutor_Execute_unauthenticatedNoSynthetic_deniedWithoutBackendOpen(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	var opens atomic.Int32
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = false
	ex.Now = func() time.Time { return time.Unix(2310, 0) }
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	ctx := context.Background()
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "nosynth"},
		Route:   aipapi.RouteIntent{Selector: "ok:g"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, err = ex.Execute(ctx, call)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsSessionDenial(err) {
		t.Fatalf("want session denial got %T %v", err, err)
	}
	if opens.Load() != 0 {
		t.Fatalf("backend opens: want 0 got %d", opens.Load())
	}
}

// compileRecorderFake ensures [testkit.FakeSecureSessionRecorder] matches [SecureSessionRecorder] at compile time.
var _ SecureSessionRecorder = (*recorderFake)(nil)

type recorderFake struct{}

func (recorderFake) RecordClientTurnAfterGate(context.Context, app.ClientTurnRecordInput) error {
	return nil
}

func (recorderFake) RecordPostHookStreamEvent(context.Context, app.StreamEventRecordInput) error {
	return nil
}

func TestExecutor_secureSessionRecorder_fieldAssignable(t *testing.T) {
	t.Parallel()
	ex := TestExecutor()
	ex.SecureSessionRecorder = recorderFake{}
	if ex.SecureSessionRecorder == nil {
		t.Fatal("expected recorder")
	}
}
