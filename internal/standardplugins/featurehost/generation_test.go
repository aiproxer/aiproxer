package featurehost_test

import (
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/state"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"gopkg.in/yaml.v3"
)

func mustYAMLNode(t *testing.T, s string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if len(n.Content) > 0 {
		return *n.Content[0]
	}
	return n
}

type dummyLifecycle struct {
	id string
}

func (d *dummyLifecycle) Start(context.Context) error { return nil }
func (d *dummyLifecycle) Stop(context.Context) error  { return nil }

func lifecycleID(lc aipplugin.Lifecycle) string {
	if d, ok := lc.(*dummyLifecycle); ok && d != nil {
		return d.id
	}
	return ""
}

func TestCompileGeneration_OverlappingGenerations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	gen1In := featurehost.GenerationInput{
		Lifecycles: []aipplugin.Lifecycle{&dummyLifecycle{id: "gen1-lc"}},
	}
	gen1Out, err := rt.CompileGeneration(ctx, gen1In)
	if err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}
	// Input lifecycles lead; featurehost appends generation-owned handles
	// (e.g. keep-warm) after them without mutating the input slice.
	if len(gen1Out.Lifecycles) == 0 || lifecycleID(gen1Out.Lifecycles[0]) != "gen1-lc" {
		t.Fatalf("expected gen1 lifecycle first, got %v", gen1Out.Lifecycles)
	}

	// Overlapping generation 2 while generation 1 is active
	gen2In := featurehost.GenerationInput{
		Lifecycles: []aipplugin.Lifecycle{&dummyLifecycle{id: "gen2-lc"}},
	}
	gen2Out, err := rt.CompileGeneration(ctx, gen2In)
	if err != nil {
		t.Fatalf("CompileGeneration #2: %v", err)
	}
	if len(gen2Out.Lifecycles) == 0 || lifecycleID(gen2Out.Lifecycles[0]) != "gen2-lc" {
		t.Fatalf("expected gen2 lifecycle first, got %v", gen2Out.Lifecycles)
	}

	// Generation 1 output remains unaffected
	if len(gen1Out.Lifecycles) == 0 || lifecycleID(gen1Out.Lifecycles[0]) != "gen1-lc" {
		t.Fatalf("gen1 was mutated by gen2 compile: %v", gen1Out.Lifecycles)
	}
}

func TestCompileGeneration_CandidateFailure_LastGoodIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	// Compile generation 1 (last-good)
	gen1In := featurehost.GenerationInput{
		Lifecycles: []aipplugin.Lifecycle{&dummyLifecycle{id: "last-good"}},
	}
	gen1Out, err := rt.CompileGeneration(ctx, gen1In)
	if err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}

	// Compile candidate with real invalid feature entry
	invalidRegs := []aipsdk.Registration{{
		Kind:    aipsdk.PluginKindFeature,
		ID:      "secrets-guard",
		Enabled: true,
		Config:  aipsdk.ConfigPayload{Node: mustYAMLNode(t, "action: totally-invalid-action\n")},
	}}
	failIn := featurehost.GenerationInput{
		Registrations: invalidRegs,
	}
	_, err = rt.CompileGeneration(ctx, failIn)
	if err == nil {
		t.Fatal("expected candidate compile error for invalid feature registration, got nil")
	}

	// Assert last-good generation is intact
	if len(gen1Out.Lifecycles) == 0 || lifecycleID(gen1Out.Lifecycles[0]) != "last-good" {
		t.Fatalf("last-good generation corrupted by failed compile: %v", gen1Out.Lifecycles)
	}

	// Successfully compile candidate 3
	gen3In := featurehost.GenerationInput{
		Lifecycles: []aipplugin.Lifecycle{&dummyLifecycle{id: "gen3-lc"}},
	}
	gen3Out, err := rt.CompileGeneration(ctx, gen3In)
	if err != nil {
		t.Fatalf("CompileGeneration #3 after failure: %v", err)
	}
	if len(gen3Out.Lifecycles) == 0 || lifecycleID(gen3Out.Lifecycles[0]) != "gen3-lc" {
		t.Fatalf("expected gen3 lifecycle first, got %v", gen3Out.Lifecycles)
	}
}

func TestCompileGeneration_ZeroProcessResourceConstruction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	detector := rt.CompactionDetector()
	if detector == nil {
		t.Fatal("expected non-nil CompactionDetector on Runtime")
	}

	// Compile generation multiple times: CorePorts must receive the process-owned
	// detector and must NOT construct new process resources.
	for range 3 {
		out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{})
		if err != nil {
			t.Fatalf("CompileGeneration: %v", err)
		}
		if out.CorePorts.CompactionDetector != detector {
			t.Fatal("CompileGeneration returned different CompactionDetector instance; must share process-owned detector")
		}
	}
}

func TestCompileGeneration_ZeroCompactionResourceConstruction_Overlapping(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sched := newTestScheduler(t)
	t.Cleanup(func() { _ = sched.Close() })

	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger:         slog.Default(),
		ExtensionState: state.NewMem(nil),
		BackgroundAux:  sched,
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	var node yaml.Node
	_ = yaml.Unmarshal([]byte("extractor:\n  enabled: true\n  route: inherit\n"), &node)
	reg := aipsdk.Registration{
		ID:          "compaction-continuity",
		FactoryKind: "compaction-continuity",
		Kind:        aipsdk.PluginKindFeature,
		Enabled:     true,
		Config:      aipsdk.ConfigPayload{Node: node},
	}

	genIn := featurehost.GenerationInput{
		Registrations: []aipsdk.Registration{reg},
	}

	// Compile generation 1
	gen1Out, err := rt.CompileGeneration(ctx, genIn)
	if err != nil {
		t.Fatalf("CompileGeneration #1: %v", err)
	}

	// Compile overlapping generation 2
	gen2Out, err := rt.CompileGeneration(ctx, genIn)
	if err != nil {
		t.Fatalf("CompileGeneration #2: %v", err)
	}

	// Overlapping generations must observe the same process-owned detector
	// through the public consumer port (per-generation reconstruction would
	// surface here; coordinator/port identity is proven inside package
	// featurehost via unexported fields).
	if gen1Out.CorePorts.CompactionDetector == nil || gen2Out.CorePorts.CompactionDetector == nil {
		t.Fatal("expected non-nil CompactionDetector in CorePorts")
	}
	if gen1Out.CorePorts.CompactionDetector != gen2Out.CorePorts.CompactionDetector {
		t.Fatalf("overlapping gen1 vs gen2 detector changed: %p vs %p", gen1Out.CorePorts.CompactionDetector, gen2Out.CorePorts.CompactionDetector)
	}
}

func TestCompileGeneration_NoGenericServiceMapOrResolveAPI(t *testing.T) {
	t.Parallel()

	rtType := reflect.TypeFor[*featurehost.Runtime]()
	for m := range rtType.Methods() {
		name := m.Name
		forbidden := []string{"Resolve", "Get", "Lookup", "Service", "Services", "GetService"}
		for _, f := range forbidden {
			if strings.EqualFold(name, f) {
				t.Fatalf("featurehost.Runtime must not expose generic service map / lookup API: %s", name)
			}
		}
	}
}

func TestCompileGeneration_DefensiveCopying(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{
		Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	origLC := &dummyLifecycle{id: "orig"}
	lcs := []aipplugin.Lifecycle{origLC}
	out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Lifecycles: lcs,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}

	// Mutate input slice; output must not change
	lcs[0] = &dummyLifecycle{id: "mutated"}
	if lifecycleID(out.Lifecycles[0]) != "orig" {
		t.Fatal("CompileGeneration did not defensively copy Lifecycles from input")
	}

	// Mutate output slice; subsequent reads of fresh compile must not change
	out.Lifecycles[0] = &dummyLifecycle{id: "mutated-out"}
	out2, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Lifecycles: []aipplugin.Lifecycle{&dummyLifecycle{id: "fresh"}},
	})
	if err != nil {
		t.Fatalf("CompileGeneration #2: %v", err)
	}
	if lifecycleID(out2.Lifecycles[0]) != "fresh" {
		t.Fatalf("expected fresh, got %s", lifecycleID(out2.Lifecycles[0]))
	}
}
