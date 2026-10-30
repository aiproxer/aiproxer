package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type failingSubmitHook struct{}

func (failingSubmitHook) ID() string                        { return "submit-fail" }
func (failingSubmitHook) Order() int                        { return 0 }
func (failingSubmitHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (failingSubmitHook) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, errors.New("submit boom")
}

type captureSessionOpener struct {
	seen   session.OpenInput
	labels map[string]string
}

func (o *captureSessionOpener) ID() string { return "capture-session" }

func (o *captureSessionOpener) Open(_ context.Context, in session.OpenInput) (session.OpenResult, error) {
	o.seen = in
	return session.OpenResult{SessionLabelUpserts: o.labels}, nil
}

func TestExecutor_prepareSubmitAndALeg_preservesTraceOnSubmitError(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	bus := hooks.New(hooks.Config{
		SubmitHooks: []sdkhooks.SubmitHook{failingSubmitHook{}},
	})
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) }
	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "client-1",
			ContinuityKey:   "ck-1",
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "u1"})
	traceID, _, _, _, outCtx, err := ex.prepareSubmitAndALeg(ctx, bus, call)
	if err == nil {
		t.Fatal("expected submit error")
	}
	if traceID != "" {
		t.Fatalf("trace id return on error: want empty got %q", traceID)
	}
	if call.ID == "" {
		t.Fatal("expected helper to assign call id")
	}
	if got := diag.TraceID(outCtx); got != call.ID {
		t.Fatalf("returned context trace id: want %q got %q", call.ID, got)
	}
	if got := diag.ALegID(outCtx); got != "" {
		t.Fatalf("returned context aleg id: want empty got %q", got)
	}
}

func TestExecutor_prepareSubmitAndALeg_sessionOpenHintsNotTrustedAsAuthority(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	opener := &captureSessionOpener{
		labels: map[string]string{"opened": "yes"},
	}
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			SessionOpeners: []session.Opener{opener},
		}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.RuntimeSnapshot = snap
	ex.Now = func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) }
	ex.SecureSession = mgr
	bus := hooks.New(hooks.Config{})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "client-2",
			ContinuityKey:   "ck-2",
			ALegID:          "client-forged-aleg",
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "u2"})
	traceID, _, aLeg, _, outCtx, err := ex.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if aLeg.ALegID == "" || aLeg.ALegID == "client-forged-aleg" {
		t.Fatalf("unexpected a-leg %q", aLeg.ALegID)
	}
	if call.Session.ALegID != aLeg.ALegID {
		t.Fatalf("call session aleg id: want %q got %q", aLeg.ALegID, call.Session.ALegID)
	}
	if opener.seen.Session.ClientSessionHint != "client-2" {
		t.Fatalf("opener saw client hint: want client-2 got %q", opener.seen.Session.ClientSessionHint)
	}
	if opener.seen.Session.ALegID != "" {
		t.Fatalf("opener must not see client-provided aleg id before BeginTurn, got %q", opener.seen.Session.ALegID)
	}

	views, ok := execctx.FromContext(outCtx)
	if !ok {
		t.Fatal("expected execctx views on returned context")
	}
	if views.Session.ALegID != aLeg.ALegID {
		t.Fatalf("views aleg id: want %q got %q", aLeg.ALegID, views.Session.ALegID)
	}
	if views.Session.ClientSessionHint != "client-2" {
		t.Fatalf("views client hint: want client-2 got %q", views.Session.ClientSessionHint)
	}
	if views.Session.AuthoritativeSessionID == "" || views.Session.AuthoritativeSessionID == views.Session.ClientSessionHint {
		t.Fatalf("client hint must remain separate from proxy session authority: %+v", views.Session)
	}
	if got := views.Session.PartitionKey(); got != views.Session.AuthoritativeSessionID {
		t.Fatalf("secure session partition key: got %q want proxy authority %q", got, views.Session.AuthoritativeSessionID)
	}
	if views.Session.Labels["opened"] != "yes" {
		t.Fatalf("views session labels: %v", views.Session.Labels)
	}
	if got := diag.TraceID(outCtx); got != traceID {
		t.Fatalf("returned context trace id: want %q got %q", traceID, got)
	}
}

func TestExecutor_projectContext_preservesPolicyLabels(t *testing.T) {
	t.Parallel()

	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "client-1",
			ALegID:                 "aleg-1",
			AuthoritativeSessionID: "auth-session-1",
		},
	}
	aLeg := b2bua.ALegRecord{
		ALegID: "aleg-1",
	}
	preSession := session.SessionView{
		AuthoritativeSessionID: "auth-session-1",
		ClientSessionHint:      "client-1",
		ALegID:                 "aleg-1",
		TurnID:                 "turn-1",
		WorkspaceID:            "ws-1",
		Labels: map[string]string{
			"hook_label": "value-1",
		},
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call,
		execview.PrincipalView{ID: "user-1"},
		scope.PrincipalScopeView{},
		true,
		aipworkspace.WorkspaceView{ID: "ws-1"},
		aLeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{
			SessionID: "auth-session-1",
			TurnID:    "turn-1",
		},
		true,
		preSession,
	)
	if err != nil {
		t.Fatal(err)
	}

	views := execctx.Views{
		Session: session.SessionView{
			Labels: map[string]string{
				"policy_version":      "v1.0",
				"effective_treatment": "strict",
			},
		},
	}
	ctx := execctx.WithViews(context.Background(), views)

	projectedCtx := ibt.projectContext(ctx)

	projectedViews, ok := execctx.FromContext(projectedCtx)
	if !ok {
		t.Fatal("expected views in projected context")
	}

	if got := projectedViews.Session.Labels["policy_version"]; got != "v1.0" {
		t.Errorf("policy_version did not survive: got %q, want %q", got, "v1.0")
	}
	if got := projectedViews.Session.Labels["effective_treatment"]; got != "strict" {
		t.Errorf("effective_treatment did not survive: got %q, want %q", got, "strict")
	}
	if got := projectedViews.Session.Labels["hook_label"]; got != "value-1" {
		t.Errorf("hook_label did not survive/merge: got %q, want %q", got, "value-1")
	}
}

func TestExecutor_identityBoundTurn_isFrozen(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) }

	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Route:    aipapi.RouteIntent{Selector: "original-selector"},
		Session: aipapi.SessionRef{
			ClientSessionID: "client-1",
			ContinuityKey:   "ck-1",
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "u1"})

	ibt, workingCall, _, err := ex.prepareIdentity(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}

	if workingCall.Route.Selector != "original-selector" {
		t.Fatalf("expected workingCall selector to be original-selector, got %q", workingCall.Route.Selector)
	}

	workingCall.Route.Selector = "mutated-selector"

	if ibt.call.Route.Selector != "original-selector" {
		t.Errorf("aliasing detected: ibt.call.Route.Selector mutated to %q", ibt.call.Route.Selector)
	}
}

func TestNewIdentityBoundTurn_Invariants(t *testing.T) {
	t.Parallel()

	validSecureTurn := execctx.SecureSessionTurn{SessionID: domain.SessionID("sess-1"), TurnID: domain.TurnID("turn-1")}
	validPreSession := session.SessionView{
		AuthoritativeSessionID: "sess-1",
		TurnID:                 "turn-1",
		ALegID:                 "aleg-1",
		WorkspaceID:            "ws-1",
	}
	validWorkspace := aipworkspace.WorkspaceView{ID: "ws-1"}
	validALeg := b2bua.ALegRecord{ALegID: "aleg-1"}
	validCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "sess-1",
			ALegID:                 "aleg-1",
		},
	}

	ibt, err := newIdentityBoundTurn(
		"trace-1",
		validCall,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		validWorkspace,
		validALeg,
		routeAuthoritySnapshot{},
		validSecureTurn,
		true,
		validPreSession,
	)
	if err != nil {
		t.Fatalf("expected valid secure ibt to succeed, got: %v", err)
	}
	if ibt == nil {
		t.Fatal("expected ibt to be non-nil")
	}

	detachedCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			ALegID: "aleg-1",
		},
	}
	detachedPreSession := session.SessionView{
		ALegID: "aleg-1",
	}
	_, err = newIdentityBoundTurn(
		"trace-1",
		detachedCall,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		validALeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		detachedPreSession,
	)
	if err != nil {
		t.Fatalf("expected valid detached ibt to succeed, got: %v", err)
	}

	badCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "sess-1",
			ALegID:                 "mismatch-aleg",
		},
	}
	_, err = newIdentityBoundTurn(
		"trace-1",
		badCall,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		validWorkspace,
		validALeg,
		routeAuthoritySnapshot{},
		validSecureTurn,
		true,
		validPreSession,
	)
	if err == nil {
		t.Error("expected newIdentityBoundTurn to fail on mismatched call A-leg ID")
	}

	badPreSession1 := validPreSession
	badPreSession1.AuthoritativeSessionID = "mismatch-sess"
	_, err = newIdentityBoundTurn(
		"trace-1",
		validCall,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		validWorkspace,
		validALeg,
		routeAuthoritySnapshot{},
		validSecureTurn,
		true,
		badPreSession1,
	)
	if err == nil {
		t.Error("expected newIdentityBoundTurn to fail on mismatched session ID")
	}

	badPreSession2 := validPreSession
	badPreSession2.TurnID = "mismatch-turn"
	_, err = newIdentityBoundTurn(
		"trace-1",
		validCall,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		validWorkspace,
		validALeg,
		routeAuthoritySnapshot{},
		validSecureTurn,
		true,
		badPreSession2,
	)
	if err == nil {
		t.Error("expected newIdentityBoundTurn to fail on mismatched turn ID")
	}

	badPreSession3 := validPreSession
	badPreSession3.WorkspaceID = "mismatch-ws"
	_, err = newIdentityBoundTurn(
		"trace-1",
		validCall,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		validWorkspace,
		validALeg,
		routeAuthoritySnapshot{},
		validSecureTurn,
		true,
		badPreSession3,
	)
	if err == nil {
		t.Error("expected newIdentityBoundTurn to fail on mismatched workspace ID")
	}

	detachedCallWithSess := &aipapi.Call{
		Session: aipapi.SessionRef{
			AuthoritativeSessionID: "sess-1",
			ALegID:                 "aleg-1",
		},
	}
	_, err = newIdentityBoundTurn(
		"trace-1",
		detachedCallWithSess,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		validALeg,
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		detachedPreSession,
	)
	if err == nil {
		t.Error("expected newIdentityBoundTurn to fail in detached mode when Call has AuthoritativeSessionID")
	}
}

func TestExecutor_prepareIdentity_sessionMetadataCharacterization(t *testing.T) {
	t.Parallel()

	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC) }

	call := &aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
		Session: aipapi.SessionRef{
			ClientSessionID: "client-metadata",
			ContinuityKey:   "ck-metadata",
			Metadata:        map[string]string{"user_role": "admin", "custom_key": "custom_val"},
		},
	}
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "u1"})

	ibt, workingCall, _, err := ex.prepareIdentity(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}

	if got := workingCall.Session.Metadata["user_role"]; got != "admin" {
		t.Errorf("workingCall: expected Session.Metadata[\"user_role\"] to be \"admin\", got %q", got)
	}
	if got := ibt.call.Session.Metadata["user_role"]; got != "admin" {
		t.Errorf("ibt.call: expected Session.Metadata[\"user_role\"] to be \"admin\", got %q", got)
	}

	detachedCtx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{})
	ibtDetached, workingCallDetached, _, err := ex.prepareIdentity(detachedCtx, bus, call)
	if err != nil {
		t.Fatal(err)
	}

	if workingCallDetached.Session.Metadata != nil {
		t.Errorf("detached workingCall: expected Session.Metadata to be nil, got %+v", workingCallDetached.Session.Metadata)
	}
	if ibtDetached.call.Session.Metadata != nil {
		t.Errorf("detached ibt.call: expected Session.Metadata to be nil, got %+v", ibtDetached.call.Session.Metadata)
	}
}
