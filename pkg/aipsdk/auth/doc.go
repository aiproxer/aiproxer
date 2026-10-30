// Package auth defines stable, protocol-neutral authentication data shapes and event schemas
// for the plugin SDK. Core "ports" in internal/core/auth (Authenticator, RemoteDecider, EventSink)
// consume these DTOs; adapters map HTTP and configuration into [InboundCallMeta] and out of events.
//
// Non-goals: this package does not implement policy, HTTP extraction, or remote clients. It does
// not use net/http, provider SDKs, enterprise transport, or canonical LLM event stream types.
//
// Secret handling: [InboundCallMeta] may carry live bearer and session-hint material for
// policy evaluation on the request path; that data must not be copied into [AuthDecisionEvent] or
// [SessionStartEvent], which are operator-visible audit views without raw tokens, API keys, SSO
// secrets, resume proofs, or personal OAuth access tokens.
package auth
