package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// sessionMetaSpy records SessionView seen by request-wide transforms (extension RequestMeta).
type sessionMetaSpy struct {
	seen []session.SessionView
}

func (s *sessionMetaSpy) ID() string                        { return "test-session-meta-spy" }
func (s *sessionMetaSpy) Order() int                        { return 0 }
func (s *sessionMetaSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (s *sessionMetaSpy) Handle(_ context.Context, _ *aipapi.Call, meta request.RequestMeta, _ request.Services) error {
	if s == nil {
		return nil
	}
	s.seen = append(s.seen, meta.Session)
	return nil
}

type prereqOrderSpy struct {
	order *[]string
}

func (s prereqOrderSpy) ID() string                        { return "test-prereq-order" }
func (s prereqOrderSpy) Order() int                        { return 0 }
func (s prereqOrderSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (s prereqOrderSpy) Handle(_ context.Context, call *aipapi.Call, _ prerequest.Meta, _ prerequest.Services) (prerequest.Decision, error) {
	*s.order = append(*s.order, "pre")
	call.Route.Selector = "set-by-pre"
	return prerequest.Allow(), nil
}

type routeHintOrderSpy struct {
	order *[]string
	route string
}

func (s *routeHintOrderSpy) ID() string                        { return "test-route-hint-order" }
func (s *routeHintOrderSpy) Order() int                        { return 0 }
func (s *routeHintOrderSpy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s *routeHintOrderSpy) Hint(_ context.Context, in routehint.Input) (routehint.Result, error) {
	*s.order = append(*s.order, "hint")
	if in.Call != nil {
		s.route = in.Call.Route.Selector
	}
	return routehint.Result{}, nil
}

func TestExecutor_prepareSubmitAndALeg_preRequestRunsBeforeRouteHint(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	var order []string
	hint := &routeHintOrderSpy{order: &order}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			PreRequestHandlers: []prerequest.Handler{prereqOrderSpy{order: &order}},
			RouteHintProviders: []routehint.Provider{hint},
		}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(4200, 0).UTC() }
	ex.SessionAuditPolicy = testSessionAuditPolicy()
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-pre-route"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "pre-route-hint"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, baseline, _, _, _, err := ex.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "pre" || order[1] != "hint" {
		t.Fatalf("order: %#v", order)
	}
	if hint.route != "set-by-pre" {
		t.Fatalf("route hint saw route %q", hint.route)
	}
	if baseline.Route.Selector != "set-by-pre" {
		t.Fatalf("baseline route selector: %q", baseline.Route.Selector)
	}
}

func TestExecutor_prepareSubmitAndALeg_requestMetaSession_propagatesIsNewForNewTurn(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	spy := &sessionMetaSpy{}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms: []request.Transform{spy},
		}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(4000, 0).UTC() }
	ex.SessionAuditPolicy = testSessionAuditPolicy()
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-meta-new"})
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "meta-new-hint"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(ctx, bus, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.seen) != 1 {
		t.Fatalf("transform runs: want 1 got %d", len(spy.seen))
	}
	got := spy.seen[0]
	if !got.IsNew {
		t.Fatalf("new turn: want SessionView.IsNew true got %+v", got)
	}
	if !got.ResumeEligible {
		t.Fatalf("new turn: want SessionView.ResumeEligible true got %+v", got)
	}
	if got.AuthoritativeSessionID == "" {
		t.Fatal("want AuthoritativeSessionID set for extension meta")
	}
	if got.TurnID == "" {
		t.Fatal("want TurnID set for extension meta")
	}
}

func TestExecutor_prepareSubmitAndALeg_requestMetaSession_resumeTurnIsNotNew(t *testing.T) {
	t.Parallel()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	spy := &sessionMetaSpy{}
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
		FeaturePlanes: freezeBundle(testFeatureBundle{
			RequestTransforms: []request.Transform{spy},
		}),
	})
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return time.Unix(4100, 0).UTC() }
	ex.SessionAuditPolicy = testSessionAuditPolicy()
	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "user-meta-resume"})
	call1 := &aipapi.Call{
		Session: aipapi.SessionRef{ClientSessionID: "meta-resume-hint"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("first")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(ctx, bus, call1)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.seen) != 1 || !spy.seen[0].IsNew {
		t.Fatalf("first turn meta: %+v", spy.seen)
	}
	resumeTok := call1.Session.ResumeToken
	if resumeTok == "" {
		t.Fatal("want resume token after first turn")
	}
	spy.seen = spy.seen[:0]
	call2 := &aipapi.Call{
		Session: aipapi.SessionRef{
			ClientSessionID:        "meta-resume-hint",
			AuthoritativeSessionID: call1.Session.AuthoritativeSessionID,
			ResumeToken:            resumeTok,
		},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("second")},
		}},
	}
	_, _, _, _, _, err = ex.prepareSubmitAndALeg(ctx, bus, call2)
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.seen) != 1 {
		t.Fatalf("second prepare transform runs: want 1 got %d (%+v)", len(spy.seen), spy.seen)
	}
	if spy.seen[0].IsNew {
		t.Fatalf("resume turn: want SessionView.IsNew false got %+v", spy.seen[0])
	}
}
