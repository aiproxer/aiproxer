package reasoningreplay

import (
	"strings"
	"unicode"
)

// CatalogVersion identifies the shared automatic reasoning-replay model family catalog.
const CatalogVersion = "compatible-auto.v2"

var backendPrefixes = []string{
	"openrouter",
	"openai-legacy",
	"openai-responses",
	"nvidia",
	"huggingface",
	"ollama",
	"ollama-cloud",
	"vllm",
	"lmstudio",
	"llamacpp",
	"opencode-go",
	"opencode-zen",
}

// familyTokens are matched with boundary-aware token rules (not unrestricted substring).
// Left: start of string or non-alnum. Right: end of string or non-letter (digits/separators OK).
// Letter continuation is rejected so incidental forms like deepseeker/qwentest/minimaximum/kimiko stay inactive.
// "moonshotai" is an explicit vendor token; "moonshot" alone does not match inside it.
var familyTokens = []string{
	"deepseek",
	"moonshotai",
	"moonshot",
	"minimax",
	"kimi",
	"qwen",
	"glm",
	"mimo",
	"hy3",
}

// BackendPrefixes returns the stable OpenAI-compatible backend prefix set for automatic eligibility.
func BackendPrefixes() []string {
	return append([]string(nil), backendPrefixes...)
}

// ModelEligible reports whether model matches an automatic reasoning-replay family.
func ModelEligible(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	if gptAutomaticEligible(m) {
		return true
	}
	for _, kw := range familyTokens {
		if familyTokenMatch(m, kw) {
			return true
		}
	}
	return false
}

// PrefixEligible reports whether any candidate backend prefix is in the automatic set.
func PrefixEligible(prefixes []string) bool {
	if len(prefixes) == 0 {
		return false
	}
	want := make(map[string]struct{}, len(backendPrefixes))
	for _, p := range backendPrefixes {
		want[p] = struct{}{}
	}
	for _, p := range prefixes {
		p = strings.ToLower(strings.TrimSpace(p))
		if _, ok := want[p]; ok {
			return true
		}
	}
	return false
}

// Eligible is true when both model and backend prefixes match automatic policy.
func Eligible(model string, prefixes []string) bool {
	return PrefixEligible(prefixes) && ModelEligible(model)
}

func familyTokenMatch(model, kw string) bool {
	if kw == "" || len(kw) > len(model) {
		return false
	}
	for i := 0; i+len(kw) <= len(model); i++ {
		if model[i:i+len(kw)] != kw {
			continue
		}
		if i > 0 && isASCIIAlphaNum(rune(model[i-1])) {
			continue
		}
		j := i + len(kw)
		if j == len(model) {
			return true
		}
		if !unicode.IsLetter(rune(model[j])) {
			return true
		}
	}
	return false
}

func gptAutomaticEligible(model string) bool {
	for i := 0; i+3 <= len(model); i++ {
		if model[i:i+3] != "gpt" {
			continue
		}
		if i > 0 && isASCIIAlphaNum(rune(model[i-1])) {
			continue
		}
		rest := trimVersionSeparators(model[i+3:])
		if rest == "" || !isDigitByte(rest[0]) {
			continue
		}
		major, rest, ok := parseUintPrefix(rest)
		if !ok || major != 5 {
			continue
		}
		minor := 0
		hasMinor := false
		if rest != "" {
			switch rest[0] {
			case '.':
				minor, rest, ok = parseUintPrefix(rest[1:])
				if !ok {
					continue
				}
				hasMinor = true
				if strings.HasPrefix(rest, ".") {
					_, rest, ok = parseUintPrefix(rest[1:])
					if !ok {
						continue
					}
				}
			case '-', '_':
				if len(rest) > 1 && isDigitByte(rest[1]) {
					// Numeric minor via dash/underscore (gpt-5-6 / gpt-5_6).
					minor, rest, ok = parseUintPrefix(rest[1:])
					if !ok {
						continue
					}
					hasMinor = true
					if strings.HasPrefix(rest, ".") {
						_, rest, ok = parseUintPrefix(rest[1:])
						if !ok {
							continue
						}
					}
				} else {
					// Named suffix (gpt-5-mini): eligible as gpt-5.
					return true
				}
			}
		}
		if hasMinor && minor > 5 {
			continue
		}
		if rest == "" || !isASCIIAlphaNum(rune(rest[0])) {
			return true
		}
	}
	return false
}

func trimVersionSeparators(s string) string {
	for len(s) > 0 {
		switch s[0] {
		case '-', '_', '/', '.':
			// Leading '.' before major is malformed for gpt-.5; only strip - _ /
			if s[0] == '.' {
				return s
			}
			s = s[1:]
		default:
			return s
		}
	}
	return s
}

func parseUintPrefix(s string) (val int, rest string, ok bool) {
	if s == "" || !isDigitByte(s[0]) {
		return 0, s, false
	}
	i := 0
	for i < len(s) && isDigitByte(s[i]) {
		val = val*10 + int(s[i]-'0')
		i++
		if val > 1_000_000 {
			return 0, s, false
		}
	}
	return val, s[i:], true
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func isASCIIAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
