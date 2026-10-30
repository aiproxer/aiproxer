package acp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspacePolicy_ResolveFromHint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wp := WorkspacePolicy{}
	got, err := wp.ResolveWorkspace(map[string]string{"project_dir": dir})
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q", got, dir)
	}
}

func TestWorkspacePolicy_HintPriority(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wp := WorkspacePolicy{}
	hints := map[string]string{
		"workspace_path": dir,
		"cwd":            "/should/be/ignored",
	}
	got, err := wp.ResolveWorkspace(hints)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q (first hint wins)", got, dir)
	}
}

func TestWorkspacePolicy_TrivialPathIgnored(t *testing.T) {
	t.Parallel()
	wp := WorkspacePolicy{DefaultDir: t.TempDir()}
	for _, trivial := range []string{".", "..", " . ", " .. "} {
		got, err := wp.ResolveWorkspace(map[string]string{"project_dir": trivial})
		if err != nil {
			t.Fatalf("trivial %q: %v", trivial, err)
		}
		if got == trivial {
			t.Errorf("trivial path %q should have been ignored", trivial)
		}
	}
}

func TestWorkspacePolicy_RelativePathTreatedAsUnset(t *testing.T) {
	t.Parallel()
	def := t.TempDir()
	wp := WorkspacePolicy{DefaultDir: def}
	got, err := wp.ResolveWorkspace(map[string]string{"project_dir": "some/relative/path"})
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got != def {
		t.Fatalf("expected fallback to default, got %q", got)
	}
}

func TestWorkspacePolicy_RequireExplicit(t *testing.T) {
	t.Parallel()
	wp := WorkspacePolicy{RequireExplicit: true}
	_, err := wp.ResolveWorkspace(map[string]string{})
	if err != ErrNoWorkspace {
		t.Fatalf("expected ErrNoWorkspace, got %v", err)
	}
}

func TestWorkspacePolicy_DefaultFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wp := WorkspacePolicy{DefaultDir: dir}
	got, err := wp.ResolveWorkspace(map[string]string{})
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	if got != dir {
		t.Fatalf("got %q, want default %q", got, dir)
	}
}

func TestWorkspacePolicy_NoDefaultNoHint(t *testing.T) {
	t.Parallel()
	wp := WorkspacePolicy{}
	_, err := wp.ResolveWorkspace(map[string]string{})
	if err != ErrNoWorkspace {
		t.Fatalf("expected ErrNoWorkspace, got %v", err)
	}
}

func TestWorkspacePolicy_UnusableHint(t *testing.T) {
	t.Parallel()
	badPath := filepath.Join(os.TempDir(), "nonexistent_acp_unusable_hint_xyz")
	wp := WorkspacePolicy{}
	_, err := wp.ResolveWorkspace(map[string]string{"project_dir": badPath})
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
	if !errors.Is(err, ErrUnusableWorkspace) {
		t.Fatalf("expected ErrUnusableWorkspace, got %v", err)
	}
}

func TestWorkspacePolicy_UnusableDefault(t *testing.T) {
	t.Parallel()
	badPath := filepath.Join(os.TempDir(), "nonexistent_acp_unusable_default_xyz")
	wp := WorkspacePolicy{DefaultDir: badPath}
	_, err := wp.ResolveWorkspace(map[string]string{})
	if err == nil {
		t.Fatal("expected error for unusable default")
	}
	if !errors.Is(err, ErrUnusableWorkspace) {
		t.Fatalf("expected ErrUnusableWorkspace, got %v", err)
	}
}

func TestWorkspacePolicy_RelativeDefaultResolved(t *testing.T) {
	t.Parallel()
	// Create a temp dir and use its base name as a relative path from cwd.
	dir := t.TempDir()
	rel, err := filepath.Rel(mustGetwd(t), dir)
	if err != nil {
		t.Skip("cannot make relative path")
	}
	wp := WorkspacePolicy{DefaultDir: rel}
	got, err := wp.ResolveWorkspace(map[string]string{})
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	abs, _ := filepath.Abs(dir)
	if got != abs {
		t.Errorf("got %q, want %q (resolved absolute)", got, abs)
	}
}

func TestIsUsableWorkspaceDirectory_CurrentDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if !isUsableWorkspaceDirectory(dir) {
		t.Fatal("temp dir should be usable")
	}
}

func TestIsUsableWorkspaceDirectory_Nonexistent(t *testing.T) {
	t.Parallel()
	badPath := filepath.Join(os.TempDir(), "nonexistent_acp_isusable_xyz")
	if isUsableWorkspaceDirectory(badPath) {
		t.Fatal("nonexistent path should not be usable")
	}
}

func TestIsUsableWorkspaceDirectory_FileNotDir(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp("", "test*.txt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	_ = f.Close()
	if isUsableWorkspaceDirectory(f.Name()) {
		t.Fatal("file should not be usable as workspace directory")
	}
}

func TestIsTrivialPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  bool
	}{
		{".", true},
		{"..", true},
		{" . ", true},
		{" .. ", true},
		{"/tmp", false},
		{"some/path", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isTrivialPath(c.input); got != c.want {
			t.Errorf("isTrivialPath(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestFirstUsableWorkspaceDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	maps := []map[string]string{
		{"project_dir": "/nonexistent"},
		{"workspace_path": dir},
	}
	got, err := firstUsableWorkspaceDir(maps, false)
	if err != nil {
		t.Fatalf("firstUsableWorkspaceDir: %v", err)
	}
	if got != dir {
		t.Fatalf("got %q, want %q", got, dir)
	}
}

func TestFirstUsableWorkspaceDir_NoneFound(t *testing.T) {
	t.Parallel()
	badPath := filepath.Join(os.TempDir(), "nonexistent_acp_firstusable_xyz")
	_, err := firstUsableWorkspaceDir([]map[string]string{{"project_dir": badPath}}, false)
	if err != ErrNoWorkspace {
		t.Fatalf("expected ErrNoWorkspace, got %v", err)
	}
}

func TestFirstWorkspaceHintStr(t *testing.T) {
	t.Parallel()
	maps := []map[string]string{
		{"project_dir": "."}, // trivial, skipped
		{"workspace_path": "/some/path"},
	}
	got := firstWorkspaceHintStr(maps)
	if got != "/some/path" {
		t.Fatalf("got %q, want /some/path", got)
	}
}

func TestFirstWorkspaceHintStr_Empty(t *testing.T) {
	t.Parallel()
	got := firstWorkspaceHintStr([]map[string]string{{"project_dir": "."}})
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
