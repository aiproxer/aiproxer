package engine_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestDetectKnownPublicPrefix_longestWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value string
		want  string
	}{
		{value: testkit.SyntheticOpenAIAPIKey, want: "sk-"},
		{value: testkit.SyntheticOpenRouterAPIKey, want: "sk-or-"},
		{value: testkit.SyntheticAnthropicSecretGuardKey, want: "sk-ant-"},
		{value: "ghp_abcdefghijklmnopqrstuvwxyz012345", want: "ghp_"},
		{value: "plain-secret-value-without-prefix", want: ""},
		{value: "sk-", want: ""}, // not a strict prefix of a longer secret
	}
	for _, tc := range cases {
		got := engine.DetectKnownPublicPrefixForTest(tc.value)
		if got != tc.want {
			t.Fatalf("value=%q got %q want %q", tc.value, got, tc.want)
		}
	}
}

func TestCatalogInventory_attachesKnownPrefix(t *testing.T) {
	t.Parallel()
	env := mapEnv{
		"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
	}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: false,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := src.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	redacted, findings, err := m.RedactString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings: %d", len(findings))
	}
	if redacted == testkit.SyntheticOpenAIAPIKey {
		t.Fatal("expected redaction")
	}
	if len(redacted) != len(testkit.SyntheticOpenAIAPIKey) {
		t.Fatalf("len=%d want %d", len(redacted), len(testkit.SyntheticOpenAIAPIKey))
	}
	if redacted[:3] != "sk-" {
		t.Fatalf("expected preserved sk- prefix, got %q", redacted[:3])
	}
	if findings[0].SourceCategory != sdk.SourceCategoryProxyEnv {
		t.Fatalf("category: %q", findings[0].SourceCategory)
	}
}

type mapEnv map[string]string

func (e mapEnv) Lookup(name string) (string, bool) {
	v, ok := e[name]
	return v, ok
}

func (e mapEnv) Snapshot() []string {
	out := make([]string, 0, len(e))
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	return out
}
