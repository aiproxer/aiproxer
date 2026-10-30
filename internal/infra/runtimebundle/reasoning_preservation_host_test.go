package runtimebundle_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func TestBuildHost_absentReasoningPreservationWithoutCodexHasNoParticipants(t *testing.T) {
	t.Parallel()
	path := runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml"))

	t.Run("inspect", func(t *testing.T) {
		t.Parallel()
		snap, err := runtimebundle.InspectInventory(t.Context(), runtimebundle.InspectInput{
			ConfigPath: path,
			Mandatory:  aipsdk.StandardDistributionRequirements(),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertInventoryFeatureRowAbsent(t, snap, standardplugins.ReasoningOutputPreservationFeatureID)
	})

	t.Run("serve", func(t *testing.T) {
		t.Parallel()
		host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
			ConfigPath:      path,
			Mandatory:       aipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		hostServeCleanup(t, host)
		assertFeatureRowAbsent(t, host.Config(), standardplugins.ReasoningOutputPreservationFeatureID)
		assertReasoningRegistrationAbsent(t, host)
		assertHasReasoningParticipants(t, host, false)
		if runtimebundle.HostProcess(host) == nil || runtimebundle.HostManager(host).Active() == nil {
			t.Fatal("BuildHost must publish generation host handles")
		}
		lease, ok := runtimebundle.HostManager(host).Acquire()
		if !ok || lease.Handler() == nil {
			t.Fatal("BuildHost must publish an acquireable handler")
		}
		lease.Release()
	})
}

func TestBuildHost_explicitReasoningPreservationFalseNoParticipants(t *testing.T) {
	t.Parallel()
	base, err := os.ReadFile(runtimebundle.MaterializeExampleConfigForTest(t, filepath.Join("..", "..", "..", "config", "examples", "dogfood-local-stub.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	marker := "    - id: tool-call-repair\n      enabled: true\n      config: {}\n"
	insert := marker + "    - id: reasoning-output-preservation\n      enabled: false\n"
	text := strings.Replace(string(base), marker, insert, 1)
	if text == string(base) {
		t.Fatal("expected dogfood feature insertion to succeed")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("inspect", func(t *testing.T) {
		t.Parallel()
		snap, err := runtimebundle.InspectInventory(t.Context(), runtimebundle.InspectInput{
			ConfigPath: path,
			Mandatory:  aipsdk.StandardDistributionRequirements(),
		})
		if err != nil {
			t.Fatal(err)
		}
		assertInventoryFeatureRowEnabled(t, snap, standardplugins.ReasoningOutputPreservationFeatureID, false)
	})

	t.Run("serve", func(t *testing.T) {
		t.Parallel()
		host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
			ConfigPath:      path,
			Mandatory:       aipsdk.StandardDistributionRequirements(),
			LogWriter:       io.Discard,
			HandlerComposer: stdhttp.ComposeStandardHTTP,
		})
		if err != nil {
			t.Fatal(err)
		}
		hostServeCleanup(t, host)
		assertFeatureRowEnabled(t, host.Config(), standardplugins.ReasoningOutputPreservationFeatureID, false)
		assertReasoningRegistrationEnabled(t, host, false)
		assertHasReasoningParticipants(t, host, false)
	})
}

func assertFeatureRowEnabled(t *testing.T, cfg *config.Config, id string, wantEnabled bool) {
	t.Helper()
	if cfg == nil {
		t.Fatal("nil config")
	}
	var found bool
	for _, p := range cfg.Plugins.Features {
		if p.FactoryID() == id || p.InstanceID() == id {
			found = true
			if p.Enabled != wantEnabled {
				t.Fatalf("feature %q Enabled=%v want %v", id, p.Enabled, wantEnabled)
			}
		}
	}
	if !found {
		t.Fatalf("feature %q not present in config features", id)
	}
}

func assertFeatureRowAbsent(t *testing.T, cfg *config.Config, id string) {
	t.Helper()
	for _, row := range cfg.Plugins.Features {
		if row.FactoryID() == id || row.InstanceID() == id {
			t.Fatalf("feature %q unexpectedly present: %+v", id, row)
		}
	}
}

// assertInventoryFeatureRowEnabled is the [runtimebundle.InspectInventory]
// equivalent of assertFeatureRowEnabled: [diag.InventorySnapshot].Features is
// built directly from the same accepted config, so this proves the shared
// strict loader injects/honors the feature row without exposing raw config.
func assertInventoryFeatureRowEnabled(t *testing.T, snap diag.InventorySnapshot, id string, wantEnabled bool) {
	t.Helper()
	var found bool
	for _, row := range snap.Features {
		if row.FactoryKind == id || row.ID == id {
			found = true
			if row.Enabled != wantEnabled {
				t.Fatalf("feature %q Enabled=%v want %v", id, row.Enabled, wantEnabled)
			}
		}
	}
	if !found {
		t.Fatalf("feature %q not present in inventory features", id)
	}
}

func assertInventoryFeatureRowAbsent(t *testing.T, snap diag.InventorySnapshot, id string) {
	t.Helper()
	for _, row := range snap.Features {
		if row.FactoryKind == id || row.ID == id {
			t.Fatalf("feature %q unexpectedly present in inventory", id)
		}
	}
}

func assertReasoningRegistrationEnabled(t *testing.T, host *runtimebundle.Host, wantEnabled bool) {
	t.Helper()
	id := standardplugins.ReasoningOutputPreservationFeatureID
	regs := config.RegistrationsFromConfig(host.Config())
	var found bool
	for _, r := range regs {
		if r.Kind != aipsdk.PluginKindFeature {
			continue
		}
		if r.FactoryKind == id || r.ID == id {
			found = true
			if r.Enabled != wantEnabled {
				t.Fatalf("registration %q Enabled=%v want %v", id, r.Enabled, wantEnabled)
			}
		}
	}
	if !found {
		t.Fatalf("feature %q missing from recomputed Registrations (inject must precede RegistrationsFromConfig)", id)
	}
}

func assertReasoningRegistrationAbsent(t *testing.T, host *runtimebundle.Host) {
	t.Helper()
	for _, r := range config.RegistrationsFromConfig(host.Config()) {
		if r.Kind == aipsdk.PluginKindFeature && (r.FactoryKind == standardplugins.ReasoningOutputPreservationFeatureID || r.ID == standardplugins.ReasoningOutputPreservationFeatureID) {
			t.Fatalf("reasoning feature unexpectedly registered: %+v", r)
		}
	}
}

func assertHasReasoningParticipants(t *testing.T, host *runtimebundle.Host, want bool) {
	t.Helper()
	// Host carries no FeatureSurface (BuildHost merges no duplicate surface;
	// CompileGeneration owns the sole merge for the published generation).
	// Recompute the same merge locally from the public Registry/Registrations
	// to characterize the participant pipeline.
	regs := config.RegistrationsFromConfig(host.Config())
	genMerged, err := featurebundle.MergeFeatureSurfaceGenerated(runtimebundle.HostProcess(host).FactoryCatalog, regs)
	if err != nil {
		t.Fatal(err)
	}
	wantTransform := reasoningpreservation.ID + "-transform"
	wantObserver := reasoningpreservation.ID + "-observer"
	var hasTransform, hasObserver bool
	for _, x := range aipfeature.Get(genMerged.Frozen, aipfeature.PlaneAttemptTransforms) {
		if x != nil && x.ID() == wantTransform {
			hasTransform = true
		}
	}
	for _, o := range aipfeature.Get(genMerged.Frozen, aipfeature.PlaneStreamObserverFactories) {
		if o != nil && o.ID() == wantObserver {
			hasObserver = true
		}
	}
	if want {
		if !hasTransform || !hasObserver {
			t.Fatalf("want reasoning participants transform=%v observer=%v", hasTransform, hasObserver)
		}
		return
	}
	if hasTransform || hasObserver {
		t.Fatalf("explicit opt-out must yield no reasoning participants transform=%v observer=%v", hasTransform, hasObserver)
	}
}
