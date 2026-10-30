package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type detachedRouteOverrideReader struct {
	called int
	state  routeoverride.State
}

func TestExecutor_detachedExecuteUsesChildBLegFailoverAndTerminalLineage(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(7)
	ex.MaxAttempts = 2
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	ex.Backends = map[string]execbackend.Backend{
		"fail": {
			Caps: caps,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("detached first backend failed"))
			},
		},
		"good": {
			Caps: caps,
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{
		ParentSessionID:     "parent-session",
		ParentALegID:        "parent-a-leg",
		ParentTraceID:       "parent-trace",
		ParentBranchBinding: "parent-branch-binding",
	})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "parent-session", ALegID: "parent-a-leg"},
		Route:   aipapi.RouteIntent{Selector: "fail:model^good:model"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("extract")},
		}},
	}
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	childALegID := call.Session.ALegID
	if childALegID == "" || childALegID == "parent-a-leg" {
		t.Fatalf("unexpected detached child A-leg %q", childALegID)
	}
	attempts, err := store.LoadAttempts(context.Background(), childALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Seq != 1 || attempts[1].Seq != 2 {
		t.Fatalf("child failover lineage: got %#v", attempts)
	}
	if attempts[0].Outcome == aipapi.AttemptSuccess || attempts[1].Outcome != aipapi.AttemptSuccess {
		t.Fatalf("child attempt outcomes: %#v", attempts)
	}
}

func (r *detachedRouteOverrideReader) Snapshot(context.Context, string) (routeoverride.State, error) {
	r.called++
	return r.state, nil
}

func TestExecutor_detachedPrepareUsesPrivateChildALegAndSkipsPrimarySessionAuthority(t *testing.T) {
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
	ex.Now = func() time.Time { return time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC) }
	override := &detachedRouteOverrideReader{state: routeoverride.State{
		ALegID: "parent-a-leg", Active: true, Selector: "parent:model", Revision: 1,
		UpdatedAt: time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC),
	}}
	ex.RouteOverrideReader = override

	parentCall := &aipapi.Call{Session: aipapi.SessionRef{
		ClientSessionID: "client", ContinuityKey: "parent-branch",
	}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("parent")}}}}
	parentCtx := context.Background()
	_, _, parentALeg, _, parentCtx, err := ex.prepareSubmitAndALeg(parentCtx, ex.Bus, parentCall)
	if err != nil {
		t.Fatal(err)
	}
	parentRecord, err := secureStore.LoadByALegID(parentCtx, parentALeg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if override.called != 1 {
		t.Fatalf("normal parent preparation must snapshot route override once, got %d", override.called)
	}

	childCall := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "child-client-session",
			AuthoritativeSessionID: string(parentRecord.SessionID),
			ALegID:                 parentALeg.ALegID,
			ContinuityKey:          "client-branch-hint",
			ResumeToken:            "parent-resume-token",
		},
		Route:    aipapi.RouteIntent{Selector: "extractor:model"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("extract")}}},
	}
	childCtx := execctx.WithDetachedSession(parentCtx, execctx.DetachedSession{
		ParentSessionID:     string(parentRecord.SessionID),
		ParentALegID:        parentALeg.ALegID,
		ParentTraceID:       "parent-trace",
		ParentBranchBinding: "captured-parent-branch",
	})
	childCtx = execctx.WithRouteCandidatePreferences(childCtx, []string{"parent-override-candidate"})
	_, baseline, childALeg, routeAuth, outCtx, err := ex.prepareSubmitAndALeg(childCtx, ex.Bus, childCall)
	if err != nil {
		t.Fatal(err)
	}
	if childALeg.ALegID == "" || childALeg.ALegID == parentALeg.ALegID {
		t.Fatalf("detached child A-leg: %+v; parent=%q", childALeg, parentALeg.ALegID)
	}
	if got := childCall.Session.ALegID; got != childALeg.ALegID {
		t.Fatalf("child call A-leg: got %q want %q", got, childALeg.ALegID)
	}
	if baseline.Route.Selector != "extractor:model" {
		t.Fatalf("detached child selector rewritten: %q", baseline.Route.Selector)
	}
	if routeAuth.active() {
		t.Fatal("detached child must not inherit parent route override")
	}
	if got := execctx.RouteCandidatePreferences(outCtx); len(got) != 0 {
		t.Fatalf("detached child inherited parent route preferences: %v", got)
	}
	if override.called != 1 {
		t.Fatalf("detached preparation must not read parent route override, calls=%d", override.called)
	}
	if _, ok := execctx.SecureSessionTurnFromContext(outCtx); ok {
		t.Fatal("detached preparation must not create a primary secure-session turn")
	}
	if got := childCall.Session.AuthoritativeSessionID; got != "" {
		t.Fatalf("parent session authority leaked into child call: got %q", got)
	}
	if got := childCall.Session.ClientSessionID; got != "" {
		t.Fatalf("client session hint leaked into child call: got %q", got)
	}
	if got := childCall.Session.ContinuityKey; got != "" {
		t.Fatalf("detached child must not become continuity authority: got key %q", got)
	}
	if got := childCall.Session.ResumeToken; got != "" {
		t.Fatalf("parent resume authority leaked into child call: got %q", got)
	}
	views, ok := execctx.FromContext(outCtx)
	if !ok || views.Session.AuthoritativeSessionID != "" || views.Session.ClientSessionHint != "" || views.Session.ALegID != childALeg.ALegID || views.Session.TurnID != "" {
		t.Fatalf("detached child session view leaked parent authority: ok=%v view=%+v", ok, views.Session)
	}
	if meta, ok := execctx.DetachedSessionFromContext(outCtx); !ok || meta.ParentBranchBinding != "captured-parent-branch" {
		t.Fatalf("captured parent branch binding lost: meta=%+v ok=%v", meta, ok)
	}
	childRecord, err := secureStore.LoadByID(outCtx, parentRecord.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !childRecord.LastActivityAt.Equal(parentRecord.LastActivityAt) {
		t.Fatalf("detached preparation touched primary activity: before=%v after=%v", parentRecord.LastActivityAt, childRecord.LastActivityAt)
	}
}

func TestExecutor_detachedPrepareAlwaysAllocatesPrivateChildALeg(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.SyntheticLocalPrincipal = true

	const parentALegID = "parent-a-leg"
	existingALeg, err := store.CreateALeg(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		inputALegID  string
		forbiddenIDs []string
	}{
		{name: "empty", forbiddenIDs: []string{parentALegID}},
		{name: "matching parent", inputALegID: parentALegID, forbiddenIDs: []string{parentALegID}},
		{name: "mismatched existing", inputALegID: existingALeg.ALegID, forbiddenIDs: []string{parentALegID, existingALeg.ALegID}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := execctx.WithDetachedSession(context.Background(), execctx.DetachedSession{
				ParentALegID: parentALegID,
			})
			call := &aipapi.Call{
				ID:      "detached-" + tc.name,
				Session: aipapi.SessionRef{ALegID: tc.inputALegID},
				Messages: []aipapi.Message{{
					Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("extract")},
				}},
			}

			_, _, childALeg, _, _, err := ex.prepareSubmitAndALeg(ctx, ex.Bus, call)
			if err != nil {
				t.Fatal(err)
			}
			if childALeg.ALegID == "" {
				t.Fatal("detached child A-leg is empty")
			}
			for _, forbiddenID := range tc.forbiddenIDs {
				if childALeg.ALegID == forbiddenID {
					t.Fatalf("detached child reused forbidden A-leg %q", forbiddenID)
				}
			}
			if got := call.Session.ALegID; got != childALeg.ALegID {
				t.Fatalf("detached call A-leg: got %q want %q", got, childALeg.ALegID)
			}
		})
	}
}
