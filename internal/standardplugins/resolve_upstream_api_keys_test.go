package standardplugins

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func clearNumberedEnv(t *testing.T, prefix string) {
	t.Helper()
	t.Setenv(prefix, "")
	for i := 1; i <= config.MaxUpstreamMaxEnvIndex; i++ {
		t.Setenv(fmt.Sprintf("%s_%d", prefix, i), "")
	}
}

func clearAllProviderEnv(t *testing.T) {
	t.Helper()
	for _, prefix := range []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY",
		"OPENROUTER_API_KEY", "NVIDIA_API_KEY", "HUGGINGFACE_API_KEY",
		"OPENCODE_GO_API_KEY", "OPENCODE_API_KEY", "OPENCODE_ZEN_API_KEY",
		"OPENAI_CODEX_ACCESS_TOKEN", "OPENAI_CODEX_API_KEY",
	} {
		t.Setenv(prefix, "")
		clearNumberedEnv(t, prefix)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_numberedOpenAI(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "k1")
	t.Setenv("OPENAI_API_KEY_2", "k2")
	t.Setenv("OPENAI_API_KEY_3", "k3")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"k1", "k2", "k3"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v", got.OpenAI, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_scansThroughGaps(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "a")
	t.Setenv("OPENAI_API_KEY_2", "")
	t.Setenv("OPENAI_API_KEY_3", "c")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"a", "c"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v (sparse scan must not stop at gaps)", got.OpenAI, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_secondaryWithoutPrimary(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY_2", "only2")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"only2"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v", got.OpenAI, want)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_anthropicAndGemini(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("ANTHROPIC_API_KEY", "a1")
	t.Setenv("ANTHROPIC_API_KEY_2", "a2")
	t.Setenv("GEMINI_API_KEY", "g1")
	got := ResolveUpstreamAPIKeysFromEnv()
	if !reflect.DeepEqual(got.Anthropic, []string{"a1", "a2"}) {
		t.Fatalf("Anthropic: %#v", got.Anthropic)
	}
	if !reflect.DeepEqual(got.Gemini, []string{"g1"}) {
		t.Fatalf("Gemini: %#v", got.Gemini)
	}
}

func TestResolveUpstreamAPIKeysFromEnv_deduplicatesEqualSecrets(t *testing.T) {
	clearNumberedEnv(t, "OPENAI_API_KEY")
	clearNumberedEnv(t, "ANTHROPIC_API_KEY")
	clearNumberedEnv(t, "GEMINI_API_KEY")
	t.Setenv("OPENAI_API_KEY", "dup")
	t.Setenv("OPENAI_API_KEY_2", "dup")
	t.Setenv("OPENAI_API_KEY_3", "next")
	got := ResolveUpstreamAPIKeysFromEnv()
	want := []string{"dup", "next"}
	if !reflect.DeepEqual(got.OpenAI, want) {
		t.Fatalf("OpenAI keys: %#v want %#v", got.OpenAI, want)
	}
}

// TestUpstreamAPIKeys_envOnlyNoYAMLPrecedence documents the requirement 2.1
// removal: factory-bound keys come from the host snapshot only. There is no
// YAML side to prefer; the bound slice is used verbatim.
func TestUpstreamAPIKeys_envOnlyNoYAMLPrecedence(t *testing.T) {
	t.Parallel()
	keys := UpstreamAPIKeys{OpenAI: []string{"env1", "env2"}}
	if got := firstResolvedAPIKey(keys.OpenAI); got != "env1" {
		t.Fatalf("primary = %q, want env1", got)
	}
}
