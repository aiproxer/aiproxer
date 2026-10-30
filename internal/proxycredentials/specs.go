package proxycredentials

import (
	"cmp"
	"slices"
	"unicode"
)

// BaseNames are the exact proxy credential environment variable bases
// that secrets-guard and related inventory loaders recognize.
// Numbered forms base_N (any decimal suffix) match via MatchProxyCredentialName.
//
// Note: internal/standardplugins/keys.go keeps its own gap-stop collection
// semantics unchanged; this package is name-matching only and does not import os.
func BaseNames() []string {
	return slices.Clone(baseNames)
}

var baseNames = []string{
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

// longestBasesFirst prefers longer bases so OPENCODE_GO_API_KEY_2 does not match OPENCODE_API_KEY.
var longestBasesFirst = func() []string {
	out := slices.Clone(baseNames)
	slices.SortStableFunc(out, func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})
	return out
}()

// MatchProxyCredentialName reports whether name is an exact proxy credential base
// or base_\d+ (any decimal suffix; no gap-stop logic).
// On match, base is the corresponding BaseNames entry.
func MatchProxyCredentialName(name string) (base string, ok bool) {
	if name == "" {
		return "", false
	}
	if slices.Contains(baseNames, name) {
		return name, true
	}
	for _, b := range longestBasesFirst {
		prefix := b + "_"
		if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		if isDecimalDigits(name[len(prefix):]) {
			return b, true
		}
	}
	return "", false
}

func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
