package catalog

import (
	"testing"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
)

func testVendorCatalogIndex() *vendor.SnapshotIndex {
	return vendor.NewSnapshotIndex(map[string]vendor.ModelFacts{
		"xiaomi/mimo-v2.5":           {Source: vendor.FactSourceCatalog},
		"tencent/hy3-preview":        {Source: vendor.FactSourceCatalog},
		"alibaba/qwen3.7-plus":       {Source: vendor.FactSourceCatalog},
		"moonshotai/kimi-k2.7-code":  {Source: vendor.FactSourceCatalog},
		"z-ai/glm-5.2":               {Source: vendor.FactSourceCatalog},
		"minimax/minimax-m3":         {Source: vendor.FactSourceCatalog},
		"deepseek/deepseek-v4-flash": {Source: vendor.FactSourceCatalog},
	})
}

func testKeywordFallbackResolver() vendor.VendorResolver {
	return vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{Index: nil}, true)
}

func TestCanonicalizer_vendorResolverCatalogMappings(t *testing.T) {
	t.Parallel()

	idx := testVendorCatalogIndex()
	c := NewCanonicalizer(vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{Index: idx}, true))

	cases := []struct {
		raw  string
		want string
	}{
		{"mimo-v2.5", "xiaomi/mimo-v2.5"},
		{"hy3-preview", "tencent/hy3-preview"},
		{"qwen3.7-plus", "alibaba/qwen3.7-plus"},
		{"qwen/qwen3.7-plus", "alibaba/qwen3.7-plus"},
		{"kimi-k2.7-code", "moonshotai/kimi-k2.7-code"},
		{"kimi-k2.7", "moonshotai/kimi-k2.7-code"},
		{"glm-5.2", "z-ai/glm-5.2"},
		{"minimax-m3", "minimax/minimax-m3"},
		{"deepseek-v4-flash", "deepseek/deepseek-v4-flash"},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			if got := c.CanonicalID(tc.raw); got != tc.want {
				t.Fatalf("CanonicalID(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCanonicalizer_keywordFallbackWhenCatalogMisses(t *testing.T) {
	t.Parallel()

	c := NewCanonicalizer(vendor.NewOpenCodeVendorResolver(
		vendor.StaticActiveSnapshotProvider{Index: testVendorCatalogIndex()},
		true,
	))
	cases := []struct {
		raw  string
		want string
	}{
		{"gemini-3.1-pro", "google/gemini-3.1-pro"},
		{"gpt-5.4", "openai/gpt-5.4"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			if got := c.CanonicalID(tc.raw); got != tc.want {
				t.Fatalf("CanonicalID(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCanonicalizer_unresolvedDoesNotInventOpenCodeVendor(t *testing.T) {
	t.Parallel()

	c := NewCanonicalizer(vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{Index: nil}, false))
	if got := c.CanonicalID("unknown-widget-v9"); got != "unknown/unknown-widget-v9" {
		t.Fatalf("CanonicalID = %q, want unknown/unknown-widget-v9", got)
	}
}

func TestCanonicalizer_openCodeGoCurrentInventoryHasNoUnknownVendorFallbacks(t *testing.T) {
	t.Parallel()

	c := NewCanonicalizer(vendor.NewOpenCodeVendorResolver(
		vendor.StaticActiveSnapshotProvider{Index: testVendorCatalogIndex()},
		true,
	))
	rawModels := []string{
		"minimax-m3",
		"minimax-m2.7",
		"minimax-m2.5",
		"kimi-k2.7-code",
		"kimi-k2.6",
		"kimi-k2.5",
		"glm-5.2",
		"glm-5.1",
		"glm-5",
		"deepseek-v4-pro",
		"deepseek-v4-flash",
		"qwen3.7-max",
		"qwen3.7-plus",
		"qwen3.6-plus",
		"qwen3.5-plus",
		"mimo-v2-pro",
		"mimo-v2-omni",
		"mimo-v2.5-pro",
		"mimo-v2.5",
		"hy3-preview",
	}
	for _, raw := range rawModels {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			got := c.CanonicalID(raw)
			if got == "opencode/"+raw {
				t.Fatalf("CanonicalID(%q) fell back to unknown opencode vendor", raw)
			}
		})
	}
}

func TestInventoryModels_vendorResolverCatalog(t *testing.T) {
	t.Parallel()

	idx := testVendorCatalogIndex()
	resolver := vendor.NewOpenCodeVendorResolver(vendor.StaticActiveSnapshotProvider{Index: idx}, true)
	entries := []ModelEntry{
		{RawID: "mimo-v2.5"},
		{RawID: "hy3-preview"},
		{RawID: "qwen3.7-plus"},
	}
	models := InventoryModels(BackendGo, entries, resolver)
	want := map[string]string{
		"mimo-v2.5":    "xiaomi/mimo-v2.5",
		"hy3-preview":  "tencent/hy3-preview",
		"qwen3.7-plus": "alibaba/qwen3.7-plus",
	}
	if len(models) != len(want) {
		t.Fatalf("models = %+v", models)
	}
	for _, m := range models {
		canonical, ok := want[m.NativeID]
		if !ok {
			t.Fatalf("unexpected model %+v", m)
		}
		if m.CanonicalID != canonical {
			t.Fatalf("model %q canonical = %q, want %q", m.NativeID, m.CanonicalID, canonical)
		}
		delete(want, m.NativeID)
	}
	if len(want) != 0 {
		t.Fatalf("missing models: %+v", want)
	}
}
