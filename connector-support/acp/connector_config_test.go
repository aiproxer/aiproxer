package acp

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

func TestDefaultInventoryModels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prefix string
		ids    []string
	}{
		{name: "cursor", prefix: "cursor", ids: []string{"composer-2", "auto"}},
		{name: "gemini", prefix: "google", ids: []string{"gemini-2.5-flash", "gemini-2.5-pro"}},
		{name: "agy namespaced", prefix: "agy", ids: []string{"google/gemini-3.5-flash-high", "anthropic/claude-opus-4.6-thinking"}},
		{name: "codex", prefix: "openai", ids: []string{"auto", "gpt-5.3-codex"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			models := DefaultInventoryModels(tc.prefix, tc.ids)
			if len(models) != len(tc.ids) {
				t.Fatalf("len = %d, want %d", len(models), len(tc.ids))
			}
			for i, m := range models {
				wantCanonical := tc.prefix + "/" + tc.ids[i]
				if m.CanonicalID != wantCanonical {
					t.Fatalf("[%d] CanonicalID = %q, want %q", i, m.CanonicalID, wantCanonical)
				}
				if m.NativeID != tc.ids[i] {
					t.Fatalf("[%d] NativeID = %q, want %q", i, m.NativeID, tc.ids[i])
				}
				if m.DisplayName != tc.ids[i] {
					t.Fatalf("[%d] DisplayName = %q, want %q", i, m.DisplayName, tc.ids[i])
				}
				if !strings.HasPrefix(m.CanonicalID, tc.prefix+"/") {
					t.Fatalf("[%d] CanonicalID %q missing prefix %q", i, m.CanonicalID, tc.prefix+"/")
				}
			}
		})
	}
}

func TestDefaultInventoryModels_empty(t *testing.T) {
	t.Parallel()
	if got := DefaultInventoryModels("cursor", nil); got != nil {
		t.Fatalf("nil ids = %v, want nil", got)
	}
	if got := DefaultInventoryModels("cursor", []string{}); len(got) != 0 {
		t.Fatalf("empty ids = %v, want empty slice", got)
	}
}

// Ensure the helper produces values assignable to modelinventory.Model slices
// used by the connector packages (compile-time shape check).
var _ []modelinventory.Model = DefaultInventoryModels("x", []string{"y"})

func TestResolveVendorModel(t *testing.T) {
	t.Parallel()
	// Cases mirror the per-connector ResolveModel tables (cursor, gemini, agy)
	// that this helper replaces. Each row parameterizes prefix, configured, and
	// fallback so the shared helper carries the full default-resolution logic.
	cases := []struct {
		name       string
		prefix     string
		configured string
		fallback   string
		effective  string
		want       string
	}{
		// Cursor-shaped (prefix=cursor, fallback=composer-2).
		{
			name:   "cursor strips colon prefix",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "cursor:composer-2", want: "composer-2",
		},
		{
			name:   "cursor strips slash prefix",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "cursor/composer-2-fast", want: "composer-2-fast",
		},
		{
			name:   "cursor passes through bare model",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "composer-2", want: "composer-2",
		},
		{
			name:   "cursor empty effective uses configured default",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "", want: "claude-3.5-sonnet",
		},
		{
			name:   "cursor bare prefix uses configured default",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "cursor", want: "claude-3.5-sonnet",
		},
		{
			name:   "cursor prefix:auto uses configured default",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "cursor:auto", want: "claude-3.5-sonnet",
		},
		{
			name:   "cursor bare auto uses configured default",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "auto", want: "claude-3.5-sonnet",
		},
		{
			name:   "cursor trims whitespace around prefixed model",
			prefix: "cursor", configured: "claude-3.5-sonnet", fallback: "composer-2",
			effective: "  cursor:gpt-5.2  ", want: "gpt-5.2",
		},
		{
			name:   "cursor empty configured falls back to hardcoded default",
			prefix: "cursor", configured: "", fallback: "composer-2",
			effective: "", want: "composer-2",
		},

		// Gemini-shaped (prefix=google, fallback=gemini-2.5-flash).
		{
			name:   "gemini strips colon prefix",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "google:gemini-2.5-flash", want: "gemini-2.5-flash",
		},
		{
			name:   "gemini strips slash prefix",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "google/gemini-2.5-pro", want: "gemini-2.5-pro",
		},
		{
			name:   "gemini passes through bare model",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "gemini-3-flash-preview", want: "gemini-3-flash-preview",
		},
		{
			name:   "gemini empty effective uses configured default",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "", want: "gemini-2.5-pro",
		},
		{
			name:   "gemini bare prefix uses configured default",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "google", want: "gemini-2.5-pro",
		},
		{
			name:   "gemini prefix:auto uses configured default",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "google:auto", want: "gemini-2.5-pro",
		},
		{
			name:   "gemini bare auto uses configured default",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "auto", want: "gemini-2.5-pro",
		},
		{
			name:   "gemini trims whitespace around prefixed model",
			prefix: "google", configured: "gemini-2.5-pro", fallback: "gemini-2.5-flash",
			effective: "  google:gemini-3.1-pro-preview  ", want: "gemini-3.1-pro-preview",
		},
		{
			name:   "gemini empty configured falls back to hardcoded default",
			prefix: "google", configured: "", fallback: "gemini-2.5-flash",
			effective: "", want: "gemini-2.5-flash",
		},

		// AGY-shaped (prefix=agy, fallback=google/gemini-3.5-flash-high).
		// AGY model IDs carry internal vendor namespaces (e.g. "google/gemini-...");
		// only the route-level "agy:" / "agy/" prefix is stripped.
		{
			name:   "agy strips colon prefix preserving internal namespace",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "agy:google/gemini-3.5-flash-high", want: "google/gemini-3.5-flash-high",
		},
		{
			name:   "agy strips slash prefix preserving internal namespace",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "agy/anthropic/claude-opus-4.6-thinking", want: "anthropic/claude-opus-4.6-thinking",
		},
		{
			name:   "agy passes through bare namespaced model",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "google/gemini-3.1-pro", want: "google/gemini-3.1-pro",
		},
		{
			name:   "agy empty effective uses configured default",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "", want: "anthropic/claude-sonnet-4.6-thinking",
		},
		{
			name:   "agy bare prefix uses configured default",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "agy", want: "anthropic/claude-sonnet-4.6-thinking",
		},
		{
			name:   "agy prefix:auto uses configured default",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "agy:auto", want: "anthropic/claude-sonnet-4.6-thinking",
		},
		{
			name:   "agy bare auto uses configured default",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "auto", want: "anthropic/claude-sonnet-4.6-thinking",
		},
		{
			name:   "agy trims whitespace around prefixed model",
			prefix: "agy", configured: "anthropic/claude-sonnet-4.6-thinking", fallback: "google/gemini-3.5-flash-high",
			effective: "  agy:google/gemini-3.5-flash-low  ", want: "google/gemini-3.5-flash-low",
		},
		{
			name:   "agy empty configured falls back to hardcoded default",
			prefix: "agy", configured: "", fallback: "google/gemini-3.5-flash-high",
			effective: "", want: "google/gemini-3.5-flash-high",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveVendorModel(tc.prefix, tc.configured, tc.fallback, tc.effective)
			if got != tc.want {
				t.Fatalf("ResolveVendorModel(%q, %q, %q, %q) = %q, want %q",
					tc.prefix, tc.configured, tc.fallback, tc.effective, got, tc.want)
			}
		})
	}
}
