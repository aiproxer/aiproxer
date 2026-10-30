package catalog_test

import (
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
)

// sampleRawCatalog is a minimal `codex debug models` payload covering the
// routable/CLI-only/hidden edge cases.
func sampleRawCatalog() []byte {
	return []byte(`{
  "models": [
    {
      "slug": "gpt-5.6-sol",
      "default_reasoning_level": "low",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"},
        {"effort": "max", "description": "d"},
        {"effort": "ultra", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": true,
		"context_window": 372000,
		"max_context_window": 372000,
		"auto_compact_token_limit": 300000,
		"comp_hash": "sol-hash"
    },
    {
      "slug": "gpt-5.6-luna",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"},
        {"effort": "max", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": true,
      "context_window": 372000,
      "max_context_window": 372000
    },
    {
      "slug": "gpt-5.5",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": true,
      "context_window": 272000,
      "max_context_window": 272000
    },
    {
      "slug": "gpt-5.3-codex-spark",
      "default_reasoning_level": "high",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"}
      ],
      "visibility": "list",
      "supported_in_api": false,
      "context_window": 128000,
      "max_context_window": 128000
    },
    {
      "slug": "codex-auto-review",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "d"},
        {"effort": "medium", "description": "d"},
        {"effort": "high", "description": "d"},
        {"effort": "xhigh", "description": "d"}
      ],
      "visibility": "hide",
      "supported_in_api": true,
      "context_window": 272000,
      "max_context_window": 1000000
    }
  ]
}`)
}

func TestParse_RoutableSlugsExcludeCLIOnlyAndHidden(t *testing.T) {
	t.Parallel()
	c, err := catalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5"}
	got := c.RoutableSlugs()
	if !slices.Equal(got, want) {
		t.Fatalf("RoutableSlugs = %v, want %v", got, want)
	}
}

func TestParse_EffortOrderDerivedFromWidestModel(t *testing.T) {
	t.Parallel()
	c, err := catalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	if !slices.Equal(c.ReasoningEffortOrder(), want) {
		t.Fatalf("ReasoningEffortOrder = %v, want %v", c.ReasoningEffortOrder(), want)
	}
	if c.DefaultReasoningEffort() != "medium" {
		t.Fatalf("DefaultReasoningEffort = %q, want %q", c.DefaultReasoningEffort(), "medium")
	}
}

func TestParse_IsSupported(t *testing.T) {
	t.Parallel()
	c, err := catalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, slug := range []string{"gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.5", "GPT-5.6-SOL"} {
		if !c.IsSupported(slug) {
			t.Fatalf("IsSupported(%q) = false, want true", slug)
		}
	}
	for _, slug := range []string{"gpt-5.3-codex-spark", "codex-auto-review", "gpt-5.1-codex", "claude-3"} {
		if c.IsSupported(slug) {
			t.Fatalf("IsSupported(%q) = true, want false", slug)
		}
	}
}

func TestParse_ProfilePerModel(t *testing.T) {
	t.Parallel()
	c, err := catalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sol, ok := c.Profile("gpt-5.6-sol")
	if !ok {
		t.Fatal("Profile(gpt-5.6-sol) not found")
	}
	if sol.DefaultReasoningLevel != "low" {
		t.Fatalf("DefaultReasoningLevel = %q, want %q", sol.DefaultReasoningLevel, "low")
	}
	if !sol.APIAccepted() {
		t.Fatal("APIAccepted = false, want true")
	}
	if sol.ContextWindow != 372000 {
		t.Fatalf("ContextWindow = %d, want 372000", sol.ContextWindow)
	}
	spark, ok := c.Profile("gpt-5.3-codex-spark")
	if !ok {
		t.Fatal("Profile(gpt-5.3-codex-spark) not found")
	}
	if spark.APIAccepted() {
		t.Fatal("spark APIAccepted = true, want false (CLI-only)")
	}
}

func TestParse_ModelsSupporting(t *testing.T) {
	t.Parallel()
	c, err := catalog.Parse(sampleRawCatalog())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.ModelsSupporting("ultra"); !slices.Equal(got, []string{"gpt-5.6-sol"}) {
		t.Fatalf("ModelsSupporting(ultra) = %v, want [gpt-5.6-sol]", got)
	}
	if got := c.ModelsSupporting("max"); !slices.Equal(got, []string{"gpt-5.6-sol", "gpt-5.6-luna"}) {
		t.Fatalf("ModelsSupporting(max) = %v, want [gpt-5.6-sol gpt-5.6-luna]", got)
	}
}

func TestParse_SkipsMalformedEntries(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"models":[
		{"default_reasoning_level":"medium"},
		"not-an-object",
		{"slug":"gpt-5.5","supported_reasoning_levels":[{"description":"no effort"}]},
		{"slug":"gpt-5.6-sol","supported_reasoning_levels":[{"effort":"low","description":"d"}],"supported_in_api":true,"visibility":"list"}
	]}`)
	c, err := catalog.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.RoutableSlugs(); !slices.Equal(got, []string{"gpt-5.6-sol"}) {
		t.Fatalf("RoutableSlugs = %v, want [gpt-5.6-sol]", got)
	}
}

func TestParse_EmptyAndMissingModels(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{[]byte(`{"models":[]}`), []byte(`{}`)} {
		c, err := catalog.Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if len(c.RoutableSlugs()) != 0 {
			t.Fatalf("RoutableSlugs = %v, want empty", c.RoutableSlugs())
		}
	}
}

func TestParse_InvalidJSONReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := catalog.Parse([]byte("not json {")); err == nil {
		t.Fatal("Parse(invalid) = nil error, want error")
	}
}

func TestParse_DefaultsVisibilityAndSupportedInAPI(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"models":[{"slug":"gpt-5.5","supported_reasoning_levels":[{"effort":"low","description":"d"}]}]}`)
	c, err := catalog.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p, ok := c.Profile("gpt-5.5")
	if !ok {
		t.Fatal("Profile(gpt-5.5) not found")
	}
	if p.Visibility != "list" {
		t.Fatalf("Visibility = %q, want %q", p.Visibility, "list")
	}
	if !p.SupportedInAPI {
		t.Fatal("SupportedInAPI = false, want true (default)")
	}
	if p.DefaultReasoningLevel != "medium" {
		t.Fatalf("DefaultReasoningLevel = %q, want %q", p.DefaultReasoningLevel, "medium")
	}
}

func TestParse_PreservesNativeCompactionMetadata(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"models":[
		{"slug":"discovered","default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high"}],"context_window":100,"max_context_window":200,"auto_compact_token_limit":150,"comp_hash":"h1"},
		{"slug":"string-values","supported_reasoning_levels":[{"effort":"low"}],"context_window":"bad","max_context_window":"bad","auto_compact_token_limit":"bad","comp_hash":42}
	]}`)
	c, err := catalog.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := c.Profile("discovered")
	if !ok {
		t.Fatal("discovered profile missing")
	}
	if p.AutoCompactTokenLimit != 150 || p.CompHash != "h1" {
		t.Fatalf("native metadata = limit %d hash %q", p.AutoCompactTokenLimit, p.CompHash)
	}
	if bad, ok := c.Profile("string-values"); !ok || bad.AutoCompactTokenLimit != 0 || bad.CompHash != "" || bad.ContextWindow != 0 || bad.MaxContextWindow != 0 {
		t.Fatalf("malformed native metadata was not safely ignored: %+v, %v", bad, ok)
	}
}

func TestLoadFallback_PreservesNativeCompactionMetadata(t *testing.T) {
	t.Parallel()
	c, err := catalog.LoadFallback("")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := c.Profile("gpt-5.5")
	if !ok {
		t.Fatal("fallback gpt-5.5 profile missing")
	}
	if p.CompHash != "" || p.AutoCompactTokenLimit != 0 {
		t.Fatalf("fallback contains unsupported native metadata: %+v", p)
	}
}

func TestParse_SkipsDuplicateSlugs(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
  "models": [
    {"slug":"gpt-5.5","supported_in_api":true,"supported_reasoning_levels":[{"effort":"low","description":"d"}]},
    {"slug":"GPT-5.5","supported_in_api":true,"default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"high","description":"d"}]}
  ]
}`)
	c, err := catalog.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	slugs := c.RoutableSlugs()
	if len(slugs) != 1 || slugs[0] != "gpt-5.5" {
		t.Fatalf("RoutableSlugs = %v, want [gpt-5.5] (first wins)", slugs)
	}
	p, ok := c.Profile("gpt-5.5")
	if !ok {
		t.Fatal("Profile(gpt-5.5) not found")
	}
	if p.DefaultReasoningLevel != "medium" {
		t.Fatalf("DefaultReasoningLevel = %q, want medium from first entry default", p.DefaultReasoningLevel)
	}
}
