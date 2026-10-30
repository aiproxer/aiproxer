package auth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	feature "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	composition "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestRequestCredentialHybridOverlapUsesConcreteOccurrences(t *testing.T) {
	const token = "ghp_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"
	m := newExactCredentialMatcher(token, "accepted-key")
	generation, err := feature.BuildGenerationServices(feature.DetectorPolicy{BetterLeaks: feature.BetterLeaksPolicy{
		Enabled: true, MinimumConfidence: "medium", Workers: 1, MaxFindings: 256, IsolateRules: []string{"github-pat"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("GITHUB_TOKEN="+token+" ", 2))}}}}
	decision, err := feature.NewGuard(feature.Config{Action: feature.ActionBlock, ScanMaxBytes: feature.DefaultScanMaxBytes}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{
		MatcherResolver: credentialTestResolver{m}, Capability: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeBlock || len(decision.Findings) != 1 || decision.Findings[0].OccurrenceCount != 2 || decision.Findings[0].SecretRefName != "accepted-key" || decision.Findings[0].RuleID != "github-pat" {
		t.Fatalf("overlap decision: outcome=%s findings=%+v", decision.Outcome, decision.Findings)
	}
}

type credentialTestResolver struct{ matcher sdk.Matcher }

func (r credentialTestResolver) Resolve(context.Context) (sdk.Matcher, error) { return r.matcher, nil }

type forbiddenCredentialEnvironment struct{}

func (forbiddenCredentialEnvironment) Lookup(string) (string, bool) {
	panic("unexpected environment read")
}
func (forbiddenCredentialEnvironment) Snapshot() []string { panic("unexpected environment read") }

func TestGenerationRedactionPolicyAcrossAccessModesAndDetectors(t *testing.T) {
	const token = "ghp_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"
	for _, mode := range []accessmode.Mode{accessmode.ModeSingleUser, accessmode.ModeMultiUser} {
		for _, detector := range []string{"exact", "betterleaks", "hybrid"} {
			for _, preserve := range []bool{false, true} {
				for _, mask := range []byte{'#', '@'} {
					for _, jsonContent := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/prefix=%t/mask=%c/json=%t", mode, detector, preserve, mask, jsonContent), func(t *testing.T) {
							cfg := feature.RuntimeConfig{Enabled: true, Action: feature.ActionRedact, ScanMaxBytes: feature.DefaultScanMaxBytes, MaskByte: mask, PreserveKnownPrefixes: preserve}
							cfg.BetterLeaks = feature.BetterLeaksPolicy{Enabled: detector != "exact", MinimumConfidence: "medium", Workers: 1, MaxFindings: 256, IsolateRules: []string{"github-pat"}}
							composed, err := composition.Compose(composition.Input{AccessMode: mode, RuntimeConfig: &cfg, Environment: forbiddenCredentialEnvironment{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
							if err != nil {
								t.Fatal(err)
							}
							var matcher sdk.Matcher
							if detector != "betterleaks" {
								if mode == accessmode.ModeMultiUser {
									matcher = newExactCredentialMatcher(token, "accepted-key")
								} else {
									cat, err := engine.BuildCatalog([]engine.CatalogInput{{Name: "local-key", Value: token, KnownPublicPrefix: "ghp_"}}, 8)
									if err != nil {
										t.Fatal(err)
									}
									matcher = engine.AsMatcher(engine.NewMatcherWithOptions(cat, engine.MatcherOptions{MaskByte: '*', PreserveKnownPrefixes: true}))
								}
							}
							text := "GITHUB_TOKEN=" + token
							part := aipapi.TextPart(text)
							if jsonContent {
								part = aipapi.Part{Kind: aipapi.PartJSON, Content: []byte(`{"value":"` + text + `"}`)}
							}
							call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{part}}}}
							decision, err := feature.NewGuard(feature.Config{Action: feature.ActionRedact, ScanMaxBytes: feature.DefaultScanMaxBytes}).Evaluate(t.Context(), call, sdk.Meta{}, sdk.Services{MatcherResolver: credentialTestResolver{matcher}, Capability: composed.Services})
							if err != nil {
								t.Fatal(err)
							}
							if decision.Outcome != sdk.OutcomeRedacted {
								t.Fatalf("outcome=%s failure=%s", decision.Outcome, decision.FailureKind)
							}
							want := strings.Repeat(string(mask), len(token))
							if preserve {
								want = "ghp_" + strings.Repeat(string(mask), len(token)-4)
							}
							result := call.Messages[0].Parts[0]
							got := result.Text
							if jsonContent {
								got = string(result.Content)
							}
							if !strings.Contains(got, want) || strings.Contains(got, token) {
								t.Fatal("generation redaction policy was not applied")
							}
						})
					}
				}
			}
		}
	}
}
