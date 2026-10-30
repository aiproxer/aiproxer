package reasoningreplay_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation/reasoningreplay"
)

func TestModelEligible_familiesAndBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		model string
		want  bool
	}{
		// DeepSeek
		{name: "deepseek_plain", model: "deepseek-r1", want: true},
		{name: "deepseek_vendor_prefix", model: "openrouter/deepseek-chat", want: true},
		{name: "deepseek_case", model: "DeepSeek-V3", want: true},
		{name: "deepseek_incidental", model: "notdeepseekchat", want: false},

		// Kimi / Moonshot
		{name: "kimi_plain", model: "kimi-k2", want: true},
		{name: "kimi_vendor", model: "moonshotai/kimi-k2", want: true},
		{name: "moonshot_plain", model: "moonshot-v1-8k", want: true},
		{name: "moonshot_vendor_token", model: "moonshotai/custom", want: true},
		{name: "moonshotai_bare", model: "moonshotai", want: true},
		{name: "kimi_case_ws", model: "  KiMi-K2  ", want: true},
		// Letter continuation after a family token is rejected (FN safer than FP).
		// Note: "kimono" does not contain "kimi" (kimo…); use kimiko for the kimi FP.
		{name: "kimi_lookalike_kimono", model: "kimono", want: false},
		{name: "kimi_incidental_kimiko", model: "kimiko", want: false},
		{name: "deepseek_incidental_deepseeker", model: "deepseeker", want: false},
		{name: "qwen_incidental_qwentest", model: "qwentest", want: false},
		{name: "minimax_incidental_minimaximum", model: "minimaximum", want: false},
		{name: "deepseek_incidental_deepseekcoder_glue", model: "deepseekcoder", want: false},

		// GLM (short — no incidental)
		{name: "glm_sep", model: "glm-4.5", want: true},
		{name: "glm_digit_glue", model: "glm4", want: true},
		{name: "glm_vendor", model: "zhipu/glm-4-flash", want: true},
		{name: "glm_incidental_prefix", model: "aglm-model", want: false},
		{name: "glm_incidental_word", model: "glamour-chat", want: false},

		// MiMo (short)
		{name: "mimo_sep", model: "mimo-v2", want: true},
		{name: "mimo_vendor", model: "xiaomi/mimo-7b", want: true},
		{name: "mimo_incidental", model: "mimosa-herb", want: false},

		// Qwen
		{name: "qwen_plain", model: "qwen2.5-72b", want: true},
		{name: "qwen_vendor", model: "alibaba/qwen-plus", want: true},
		{name: "qwen_incidental", model: "xqweny", want: false},

		// HY3 (short)
		{name: "hy3_sep", model: "hy3-preview", want: true},
		{name: "hy3_vendor", model: "tencent/hy3-chat", want: true},
		{name: "hy3_incidental_letter", model: "hy3x-model", want: false},

		// MiniMax
		{name: "minimax_plain", model: "minimax-m1", want: true},
		{name: "minimax_case", model: "MiniMax-Text-01", want: true},
		{name: "minimax_incidental", model: "notminimax", want: false},

		// GPT automatic include 5 .. 5.5
		{name: "gpt5", model: "gpt-5", want: true},
		{name: "gpt5_0", model: "gpt-5.0", want: true},
		{name: "gpt5_2", model: "gpt-5.2", want: true},
		{name: "gpt5_5", model: "gpt-5.5", want: true},
		{name: "gpt5_5_mini", model: "gpt-5.5-mini", want: true},
		{name: "gpt5_5_patch", model: "gpt-5.5.1", want: true},
		{name: "gpt5_vendor", model: "openai/gpt-5.2", want: true},
		{name: "gpt5_case", model: "GPT-5.1", want: true},
		{name: "gpt5_glue", model: "gpt5", want: true},
		{name: "gpt5_0_glue", model: "gpt5.0", want: true},

		// GPT automatic exclude
		{name: "gpt5_6", model: "gpt-5.6", want: false},
		{name: "gpt5_6_mini", model: "gpt-5.6-mini", want: false},
		{name: "gpt5_10", model: "gpt-5.10", want: false},
		{name: "gpt5_6_patch", model: "gpt-5.6.1", want: false},
		// Dash/underscore minor separators must not be treated as bare gpt-5 suffixes.
		{name: "gpt5_dash_minor_6", model: "gpt-5-6", want: false},
		{name: "gpt5_underscore_minor_6", model: "gpt-5_6", want: false},
		{name: "gpt5_underscore_all_minor_6", model: "gpt_5_6", want: false},
		{name: "gpt5_dash_minor_10", model: "gpt-5-10", want: false},
		{name: "gpt6", model: "gpt-6", want: false},
		{name: "gpt6_0", model: "gpt-6.0", want: false},
		{name: "gpt4o", model: "gpt-4o", want: false},
		{name: "gpt4_1", model: "gpt-4.1", want: false},
		{name: "gpt35", model: "gpt-3.5-turbo", want: false},
		{name: "gpt_bare", model: "gpt", want: false},
		{name: "gpt_malformed_dot", model: "gpt-5.", want: false},
		{name: "gpt_malformed_double", model: "gpt-5..1", want: false},
		{name: "gpt_incidental_mygpt", model: "mygpt-5", want: false},
		{name: "gpt_incidental_chatgpt", model: "chatgpt-5", want: false},
		// Named suffixes after major remain eligible (not numeric minors).
		{name: "gpt5_named_suffix_mini", model: "gpt-5-mini", want: true},
		{name: "gpt5_named_suffix_chat", model: "gpt-5-chat-latest", want: true},

		// Empty / junk
		{name: "empty", model: "", want: false},
		{name: "spaces", model: "   ", want: false},
		{name: "unrelated", model: "claude-3-5-sonnet", want: false},
		{name: "llama", model: "meta-llama/llama-3.1-70b", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := reasoningreplay.ModelEligible(tc.model)
			if got != tc.want {
				t.Fatalf("ModelEligible(%q)=%v want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestPrefixEligible_catalogPrefixes(t *testing.T) {
	t.Parallel()
	if !reasoningreplay.PrefixEligible([]string{"openrouter"}) {
		t.Fatal("openrouter must be eligible")
	}
	if reasoningreplay.PrefixEligible(nil) {
		t.Fatal("empty prefixes must not be eligible")
	}
	if reasoningreplay.PrefixEligible([]string{"not-a-catalog-family"}) {
		t.Fatal("unknown prefix must not be eligible")
	}
	if !reasoningreplay.PrefixEligible([]string{"  OpenAI-Responses  "}) {
		t.Fatal("prefix match must trim/case-fold")
	}
}

func TestEligible_requiresPrefixAndModel(t *testing.T) {
	t.Parallel()
	if !reasoningreplay.Eligible("kimi-k2", []string{"openrouter"}) {
		t.Fatal("want eligible")
	}
	if reasoningreplay.Eligible("kimi-k2", []string{"nope"}) {
		t.Fatal("bad prefix must not be eligible")
	}
	if reasoningreplay.Eligible("claude-3", []string{"openrouter"}) {
		t.Fatal("unmatched model must not be eligible")
	}
}

func TestCatalogVersion_isV2(t *testing.T) {
	t.Parallel()
	v := strings.TrimSpace(reasoningreplay.CatalogVersion)
	if v == "" || v == "kimi-moonshot.v1" {
		t.Fatalf("CatalogVersion=%q want v2 identifier", v)
	}
	if !strings.Contains(v, "v2") {
		t.Fatalf("CatalogVersion=%q must advertise v2", v)
	}
}

func TestBackendPrefixes_stableOpenAICompatibleSet(t *testing.T) {
	t.Parallel()
	want := []string{
		"openrouter", "openai-legacy", "openai-responses", "nvidia", "huggingface",
		"ollama", "ollama-cloud", "vllm", "lmstudio", "llamacpp", "opencode-go", "opencode-zen",
	}
	got := reasoningreplay.BackendPrefixes()
	set := map[string]struct{}{}
	for _, p := range got {
		set[p] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			t.Fatalf("missing prefix %q in %v", w, got)
		}
	}
}
