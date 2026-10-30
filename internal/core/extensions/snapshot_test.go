package extensions_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolpolicy"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type stubSessionOpener string

func (s stubSessionOpener) ID() string { return string(s) }

func (stubSessionOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{}, nil
}

func TestNewRequestRuntimeSnapshot_sessionOpenersIsolatedFromCallerSliceMutation(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	openers := []session.Opener{stubSessionOpener("stable-id")}
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSessionOpeners, "test", openers)
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{FeaturePlanes: cset.Freeze()})
	openers[0] = stubSessionOpener("caller-mutated")
	got := snap.SessionOpeners()
	if len(got) != 1 || got[0].ID() != "stable-id" {
		t.Fatalf("want snapshot to keep original opener id; got %#v", got[0])
	}
}

func TestRequestRuntimeSnapshotFromContext_missing(t *testing.T) {
	t.Parallel()
	if extensions.RequestRuntimeSnapshotFromContext(context.Background()) != nil {
		t.Fatal("want nil without WithRequestRuntimeSnapshot")
	}
}

func TestWithRequestRuntimeSnapshot_roundTrip(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{Generation: 7})
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), snap)
	got := extensions.RequestRuntimeSnapshotFromContext(ctx)
	if got != snap {
		t.Fatal("pointer mismatch")
	}
	if got.HookBus() != bus {
		t.Fatal("bus mismatch")
	}
	if got.Generation() != 7 {
		t.Fatalf("gen %d", got.Generation())
	}
	// Facades are non-nil defaults
	var _ state.Store = got.State() //nolint:staticcheck // QF1011: explicit interface satisfaction check
}

func TestWithRequestRuntimeSnapshot_nilSnap(t *testing.T) {
	t.Parallel()
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), nil)
	if ctx == nil {
		t.Fatal("ctx")
	}
	if extensions.RequestRuntimeSnapshotFromContext(ctx) != nil {
		t.Fatal("want nil snapshot when snap nil")
	}
}

func TestRequestRuntimeSnapshot_SessionOpeners_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneSessionOpeners, "test", []session.Opener{stubSessionOpener("a")})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.SessionOpeners()
	if len(got) != 1 || got[0].ID() != "a" {
		t.Fatalf("first SessionOpeners: %+v", got)
	}
	got[0] = stubSessionOpener("mutated")
	again := snap.SessionOpeners()
	if len(again) != 1 || again[0].ID() != "a" {
		t.Fatalf("snapshot openers mutated via returned slice; got %q", again[0].ID())
	}
}

type stubCat struct{}

func (stubCat) ID() string                        { return "c1" }
func (stubCat) Order() int                        { return 0 }
func (stubCat) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubCat) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

type stubRtx struct{}

func (stubRtx) ID() string                        { return "r1" }
func (stubRtx) Order() int                        { return 0 }
func (stubRtx) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubRtx) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return nil
}

func TestRequestRuntimeSnapshot_ToolCatalogFilters_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCatalogFilters, "test", []toolcatalog.Filter{stubCat{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.ToolCatalogFilters()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.ToolCatalogFilters()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

func TestRequestRuntimeSnapshot_RequestTransforms_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneRequestTransforms, "test", []request.Transform{stubRtx{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.RequestTransforms()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.RequestTransforms()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

type stubGate struct{}

func (stubGate) ID() string                        { return "g" }
func (stubGate) Order() int                        { return 0 }
func (stubGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubGate) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.PassOriginalOutcome(), nil
}

type stubAttemptTransform struct{ id string }

func (s stubAttemptTransform) ID() string                      { return s.id }
func (stubAttemptTransform) Order() int                        { return 0 }
func (stubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (stubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type stubStreamObserverFactory struct{ id string }

func (s stubStreamObserverFactory) ID() string                      { return s.id }
func (stubStreamObserverFactory) Order() int                        { return 0 }
func (stubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return nil, nil
}

func TestRequestRuntimeSnapshot_AttemptTransformsAndStreamObservers_defensiveCopyAndSort(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneAttemptTransforms, "test", []request.AttemptTransform{
		stubAttemptTransform{id: "z"},
		stubAttemptTransform{id: "a"},
	})
	_ = aipfeature.Contribute(cset, aipfeature.PlaneStreamObserverFactories, "test", []response.StreamObserverFactory{
		stubStreamObserverFactory{id: "z"},
		stubStreamObserverFactory{id: "a"},
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	gotAT := snap.AttemptTransforms()
	if len(gotAT) != 2 || gotAT[0].ID() != "a" || gotAT[1].ID() != "z" {
		t.Fatalf("AttemptTransforms sorted IDs=%v want [a z]", idsOfAttempt(gotAT))
	}
	gotAT[0] = nil
	againAT := snap.AttemptTransforms()
	if len(againAT) != 2 || againAT[0] == nil || againAT[0].ID() != "a" {
		t.Fatal("AttemptTransforms must return a defensive copy")
	}
	gotSO := snap.StreamObserverFactories()
	if len(gotSO) != 2 || gotSO[0].ID() != "a" || gotSO[1].ID() != "z" {
		t.Fatalf("StreamObserverFactories sorted IDs=%v want [a z]", idsOfStreamObs(gotSO))
	}
	gotSO[0] = nil
	againSO := snap.StreamObserverFactories()
	if len(againSO) != 2 || againSO[0] == nil || againSO[0].ID() != "a" {
		t.Fatal("StreamObserverFactories must return a defensive copy")
	}
}

func idsOfAttempt(in []request.AttemptTransform) []string {
	out := make([]string, len(in))
	for i, t := range in {
		if t == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = t.ID()
	}
	return out
}

func idsOfStreamObs(in []response.StreamObserverFactory) []string {
	out := make([]string, len(in))
	for i, f := range in {
		if f == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = f.ID()
	}
	return out
}

func TestRequestRuntimeSnapshot_CompletionGates_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneCompletionGates, "test", []completion.Gate{stubGate{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.CompletionGates()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.CompletionGates()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

type stubTrafficRed struct{}

func (stubTrafficRed) ID() string { return "tr-red" }

func (stubTrafficRed) Redact(context.Context, sdktraffic.Leg, sdktraffic.CaptureMeta, []byte) ([]byte, error) {
	return nil, nil
}

type stubSnapPolicy struct{}

func (stubSnapPolicy) ID() string                        { return "snap-pol" }
func (stubSnapPolicy) Order() int                        { return 0 }
func (stubSnapPolicy) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (stubSnapPolicy) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

// snapOrdPol is a minimal [toolpolicy.Policy] for snapshot ordering tests.
type snapOrdPol struct {
	id  string
	ord int
}

func (p snapOrdPol) ID() string                      { return p.id }
func (p snapOrdPol) Order() int                      { return p.ord }
func (snapOrdPol) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (snapOrdPol) Handle(context.Context, aipapi.ToolEvent, toolpolicy.Meta, toolpolicy.Services) (toolpolicy.Decision, error) {
	return toolpolicy.DecisionAllow, nil
}

func TestRequestRuntimeSnapshot_ToolCallPoliciesExecution_sortedAtSnapshotBuild(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{
		snapOrdPol{id: "zzz", ord: 10},
		snapOrdPol{id: "aaa", ord: 0},
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	exec := snap.ToolCallPoliciesExecution()
	if len(exec) != 2 {
		t.Fatalf("len %d", len(exec))
	}
	if exec[0].ID() != "aaa" || exec[1].ID() != "zzz" {
		t.Fatalf("order got [%s %s]", exec[0].ID(), exec[1].ID())
	}
	e2 := snap.ToolCallPoliciesExecution()
	if len(e2) != 2 || &e2[0] != &exec[0] {
		t.Fatal("expected ToolCallPoliciesExecution to reuse same backing slice")
	}
}

func TestRequestRuntimeSnapshot_ToolCallPolicies_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallPolicies, "test", []toolpolicy.Policy{stubSnapPolicy{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.ToolCallPolicies()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.ToolCallPolicies()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}

type snapOrdFin struct {
	id  string
	ord int
}

func (f snapOrdFin) ID() string { return f.id }
func (f snapOrdFin) Order() int { return f.ord }
func (snapOrdFin) Finalize(context.Context, toolcall.CompletedCall, aipapi.ToolDef, []aipapi.ToolDef, toolcall.Meta) (toolcall.Result, error) {
	return toolcall.Result{Action: toolcall.ActionPass}, nil
}

func TestRequestRuntimeSnapshot_ToolCallFinalizersExecution_sortedAtSnapshotBuild(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallFinalizers, "test", []toolcall.Finalizer{
		snapOrdFin{id: "zzz", ord: 10},
		snapOrdFin{id: "aaa", ord: 0},
		nil,
	})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	exec := snap.ToolCallFinalizersExecution()
	if len(exec) != 2 {
		t.Fatalf("len %d", len(exec))
	}
	if exec[0].ID() != "aaa" || exec[1].ID() != "zzz" {
		t.Fatalf("order got [%s %s]", exec[0].ID(), exec[1].ID())
	}
	e2 := snap.ToolCallFinalizersExecution()
	if len(e2) != 2 || &e2[0] != &exec[0] {
		t.Fatal("expected ToolCallFinalizersExecution to reuse same backing slice")
	}
}

func TestRequestRuntimeSnapshot_ToolCallFinalizers_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneToolCallFinalizers, "test", []toolcall.Finalizer{snapOrdFin{id: "f1", ord: 0}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.ToolCallFinalizers()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.ToolCallFinalizers()
	if len(again) != 1 || again[0] == nil {
		t.Fatalf("after mutate again=%v", again)
	}
}

func TestRequestRuntimeSnapshot_UsageObserver_defaultsWhenUnsetAndCallable(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{})
	uo := snap.UsageObserver()
	if uo == nil {
		t.Fatal("want non-nil usage observer")
	}
	if err := uo.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRuntimeSnapshot_TrafficRedactors_returnsDefensiveCopy(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	cset := aipfeature.NewContributionSet()
	_ = aipfeature.Contribute(cset, aipfeature.PlaneTrafficRedactors, "test", []sdktraffic.Redactor{stubTrafficRed{}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		FeaturePlanes: cset.Freeze(),
	})
	got := snap.TrafficRedactors()
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	got[0] = nil
	again := snap.TrafficRedactors()
	if len(again) != 1 {
		t.Fatalf("after mutate len %d", len(again))
	}
}
