package catalog

import (
	"errors"
	"strings"
	"testing"
)

func TestModelCatalog_resolveCanonicalAndNative(t *testing.T) {
	t.Parallel()

	catalog := NewModelCatalog(BackendGo, []ModelEntry{
		{RawID: "kimi-k2.7-code"},
		{RawID: "minimax-m3", Endpoint: "https://example.test/v1/messages", AISDKPackage: "@ai-sdk/anthropic"},
	}, testKeywordFallbackResolver())

	res, err := catalog.Resolve("moonshotai/kimi-k2.7-code")
	if err != nil {
		t.Fatal(err)
	}
	if res.WireModel != "kimi-k2.7-code" || res.Flavor != FlavorOpenAIChat {
		t.Fatalf("resolve canonical = %+v", res)
	}

	res, err = catalog.Resolve("opencode-go/kimi-k2.7-code")
	if err != nil {
		t.Fatal(err)
	}
	if res.WireModel != "kimi-k2.7-code" {
		t.Fatalf("resolve native = %+v", res)
	}

	res, err = catalog.Resolve("minimax/minimax-m3")
	if err != nil {
		t.Fatal(err)
	}
	if res.Flavor != FlavorAnthropicMessages {
		t.Fatalf("minimax flavor = %q", res.Flavor)
	}
}

func TestModelCatalog_unknownModelFailsExplicitly(t *testing.T) {
	t.Parallel()

	catalog := NewModelCatalog(BackendZen, []ModelEntry{{RawID: "gpt-5.4"}}, testKeywordFallbackResolver())
	_, err := catalog.Resolve("unknown/vendor-model")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnknownModel) && !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("err = %v", err)
	}
}

func TestModelCatalog_omenAlphaStealthModel(t *testing.T) {
	t.Parallel()

	catalog := NewModelCatalog(BackendGo, nil, testKeywordFallbackResolver())

	res, err := catalog.Resolve("omen-alpha")
	if err != nil {
		t.Fatalf("resolve omen-alpha: %v", err)
	}
	if res.WireModel != "omen-alpha" || res.Flavor != FlavorOpenAIChat {
		t.Fatalf("omen-alpha = %+v", res)
	}

	res, err = catalog.Resolve("opencode-go/omen-alpha")
	if err != nil {
		t.Fatalf("resolve opencode-go/omen-alpha: %v", err)
	}
	if res.WireModel != "omen-alpha" || res.Flavor != FlavorOpenAIChat {
		t.Fatalf("opencode-go/omen-alpha = %+v", res)
	}
}

func TestModelCatalog_legacyAliasMapping(t *testing.T) {
	t.Parallel()

	catalog := NewModelCatalog(BackendGo, []ModelEntry{
		{RawID: "kimi-k2.7-code"},
	}, testKeywordFallbackResolver())

	res, err := catalog.Resolve("opencode-go/kimi-k2.7")
	if err != nil {
		t.Fatalf("resolve kimi-k2.7: %v", err)
	}
	if res.WireModel != "kimi-k2.7-code" {
		t.Fatalf("wireModel = %q want kimi-k2.7-code", res.WireModel)
	}
}
