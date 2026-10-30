// Package openresponsescompat implements the generic remote OpenResponses
// backend mode for remote OpenResponses-capable providers and routers.
//
// It provides strict secret-free configuration, factory construction, and a
// create mapping for both canonical authority forms pinned to the OpenResponses
// 2026-04-24 profile. Item-authority calls map directly; legacy message-authority
// calls (OpenAI Chat, OpenAI Responses, Anthropic, Gemini) are projected to
// ordered items through the explicit canonical legacy→ordered-items projector
// before request building. A schema-valid non-streaming JSON or streaming SSE
// request is built without forwarding proxy IDs, sessions, native refs, or
// arbitrary call extensions. Complete JSON response resources and incremental
// SSE streams are parsed through the production codec/state semantics into
// canonical lifecycle events; SSE reads are strictly pull-driven and bounded,
// and the first canonical event is peeked before commitment so pre-output
// transport/protocol failures stay retryable by core. Conflicting authority,
// provider replay dialects, source-specific opaque extensions, and unsupported
// content are rejected before any HTTP round trip.
//
// The package reuses shared infrastructure (endpoint descriptors, env-based
// credential resolution, static model inventory, canonical capabilities and
// dialects, shared outbound HTTP client) and imports no provider SDK or
// external executable connector.
package openresponsescompat
