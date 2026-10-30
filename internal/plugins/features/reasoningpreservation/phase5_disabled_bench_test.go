package reasoningpreservation_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
)

func TestPhase5_noProductionProbeGlobals(t *testing.T) {
	t.Parallel()
	banned := []string{
		"activeProbes",
		"InstallTestProbes",
		"TestProbes",
		"noteConstruction",
		"noteStore",
		"noteAnchor",
	}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body := string(raw)
		for _, needle := range banned {
			if strings.Contains(body, needle) {
				t.Errorf("%s: production must not contain %q", path, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func BenchmarkPhase5_disabledInventoryNoStoreOrTelemetry(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		inv := reasoningpreservation.BuildSafeInventory(reasoningpreservation.Config{}, nil)
		if inv.Enabled || len(inv.AggregateCounters) != 0 {
			b.Fatal("disabled inventory must stay empty")
		}
	}
}

func BenchmarkPhase5_enabledObservePathSmoke(b *testing.B) {
	cfg := decodeValidConfig(b, `
action: observe
use_builtin_catalog: false
rules:
  - id: test-be
    backend: be
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: log_skip
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 4096
  max_session_bytes: 32768
`)
	b.ReportAllocs()
	for b.Loop() {
		parts, _, err := reasoningpreservation.FeatureBundleWithParts(cfg)
		if err != nil {
			b.Fatal(err)
		}
		if parts.Store == nil || parts.Telemetry == nil {
			b.Fatal("enabled path must construct store and telemetry")
		}
	}
}
