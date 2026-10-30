package app

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// RawAuditAllowed reports whether session policy permits embedding raw payloads in audit records.
func RawAuditAllowed(pol domain.PolicyMetadata) bool {
	return strings.EqualFold(strings.TrimSpace(pol.AuditMode), "full")
}

// RedactCorrelationJSON applies correlation redaction for operator-visible JSON strings.
func RedactCorrelationJSON(raw string, pol domain.PolicyMetadata) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(pol.RedactionProfile), "strict") {
		return `{"redacted":true}`
	}
	if !json.Valid([]byte(raw)) {
		return `{"invalid_json":true}`
	}
	return raw
}

// DigestJSONFields returns a structural digest of JSON without echoing full payloads.
func DigestJSONFields(raw string, pol domain.PolicyMetadata) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Preserve structure hints without echoing full payloads (default non-raw audit).
	var top map[string]any
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return `{"digest":"invalid_json"}`
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	out := map[string]any{
		"field_keys": keys,
		"byte_len":   len(raw),
	}
	if strings.EqualFold(strings.TrimSpace(pol.RedactionProfile), "strict") {
		out["strict"] = true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"digest":"marshal_error"}`
	}
	return string(b)
}
