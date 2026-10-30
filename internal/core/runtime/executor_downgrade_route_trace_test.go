package runtime_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// downgradeRouteTraceCatalogResolver returns distinct catalog match metadata depending on whether
// reasoning was already stripped from the call (post-downgrade) so route trace can detect stale facts.
type downgradeRouteTraceCatalogResolver struct{}

func (downgradeRouteTraceCatalogResolver) Resolve(
	ctx context.Context,
	cand routing.AttemptCandidate,
	call aipapi.Call,
	backend aipapi.BackendCaps,
) modelcatalog.EffectiveFacts {
	_ = ctx
	input := strings.TrimSpace(cand.Primary.Model)
	be := maps.Clone(backend)
	postDowngrade := strings.TrimSpace(call.Options.ReasoningEffort) == ""
	if !postDowngrade {
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:    modelcatalog.FactSourceCatalog,
				MatchKind: modelcatalog.MatchExact,
			},
			BackendCaps:   backend,
			EffectiveCaps: be,
			Matched:       true,
			Match: modelcatalog.MatchResult{
				Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input,
			},
		}
	}
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchAmbiguous,
		},
		BackendCaps:   backend,
		EffectiveCaps: be,
		Matched:       true,
		Match: modelcatalog.MatchResult{
			Kind: modelcatalog.MatchAmbiguous, InputModel: input, MatchedID: "",
			Candidates: []string{"post-a", "post-b"},
		},
	}
}

func TestExecutor_downgrade_noEligibility_routeTraceUsesPostDowngradeFacts(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	trace := diag.NewRouteTraceBuffer(16)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(11)
	ex.RouteTrace = trace
	ex.CatalogResolver = downgradeRouteTraceCatalogResolver{}
	// EligibilityResolver intentionally nil = regression covers facts refresh on this path.
	ex.Backends = map[string]execbackend.Backend{
		"rb": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "rb:gpt-4o"},
		Options: aipapi.GenerationOptions{
			ReasoningEffort: "low",
		},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(context.Background(), s)
	_ = s.Close()

	var last *diag.RouteTraceCatalog
	for _, e := range trace.Snapshot() {
		if e.Decision == "plan_candidate" && e.Catalog != nil {
			last = e.Catalog
		}
	}
	if last == nil {
		t.Fatal("expected at least one plan_candidate route trace with catalog metadata")
		return
	}
	if last.Negotiation != string(aipapi.NegotiationDowngrade) {
		t.Fatalf("negotiation in trace: %q", last.Negotiation)
	}
	if got, want := last.MatchKind, modelcatalog.MatchAmbiguous.String(); got != want {
		t.Fatalf("route trace catalog match_kind: got %q want %q (pre-downgrade would be exact)", got, want)
	}
}
