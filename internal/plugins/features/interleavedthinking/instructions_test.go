package interleavedthinking

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInstructions_Default(t *testing.T) {
	t.Parallel()
	got, err := ResolveInstructions("", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != DefaultInstructions {
		t.Fatalf("expected DefaultInstructions, got %q", got)
	}
}

func TestResolveInstructions_Inline(t *testing.T) {
	t.Parallel()
	inline := "You are a custom thinker model."
	got, err := ResolveInstructions("", "", inline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != inline {
		t.Fatalf("expected %q, got %q", inline, got)
	}
}

func TestResolveInstructions_FromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "instructions.txt")
	content := "Custom thinker prompt from file"
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	got, err := ResolveInstructions(dir, "instructions.txt", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != content {
		t.Fatalf("expected %q, got %q", content, got)
	}
}

func TestResolveInstructions_EmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(filePath, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := ResolveInstructions(dir, "empty.txt", "")
	if err == nil {
		t.Fatalf("expected error for empty file, got nil")
	}
}

func TestResolveInstructions_PathTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	_, err := ResolveInstructions(subDir, "../outside.txt", "")
	if err == nil {
		t.Fatalf("expected error for escaping path traversal, got nil")
	}
}

func TestResolveInstructions_NulByte(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := ResolveInstructions(dir, "file\x00name.txt", "")
	if err == nil {
		t.Fatalf("expected error for NUL byte, got nil")
	}
}

func TestResolveInstructions_ExceedsMaxBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "huge.txt")
	largeContent := strings.Repeat("A", DefaultMaxInstructionsBytes+10)
	if err := os.WriteFile(filePath, []byte(largeContent), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := ResolveInstructions(dir, "huge.txt", "")
	if err == nil {
		t.Fatalf("expected error for exceeding max bytes, got nil")
	}
}
