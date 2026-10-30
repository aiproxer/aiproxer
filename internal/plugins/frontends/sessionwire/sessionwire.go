// Package sessionwire holds shared AIProxer session carrier keys and decode helpers for frontends.
// Security decisions stay in core; adapters only lift protocol-legal fields into [aipapi.SessionRef].
package sessionwire

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// AIProxer-controlled HTTP carriers for secure-session resume (proxy-issued values round-tripped by clients).
const (
	HeaderAuthoritativeSessionID = aipsdk.HeaderSessionID
	HeaderResumeToken            = aipsdk.HeaderResumeToken
	HeaderALegID                 = aipsdk.HeaderALegID
	HeaderSessionHint            = aipsdk.HeaderSessionHint
	// Metadata keys for JSON request bodies (OpenAI-style metadata maps).
	MetaKeyAuthoritativeSessionID = "aip_session_id"
	MetaKeyResumeToken            = "aip_resume_token"
)

// HTTPStatusForSessionDenial maps stable [aipapi.SessionDenialCode] values to HTTP status codes
// for frontend adapters (protocol-neutral transport semantics).
func HTTPStatusForSessionDenial(code aipapi.SessionDenialCode) int {
	switch code {
	case aipapi.SessionDeniedMissingPrincipal:
		return 401
	case aipapi.SessionDeniedStorageUnavailable,
		aipapi.SessionDeniedMandatoryAuditFailure,
		aipapi.SessionDeniedPolicyUnavailable:
		return 503
	default:
		return 400
	}
}

// ApplyAuthoritativeHeaders merges AIProxer session headers into ref when present (trimmed).
// Caller supplies ref; zero fields are left unchanged when a header is absent.
func ApplyAuthoritativeHeaders(ref *aipapi.SessionRef, h http.Header) {
	ApplyAuthoritativeHeadersNamed(ref, h, nil, nil)
}

// ApplyAuthoritativeHeadersNamed merges session-id and resume-token from the given
// header names. Empty name lists use [HeaderAuthoritativeSessionID] and [HeaderResumeToken].
func ApplyAuthoritativeHeadersNamed(ref *aipapi.SessionRef, h http.Header, sessionID, resumeToken []string) {
	if ref == nil || h == nil {
		return
	}
	if len(sessionID) == 0 {
		sessionID = []string{HeaderAuthoritativeSessionID}
	}
	if len(resumeToken) == 0 {
		resumeToken = []string{HeaderResumeToken}
	}
	if v := firstHeader(h, sessionID); v != "" {
		ref.AuthoritativeSessionID = v
	}
	if v := firstHeader(h, resumeToken); v != "" {
		ref.ResumeToken = v
	}
}

func firstHeader(h http.Header, names []string) string {
	return aipsdk.FirstHeader(h, names)
}

// ApplyMetadata merges string metadata entries into ref when keys are present (trimmed).
// Headers should be applied after metadata if headers must win on conflict.
func ApplyMetadata(ref *aipapi.SessionRef, meta map[string]string) {
	if ref == nil || len(meta) == 0 {
		return
	}
	if v := strings.TrimSpace(meta[MetaKeyAuthoritativeSessionID]); v != "" {
		ref.AuthoritativeSessionID = v
	}
	if v := strings.TrimSpace(meta[MetaKeyResumeToken]); v != "" {
		ref.ResumeToken = v
	}
}

// ValidateMetadata checks AIProxer-controlled session metadata carriers before callers copy
// them into the canonical session reference.
func ValidateMetadata(meta map[string]string) error {
	if len(meta) == 0 {
		return nil
	}
	if err := validateMetadataCarrier(MetaKeyAuthoritativeSessionID, meta[MetaKeyAuthoritativeSessionID], aipapi.MaxAuthoritativeSessionIDBytes); err != nil {
		return err
	}
	if err := validateMetadataCarrier(MetaKeyResumeToken, meta[MetaKeyResumeToken], aipapi.MaxResumeTokenBytes); err != nil {
		return err
	}
	return nil
}

func validateMetadataCarrier(key, value string, maxBytes int) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) <= maxBytes {
		return nil
	}
	return fmt.Errorf("metadata %s exceeds %d bytes", key, maxBytes)
}

// WithoutSensitiveToken replaces exact rawToken substrings in s for log-safe strings.
func WithoutSensitiveToken(s, rawToken string) string {
	if s == "" || rawToken == "" {
		return s
	}
	return strings.ReplaceAll(s, rawToken, "[REDACTED]")
}

// WriteSessionResponseCarrier sets AIProxer session response headers from largebody.SessionResponseCarrier
// when fields are non-empty. It reveals the sensitive resume token only into the designated response
// header at the frontend transport boundary (Requirements 14.6, 18.2, 22.3).
func WriteSessionResponseCarrier(w http.ResponseWriter, carrier largebody.SessionResponseCarrier) {
	if w == nil {
		return
	}
	WriteSessionResponseCarrierHeaders(w.Header(), carrier)
}

// WriteSessionResponseCarrierHeaders sets AIProxer session response headers on h from carrier when fields are non-empty.
func WriteSessionResponseCarrierHeaders(h http.Header, carrier largebody.SessionResponseCarrier) {
	if h == nil {
		return
	}
	if sid := strings.TrimSpace(carrier.AuthoritativeSessionID); sid != "" {
		h.Set(HeaderAuthoritativeSessionID, sid)
	}
	if tok := strings.TrimSpace(carrier.ResumeToken.Reveal()); tok != "" {
		h.Set(HeaderResumeToken, tok)
	}
	if aLegID := strings.TrimSpace(carrier.ALegID); aLegID != "" {
		h.Set(HeaderALegID, aLegID)
	}
}

// WriteResponseCarriers sets AIProxer session response headers from call.Session when fields are non-empty.
func WriteResponseCarriers(w http.ResponseWriter, call *aipapi.Call) {
	if w == nil || call == nil {
		return
	}
	WriteSessionResponseCarrierHeaders(w.Header(), largebody.SessionResponseCarrier{
		AuthoritativeSessionID: call.Session.AuthoritativeSessionID,
		ALegID:                 call.Session.ALegID,
		ResumeToken:            largebody.NewSensitiveString(call.Session.ResumeToken),
	})
}
