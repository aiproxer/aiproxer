package openaicaps_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation/reasoningreplay"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestCompatibleReplayEligible_parityWithBuiltinResolveMatch(t *testing.T) {
	t.Parallel()
	cfg := mustDecodeRP(t, `
action: restore
use_builtin_catalog: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	cases := []struct {
		name     string
		model    string
		prefixes []string
		want     bool
	}{
		{name: "kimi_openrouter", model: "moonshotai/kimi-k2", prefixes: []string{"openrouter"}, want: true},
		{name: "deepseek_vllm", model: "deepseek-r1", prefixes: []string{"vllm"}, want: true},
		{name: "glm_ollama", model: "glm-4.5", prefixes: []string{"ollama"}, want: true},
		{name: "qwen_hf", model: "qwen2.5-72b", prefixes: []string{"huggingface"}, want: true},
		{name: "minimax_nvidia", model: "MiniMax-M1", prefixes: []string{"nvidia"}, want: true},
		{name: "mimo_lmstudio", model: "xiaomi/mimo-v2", prefixes: []string{"lmstudio"}, want: true},
		{name: "hy3_llamacpp", model: "hy3-chat", prefixes: []string{"llamacpp"}, want: true},
		{name: "gpt55_legacy", model: "gpt-5.5", prefixes: []string{"openai-legacy"}, want: true},
		{name: "gpt52_responses", model: "openai/gpt-5.2", prefixes: []string{"openai-responses"}, want: true},
		{name: "gpt56_excluded", model: "gpt-5.6", prefixes: []string{"openai-legacy"}, want: false},
		{name: "gpt5_dash_minor_excluded", model: "gpt-5-6", prefixes: []string{"openai-legacy"}, want: false},
		{name: "gpt5_named_suffix_mini", model: "gpt-5-mini", prefixes: []string{"openai-legacy"}, want: true},
		{name: "gpt4_excluded", model: "gpt-4o", prefixes: []string{"openai-legacy"}, want: false},
		{name: "claude_excluded", model: "claude-3-5", prefixes: []string{"openrouter"}, want: false},
		{name: "kimi_bad_prefix", model: "kimi-k2", prefixes: []string{"not-catalog"}, want: false},
		{name: "glm_incidental", model: "glamour", prefixes: []string{"openrouter"}, want: false},
		{name: "deepseeker_incidental", model: "deepseeker", prefixes: []string{"openrouter"}, want: false},
		{name: "qwentest_incidental", model: "qwentest", prefixes: []string{"openrouter"}, want: false},
		{name: "minimaximum_incidental", model: "minimaximum", prefixes: []string{"openrouter"}, want: false},
		{name: "kimiko_incidental", model: "kimiko", prefixes: []string{"openrouter"}, want: false},
		{name: "moonshotai_vendor", model: "moonshotai/custom", prefixes: []string{"openrouter"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capsEligible := openaicaps.CompatibleReplayEligible(tc.model, tc.prefixes)
			shared := reasoningreplay.Eligible(tc.model, tc.prefixes)
			match, err := reasoningpreservation.ResolveMatch(cfg, reasoningpreservation.CandidateIdentity{
				BackendID:       "instance",
				BackendPrefixes: tc.prefixes,
				Model:           tc.model,
			})
			if err != nil {
				t.Fatal(err)
			}
			builtinEligible := reasoningpreservation.MatchEligible(match.Kind) && match.Kind == reasoningpreservation.MatchBuiltin
			if capsEligible != tc.want || shared != tc.want || builtinEligible != tc.want {
				t.Fatalf("model=%q prefixes=%v caps=%v shared=%v builtin=%v(%s) want %v",
					tc.model, tc.prefixes, capsEligible, shared, builtinEligible, match.Kind, tc.want)
			}
		})
	}
}

func TestResolveCompatibleReplaySupport_chatAndResponsesDialects(t *testing.T) {
	t.Parallel()
	model := "deepseek-r1"
	prefixes := []string{"openrouter"}
	chat := openaicaps.ResolveCompatibleReplaySupport(openaicaps.FlavorChat, model, prefixes)
	if len(chat.Dialects) != 1 || chat.Dialects[0] != aipapi.ReasoningDialectOpenAIChatTextV1 {
		t.Fatalf("chat dialects=%v", chat.Dialects)
	}
	resp := openaicaps.ResolveCompatibleReplaySupport(openaicaps.FlavorResponses, model, prefixes)
	if len(resp.Dialects) != 1 || resp.Dialects[0] != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("responses dialects=%v", resp.Dialects)
	}
	empty := openaicaps.ResolveCompatibleReplaySupport(openaicaps.FlavorChat, "gpt-5.6", prefixes)
	if len(empty.Dialects) != 0 {
		t.Fatalf("gpt-5.6 must resolve empty support, got %v", empty.Dialects)
	}
}

func TestCompatibleReplay_explicitOperatorKeywordStillSubstring(t *testing.T) {
	t.Parallel()
	// Explicit operator keywords keep contains-semantics; gpt-5.6 can be opted in.
	cfg := mustDecodeRP(t, `
action: restore
use_builtin_catalog: true
rules:
  - id: allow-56
    backend: be-1
    model_keywords: ["gpt-5.6"]
    enabled: true
on_ambiguous: log_skip
on_unrepresentable: reject
on_state_error: reject
state:
  ttl: 1h
  max_turns_per_session: 4
  max_reasoning_bytes_per_turn: 1024
  max_session_bytes: 4096
`)
	got, err := reasoningpreservation.ResolveMatch(cfg, reasoningpreservation.CandidateIdentity{
		BackendID: "be-1",
		Model:     "openai/gpt-5.6-mini",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != reasoningpreservation.MatchExplicitEnabledModel {
		t.Fatalf("kind=%q want explicit enabled", got.Kind)
	}
	if openaicaps.CompatibleReplayEligible("openai/gpt-5.6-mini", []string{"openai-legacy"}) {
		t.Fatal("automatic caps eligibility must still exclude gpt-5.6")
	}
}

func mustDecodeRP(t *testing.T, yamlBody string) reasoningpreservation.Config {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(yamlBody), &doc); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	node := doc
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		node = *doc.Content[0]
	}
	cfg, err := reasoningpreservation.DecodeConfig(node)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	return cfg
}
