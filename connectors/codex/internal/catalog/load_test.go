package catalog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
)

func TestLoad_DisabledUsesShippedFallback(t *testing.T) {
	t.Parallel()
	c, src, err := catalog.Load(context.Background(), catalog.LoadOptions{Enabled: false})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src != catalog.SourceShippedFallback {
		t.Fatalf("source = %q, want %q", src, catalog.SourceShippedFallback)
	}
	if len(c.RoutableSlugs()) == 0 {
		t.Fatal("shipped fallback has no routable slugs")
	}
}

func TestLoad_DiscoveryFailureFallsBackToShipped(t *testing.T) {
	t.Parallel()
	// A non-executable file resolves as the binary but fails at spawn,
	// forcing discovery failure deterministically on hosts with and without a
	// codex CLI installed.
	c, src, err := catalog.Load(context.Background(), catalog.LoadOptions{Enabled: true, CodexBinaryPath: brokenCodexBinary(t), Timeout: time.Second})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src != catalog.SourceShippedFallback {
		t.Fatalf("source = %q, want %q", src, catalog.SourceShippedFallback)
	}
	if len(c.RoutableSlugs()) == 0 {
		t.Fatal("fallback has no routable slugs")
	}
}

func TestLoad_CanceledContextReturnsErrorWithoutFallback(t *testing.T) {
	t.Parallel()
	// Caller cancellation must propagate; a canceled Load must not silently
	// serve the shipped fallback snapshot.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := catalog.Load(ctx, catalog.LoadOptions{Enabled: true, Timeout: time.Second})
	if err == nil {
		t.Fatal("Load(canceled) = nil error, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) error = %v, want errors.Is(context.Canceled)", err)
	}
}

func TestLoad_DiscoveryFailureFallsBackToOverride(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, sampleRawCatalog(), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	// A non-executable file resolves as the binary but fails at spawn,
	// forcing discovery failure deterministically.
	c, src, err := catalog.Load(context.Background(), catalog.LoadOptions{
		Enabled:         true,
		CodexBinaryPath: brokenCodexBinary(t),
		FallbackPath:    path,
		Timeout:         time.Second,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if src != catalog.SourceOverrideFallback {
		t.Fatalf("source = %q, want %q", src, catalog.SourceOverrideFallback)
	}
	want := []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5"}
	if !slices.Equal(c.RoutableSlugs(), want) {
		t.Fatalf("RoutableSlugs = %v, want %v", c.RoutableSlugs(), want)
	}
}

func TestLoad_CanceledContextWithOverrideReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(path, sampleRawCatalog(), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, src, err := catalog.Load(ctx, catalog.LoadOptions{
		Enabled:      true,
		FallbackPath: path,
		Timeout:      time.Second,
	})
	if err == nil {
		t.Fatalf("Load(canceled) = %+v, %q, nil error; want cancellation without fallback", c.RoutableSlugs(), src)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(canceled) error = %v, want errors.Is(context.Canceled)", err)
	}
}

// brokenCodexBinary returns a path that resolves as the codex binary (regular
// file) but fails at spawn (no execute permission), forcing discovery failure
// deterministically regardless of host PATH contents.
func brokenCodexBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
