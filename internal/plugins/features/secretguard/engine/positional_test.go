package engine

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestMatcher_ScanOccurrencesReturnsDeterministicSafeSpans(t *testing.T) {
	t.Parallel()

	cat, err := BuildCatalog([]CatalogInput{{
		Name:           "OPENAI_API_KEY",
		Aliases:        []string{"API_KEY"},
		Value:          testkit.SyntheticOpenAIAPIKey,
		SourceCategory: sdk.SourceCategoryProxyEnv,
	}}, 8)
	if err != nil {
		t.Fatal(err)
	}
	matcher := NewMatcher(cat)
	prefix := "before "
	input := []byte(prefix + testkit.SyntheticOpenAIAPIKey + " after")
	got := matcher.ScanOccurrences(input)
	if len(got) != 1 {
		t.Fatalf("occurrences = %d, want one", len(got))
	}
	if got[0].Start != len(prefix) || got[0].End != len(prefix)+len(testkit.SyntheticOpenAIAPIKey) {
		t.Fatalf("span = %d..%d, want %d..%d", got[0].Start, got[0].End, len(prefix), len(prefix)+len(testkit.SyntheticOpenAIAPIKey))
	}
	if got[0].SecretRefName != "API_KEY" || got[0].SourceCategory != sdk.SourceCategoryProxyEnv {
		t.Fatalf("safe attribution = %#v", got[0])
	}
	if len(got[0].Aliases) != 1 || got[0].Aliases[0] != "OPENAI_API_KEY" {
		t.Fatalf("safe aliases = %#v", got[0].Aliases)
	}
}
