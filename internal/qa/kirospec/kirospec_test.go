package kirospec

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateLifecycleRejectsInvalidArchive(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSpec(t, root, "archive/finished", `{"phase":"completed","completed":true,"ready_for_implementation":true}`, map[string]string{"tasks.md": "- [ ] unfinished"})

	joined := strings.Join(ValidateLifecycle(root), "\n")
	for _, want := range []string{"ready_for_implementation must be false", "unchecked task"} {
		if !strings.Contains(joined, want) {
			t.Errorf("errors %q do not contain %q", joined, want)
		}
	}
}

func TestValidateLifecycleFlagsFullyCheckedActiveSpec(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSpec(t, root, "done-pending", `{"phase":"tasks-generated"}`, map[string]string{"tasks.md": "- [x] 1. First\n- [x] 2. Second\n"})
	writeSpec(t, root, "in-progress", `{"phase":"tasks-generated"}`, map[string]string{"tasks.md": "- [x] 1. First\n- [ ] 2. Second\n"})
	writeSpec(t, root, "no-boxes", `{"phase":"tasks-generated"}`, map[string]string{"tasks.md": "# Plan\n\nNo task list yet.\n"})

	joined := strings.Join(ValidateLifecycle(root), "\n")
	if !strings.Contains(joined, "active/done-pending: all 2 tasks are checked") {
		t.Errorf("errors %q do not flag the fully checked active spec", joined)
	}
	for _, name := range []string{"in-progress", "no-boxes"} {
		if strings.Contains(joined, "active/"+name) {
			t.Errorf("errors %q unexpectedly flag %s", joined, name)
		}
	}
}

func TestValidateStaleReferencesRejectsArchivedActivePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSpec(t, filepath.Join(root, ".kiro", "specs"), "archive/finished", `{"phase":"completed","completed":true}`, map[string]string{"tasks.md": "- [x] done"})
	path := filepath.Join(root, "internal", "check.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "package internal\n// " + strings.Repeat("x", 128*1024) + " .kiro/specs/finished/spec.json"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(ValidateStaleReferences(root), "\n")
	if !strings.Contains(joined, "archived spec finished through stale active path") {
		t.Fatalf("errors %q do not report the stale active-spec reference", joined)
	}
}

func TestValidateBudgets(t *testing.T) {
	t.Parallel()
	areas := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "## Requirement %d\n\n1. criterion\n\n", i)
		}
		return b.String()
	}
	tasks := func(n int) string {
		var b strings.Builder
		b.WriteString("- [ ] 1. Group\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "- [ ] 1.%d Leaf\n  - detail\n", i)
		}
		return b.String()
	}
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"fits", map[string]string{"requirements.md": areas(5) + "## Deferred\n", "design.md": strings.Repeat("x\n", 300), "tasks.md": tasks(12)}, ""},
		{"too many areas", map[string]string{"requirements.md": areas(6) + "## Deferred\n"}, "6 requirement areas exceeds budget 5"},
		{"too many criteria", map[string]string{"requirements.md": "## Requirement 1\n" + strings.Repeat("1. c\n", 26) + "## Deferred\n"}, "26 acceptance criteria exceeds budget 25"},
		{"design too long", map[string]string{"requirements.md": "## Deferred\n", "design.md": strings.Repeat("x\n", 301)}, "301 design.md lines exceeds budget 300"},
		{"too many leaf tasks", map[string]string{"requirements.md": "## Deferred\n", "tasks.md": tasks(13)}, "13 leaf tasks exceeds budget 12"},
		{"indented subtasks", map[string]string{"requirements.md": "## Deferred\n", "tasks.md": "- [ ] Parent\n" + strings.Repeat("  - [ ] child\n", 13)}, "13 leaf tasks exceeds budget 12"},
		{"missing deferred", map[string]string{"requirements.md": areas(1)}, "needs a Deferred section"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeSpec(t, root, "new-spec", `{"phase":"tasks-generated"}`, tc.files)
			joined := strings.Join(ValidateBudgets(root), "\n")
			if tc.want == "" {
				if strings.Contains(joined, "new-spec") {
					t.Fatalf("unexpected findings: %s", joined)
				}
				return
			}
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("findings %q do not contain %q", joined, tc.want)
			}
		})
	}
}

func TestGrandfatheredCeilingsMatchRepository(t *testing.T) {
	t.Parallel()
	specsRoot := filepath.Join("..", "..", "..", ".kiro", "specs")
	for name, ceiling := range grandfathered {
		got, _, err := Measure(filepath.Join(specsRoot, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Areas < ceiling.Areas || got.Criteria < ceiling.Criteria || got.Design < ceiling.Design || got.Tasks < ceiling.Tasks {
			t.Errorf("%s shrank to %+v; lower its grandfathered ceiling %+v so it cannot grow back", name, got, ceiling)
		}
	}
}

func writeSpec(t *testing.T, root, name, metadata string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
