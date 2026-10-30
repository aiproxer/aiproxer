package aipsdk

import (
	"net/http"
	"strings"
)

// Default inbound HTTP header names accepted by the standard distribution.
// Configured aliases are additional accept names; these defaults stay first so
// existing clients keep winning when both a default and an alias are present.
const (
	HeaderAuthorization     = "Authorization"
	HeaderAPIKey            = "x-api-key"
	HeaderGoogleAPIKey      = "x-goog-api-key"
	HeaderAzureAPIKey       = "api-key"
	HeaderRoute             = "X-AIP-Route"
	HeaderSessionID         = "X-AIP-Session-Id"
	HeaderResumeToken       = "X-AIP-Resume-Token"
	HeaderALegID            = "X-AIP-A-Leg-Id"
	HeaderSessionHint       = "X-AIP-Session-Hint"
	HeaderWorkspace         = "X-AIP-Workspace"
	HeaderTraceID           = "X-Trace-ID"
	HeaderDiagnosticsSecret = "X-AIP-Diagnostics-Secret"
)

// HTTPHeaders is the resolved inbound header-name set for one frontend mount
// (defaults plus optional operator aliases). Zero value means [DefaultHTTPHeaders].
type HTTPHeaders struct {
	APIKey            []string
	Route             []string
	SessionID         []string
	ResumeToken       []string
	ALegID            []string
	SessionHint       []string
	Workspace         []string
	Trace             []string
	DiagnosticsSecret []string
}

// DefaultHTTPHeaders returns the standard inbound header names in check order.
func DefaultHTTPHeaders() HTTPHeaders {
	return HTTPHeaders{
		APIKey:            []string{HeaderAuthorization, HeaderAPIKey, HeaderGoogleAPIKey, HeaderAzureAPIKey},
		Route:             []string{HeaderRoute},
		SessionID:         []string{HeaderSessionID},
		ResumeToken:       []string{HeaderResumeToken},
		ALegID:            []string{HeaderALegID},
		SessionHint:       []string{HeaderSessionHint},
		Workspace:         []string{HeaderWorkspace},
		Trace:             []string{HeaderTraceID},
		DiagnosticsSecret: []string{HeaderDiagnosticsSecret},
	}
}

// OrDefault fills empty slices from [DefaultHTTPHeaders].
func (h HTTPHeaders) OrDefault() HTTPHeaders {
	d := DefaultHTTPHeaders()
	if len(h.APIKey) == 0 {
		h.APIKey = d.APIKey
	}
	if len(h.Route) == 0 {
		h.Route = d.Route
	}
	if len(h.SessionID) == 0 {
		h.SessionID = d.SessionID
	}
	if len(h.ResumeToken) == 0 {
		h.ResumeToken = d.ResumeToken
	}
	if len(h.ALegID) == 0 {
		h.ALegID = d.ALegID
	}
	if len(h.SessionHint) == 0 {
		h.SessionHint = d.SessionHint
	}
	if len(h.Workspace) == 0 {
		h.Workspace = d.Workspace
	}
	if len(h.Trace) == 0 {
		h.Trace = d.Trace
	}
	if len(h.DiagnosticsSecret) == 0 {
		h.DiagnosticsSecret = d.DiagnosticsSecret
	}
	return h
}

// First returns the first non-empty trimmed header value among names.
func FirstHeader(h http.Header, names []string) string {
	if h == nil {
		return ""
	}
	for _, name := range names {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// RouteSelector returns the first non-empty route header.
func (h HTTPHeaders) RouteSelector(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().Route)
}

// SessionIDValue returns the first non-empty session-id header.
func (h HTTPHeaders) SessionIDValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().SessionID)
}

// ResumeTokenValue returns the first non-empty resume-token header.
func (h HTTPHeaders) ResumeTokenValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().ResumeToken)
}

// ALegIDValue returns the first non-empty A-leg-id header.
func (h HTTPHeaders) ALegIDValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().ALegID)
}

// SessionHintValue returns the first non-empty session-hint header.
func (h HTTPHeaders) SessionHintValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().SessionHint)
}

// WorkspaceValue returns the first non-empty workspace header.
func (h HTTPHeaders) WorkspaceValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().Workspace)
}

// TraceIDValue returns the first non-empty trace header.
func (h HTTPHeaders) TraceIDValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().Trace)
}

// DiagnosticsSecretValue returns the first non-empty diagnostics-secret header.
func (h HTTPHeaders) DiagnosticsSecretValue(hdr http.Header) string {
	return FirstHeader(hdr, h.OrDefault().DiagnosticsSecret)
}

// APIKeyFrom extracts the inbound API key. Authorization is Bearer-only;
// other names use the raw header value (Bearer prefix is still stripped when present).
func (h HTTPHeaders) APIKeyFrom(hdr http.Header) string {
	if hdr == nil {
		return ""
	}
	for _, name := range h.OrDefault().APIKey {
		raw := strings.TrimSpace(hdr.Get(name))
		if raw == "" {
			continue
		}
		if tok := BearerCredential(raw); tok != "" {
			return tok
		}
		if strings.EqualFold(name, HeaderAuthorization) {
			continue
		}
		return raw
	}
	return ""
}

// BearerCredential returns the token when raw is a Bearer authorization value.
func BearerCredential(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sp := strings.IndexByte(raw, ' ')
	if sp <= 0 {
		return ""
	}
	if !strings.EqualFold(raw[:sp], "Bearer") {
		return ""
	}
	return strings.TrimSpace(raw[sp+1:])
}
