package runtime

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type aliasRecordingBLeg struct {
	calls []string
}

func (r *aliasRecordingBLeg) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, nil }

func (r *aliasRecordingBLeg) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	r.calls = append(r.calls, "cancel:"+string(cause.Kind))
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (r *aliasRecordingBLeg) Close() error {
	r.calls = append(r.calls, "close")
	return nil
}

func TestRuntime_SecureAuthoritativeDoesNotAliasHintLifecycle(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secureStore := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, secureStore, store)
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.Now = func() time.Time { return time.Unix(3000, 0) }
	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex.RuntimeSnapshot = snap
	setSecureSessionDenialMapper(ex)

	parentID := "parent-hint-a-leg"
	parentLC := ex.lifecycleCoordinator().StartALeg(parentID)
	parentBLeg := &aliasRecordingBLeg{}
	if err := parentLC.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{ID: "b-parent", Attempt: parentBLeg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ex.lifecycleCoordinator().CancelALeg(context.Background(), parentID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
		ex.lifecycleCoordinator().EndALeg(parentID)
	})

	call := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID: "hint",
			ALegID:          parentID,
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi-secure")}}},
	}

	prep, _, cleanup, err := ex.prepareRequest(context.Background(), call)
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	defer cleanup()
	defer ex.lifecycleCoordinator().EndALeg(prep.identity.aLeg.ALegID)

	if prep.identity == nil || prep.identity.aLeg.ALegID == "" {
		t.Fatal("prepareRequest must produce authoritative a-leg")
	}
	if prep.identity.aLeg.ALegID == parentID {
		t.Fatalf("secure authoritative lifecycle must remain isolated from client hint: got same ALegID %q", parentID)
	}
	if prep.aScope == nil {
		t.Fatal("prepareRequest must allocate aScope")
	}
	if prep.aScope == parentLC {
		t.Fatalf("secure authoritative aScope must not alias hint lifecycle: same *ALeg %p", parentLC)
	}

	childBLeg := &aliasRecordingBLeg{}
	if err := prep.aScope.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{ID: "b-child", Attempt: childBLeg}); err != nil {
		t.Fatal(err)
	}

	if err := prep.aScope.Cancel(context.Background(), leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got := childBLeg.calls; !reflect.DeepEqual(got, []string{"cancel:explicit", "close"}) {
		t.Fatalf("child calls = %v want [cancel:explicit close]", got)
	}
	if got := parentBLeg.calls; len(got) != 0 {
		t.Fatalf("parent must stay untouched after child cancel, got %v", got)
	}

	prep.aScope.End()
	if err := ex.lifecycleCoordinator().CancelALeg(context.Background(), parentID, leglifecycle.CancelCause{Kind: leglifecycle.CancelClientGone}); err != nil {
		t.Fatal(err)
	}
	if got := parentBLeg.calls; !reflect.DeepEqual(got, []string{"cancel:client_gone", "close"}) {
		t.Fatalf("parent calls after End(child) + cancel parent = %v want [cancel:client_gone close]", got)
	}
}

func TestRuntime_DetachedPrivateChildLifecycleIsolatedFromParentHint(t *testing.T) {
	t.Parallel()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secureStore := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, secureStore, store)
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.SecureSession = mgr
	ex.SyntheticLocalPrincipal = true
	ex.Now = func() time.Time { return time.Unix(3100, 0) }
	snap := extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})
	ex.RuntimeSnapshot = snap
	setSecureSessionDenialMapper(ex)

	parentID := "parent-hint-a-leg-detached"
	parentLC := ex.lifecycleCoordinator().StartALeg(parentID)
	parentBLeg := &aliasRecordingBLeg{}
	if err := parentLC.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{ID: "b-parent", Attempt: parentBLeg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ex.lifecycleCoordinator().CancelALeg(context.Background(), parentID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
		ex.lifecycleCoordinator().EndALeg(parentID)
	})

	detachedCtx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{
		ParentSessionID: "dummy-parent-session",
		ParentALegID:    parentID,
		ParentTraceID:   "parent-trace",
	})
	detachedCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			ALegID:                 parentID,
			AuthoritativeSessionID: "should-not-leak",
			ClientSessionID:        "should-not-leak-client",
		},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("child")}}},
	}

	prep, _, cleanup, err := ex.prepareRequest(detachedCtx, detachedCall)
	if err != nil {
		t.Fatalf("prepareRequest detached: %v", err)
	}
	defer cleanup()
	defer ex.lifecycleCoordinator().EndALeg(prep.identity.aLeg.ALegID)

	childALegID := prep.identity.aLeg.ALegID
	if childALegID == "" || childALegID == parentID {
		t.Fatalf("detached child must own private A-leg distinct from parent: got %q parent %q", childALegID, parentID)
	}
	if prep.aScope == nil {
		t.Fatal("detached prepareRequest must allocate private aScope")
	}
	if prep.aScope == parentLC {
		t.Fatalf("detached child lifecycle must not alias parent lineage hint: same *ALeg %p", parentLC)
	}

	childBLeg := &aliasRecordingBLeg{}
	if err := prep.aScope.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{ID: "b-child", Attempt: childBLeg}); err != nil {
		t.Fatal(err)
	}
	if err := prep.aScope.Cancel(context.Background(), leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got := childBLeg.calls; !reflect.DeepEqual(got, []string{"cancel:explicit", "close"}) {
		t.Fatalf("child calls = %v want [cancel:explicit close]", got)
	}
	if got := parentBLeg.calls; len(got) != 0 {
		t.Fatalf("parent must not be canceled when detached child is canceled, got %v", got)
	}
	prep.aScope.End()
	if err := ex.lifecycleCoordinator().CancelALeg(context.Background(), parentID, leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}); err != nil {
		t.Fatal(err)
	}
	if got := parentBLeg.calls; !reflect.DeepEqual(got, []string{"cancel:explicit", "close"}) {
		t.Fatalf("parent after child End: got %v", got)
	}
}
