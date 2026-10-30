// Package agentfacts contains stable client identity facts shared by root-module
// features. Callers pass an already accepted, bounded client identity value;
// transport validation remains owned by internal/core/identity. This package
// does not inspect prompts or make session-classification decisions.
//
// The separately versioned Codex connector keeps its own matcher so it does not
// depend on root-internal packages and preserves connector module isolation.
package agentfacts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Family string

const (
	FamilyCodex    Family = "codex"
	FamilyRoo      Family = "roo"
	FamilyOpenCode Family = "opencode"
	FamilyPi       Family = "pi"
	FamilyDroid    Family = "droid"
	FamilyHermes   Family = "hermes"
)

type MatchConfidence string

const ConfidenceHigh MatchConfidence = "high"

type Match struct {
	Family     Family
	Confidence MatchConfidence
}

type markerKind uint8

const (
	markerToken markerKind = iota
	markerVersionPrefix
)

type identityMarker struct {
	family Family
	value  string
	kind   markerKind
}

// identityMarkers is the single stable identity vocabulary for root-module
// consumers. Tokens keep hyphens and underscores intact so a family name
// embedded in an unrelated product token does not match.
var identityMarkers = [...]identityMarker{
	{family: FamilyCodex, value: "codex_cli_rs", kind: markerToken},
	{family: FamilyRoo, value: "roo-code", kind: markerToken},
	{family: FamilyOpenCode, value: "opencode", kind: markerToken},
	{family: FamilyPi, value: "pi-coding-agent", kind: markerToken},
	{family: FamilyPi, value: "pi", kind: markerVersionPrefix},
	{family: FamilyDroid, value: "factory-cli", kind: markerToken},
	{family: FamilyDroid, value: "factory_cli", kind: markerToken},
	{family: FamilyDroid, value: "factorydroid", kind: markerToken},
	{family: FamilyDroid, value: "droid", kind: markerToken},
	{family: FamilyHermes, value: "hermes-agent", kind: markerToken},
	{family: FamilyHermes, value: "hermes", kind: markerVersionPrefix},
}

// MatchIdentity returns the single known high-confidence client family in a
// bounded client identity value. Only stable exact, prefix, or token rules are
// recognized; generic SDK User-Agents remain unknown.
func MatchIdentity(candidate string) (Match, bool) {
	normalized := strings.ToLower(strings.TrimSpace(candidate))
	if normalized == "" {
		return Match{}, false
	}

	var found Match
	for _, marker := range identityMarkers {
		matched := false
		switch marker.kind {
		case markerToken:
			matched = containsToken(normalized, marker.value)
		case markerVersionPrefix:
			matched = containsVersionPrefix(normalized, marker.value)
		}
		if matched {
			if found.Family != "" && found.Family != marker.family {
				return Match{}, false
			}
			found = Match{Family: marker.family, Confidence: ConfidenceHigh}
		}
	}
	return found, found.Family != ""
}

func containsToken(candidate, token string) bool {
	for offset := 0; offset <= len(candidate)-len(token); {
		found := strings.Index(candidate[offset:], token)
		if found < 0 {
			return false
		}
		start := offset + found
		end := start + len(token)
		if hasTokenBoundaryBefore(candidate, start) && hasTokenBoundaryAfter(candidate, end) {
			return true
		}
		offset = end
	}
	return false
}

func containsVersionPrefix(candidate, token string) bool {
	for offset := 0; offset <= len(candidate)-len(token); {
		found := strings.Index(candidate[offset:], token)
		if found < 0 {
			return false
		}
		start := offset + found
		end := start + len(token)
		if hasTokenBoundaryBefore(candidate, start) && end < len(candidate) && candidate[end] == '/' {
			return true
		}
		offset = end
	}
	return false
}

func hasTokenBoundaryBefore(candidate string, index int) bool {
	if index == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(candidate[:index])
	return !isIdentityTokenRune(r)
}

func hasTokenBoundaryAfter(candidate string, index int) bool {
	if index == len(candidate) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(candidate[index:])
	return !isIdentityTokenRune(r)
}

func isIdentityTokenRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}
