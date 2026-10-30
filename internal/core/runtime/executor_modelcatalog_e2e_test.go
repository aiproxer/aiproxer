package runtime_test

import (
	"context"
	"maps"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// noMatchCatalogResolver returns an explicit no-match to verify backend-only limiting (no catalog-driven rejection).
type noMatchCatalogResolver struct{}

func (noMatchCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceBackendDeclaration,
			MatchKind: modelcatalog.MatchNoMatch,
		},
		BackendCaps:   backend,
		EffectiveCaps: be,
		Matched:       false,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchNoMatch, InputModel: input},
	}
}

func TestExecutor_catalogNoMatch_opensWithBackendCapabilities(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened bool
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	ex.CatalogResolver = noMatchCatalogResolver{}
	ex.Backends = map[string]execbackend.Backend{
		"be": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = true
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "be:some-model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("expected backend open for no-match catalog")
	}
	_, _ = aipapi.Collect(context.Background(), s)
	_ = s.Close()
}

// snapshotGenResolver changes reported snapshot generation between Resolve calls to simulate
// a catalog refresh between requests (later requests see a new generation).
type snapshotGenResolver struct {
	n atomic.Int64
}

func (s *snapshotGenResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	g := s.n.Add(1)
	return modelcatalog.EffectiveFacts{
		Facts:         modelcatalog.ModelFacts{Source: modelcatalog.FactSourceCatalog, MatchKind: modelcatalog.MatchExact},
		BackendCaps:   backend,
		EffectiveCaps: be,
		Matched:       true,
		Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		Snapshot:      modelcatalog.SnapshotRef{Generation: "gen" + strconv.FormatInt(g, 10)},
	}
}

func TestExecutor_sequentialExecutes_seeNewCatalogGenerations(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sgr := &snapshotGenResolver{}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.EligibilityResolver = modelcatalog.NewEligibilityResolver(modelcatalog.DefaultSizeEstimator{})
	ex.CatalogResolver = sgr
	ex.Backends = map[string]execbackend.Backend{
		"be": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "be:m1"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("a")},
		}},
	}
	s1, err := ex.Execute(context.Background(), call)
	_, _ = aipapi.Collect(context.Background(), mustStream(t, s1, err))
	gAfterFirst := sgr.n.Load()
	s2, err2 := ex.Execute(context.Background(), call)
	_, _ = aipapi.Collect(context.Background(), mustStream(t, s2, err2))
	gAfterSecond := sgr.n.Load()
	if gAfterSecond <= gAfterFirst {
		t.Fatalf("expected more resolve calls on second execute, first=%d second=%d", gAfterFirst, gAfterSecond)
	}
}

func mustStream(t *testing.T, s aipapi.EventStream, err error) aipapi.EventStream {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("nil stream")
	}
	return s
}
