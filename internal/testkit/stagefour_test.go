package testkit_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestExecCtxViewsFixture_roundTrip(t *testing.T) {
	t.Parallel()
	v := testkit.ExecCtxViewsFixture()
	ctx := execctx.WithViews(context.Background(), v)
	got, ok := execctx.FromContext(ctx)
	if !ok || got.Principal.ID != v.Principal.ID {
		t.Fatalf("views: %+v", got)
	}
}

func TestNewTestRequestRuntimeSnapshot_fakes(t *testing.T) {
	t.Parallel()
	bus := hooks.New(hooks.Config{})
	st := testkit.NewFakeStateStore()
	rec := &testkit.RecordingTrafficObserver{}
	snap := testkit.NewTestRequestRuntimeSnapshot(bus, testkit.TestSnapshotOptions{
		Generation:      99,
		State:           st,
		TrafficObserver: rec,
		Workspace: testkit.FakeWorkspaceResolver{View: workspace.WorkspaceView{
			ProjectRoot: "/tmp/ws",
		}},
	})
	if snap.Generation() != 99 {
		t.Fatalf("gen %d", snap.Generation())
	}
	ctx := context.Background()
	ctx = extensions.WithRequestRuntimeSnapshot(ctx, snap)
	if extensions.RequestRuntimeSnapshotFromContext(ctx) != snap {
		t.Fatal("snapshot pointer")
	}
	_ = snap.TrafficObserver().OnObservation(ctx, traffic.Observation{Leg: traffic.LegCTP, TraceID: "t1"})
	if len(rec.Seen) != 1 {
		t.Fatalf("observations %d", len(rec.Seen))
	}
	wv, err := snap.Workspace().Resolve(ctx)
	if err != nil || wv.ProjectRoot != "/tmp/ws" {
		t.Fatalf("workspace %+v %v", wv, err)
	}
	ctx = execctx.WithViews(ctx, testkit.ExecCtxViewsFixture())
	if err := st.Put(ctx, state.ScopeRequest, "ns", "k", "v", 0); err != nil {
		t.Fatal(err)
	}
	var out string
	found, err := st.Get(ctx, state.ScopeRequest, "ns", "k", &out)
	if err != nil || !found || out != "v" {
		t.Fatalf("get found=%v out=%q err=%v", found, out, err)
	}
}
