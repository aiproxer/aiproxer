package reasoningpreservation_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"gopkg.in/yaml.v3"
)

func TestEnsureCompanionRulesPreservesFeatureNodes(t *testing.T) {
	t.Parallel()
	input := mustYAML(t, `
action: observe
use_builtin_catalog: false
rules:
  - id: operator-rule
    backend: operator-backend
    model_keywords: [gpt-5]
    enabled: true
  - id: disabled-backend
    backend: backend-disabled
    enabled: false
on_ambiguous: log_skip
on_unrepresentable: log_skip
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got, err := reasoningpreservation.EnsureCompanionRules(input, []string{"backend-new", "backend-disabled"}, "companion-")
	if err != nil {
		t.Fatal(err)
	}
	text := nodeYAML(t, got)
	for _, want := range []string{"action: observe", "use_builtin_catalog: false", "operator-rule", "model_keywords:", "on_state_error: reject", "ttl: 1h"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mutated config lost %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "backend: backend-new") != 1 {
		t.Fatalf("expected one new companion rule:\n%s", text)
	}
	if got.Content[0].Content[0].Value != input.Content[0].Content[0].Value {
		t.Fatal("existing mapping was unexpectedly reordered or replaced")
	}
	decodedGot, err := reasoningpreservation.DecodeConfig(got)
	if err != nil {
		t.Fatalf("mutated config no longer validates: %v", err)
	}
	for _, rule := range decodedGot.Rules {
		if rule.Backend == "backend-disabled" && rule.Enabled != nil && *rule.Enabled {
			t.Fatal("explicit disabled backend-only rule was overridden")
		}
	}
}

func TestEnsureCompanionRulesRejectsUnknownFeatureKeysBeforeMutation(t *testing.T) {
	t.Parallel()
	input := mustYAML(t, `
action: restore
unknown: true
`)
	before := nodeYAML(t, input)
	if _, err := reasoningpreservation.EnsureCompanionRules(input, []string{"backend-new"}, "companion-"); err == nil {
		t.Fatal("unknown feature key was accepted")
	}
	if after := nodeYAML(t, input); after != before {
		t.Fatalf("invalid config was mutated:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestNewCompanionConfigIsDeterministicAndCollisionSafe(t *testing.T) {
	t.Parallel()
	ids := []string{"backend/a", "backend-a", strings.Repeat("long-id-", 20)}
	first, err := reasoningpreservation.NewCompanionConfig(ids, "companion-")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reasoningpreservation.NewCompanionConfig(ids, "companion-")
	if err != nil {
		t.Fatal(err)
	}
	if nodeYAML(t, first) != nodeYAML(t, second) {
		t.Fatal("companion IDs/config are not deterministic")
	}
	decoded, err := reasoningpreservation.DecodeConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(decoded.Rules))
	for _, rule := range decoded.Rules {
		if len(rule.ID) > 64 || seen[rule.ID] {
			t.Fatalf("companion ID is not bounded and unique: %+v", decoded.Rules)
		}
		seen[rule.ID] = true
	}
	firstPrefix, err := reasoningpreservation.NewCompanionConfig([]string{"backend/a"}, "first-")
	if err != nil {
		t.Fatal(err)
	}
	secondPrefix, err := reasoningpreservation.NewCompanionConfig([]string{"backend/a"}, "second-")
	if err != nil {
		t.Fatal(err)
	}
	if nodeYAML(t, firstPrefix) == nodeYAML(t, secondPrefix) {
		t.Fatal("different rule prefixes produced identical configs")
	}
	longPrefix, err := reasoningpreservation.NewCompanionConfig([]string{"backend/a", "backend-a"}, strings.Repeat("prefix-", 20))
	if err != nil {
		t.Fatal(err)
	}
	longDecoded, err := reasoningpreservation.DecodeConfig(longPrefix)
	if err != nil {
		t.Fatal(err)
	}
	longSeen := make(map[string]bool, len(longDecoded.Rules))
	for _, rule := range longDecoded.Rules {
		if len(rule.ID) > 64 || longSeen[rule.ID] {
			t.Fatalf("oversized prefix produced invalid or duplicate ID: %+v", longDecoded.Rules)
		}
		longSeen[rule.ID] = true
	}
}

func nodeYAML(t *testing.T, n yaml.Node) string {
	t.Helper()
	b, err := yaml.Marshal(&n)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
