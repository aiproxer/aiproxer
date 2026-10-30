package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type preflightCountingStore struct {
	*b2bua.MemoryStore
	nextBLeg atomic.Int32
	first    atomic.Int32
	inter    atomic.Int32
}

func (s *preflightCountingStore) NextBLeg(ctx context.Context, aLegID string) (b2bua.BLegRecord, error) {
	s.nextBLeg.Add(1)
	return s.MemoryStore.NextBLeg(ctx, aLegID)
}

func (s *preflightCountingStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	s.first.Add(1)
	return s.MemoryStore.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func (s *preflightCountingStore) SetInterleavedState(ctx context.Context, aLegID string, state interleavedstate.State) error {
	s.inter.Add(1)
	return s.MemoryStore.SetInterleavedState(ctx, aLegID, state)
}

type preflightAffinityStore struct {
	sets atomic.Int32
}

func (s *preflightAffinityStore) Get(context.Context, affinity.Key) (affinity.Binding, bool, error) {
	return affinity.Binding{}, false, nil
}

func (s *preflightAffinityStore) Set(context.Context, affinity.Binding) error {
	s.sets.Add(1)
	return nil
}

func (s *preflightAffinityStore) Delete(context.Context, affinity.Key) error {
	return nil
}

func TestCompileSelector_preflightHasNoExecutionSideEffects(t *testing.T) {
	t.Parallel()
	base, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st := &preflightCountingStore{MemoryStore: base}
	aff := &preflightAffinityStore{}
	var opens atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.AffinityStore = aff
	ex.DefaultBackend = "openai"
	ex.Backends = map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens.Add(1)
				t.Error("preflight must not open a backend")
				return nil, nil
			},
		},
	}
	_ = ex

	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^cheap$`, Replacement: "openai:gpt-4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	forms := []string{
		"openai:gpt-4",
		"gpt-4",
		"cheap",
		"a:m|b:m",
		"[weight=1]a:m^[weight=1]b:m",
		"a:m!b:m",
		"{ttft_timeout=60}openai:gpt-4",
		"{affinity=session}openai:gpt-4",
		"[first]cheap:m^[weight=100]expensive:m",
		"[thinker]a:m^b:m",
	}
	for _, raw := range forms {
		if _, err := routing.CompileSelector(raw, aliases, "openai"); err != nil {
			t.Fatalf("CompileSelector(%q): %v", raw, err)
		}
	}
	if st.nextBLeg.Load() != 0 {
		t.Fatalf("preflight allocated B-legs: %d", st.nextBLeg.Load())
	}
	if st.first.Load() != 0 {
		t.Fatalf("preflight mutated WeightedFirstConsumed: %d", st.first.Load())
	}
	if st.inter.Load() != 0 {
		t.Fatalf("preflight mutated interleaved state: %d", st.inter.Load())
	}
	if aff.sets.Load() != 0 {
		t.Fatalf("preflight mutated affinity: %d", aff.sets.Load())
	}
	if opens.Load() != 0 {
		t.Fatalf("preflight opened backends: %d", opens.Load())
	}
}

func TestBuildRoutePlan_sharesCompileSelectorHelper(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^alias$`, Replacement: "backendB:model-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := routing.CompileSelector("alias", aliases, "backendA")
	if err != nil {
		t.Fatalf("CompileSelector must succeed so buildRoutePlan can share it: %v", err)
	}
	if compiled == nil || len(compiled.Alternatives) == 0 || compiled.Alternatives[0].Primary == nil {
		t.Fatal("compiled selector missing primary")
	}
	if compiled.Alternatives[0].Primary.Backend != "backendB" {
		t.Fatalf("compiled backend: got %q want backendB", compiled.Alternatives[0].Primary.Backend)
	}
	ex := runtime.TestExecutor()
	ex.SelectorAliases = aliases
	ex.DefaultBackend = "backendA"
	got, err := ex.BuildRoutePlanPrimaryBackendForTest(context.Background(), "alias")
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if got != compiled.Alternatives[0].Primary.Backend {
		t.Fatalf("buildRoutePlan drifted from CompileSelector: got %q want %q", got, compiled.Alternatives[0].Primary.Backend)
	}
}

func TestBuildRoutePlan_sharesPrepareSelectorHelper(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: `^alias$`, Replacement: "backendB:model-x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := routing.PrepareSelector("alias", aliases, "backendA", nil, config.ExecutionCompositionSafe, nil)
	if err != nil {
		t.Fatalf("PrepareSelector must succeed: %v", err)
	}
	if prepared == nil || len(prepared.Alternatives) == 0 || prepared.Alternatives[0].Primary == nil {
		t.Fatal("prepared selector missing primary")
	}
	ex := runtime.TestExecutor()
	ex.SelectorAliases = aliases
	ex.DefaultBackend = "backendA"
	got, err := ex.BuildRoutePlanPrimaryBackendForTest(context.Background(), "alias")
	if err != nil {
		t.Fatalf("buildRoutePlan: %v", err)
	}
	if got != prepared.Alternatives[0].Primary.Backend {
		t.Fatalf("buildRoutePlan drifted from PrepareSelector: got %q want %q", got, prepared.Alternatives[0].Primary.Backend)
	}
}
