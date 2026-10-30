package runtimebundle_test

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func interleavedBuildTestRegistry(t *testing.T) *pluginreg.Registry {
	t.Helper()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterFeature(interleavedthinking.ID, func(n yaml.Node) (aipfeature.FeatureBundle, error) {
		_, err := interleavedthinking.DecodeConfig(n)
		if err != nil {
			return aipfeature.FeatureBundle{}, err
		}
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func interleavedBuildTestConfig(t *testing.T, interleaved config.InterleavedConfig, configDir string) *config.Config {
	t.Helper()
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:0"},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
			{Kind: "stub", ID: "stub", Enabled: true, Config: empty},
		}},
		Interleaved: interleaved,
		ConfigDir:   configDir,
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestBuild_interleavedDisabled_leavesExecutorInert(t *testing.T) {
	t.Parallel()
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{Enabled: false}, "")
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if built.Executor().InterleavedProcessor != nil {
		t.Fatal("disabled interleaved must not wire Processor")
	}
}

func TestBuild_interleavedEnabled_wiresExecutor(t *testing.T) {
	t.Parallel()
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{
		Enabled: true,
	}, "")
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if built.Executor().InterleavedProcessor == nil {
		t.Fatal("enabled interleaved must wire Processor")
	}
}

func TestBuild_interleavedEnabled_loadsInstructionsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "thinker.md")
	const want = "Custom thinker instructions for wiring test."
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	var featNode yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\ninstructions_file: thinker.md\n"), &featNode); err != nil {
		t.Fatal(err)
	}
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{}, dir)
	cfg.Plugins.Features = []config.PluginConfig{
		{ID: interleavedthinking.ID, Enabled: true, Config: featNode},
	}
	_, built := mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	proc := built.Executor().InterleavedProcessor
	if proc == nil {
		t.Fatal("expected non-nil Processor")
	}
	turn, err := proc.BeginTurn(context.Background(), runtime.InterleavedTurnInput{})
	if err != nil {
		t.Fatal(err)
	}
	shaped, err := turn.ShapeThinker(aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range shaped.Instructions {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, want) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("want instructions %q in shaped call, got %+v", want, shaped.Instructions)
	}
}

func TestBuild_interleavedEnabled_missingInstructionsFileFails(t *testing.T) {
	t.Parallel()
	var featNode yaml.Node
	if err := yaml.Unmarshal([]byte("enabled: true\ninstructions_file: missing.md\n"), &featNode); err != nil {
		t.Fatal(err)
	}
	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{}, t.TempDir())
	cfg.Plugins.Features = []config.PluginConfig{
		{ID: interleavedthinking.ID, Enabled: true, Config: featNode},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: interleavedBuildTestRegistry(t),
	})
	if err == nil {
		t.Fatal("expected build failure for missing instructions file")
	}
	if !strings.Contains(err.Error(), "instructions_file") {
		t.Fatalf("error %q must mention instructions_file", err.Error())
	}
}

func TestInterleavedProcessor_SingleConstructionPerGeneration(t *testing.T) {
	t.Parallel()

	cfg := interleavedBuildTestConfig(t, config.InterleavedConfig{
		Enabled: true,
	}, "")
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: interleavedBuildTestRegistry(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ps.Close() }()

	accessMode, _ := cfg.EffectiveAccessMode()
	featOut, err := ps.StandardFeatures.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations: config.RegistrationsFromConfig(cfg),
		AccessMode:    accessMode,
		InterleavedConfig: interleavedthinking.Config{
			Enabled:               true,
			StreamToClient:        "visible",
			RegularTurnsRemaining: 5,
			MaxMemoBytes:          4096,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if featOut.CorePorts.InterleavedProcessor == nil {
		t.Fatal("expected non-nil CorePorts.InterleavedProcessor from featurehost CompileGeneration")
	}

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		CandidateOpts: &runtimebundle.BuildOptions{
			PluginRegistry:    interleavedBuildTestRegistry(t),
			FeaturePlanes:     featOut.Planes,
			FeatureLifecycles: featOut.Lifecycles,
			CorePorts:         featOut.CorePorts,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cand.Close() }()

	// Executor must consume ONLY CorePorts.InterleavedProcessor (exact pointer equality, no duplicate construction)
	if cand.Executor().InterleavedProcessor != featOut.CorePorts.InterleavedProcessor {
		t.Fatalf("dual processor construction detected: executor processor %p != CorePorts processor %p",
			cand.Executor().InterleavedProcessor, featOut.CorePorts.InterleavedProcessor)
	}
}

func TestLegacyNormalization_E2E_Parity_OldAndNew(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	instructionsPath := filepath.Join(dir, "custom_thinker.md")
	const customInstructions = "Custom thinker instructions for e2e parity test."
	if err := os.WriteFile(instructionsPath, []byte(customInstructions), 0o644); err != nil {
		t.Fatal(err)
	}

	oldYAML := `server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
interleaved:
  enabled: true
  stream_to_client: visible
  regular_turns_remaining: 5
  max_memo_bytes: 8192
  instructions_file: custom_thinker.md
plugins:
  backends:
    - id: stub
      kind: stub
      enabled: true
`
	newYAML := `server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
plugins:
  backends:
    - id: stub
      kind: stub
      enabled: true
  features:
    - id: interleaved-thinking
      enabled: true
      config:
        enabled: true
        stream_to_client: visible
        regular_turns_remaining: 5
        max_memo_bytes: 8192
        instructions_file: custom_thinker.md
`

	oldPath := filepath.Join(dir, "old_config.yaml")
	if err := os.WriteFile(oldPath, []byte(oldYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "new_config.yaml")
	if err := os.WriteFile(newPath, []byte(newYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Strict-load both through the REAL load path
	oldEff, _, oldOwner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(ctx, oldPath, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatalf("LoadBootstrapEffectiveWithSource old: %v", err)
	}
	if err := oldOwner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	newEff, _, newOwner, _, err := runtimebundle.LoadBootstrapEffectiveWithSource(ctx, newPath, config.StreamRecoveryOverrides{}, nil)
	if err != nil {
		t.Fatalf("LoadBootstrapEffectiveWithSource new: %v", err)
	}
	if err := newOwner.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// 2. Assert legacy top-level interleaved node is consumed and not retained in core config
	if oldEff.Config.Interleaved.Enabled {
		t.Fatal("legacy top-level interleaved node must be consumed from core config")
	}

	// 3. Find interleaved-thinking feature in both
	var oldFeat, newFeat *config.PluginConfig
	for i := range oldEff.Config.Plugins.Features {
		if oldEff.Config.Plugins.Features[i].ID == interleavedthinking.ID {
			oldFeat = &oldEff.Config.Plugins.Features[i]
			break
		}
	}
	for i := range newEff.Config.Plugins.Features {
		if newEff.Config.Plugins.Features[i].ID == interleavedthinking.ID {
			newFeat = &newEff.Config.Plugins.Features[i]
			break
		}
	}
	if oldFeat == nil {
		t.Fatalf("old config normalized features missing %q", interleavedthinking.ID)
	}
	if newFeat == nil {
		t.Fatalf("new config features missing %q", interleavedthinking.ID)
	}

	oldCfg, err := interleavedthinking.DecodeConfig(oldFeat.Config)
	if err != nil {
		t.Fatalf("DecodeConfig old: %v", err)
	}
	newCfg, err := interleavedthinking.DecodeConfig(newFeat.Config)
	if err != nil {
		t.Fatalf("DecodeConfig new: %v", err)
	}

	if !reflect.DeepEqual(oldCfg, newCfg) {
		t.Fatalf("feature config parity mismatch:\n old: %+v\n new: %+v", oldCfg, newCfg)
	}
	if oldCfg.StreamToClient != "visible" || oldCfg.RegularTurnsRemaining != 5 || oldCfg.MaxMemoBytes != 8192 || oldCfg.InstructionsFile != "custom_thinker.md" {
		t.Fatalf("non-default fields mismatch: %+v", oldCfg)
	}

	// 4. Compile generation for both and assert processor policy equals canonical
	psOld, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  oldEff.Config,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: interleavedBuildTestRegistry(t)},
	})
	if err != nil {
		t.Fatalf("NewProcessServices old: %v", err)
	}
	defer func() { _ = psOld.Close() }()

	accessModeOld, _ := oldEff.Config.EffectiveAccessMode()
	featOutOld, err := psOld.StandardFeatures.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: config.RegistrationsFromConfig(oldEff.Config),
		AccessMode:    accessModeOld,
		ConfigDir:     dir,
	})
	if err != nil {
		t.Fatalf("CompileGeneration old: %v", err)
	}

	psNew, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg:  newEff.Config,
		Log:  slog.Default(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: interleavedBuildTestRegistry(t)},
	})
	if err != nil {
		t.Fatalf("NewProcessServices new: %v", err)
	}
	defer func() { _ = psNew.Close() }()

	accessModeNew, _ := newEff.Config.EffectiveAccessMode()
	featOutNew, err := psNew.StandardFeatures.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: config.RegistrationsFromConfig(newEff.Config),
		AccessMode:    accessModeNew,
		ConfigDir:     dir,
	})
	if err != nil {
		t.Fatalf("CompileGeneration new: %v", err)
	}

	procOld := featOutOld.CorePorts.InterleavedProcessor
	procNew := featOutNew.CorePorts.InterleavedProcessor
	if procOld == nil {
		t.Fatal("expected non-nil InterleavedProcessor from old config")
	}
	if procNew == nil {
		t.Fatal("expected non-nil InterleavedProcessor from new config")
	}

	turnIn := runtime.InterleavedTurnInput{ALegID: "leg-1", Selector: "stub"}
	turnOld, err := procOld.BeginTurn(ctx, turnIn)
	if err != nil {
		t.Fatalf("BeginTurn old: %v", err)
	}
	turnNew, err := procNew.BeginTurn(ctx, turnIn)
	if err != nil {
		t.Fatalf("BeginTurn new: %v", err)
	}

	if turnOld.Visible() != turnNew.Visible() || !turnOld.Visible() {
		t.Fatalf("turn visibility mismatch: old=%v new=%v want true", turnOld.Visible(), turnNew.Visible())
	}

	dummyCall := aipapi.Call{
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
		},
	}
	shapedOld, err := turnOld.ShapeThinker(dummyCall)
	if err != nil {
		t.Fatalf("ShapeThinker old: %v", err)
	}
	shapedNew, err := turnNew.ShapeThinker(dummyCall)
	if err != nil {
		t.Fatalf("ShapeThinker new: %v", err)
	}

	var oldFound, newFound bool
	for _, m := range shapedOld.Instructions {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, customInstructions) {
				oldFound = true
			}
		}
	}
	for _, m := range shapedNew.Instructions {
		for _, p := range m.Parts {
			if strings.Contains(p.Text, customInstructions) {
				newFound = true
			}
		}
	}
	if !oldFound || !newFound {
		t.Fatalf("instructions not found: oldFound=%v newFound=%v", oldFound, newFound)
	}

	// Exercise thinker observe, finalize, executor shape to assert turns remaining & memo capacity
	payload10k := strings.Repeat("x", 10000)
	_, _ = turnOld.ObserveThinkerEvent(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: payload10k})
	memoOld, err := turnOld.FinalizeThinkerStatus(ctx, false, true)
	if err != nil {
		t.Fatalf("FinalizeThinker old: %v", err)
	}
	_, err = turnOld.ShapeExecutor(ctx, dummyCall, memoOld)
	if err != nil {
		t.Fatalf("ShapeExecutor old: %v", err)
	}

	_, _ = turnNew.ObserveThinkerEvent(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: payload10k})
	memoNew, err := turnNew.FinalizeThinkerStatus(ctx, false, true)
	if err != nil {
		t.Fatalf("FinalizeThinker new: %v", err)
	}
	_, err = turnNew.ShapeExecutor(ctx, dummyCall, memoNew)
	if err != nil {
		t.Fatalf("ShapeExecutor new: %v", err)
	}

	_, oldTurns := turnOld.ShapeDiagnostics()
	_, newTurns := turnNew.ShapeDiagnostics()
	if oldTurns != newTurns || oldTurns != 4 {
		t.Fatalf("turns remaining mismatch: old=%d new=%d want 4 (initial 5 decremented by 1)", oldTurns, newTurns)
	}
	if len(memoOld.Text) != 8192 || len(memoNew.Text) != 8192 {
		t.Fatalf("memo length: got old=%d new=%d, want 8192", len(memoOld.Text), len(memoNew.Text))
	}
	if memoOld.Text != memoNew.Text || memoOld.Text != strings.Repeat("x", 8192) {
		t.Fatalf("memo text mismatch: old=%q new=%q", memoOld.Text, memoNew.Text)
	}

	// 5. Assert conflict rejection when both old and new are present
	conflictPath := filepath.Join(dir, "conflict.yaml")
	conflictContent := `server:
  address: "127.0.0.1:0"
continuity:
  in_memory: true
interleaved:
  enabled: true
plugins:
  backends:
    - id: stub
      kind: stub
      enabled: true
  features:
    - id: interleaved-thinking
      enabled: true
`
	if err := os.WriteFile(conflictPath, []byte(conflictContent), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = runtimebundle.LoadBootstrapEffectiveWithSource(ctx, conflictPath, config.StreamRecoveryOverrides{}, nil)
	if err == nil {
		t.Fatal("expected conflict error when both legacy interleaved and canonical feature exist")
	}
	if !strings.Contains(err.Error(), "both legacy top-level 'interleaved' and canonical 'plugins.features' entry") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
