package workspace_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// The projection mirrors session.WithSessionView/SessionViewFromContext exactly: an
// attach that snapshots its input, a read that snapshots its output, and a nil parent
// that is treated as [context.TODO].

func TestWorkspaceViewContextRoundTrip(t *testing.T) {
	t.Parallel()

	want := workspace.WorkspaceView{
		ID: "ws-1", ProjectRoot: "/repo", DirtyTree: true,
		Markers: []string{"go.mod"},
		Labels:  map[string]string{"kind": "git"},
	}
	ctx := workspace.WithWorkspaceView(context.Background(), want)
	got, ok := workspace.WorkspaceViewFromContext(ctx)
	if !ok {
		t.Fatal("workspace view missing")
	}
	if got.ID != "ws-1" || got.ProjectRoot != "/repo" || !got.DirtyTree {
		t.Fatalf("workspace view = %+v, want %+v", got, want)
	}
	if len(got.Markers) != 1 || got.Markers[0] != "go.mod" {
		t.Fatalf("markers = %v", got.Markers)
	}
	if got.Labels["kind"] != "git" {
		t.Fatalf("labels = %v", got.Labels)
	}
}

// TestWorkspaceViewContextDefensivelyCopies is the requirement 5.5 detachment rule for
// the third public projection: neither the caller's map and slice nor a returned view's
// may reach the stored snapshot, because one pinned snapshot is read by every
// participant of one turn.
func TestWorkspaceViewContextDefensivelyCopies(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"kind": "git"}
	markers := []string{"go.mod"}
	ctx := workspace.WithWorkspaceView(context.Background(), workspace.WorkspaceView{
		ProjectRoot: "/repo", Labels: labels, Markers: markers,
	})
	labels["kind"] = "mutated"
	markers[0] = "mutated"

	got, ok := workspace.WorkspaceViewFromContext(ctx)
	if !ok {
		t.Fatal("workspace view missing")
	}
	if got.Labels["kind"] != "git" || got.Markers[0] != "go.mod" {
		t.Fatalf("stored workspace view changed through the caller's own maps and slices: %+v", got)
	}
	got.Labels["kind"] = "mutated-again"
	got.Markers[0] = "mutated-again"

	gotAgain, ok := workspace.WorkspaceViewFromContext(ctx)
	if !ok || gotAgain.Labels["kind"] != "git" || gotAgain.Markers[0] != "go.mod" {
		t.Fatalf("stored workspace view aliases a returned snapshot: %+v ok=%v", gotAgain, ok)
	}
}

// TestWorkspaceViewContextAbsentAndNilParent pins the two answers a consumer must be
// able to tell apart: ABSENT means no authority was projected at all, and is reported
// as ok=false, while an ATTACHED EMPTY view is a genuine view with no project root.
func TestWorkspaceViewContextAbsentAndNilParent(t *testing.T) {
	t.Parallel()

	if _, ok := workspace.WorkspaceViewFromContext(context.Background()); ok {
		t.Fatal("an unprojected context must report an absent workspace view")
	}
	if _, ok := workspace.WorkspaceViewFromContext(nil); ok { //nolint:staticcheck // SA1012: intentional nil context contract
		t.Fatal("a nil context must report an absent workspace view")
	}

	ctx := workspace.WithWorkspaceView(context.Background(), workspace.WorkspaceView{})
	got, ok := workspace.WorkspaceViewFromContext(ctx)
	if !ok {
		t.Fatal("an attached empty view must be reported as present, not absent")
	}
	if got.ID != "" || got.ProjectRoot != "" || got.DirtyTree ||
		len(got.Markers) != 0 || len(got.Labels) != 0 {
		t.Fatalf("attached empty view = %+v", got)
	}
}

func TestWorkspaceViewContextNilParentUsesTODO(t *testing.T) {
	t.Parallel()

	ctx := workspace.WithWorkspaceView(nil, workspace.WorkspaceView{ProjectRoot: "/repo"}) //nolint:staticcheck // SA1012: intentional nil parent
	if ctx == nil {
		t.Fatal("want non-nil context (nil parent uses context.TODO)")
	}
	if got, ok := workspace.WorkspaceViewFromContext(ctx); !ok || got.ProjectRoot != "/repo" {
		t.Fatalf("workspace view = %+v ok=%v", got, ok)
	}
}

// TestWorkspaceViewContextChildInheritsTheProjectedSnapshot proves the projection is an
// ordinary context value, so the runtime can hand the request-part stage a descendant
// context without re-attaching anything.
func TestWorkspaceViewContextChildInheritsTheProjectedSnapshot(t *testing.T) {
	t.Parallel()

	type unrelatedKey struct{}
	ctx := workspace.WithWorkspaceView(context.Background(), workspace.WorkspaceView{ProjectRoot: "/repo"})
	child := context.WithValue(ctx, unrelatedKey{}, "value")
	got, ok := workspace.WorkspaceViewFromContext(child)
	if !ok || got.ProjectRoot != "/repo" {
		t.Fatalf("workspace view = %+v ok=%v", got, ok)
	}
}
