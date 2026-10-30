package secretguard

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestGuard_BetterLeaksCancellationPropagatesAllActions(t *testing.T) {
	t.Parallel()
	for _, action := range []string{ActionLog, ActionBlock, ActionRedact} {
		for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(action+"/"+want.Error(), func(t *testing.T) {
				t.Parallel()
				g, services := newBetterLeaksActionGuard(t, action, 0, 0)
				var ctx context.Context
				var cancel context.CancelFunc
				if want == context.Canceled {
					ctx, cancel = context.WithCancel(t.Context())
					cancel()
				} else {
					ctx, cancel = context.WithDeadline(t.Context(), time.Time{})
				}
				defer cancel()
				call := betterLeaksTextCall("credential=" + adapterGitHubToken)
				before := aipapi.CloneCall(call)
				decision, err := g.Evaluate(ctx, &call, sdk.Meta{}, services)
				if !errors.Is(err, want) {
					t.Errorf("cancellation identity lost: error=%v outcome=%q failure_kind=%q", err, decision.Outcome, decision.FailureKind)
				}
				if !reflect.DeepEqual(call, before) {
					t.Error("canceled evaluation changed the original call")
				}
				assertBetterLeaksDecisionSafe(t, decision)
			})
		}
	}
}

type positionalCollectionCounter struct {
	sdk.Matcher
	positions sdk.PositionalMatcher
	calls     int
}

func (m *positionalCollectionCounter) ScanOccurrences(input []byte) []sdk.PositionalOccurrence {
	m.calls++
	return m.positions.ScanOccurrences(input)
}

func (m *positionalCollectionCounter) RedactionOptions() engine.MatcherOptions {
	provider, ok := m.Matcher.(interface{ RedactionOptions() engine.MatcherOptions })
	if !ok {
		panic("fixture matcher does not expose redaction options")
	}
	return provider.RedactionOptions()
}

func TestScanCall_ExactOnlySkipsHybridCollection(t *testing.T) {
	t.Parallel()
	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "REQUEST_KEY", Value: adapterGitHubToken, SourceCategory: sdk.SourceCategoryRequestCred,
	}}, 8)
	if err != nil {
		t.Fatal("build fixture catalog")
	}
	matcher := engine.AsMatcher(engine.NewMatcher(cat))
	positions, ok := matcher.(sdk.PositionalMatcher)
	if !ok {
		t.Fatal("fixture matcher does not expose positions")
	}
	raw, _ := scalarDenseJSON(128 << 10)
	for _, mode := range []scanMode{modeScan, modeLogScan, modeRedact} {
		for _, disabledGeneration := range []bool{false, true} {
			for _, jsonPayload := range []bool{false, true} {
				t.Run(fmtExactOnlyCase(mode, disabledGeneration, jsonPayload), func(t *testing.T) {
					tracked := &positionalCollectionCounter{Matcher: matcher, positions: positions}
					call := betterLeaksTextCall("credential=" + adapterGitHubToken)
					if jsonPayload {
						call = betterLeaksJSONCall(raw)
					}
					before := aipapi.CloneCall(call)
					var generation *GenerationServices
					if disabledGeneration {
						generation = &GenerationServices{redaction: tracked.RedactionOptions()}
					}
					out, err := scanCall(t.Context(), &call, tracked, mode, DefaultScanMaxBytes, generation)
					if err != nil || len(out.Findings) != 1 || out.Findings[0].OccurrenceCount != 1 {
						t.Fatalf("exact-only finding lost: error=%v findings=%d", err, len(out.Findings))
					}
					if tracked.calls != 0 || len(out.exactPrivateFindings) != 0 {
						t.Errorf("unused hybrid collection: positional_calls=%d private_groups=%d", tracked.calls, len(out.exactPrivateFindings))
					}
					if mode == modeRedact {
						if out.MutationCount != 1 || reflect.DeepEqual(call, before) {
							t.Error("exact-only redaction did not rewrite the matched fragment")
						}
					} else if !reflect.DeepEqual(call, before) {
						t.Error("exact-only scan changed the original call")
					}
					assertBetterLeaksDecisionSafe(t, sdk.Decision{Findings: out.Findings})
				})
			}
		}
	}
}

func fmtExactOnlyCase(mode scanMode, generation, jsonPayload bool) string {
	name := map[scanMode]string{modeScan: "scan", modeLogScan: "log", modeRedact: "redact"}[mode]
	if generation {
		name += "/disabled_generation"
	} else {
		name += "/no_generation"
	}
	if jsonPayload {
		return name + "/scalar_dense_json"
	}
	return name + "/text"
}

func TestGuard_HybridOverlapRetainsCoverageAllActions(t *testing.T) {
	t.Parallel()
	cat, err := engine.BuildCatalog([]engine.CatalogInput{{
		Name: "REQUEST_KEY", Value: adapterGitHubToken, SourceCategory: sdk.SourceCategoryRequestCred,
	}}, 8)
	if err != nil {
		t.Fatal("build fixture catalog")
	}
	for _, action := range []string{ActionLog, ActionBlock, ActionRedact} {
		t.Run(action, func(t *testing.T) {
			g, services := newBetterLeaksActionGuard(t, action, 0, 0)
			services.MatcherResolver = engine.NewStaticMatcherResolver(cat, engine.MatcherOptions{})
			call := betterLeaksJSONCall([]byte(`["` + adapterGitHubToken + `","` + adapterGitHubToken + `"]`))
			before := aipapi.CloneCall(call)
			d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, services)
			if err != nil || len(d.Findings) != 1 || d.Findings[0].OccurrenceCount != 2 || d.Findings[0].RuleID != "github-pat" {
				t.Fatalf("hybrid overlap lost coverage: error=%v findings=%d", err, len(d.Findings))
			}
			if action == ActionRedact {
				if d.Outcome != sdk.OutcomeRedacted || d.MutationCount != 1 {
					fatalDecisionSummary(t, d)
				}
			} else if !reflect.DeepEqual(call, before) {
				t.Error("hybrid scan changed the original call")
			}
			assertBetterLeaksDecisionSafe(t, d)
		})
	}
}
