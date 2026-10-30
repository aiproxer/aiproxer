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

// narrowVisionCatalogResolver removes vision from effective caps for backend "narrow" to force negotiation reject.
type narrowVisionCatalogResolver struct{}

func (narrowVisionCatalogResolver) Resolve(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	_ = ctx
	_ = call
	input := strings.TrimSpace(cand.Primary.Model)
	if cand.Primary.Backend == "narrow" {
		out := maps.Clone(backend)
		delete(out, aipapi.CapabilityVision)
		return modelcatalog.EffectiveFacts{
			Facts: modelcatalog.ModelFacts{
				Source:            modelcatalog.FactSourceCatalog,
				MatchKind:         modelcatalog.MatchExact,
				ContextLimit:      modelcatalog.LimitFact{State: modelcatalog.LimitUnknown},
				Tools:             modelcatalog.CapabilityUnknown,
				StructuredOutputs: modelcatalog.CapabilityUnknown,
				Reasoning:         modelcatalog.CapabilityUnknown,
				Vision:            modelcatalog.CapabilityUnknown,
				Documents:         modelcatalog.CapabilityUnknown,
			},
			BackendCaps:   backend,
			EffectiveCaps: out,
			Matched:       true,
			Match:         modelcatalog.MatchResult{Kind: modelcatalog.MatchExact, InputModel: input, MatchedID: input},
		}
	}
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

func TestExecutor_catalogNarrowsCaps_firstCandidateRejected_secondOpens(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	rt := diag.NewRouteTraceBuffer(16)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.RouteTrace = rt
	ex.CatalogResolver = narrowVisionCatalogResolver{}
	ex.Backends = map[string]execbackend.Backend{
		"narrow": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityVision),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Fatal("narrow must not open after vision-removed negotiation reject")
				return nil, nil
			},
		},
		"wide": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityVision),
			Open: func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = cand.Primary.Backend
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "narrow:gpt-4o|wide:gpt-4o"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "https://example.com/x.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "wide" {
		t.Fatalf("opened backend: %q", opened)
	}
	_, _ = aipapi.Collect(context.Background(), s)
	_ = s.Close()

	snap := rt.Snapshot()
	var rejectTrace *diag.RouteTraceEntry
	for i := range snap {
		e := snap[i]
		if e.Decision != "plan_candidate" || e.Catalog == nil {
			continue
		}
		if e.Catalog.Negotiation == string(aipapi.NegotiationReject) && e.Catalog.FactSource == modelcatalog.FactSourceCatalog.String() {
			rejectTrace = &snap[i]
			break
		}
	}
	if rejectTrace == nil {
		t.Fatalf("expected route trace plan_candidate with negotiation reject and catalog source, got %#v", snap)
		return
	}
	if rejectTrace.Catalog.MatchKind != modelcatalog.MatchExact.String() {
		t.Fatalf("catalog match_kind: %q", rejectTrace.Catalog.MatchKind)
	}
	if rejectTrace.Catalog.Eligibility != "skipped" {
		t.Fatalf("catalog eligibility: %q", rejectTrace.Catalog.Eligibility)
	}
}

func TestExecutor_catalogDisabled_noResolver_usesBackendCaps(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opened string
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"only": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityVision),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opened = "only"
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "only:gpt-4o"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:      aipapi.PartImageRef,
				ImageRef:  "https://example.com/x.png",
				ImageMIME: "image/png",
			}},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "only" {
		t.Fatalf("opened: %q", opened)
	}
	_, _ = aipapi.Collect(context.Background(), s)
	_ = s.Close()
}
