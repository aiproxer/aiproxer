# 2026-07-24 - [Fix error message leak in JSON parsing during audit redaction]

**Vulnerability:** A malformed raw event payload failing JSON parsing in `redactAuditResultJSON` exposed the entire payload because it fell back to returning `raw` instead of a masked event digest.
**Learning:** `json.Unmarshal` failures must not leak untrusted input strings back into the parsed outcome structure, as this circumvents the `best_effort` redaction policy.
**Prevention:** Always fallback to wrapping `DigestJSONFields(raw, pol)` in an `event_digest` object on unmarshal failures.

# 2026-09-20 - Constant-Time Length Leak in ValidateState

**Vulnerability:** Length leak timing attack in ValidateState via subtle.ConstantTimeCompare.
**Learning:** subtle.ConstantTimeCompare returns immediately when length differs, revealing length of secret.
**Prevention:** Always hash secrets to fixed lengths (e.g. SHA256) before constant time comparison.
