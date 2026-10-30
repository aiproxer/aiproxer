package workspace_test

import (
	"context"
	"errors"
	"testing"

	corews "github.com/aiproxer/aiproxer/internal/core/workspace"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type stubRes struct {
	v aipworkspace.WorkspaceView
	e error
}

func (s stubRes) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return s.v, s.e
}

func TestStrictChain_propagatesFirstError(t *testing.T) {
	t.Parallel()
	chain := corews.NewStrictChain([]aipworkspace.Resolver{
		stubRes{e: errors.New("boom")},
		stubRes{v: aipworkspace.WorkspaceView{ID: "w"}},
	})
	_, err := chain.Resolve(context.Background())
	if err == nil || err.Error() != "boom" {
		t.Fatalf("got %v", err)
	}
}

func TestStrictChain_okMerges(t *testing.T) {
	t.Parallel()
	chain := corews.NewStrictChain([]aipworkspace.Resolver{
		stubRes{v: aipworkspace.WorkspaceView{ProjectRoot: "/a"}},
		stubRes{v: aipworkspace.WorkspaceView{ID: "id1"}},
	})
	got, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectRoot != "/a" || got.ID != "id1" {
		t.Fatalf("got %#v", got)
	}
}

func TestResolverChain_failOpenSkipsErrors(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain([]aipworkspace.Resolver{
		stubRes{e: errors.New("boom")},
		stubRes{v: aipworkspace.WorkspaceView{ProjectRoot: "/ok"}},
	})
	got, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectRoot != "/ok" {
		t.Fatalf("root %q", got.ProjectRoot)
	}
}

func TestResolverChain_emptyUsesDisabled(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain(nil)
	_, err := chain.Resolve(context.Background())
	if !errors.Is(err, aipworkspace.ErrResolverNotConfigured) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolverChain_mergePropagatesWorkspaceID(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain([]aipworkspace.Resolver{
		stubRes{v: aipworkspace.WorkspaceView{ID: "ws-a", ProjectRoot: "/r1"}},
		stubRes{v: aipworkspace.WorkspaceView{ProjectRoot: "/r2"}},
	})
	got, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "ws-a" {
		t.Fatalf("id %q", got.ID)
	}
	if got.ProjectRoot != "/r2" {
		t.Fatalf("root %q", got.ProjectRoot)
	}
}

func TestResolverChain_mergeMarkersAndLabels(t *testing.T) {
	t.Parallel()
	chain := corews.NewResolverChain([]aipworkspace.Resolver{
		stubRes{v: aipworkspace.WorkspaceView{Markers: []string{"a"}, Labels: map[string]string{"k": "1"}}},
		stubRes{v: aipworkspace.WorkspaceView{Markers: []string{"a", "b"}, Labels: map[string]string{"k": "2", "x": "y"}, DirtyTree: true}},
	})
	got, err := chain.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Markers) != 2 || got.Markers[0] != "a" || got.Markers[1] != "b" {
		t.Fatalf("markers %+v", got.Markers)
	}
	if got.Labels["k"] != "2" || got.Labels["x"] != "y" {
		t.Fatalf("labels %+v", got.Labels)
	}
	if !got.DirtyTree {
		t.Fatal("expected dirty")
	}
}
