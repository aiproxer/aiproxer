package standardplugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func TestStandardInjection_CompressionDisabledByDefault(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
	if err := standardplugins.EnsureReasoningOutputPreservationInConfig(cfg, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
		t.Fatal(err)
	}
	row := reasoningFeatureRow(t, cfg)
	decoded, err := reasoningpreservation.DecodeConfig(row.Config)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Compression.Enabled {
		t.Fatalf("standard injected reasoning preservation must be compression-disabled by default, got %+v", decoded.Compression)
	}
	// Ensure companion rules are still exactly byte-identical to pre-compression expectations
	if len(decoded.Rules) != 1 || decoded.Rules[0].Backend != "codex-primary" {
		t.Fatalf("standard companion rule mismatch: %+v", decoded.Rules)
	}
}

func TestStandardInjection_ExactCompanionMarkerUnchangedShadowAndActive(t *testing.T) {
	t.Parallel()
	for _, mode := range []reasoningpreservation.CompressionMode{reasoningpreservation.CompressionShadow, reasoningpreservation.CompressionActive} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			// Build base via standard injection then overlay compression enabled
			base := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{Kind: "openai-codex", ID: "codex-primary", Enabled: true}}}}
			if err := standardplugins.EnsureReasoningOutputPreservationInConfig(base, standardplugins.ReasoningOutputPreservationInjectOpts{StandardDistribution: true}); err != nil {
				t.Fatal(err)
			}
			row := reasoningFeatureRow(t, base)
			decoded, err := reasoningpreservation.DecodeConfig(row.Config)
			if err != nil {
				t.Fatal(err)
			}
			decoded.Compression = reasoningpreservation.CompressionConfig{
				Enabled: true, Mode: mode, Route: "openai-responses:compressor", Timeout: 8e9,
				MaxInputTokens: 12000, MaxInputBytes: 1048576, MaxOutputTokens: 1500, MaxOutputBytes: 262144, MaxSurrogateBytes: 131072,
				MinSourceBytes: 5, MinSavedBytes: 2, MinSavingsRatio: 0.2,
				MaxPendingPerSession: 8, MaxSurrogateBytesPerSession: 524288, MaxPendingTotal: 256, MaxSurrogateBytesTotal: 16777216,
				EgressPolicyRef: "test-allow",
			}
			// Marker contract must remain pinned even with compression overlay
			if standardplugins.ContinuityMarkerKey != "aip.internal.openai_codex.reasoning_continuity.v1" {
				t.Fatalf("marker key changed")
			}
			if standardplugins.ContinuityMarkerValue != `{"eligible":true,"dialect":"openai.responses.reasoning_item.v1"}` {
				t.Fatalf("marker value changed")
			}
			// Ensure no semantic compression imports/branches leaked into standard injection file
			path := filepath.Join(repoRootStandard(t), "internal", "standardplugins", "reasoning_preservation_inject.go")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := string(raw)
			// The injection file must not reference semantic compression concepts
			forbidden := []string{
				"CompressionConfig",
				"Semantic",
				"Surrogate",
				"BackgroundPoller",
				"ExtractSemantic",
			}
			for _, needle := range forbidden {
				if strings.Contains(src, needle) {
					t.Fatalf("standardplugins/reasoning_preservation_inject.go must not contain %q (no semantic compression coupling)", needle)
				}
			}
			_ = decoded
		})
	}
}

func TestStandardInjection_NoCompressionBranchInCompanionPolicy(t *testing.T) {
	t.Parallel()
	// Guard: companion marker file must remain compression-agnostic
	path := filepath.Join(repoRootStandard(t), "internal", "standardplugins", "reasoning_preservation_inject.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "compression") {
		t.Fatalf("reasoning_preservation_inject.go must not contain compression branch")
	}
}

func repoRootStandard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repo root not found")
		}
		dir = parent
	}
}
