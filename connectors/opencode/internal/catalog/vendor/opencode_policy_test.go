package vendor

import "testing"

func openCodeTestVendorIndex() *SnapshotIndex {
	return NewSnapshotIndex(map[string]ModelFacts{
		"anthropic/claude-opus-4-7": {Source: FactSourceCatalog},
		"xiaomi/mimo-v2.5":          {Source: FactSourceCatalog},
		"alibaba/qwen3.7-plus":      {Source: FactSourceCatalog},
		"alibaba/qwen3.6-plus":      {Source: FactSourceCatalog},
		"deepseek/deepseek-chat":    {Source: FactSourceCatalog},
		"meta-llama/llama3.3":       {Source: FactSourceCatalog},
		"vendor/model-free":         {Source: FactSourceCatalog},
		"vendor/model":              {Source: FactSourceCatalog},
		"other/model":               {Source: FactSourceCatalog},
		"anthropic/claude-sonnet-4": {Source: FactSourceCatalog},
		"amazon/claude-sonnet-4":    {Source: FactSourceCatalog},
	})
}

func TestOpenCodeVendorResolver_exactCatalogIDWins(t *testing.T) {
	t.Parallel()
	idx := openCodeTestVendorIndex()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	got := r.Resolve("alibaba/qwen3.7-plus")
	if got.Kind != VendorResolveExact {
		t.Fatalf("Kind = %v want exact", got.Kind)
	}
	if got.CanonicalID != "alibaba/qwen3.7-plus" {
		t.Fatalf("CanonicalID = %q", got.CanonicalID)
	}
}

func TestOpenCodeVendorResolver_dashedCatalogMatchPreservesCallerSuffix(t *testing.T) {
	t.Parallel()
	idx := openCodeTestVendorIndex()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	got := r.Resolve("claude-opus-4.7")
	if got.Kind != VendorResolveCatalogSuffix {
		t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
	}
	if got.CanonicalID != "anthropic/claude-opus-4.7" {
		t.Fatalf("CanonicalID = %q want anthropic/claude-opus-4.7", got.CanonicalID)
	}
	if got.MatchedCatalog != "anthropic/claude-opus-4-7" {
		t.Fatalf("MatchedCatalog = %q", got.MatchedCatalog)
	}
}

func TestOpenCodeVendorResolver_stripProviderPrefix(t *testing.T) {
	t.Parallel()
	idx := openCodeTestVendorIndex()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	got := r.Resolve("opencode/mimo-v2.5")
	if got.Kind != VendorResolveCatalogSuffix {
		t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
	}
	if got.CanonicalID != "xiaomi/mimo-v2.5" {
		t.Fatalf("CanonicalID = %q want xiaomi/mimo-v2.5", got.CanonicalID)
	}
}

func TestOpenCodeVendorResolver_vendorAliasPrefersRouteModel(t *testing.T) {
	t.Parallel()
	idx := openCodeTestVendorIndex()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	got := r.Resolve("qwen/qwen3.7-plus")
	if got.Kind != VendorResolveVendorAlias {
		t.Fatalf("Kind = %v want vendor_alias", got.Kind)
	}
	if got.CanonicalID != "alibaba/qwen3.7-plus" {
		t.Fatalf("CanonicalID = %q want alibaba/qwen3.7-plus", got.CanonicalID)
	}
	if got.RouteModel != "qwen/qwen3.7-plus" {
		t.Fatalf("RouteModel = %q want qwen/qwen3.7-plus", got.RouteModel)
	}
	if got.CatalogVendor != "alibaba" {
		t.Fatalf("CatalogVendor = %q", got.CatalogVendor)
	}
}

func TestOpenCodeVendorResolver_ambiguousSuffix(t *testing.T) {
	t.Parallel()
	idx := openCodeTestVendorIndex()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	got := r.Resolve("claude-sonnet-4")
	if got.Kind != VendorResolveAmbiguous {
		t.Fatalf("Kind = %v want ambiguous", got.Kind)
	}
	want := []string{"amazon/claude-sonnet-4", "anthropic/claude-sonnet-4"}
	if len(got.Candidates) != len(want) {
		t.Fatalf("Candidates = %v", got.Candidates)
	}
	for i := range want {
		if got.Candidates[i] != want[i] {
			t.Fatalf("Candidates[%d] = %q want %q", i, got.Candidates[i], want[i])
		}
	}
}

func TestOpenCodeVendorResolver_catalogSuffixStripsProviderDecoratorsForLookupOnly(t *testing.T) {
	t.Parallel()
	idx := openCodeTestVendorIndex()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	cases := []struct {
		name      string
		input     string
		canonical string
		matched   string
	}{
		{name: "dash free", input: "qwen3.6-plus-free", canonical: "alibaba/qwen3.6-plus-free", matched: "alibaba/qwen3.6-plus"},
		{name: "colon free", input: "deepseek-chat:free", canonical: "deepseek/deepseek-chat:free", matched: "deepseek/deepseek-chat"},
		{name: "dash cloud", input: "llama3.3-cloud", canonical: "meta-llama/llama3.3-cloud", matched: "meta-llama/llama3.3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := r.Resolve(tc.input)
			if got.Kind != VendorResolveCatalogSuffix {
				t.Fatalf("Kind = %v want catalog_suffix", got.Kind)
			}
			if got.CanonicalID != tc.canonical {
				t.Fatalf("CanonicalID = %q want %q", got.CanonicalID, tc.canonical)
			}
			if got.MatchedCatalog != tc.matched {
				t.Fatalf("MatchedCatalog = %q want %q", got.MatchedCatalog, tc.matched)
			}
		})
	}
}

func TestOpenCodeVendorResolver_noSnapshotKeywordFallbackGPT(t *testing.T) {
	t.Parallel()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: nil}, true)
	got := r.Resolve("gpt-5.4")
	if got.Kind != VendorResolveKeywordFallback {
		t.Fatalf("Kind = %v want keyword_fallback", got.Kind)
	}
	if got.CanonicalID != "openai/gpt-5.4" {
		t.Fatalf("CanonicalID = %q want openai/gpt-5.4", got.CanonicalID)
	}
	if got.CatalogVendor != "openai" {
		t.Fatalf("CatalogVendor = %q want openai", got.CatalogVendor)
	}
}

func TestOpenCodeVendorResolver_noSnapshotKeywordFallbackClaude(t *testing.T) {
	t.Parallel()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: nil}, true)
	got := r.Resolve("claude-sonnet-4")
	if got.Kind != VendorResolveKeywordFallback {
		t.Fatalf("Kind = %v want keyword_fallback", got.Kind)
	}
	if got.CanonicalID != "anthropic/claude-sonnet-4" {
		t.Fatalf("CanonicalID = %q want anthropic/claude-sonnet-4", got.CanonicalID)
	}
}

func TestOpenCodeVendorResolver_noSnapshotKeywordFallbackQwenUsesCatalogVendor(t *testing.T) {
	t.Parallel()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: nil}, true)
	got := r.Resolve("qwen3.6-plus-free")
	if got.Kind != VendorResolveKeywordFallback {
		t.Fatalf("Kind = %v want keyword_fallback", got.Kind)
	}
	if got.CanonicalID != "alibaba/qwen3.6-plus-free" {
		t.Fatalf("CanonicalID = %q want alibaba/qwen3.6-plus-free", got.CanonicalID)
	}
	if got.CatalogVendor != "alibaba" {
		t.Fatalf("CatalogVendor = %q want alibaba", got.CatalogVendor)
	}
}

func TestOpenCodeVendorResolver_keywordFallbackDoesNotMatchMidToken(t *testing.T) {
	t.Parallel()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: nil}, true)
	for _, input := range []string{
		"internal-kimi-proxy",
		"legacyglm-wrapper",
		"notgpt-model",
		"vendor/qwenish-alias",
	} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got := r.Resolve(input)
			if got.Kind != VendorResolveNoMatch {
				t.Fatalf("Kind = %v want no_match for %q with canonical %q", got.Kind, input, got.CanonicalID)
			}
		})
	}
}

func TestOpenCodeVendorResolver_vendorAliasMappings(t *testing.T) {
	t.Parallel()
	idx := NewSnapshotIndex(map[string]ModelFacts{
		"zhipuai/glm-5.2":      {Source: FactSourceCatalog},
		"moonshotai/kimi-k2.7": {Source: FactSourceCatalog},
	})
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: idx}, true)
	cases := []struct {
		input string
		want  string
	}{
		{"z-ai/glm-5.2", "zhipuai/glm-5.2"},
		{"zai/glm-5.2", "zhipuai/glm-5.2"},
		{"zai-org/glm-5.2", "zhipuai/glm-5.2"},
		{"zhipu/glm-5.2", "zhipuai/glm-5.2"},
		{"moonshot/kimi-k2.7", "moonshotai/kimi-k2.7"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := r.Resolve(tc.input)
			if got.Kind != VendorResolveVendorAlias {
				t.Fatalf("Kind = %v want vendor_alias", got.Kind)
			}
			if got.CanonicalID != tc.want {
				t.Fatalf("CanonicalID = %q want %q", got.CanonicalID, tc.want)
			}
		})
	}
}

func TestOpenCodeSuffixLookupVariants(t *testing.T) {
	t.Parallel()
	got := OpenCodeSuffixLookupVariants("qwen3.6-plus-free")
	want := []string{"qwen3.6-plus-free", "qwen3.6-plus"}
	if len(got) != len(want) {
		t.Fatalf("variants = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("variants[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

func TestOpenCodeVendorResolver_omenKeywordFallback(t *testing.T) {
	t.Parallel()
	r := NewOpenCodeVendorResolver(StaticActiveSnapshotProvider{Index: nil}, true)
	got := r.Resolve("omen-alpha")
	if got.Kind != VendorResolveKeywordFallback {
		t.Fatalf("Kind = %v want keyword_fallback", got.Kind)
	}
	if got.CanonicalID != "opencode/omen-alpha" {
		t.Fatalf("CanonicalID = %q want opencode/omen-alpha", got.CanonicalID)
	}
	if got.CatalogVendor != "opencode" {
		t.Fatalf("CatalogVendor = %q want opencode", got.CatalogVendor)
	}
}
