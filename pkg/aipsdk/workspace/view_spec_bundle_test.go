package workspace_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestWorkspaceView_zeroIsEmpty(t *testing.T) {
	t.Parallel()
	var v workspace.WorkspaceView
	if v.ID != "" || v.ProjectRoot != "" || v.DirtyTree {
		t.Fatalf("zero value: %#v", v)
	}
	if v.Markers != nil || v.Labels != nil {
		t.Fatalf("expected nil slices/maps, got markers=%v labels=%v", v.Markers, v.Labels)
	}
}

func TestWorkspaceView_fieldsRoundTrip(t *testing.T) {
	t.Parallel()
	v := workspace.WorkspaceView{
		ID:          "ws-1",
		ProjectRoot: "/tmp/proj",
		DirtyTree:   true,
		Markers:     []string{"a"},
		Labels:      map[string]string{"k": "v"},
	}
	if v.ID != "ws-1" || !v.DirtyTree {
		t.Fatalf("fields: %#v", v)
	}
}
