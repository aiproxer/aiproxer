package configreload

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// RedactedPlaceholder is the bounded token substituted for secret material.
const RedactedPlaceholder = "[redacted]"

var (
	dsnPasswordRe  = regexp.MustCompile(`(?i)\b(password|passwd|pwd)=([^\s&]+)`)
	apiKeyQueryRe  = regexp.MustCompile(`(?i)([?&](api[_-]?key|access[_-]?token|token|secret|password)=)([^&]*)`)
	bearerRe       = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]+=*`)
	skTokenRe      = regexp.MustCompile(`\bsk-[A-Za-z0-9\-_]{8,}`)
	secretAssignRe = regexp.MustCompile(`(?i)\b([a-z0-9_-]*(?:api[_-]?key|access[_-]?token|client[_-]?secret|token|secret|password|passwd|pwd))\s*([=:])\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
	urlPasswordRe  = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^:/\s]+:)([^@\s]+)(@)`)
)

// SanitizeConfigKey returns a safe config field path. Values embedded after '='
// or opaque segments that look like secrets are replaced.
func SanitizeConfigKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if before, _, ok := strings.Cut(key, "="); ok {
		return sanitizePathSegment(before) + "=" + RedactedPlaceholder
	}
	return sanitizePathSegment(key)
}

func sanitizePathSegment(s string) string {
	if skTokenRe.MatchString(s) || bearerRe.MatchString(s) {
		return RedactedPlaceholder
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) && r != '"' && r != '\'' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

// SanitizeDSN redacts passwords and credential material from DSN-like strings.
func SanitizeDSN(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return ""
	}
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		if u.User != nil {
			user := u.User.Username()
			if _, has := u.User.Password(); has {
				u.User = url.UserPassword(user, RedactedPlaceholder)
			}
		}
		return scrubSecretSubstrings(u.String())
	}
	return scrubSecretSubstrings(dsnPasswordRe.ReplaceAllString(dsn, "${1}="+RedactedPlaceholder))
}

// SanitizeURL redacts userinfo and credential-bearing query parameters.
func SanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return scrubSecretSubstrings(raw)
	}
	if u.User != nil {
		user := u.User.Username()
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(user, RedactedPlaceholder)
		} else if user != "" && looksSecret(user) {
			u.User = url.User(RedactedPlaceholder)
		}
	}
	if u.RawQuery != "" {
		u.RawQuery = apiKeyQueryRe.ReplaceAllString(u.RawQuery, "${1}"+RedactedPlaceholder)
	}
	return scrubSecretSubstrings(u.String())
}

// SanitizeOpaqueYAML returns a bounded description without node values.
func SanitizeOpaqueYAML(node *yaml.Node) string {
	if node == nil {
		return "opaque_yaml:nil"
	}
	kind := "unknown"
	switch node.Kind {
	case yaml.DocumentNode:
		kind = "document"
	case yaml.SequenceNode:
		kind = "sequence"
	case yaml.MappingNode:
		kind = "mapping"
	case yaml.ScalarNode:
		kind = "scalar"
	case yaml.AliasNode:
		kind = "alias"
	}
	n := min(countYAMLNodes(node), 64)
	return fmt.Sprintf("opaque_yaml:kind=%s:nodes=%d", kind, n)
}

func countYAMLNodes(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	total := 1
	for _, c := range n.Content {
		total += countYAMLNodes(c)
		if total > 64 {
			return 64
		}
	}
	return total
}

// SanitizeFailure returns a bounded, secret-safe failure summary.
func SanitizeFailure(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = scrubSecretSubstrings(msg)
	msg = dsnPasswordRe.ReplaceAllString(msg, "${1}="+RedactedPlaceholder)
	msg = apiKeyQueryRe.ReplaceAllString(msg, "${1}"+RedactedPlaceholder)
	// Prefer typed category when present.
	var cat string
	type categorizer interface{ Category() string }
	var c categorizer
	if errors.As(err, &c) && c != nil {
		cat = c.Category()
	}
	if cat != "" {
		return "failure:" + SanitizeConfigKey(cat)
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return "failure:" + msg
}

// SanitizePanicValue returns only a safe type token or redacted placeholder.
func SanitizePanicValue(v any) string {
	if v == nil {
		return "panic:nil"
	}
	switch x := v.(type) {
	case string:
		return "panic:string:" + scrubSecretSubstrings(truncateRunes(x, 48))
	case error:
		return "panic:error:" + SanitizeFailure(x)
	default:
		t := reflect.TypeOf(v)
		if t == nil {
			return "panic:unknown"
		}
		name := t.String()
		if looksSecret(name) {
			return "panic:type:" + RedactedPlaceholder
		}
		return "panic:type:" + truncateRunes(name, 64)
	}
}

func scrubSecretSubstrings(s string) string {
	out := bearerRe.ReplaceAllString(s, "Bearer "+RedactedPlaceholder)
	out = skTokenRe.ReplaceAllString(out, RedactedPlaceholder)
	out = secretAssignRe.ReplaceAllString(out, "${1}${2}"+RedactedPlaceholder)
	out = urlPasswordRe.ReplaceAllString(out, "${1}"+RedactedPlaceholder+"${3}")
	return out
}

func looksSecret(s string) bool {
	low := strings.ToLower(s)
	if strings.Contains(low, "password") || strings.Contains(low, "secret") ||
		strings.Contains(low, "api_key") || strings.Contains(low, "apikey") ||
		strings.Contains(low, "token") || strings.Contains(low, "bearer") {
		return true
	}
	return skTokenRe.MatchString(s) || bearerRe.MatchString(s)
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return scrubSecretSubstrings(s)
	}
	return scrubSecretSubstrings(string(r[:maxLen]))
}
