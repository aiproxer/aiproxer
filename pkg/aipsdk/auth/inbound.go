package auth

// InboundCallMeta is protocol-neutral request metadata for authentication.
// Driving adapters (e.g. stdhttp) populate it. Secret-adjacent fields exist only here
// for policy evaluation; they are not part of public audit or session events.
type InboundCallMeta struct {
	TraceID  string
	Frontend string
	Method   string
	Path     string
	// ClientAddr is a transport-level remote address (for example host:port). In some jurisdictions
	// it may be treated as personal data; consider redaction and retention policies in logs and audits.
	ClientAddr string
	// AuthorizationBearer is the raw token from an HTTP Authorization header only when the scheme
	// is Bearer; otherwise empty. Treat as secret at rest and in logs.
	AuthorizationBearer string
	// SessionHint is a client-supplied resume or session handle; may be secret if it authorizes.
	SessionHint string
}
