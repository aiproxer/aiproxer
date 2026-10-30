// Package aipapi defines the canonical public contracts shared across frontends,
// backends, and future external integrations.
//
// Ownership: this package is a stable contract surface, not the application policy core.
// Add canonical request, event, capability, and error shapes here. Keep routing, recovery,
// attempt lineage, extension-stage orchestration, and other product policy in internal/core.
// Do not import internal packages, provider SDKs, the stdhttp server layer, or composition
// roots from here; those stay at the edge or in the core (see internal/archtest).
//
// Tool-call and assistant history (requirements 8.x): only a documented subset of
// provider-specific tool history is round-tripped through Message and Part values today.
// OpenAI Chat and OpenAI Responses frontends implement the supported shapes; other
// frontends may ignore or normalize unsupported tool rows. See frontend package docs
// next to each adapter for the exact supported subset per protocol.
//
// Streaming assistant multimodal references: EventAssistantImageRef and EventAssistantFileRef
// carry URL- or id-style refs (see Event fields) and aggregate into Collected.AssistantMedia.
// Parity matrices: .kiro/specs/archive/llm-api-parity/design.md.
package aipapi
