package vendor

import (
	"strings"
	"unicode"
)

var openCodeCatalogVendorAliases = map[string]string{
	"qwen":     "alibaba",
	"z-ai":     "zhipuai",
	"zai":      "zhipuai",
	"zai-org":  "zhipuai",
	"zhipu":    "zhipuai",
	"moonshot": "moonshotai",
}

type openCodeKeywordRule struct {
	keyword string
	vendor  string
}

var openCodeVendorKeywordRules = []openCodeKeywordRule{
	{keyword: "gpt", vendor: "openai"},
	{keyword: "claude", vendor: "anthropic"},
	{keyword: "gemini", vendor: "google"},
	{keyword: "gemma", vendor: "google"},
	{keyword: "kimi", vendor: "moonshotai"},
	{keyword: "glm", vendor: "z-ai"},
	{keyword: "qwen", vendor: "alibaba"},
	{keyword: "deepseek", vendor: "deepseek"},
	{keyword: "minimax", vendor: "minimax"},
	{keyword: "mimo", vendor: "xiaomi"},
	{keyword: "hy3", vendor: "tencent"},
	{keyword: "omen", vendor: "opencode"},
}

func OpenCodeVendorPolicy() VendorPolicy {
	return VendorPolicy{
		MapVendor:            openCodeMapVendor,
		SuffixLookupVariants: OpenCodeSuffixLookupVariants,
		KeywordFallback:      openCodeKeywordFallbackCanonical,
	}
}

func NewOpenCodeVendorResolver(active ActiveSnapshotProvider, keywordFallback bool) *DefaultVendorResolver {
	return NewVendorResolver(active, keywordFallback, OpenCodeVendorPolicy())
}

func openCodeMapVendor(vendor string) string {
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	if vendor == "" {
		return ""
	}
	if mapped, ok := openCodeCatalogVendorAliases[vendor]; ok {
		return mapped
	}
	return vendor
}

func OpenCodeSuffixLookupVariants(suffix string) []string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return nil
	}
	seen := map[string]struct{}{suffix: {}}
	variants := []string{suffix}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		variants = append(variants, v)
	}
	lower := strings.ToLower(suffix)
	for _, marker := range []string{":free", "-free", "-cloud"} {
		if strings.HasSuffix(lower, marker) {
			add(suffix[:len(suffix)-len(marker)])
		}
	}
	return variants
}

func openCodeKeywordFallbackCanonical(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	suffix := NormalizeStripOneProviderPrefix(model)
	for _, variant := range OpenCodeSuffixLookupVariants(suffix) {
		lower := strings.ToLower(variant)
		for _, rule := range openCodeVendorKeywordRules {
			if openCodeKeywordRuleMatchesPrefix(lower, rule.keyword) {
				return rule.vendor + "/" + suffix, true
			}
		}
	}
	return "", false
}

func openCodeKeywordRuleMatchesPrefix(model, keyword string) bool {
	if !strings.HasPrefix(model, keyword) {
		return false
	}
	if len(model) == len(keyword) {
		return true
	}
	next := rune(model[len(keyword)])
	return unicode.IsDigit(next) || next == '-' || next == '_' || next == '.' || next == ':'
}
