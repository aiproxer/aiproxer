package engine

import (
	"cmp"
	"slices"
	"strings"
)

// declaredPublicPrefixes are registry-declared public prefixes that may be retained
// during redaction when MatcherOptions.PreserveKnownPrefixes is enabled.
// Longer prefixes win when multiple match (sorted descending by length).
var declaredPublicPrefixes = []string{
	"sk-ant-api03-",
	"sk-ant-",
	"sk-or-v1-",
	"sk-or-",
	"sk-",
	"github_pat_",
	"ghp_",
	"gho_",
	"ghu_",
	"ghs_",
	"ghr_",
	"xoxb-",
	"xoxp-",
	"xoxa-",
	"xapp-",
	"whsec_",
	"rk_live_",
	"rk_test_",
	"sk_live_",
	"sk_test_",
	"npm_",
	"pypi-",
}

func init() {
	slices.SortStableFunc(declaredPublicPrefixes, func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})
}

// detectKnownPublicPrefix returns the longest declared public prefix that is a
// strict prefix of value, or "" when none match.
func detectKnownPublicPrefix(value string) string {
	if value == "" {
		return ""
	}
	for _, p := range declaredPublicPrefixes {
		if len(p) == 0 || len(p) >= len(value) {
			continue
		}
		if strings.HasPrefix(value, p) {
			return p
		}
	}
	return ""
}

// DetectKnownPublicPrefix exposes the established registry prefix detection to
// feature-private transient matchers without exposing catalog values.
func DetectKnownPublicPrefix(value string) string {
	return detectKnownPublicPrefix(value)
}
