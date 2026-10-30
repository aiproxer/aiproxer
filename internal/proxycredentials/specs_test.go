package proxycredentials_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/internal/proxycredentials"
)

func TestMatchProxyCredentialName_exactAndNumbered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		ok   bool
	}{
		{"OPENAI_API_KEY", "OPENAI_API_KEY", true},
		{"OPENAI_API_KEY_2", "OPENAI_API_KEY", true},
		{"OPENAI_API_KEY_7", "OPENAI_API_KEY", true},
		{"OPENAI_API_KEY_01", "OPENAI_API_KEY", true},
		{"ANTHROPIC_API_KEY_99", "ANTHROPIC_API_KEY", true},
		{"OPENCODE_GO_API_KEY_3", "OPENCODE_GO_API_KEY", true},
		{"OPENCODE_API_KEY_2", "OPENCODE_API_KEY", true},
		{"OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_ACCESS_TOKEN", true},
		{"OPENAI_CODEX_API_KEY_4", "OPENAI_CODEX_API_KEY", true},
		{"OPENAI_API_KEY_", "", false},
		{"OPENAI_API_KEY_x", "", false},
		{"AWS_SECRET_ACCESS_KEY", "", false},
		{"", "", false},
		{"NOT_A_KEY", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			base, ok := proxycredentials.MatchProxyCredentialName(tc.name)
			if ok != tc.ok || base != tc.base {
				t.Fatalf("MatchProxyCredentialName(%q)=(%q,%v) want (%q,%v)", tc.name, base, ok, tc.base, tc.ok)
			}
		})
	}
}

func TestBaseNames_includesRequiredSpecs(t *testing.T) {
	t.Parallel()
	want := []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"OPENROUTER_API_KEY",
		"NVIDIA_API_KEY",
		"HUGGINGFACE_API_KEY",
		"OPENCODE_GO_API_KEY",
		"OPENCODE_API_KEY",
		"OPENCODE_ZEN_API_KEY",
		"OPENAI_CODEX_ACCESS_TOKEN",
		"OPENAI_CODEX_API_KEY",
	}
	got := proxycredentials.BaseNames()
	if len(got) != len(want) {
		t.Fatalf("BaseNames len=%d want %d", len(got), len(want))
	}
	for _, n := range want {
		if !slices.Contains(got, n) {
			t.Fatalf("BaseNames missing %q", n)
		}
	}
}
