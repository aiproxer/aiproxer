package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectedPath_RefusesReceiverAndEscapingDirectories(t *testing.T) {
	t.Parallel()
	container := t.TempDir()
	work := filepath.Join(container, "worktrees", "task")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := selectedPath(container, work); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{container, filepath.Join(container, "branches", "main"), filepath.Join(container, "worktrees"), "relative/task"} {
		if _, err := selectedPath(container, path); err == nil {
			t.Fatalf("accepted unsafe target %q", path)
		}
	}
}

func TestSnapshot_RefusesDirtyIgnoredActiveAndUndeliveredWork(t *testing.T) {
	t.Parallel()
	for _, state := range []snapshot{
		{Dirty: " M tracked.go"},
		{Ignored: []string{"user-cache.txt"}},
		{Users: []string{"pid 42"}},
		{Head: "tip", PublishedHead: "other", State: "MERGED"},
		{Head: "tip", PublishedHead: "tip", State: "OPEN"},
	} {
		if err := safeSnapshot(state, false, false); err == nil {
			t.Fatalf("accepted unsafe state %+v", state)
		}
	}
	state := snapshot{Head: "tip", PublishedHead: "tip", State: "MERGED"}
	if err := safeSnapshot(state, false, false); err != nil {
		t.Fatal(err)
	}
	state.Ignored = []string{".codegraph/codegraph.db"}
	if err := safeSnapshot(state, false, false); err == nil {
		t.Fatal("implicitly discarded index")
	}
	if err := safeSnapshot(state, true, false); err != nil {
		t.Fatal(err)
	}
	state.Ignored = append(state.Ignored, "valuable.log")
	if err := safeSnapshot(state, true, false); err == nil {
		t.Fatal("index-only authorization discarded unrelated ignored work")
	}
}
