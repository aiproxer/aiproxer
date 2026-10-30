// Package contract contains dependency-neutral semantic TCK metadata shared by
// frontend, core, backend, and executable connector contract tests.
package contract

import "github.com/aiproxer/aiproxer/pkg/aipapi"

type (
	SemanticFeature   string
	ScenarioID        string
	ScenarioTransport string
)

const (
	FeatureText             SemanticFeature = "text"
	FeatureStreaming        SemanticFeature = "streaming"
	FeatureTools            SemanticFeature = "tools"
	FeatureParallelTools    SemanticFeature = "parallel_tools"
	FeatureVision           SemanticFeature = "vision"
	FeatureVideo            SemanticFeature = "video"
	FeatureDocuments        SemanticFeature = "documents"
	FeatureAnnotations      SemanticFeature = "annotations"
	FeatureStructuredOutput SemanticFeature = "structured_output"
	FeatureReasoning        SemanticFeature = "reasoning"
	FeatureReasoningReplay  SemanticFeature = "reasoning_replay"
	FeatureAssistantPhase   SemanticFeature = "assistant_phase"
	FeatureAssistantMedia   SemanticFeature = "assistant_media"
	FeatureOrderedItems     SemanticFeature = "ordered_items"
	FeatureItemReferences   SemanticFeature = "item_references"
	FeatureCompaction       SemanticFeature = "compaction"
	FeatureExtensions       SemanticFeature = "extensions"
	FeatureUsage            SemanticFeature = "usage"
	FeatureErrors           SemanticFeature = "errors"
	FeatureCancellation     SemanticFeature = "cancellation"
	FeatureLifecycle        SemanticFeature = "lifecycle"

	TransportHTTP      ScenarioTransport = "http"
	TransportStreaming ScenarioTransport = "streaming"
	TransportWebSocket ScenarioTransport = "websocket"
	TransportConnector ScenarioTransport = "connector"
)

type ScenarioDescriptor struct {
	ID        ScenarioID                  `json:"id"`
	Feature   SemanticFeature             `json:"feature"`
	Requires  aipapi.ProtocolRequirements `json:"requires"`
	Transport ScenarioTransport           `json:"transport"`
	// Prerequisites and ExpectedEvidence make scenario selection auditable:
	// runners must satisfy the former and record the latter at their boundary.
	Prerequisites    []string `json:"prerequisites,omitempty"`
	ExpectedEvidence []string `json:"expected_evidence,omitempty"`
}

func (s ScenarioDescriptor) IsCancellation() bool {
	return s.Feature == FeatureCancellation
}

// BaselineScenarioCorpus is the single metadata source shared by all TCKs.
// Scenario builders remain in the transport-specific runners.
func BaselineScenarioCorpus() []ScenarioDescriptor {
	return []ScenarioDescriptor{
		{ID: "text-baseline", Feature: FeatureText, Transport: TransportHTTP, Prerequisites: []string{"valid text request"}, ExpectedEvidence: []string{"canonical call", "terminal response"}},
		{ID: "text-streaming", Feature: FeatureStreaming, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"streaming transport", "valid text request"}, ExpectedEvidence: []string{"ordered canonical events", "terminal response"}},
		{ID: "usage-present", Feature: FeatureUsage, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"usage reporting enabled"}, ExpectedEvidence: []string{"usage event present", "explicit counters including zero"}},
		{ID: "usage-zero", Feature: FeatureUsage, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"provider reports zero counters"}, ExpectedEvidence: []string{"usage event present", "zero counters preserved"}},
		{ID: "tools-execution", Feature: FeatureTools, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"tool declaration", "tool call result"}, ExpectedEvidence: []string{"tool call lifecycle", "tool result replay"}},
		{ID: "parallel-tools", Feature: FeatureParallelTools, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityParallelToolCalls}}, Transport: TransportHTTP, Prerequisites: []string{"parallel tool declarations", "independent tool calls"}, ExpectedEvidence: []string{"parallel tool call identity", "ordered tool results"}},
		{ID: "tool-call-replay", Feature: FeatureTools, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"prior tool call item"}, ExpectedEvidence: []string{"tool call ID and arguments preserved"}},
		{ID: "tool-result-replay", Feature: FeatureTools, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"matching prior tool call"}, ExpectedEvidence: []string{"tool result references call ID"}},
		{ID: "vision-input", Feature: FeatureVision, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityVision}}, Transport: TransportHTTP, Prerequisites: []string{"valid image part"}, ExpectedEvidence: []string{"canonical image part"}},
		{ID: "video-input", Feature: FeatureVideo, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityVideoInput}}, Transport: TransportHTTP, Prerequisites: []string{"valid video part"}, ExpectedEvidence: []string{"canonical video requirement or explicit rejection"}},
		{ID: "annotations-output", Feature: FeatureAnnotations, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityAnnotations}}, Transport: TransportHTTP, Prerequisites: []string{"provider annotations"}, ExpectedEvidence: []string{"annotation provenance or explicit rejection"}},
		{ID: "documents-input", Feature: FeatureDocuments, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityDocuments}}, Transport: TransportHTTP, Prerequisites: []string{"valid document part"}, ExpectedEvidence: []string{"canonical document part"}},
		{ID: "structured-output", Feature: FeatureStructuredOutput, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStructuredOutputs}}, Transport: TransportHTTP, Prerequisites: []string{"valid response schema"}, ExpectedEvidence: []string{"structured-output requirement"}},
		{ID: "reasoning-output", Feature: FeatureReasoning, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityReasoning}}, Transport: TransportHTTP, Prerequisites: []string{"reasoning option"}, ExpectedEvidence: []string{"reasoning dialect or event"}},
		{ID: "assistant-phase", Feature: FeatureAssistantPhase, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityAssistantPhase}}, Transport: TransportHTTP, Prerequisites: []string{"assistant phase metadata"}, ExpectedEvidence: []string{"assistant phase preserved"}},
		{ID: "assistant-media", Feature: FeatureAssistantMedia, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityAssistantMediaRefs}}, Transport: TransportHTTP, Prerequisites: []string{"assistant media reference"}, ExpectedEvidence: []string{"assistant media preserved"}},
		{ID: "ordered-items", Feature: FeatureOrderedItems, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityOrderedItems}}, Transport: TransportHTTP, Prerequisites: []string{"item authority"}, ExpectedEvidence: []string{"ordered item sequence"}},
		{ID: "compaction-lifecycle", Feature: FeatureCompaction, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityCompaction}}, Transport: TransportHTTP, Prerequisites: []string{"compaction operation", "item authority"}, ExpectedEvidence: []string{"compaction operation", "compaction item/state"}},
		{ID: "item-reference-dialect", Feature: FeatureItemReferences, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityItemReferences}, ItemDialects: []aipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}}}, Transport: TransportHTTP, Prerequisites: []string{"backward item reference"}, ExpectedEvidence: []string{"reference preserved"}},
		{ID: "reasoning-replay-dialect", Feature: FeatureReasoningReplay, Requires: aipapi.ProtocolRequirements{ReasoningDialects: []aipapi.DialectRequirement{{Kind: "reasoning", Dialect: "reasoning_replay"}}}, Transport: TransportHTTP, Prerequisites: []string{"historical reasoning item"}, ExpectedEvidence: []string{"reasoning replay dialect"}},
		{ID: "opaque-extension-type", Feature: FeatureExtensions, Requires: aipapi.ProtocolRequirements{ExtensionTypes: []aipapi.ExtensionRequirement{{Namespace: "com.example", Type: "custom"}}}, Transport: TransportHTTP, Prerequisites: []string{"bounded extension"}, ExpectedEvidence: []string{"extension preserved"}},
		{ID: "recoverable-error", Feature: FeatureErrors, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"error after response start", "retryable provider error"}, ExpectedEvidence: []string{"error event", "recoverable classification"}},
		{ID: "terminal-error", Feature: FeatureErrors, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"terminal provider error"}, ExpectedEvidence: []string{"error event", "terminal classification"}},
		{ID: "cancellation", Feature: FeatureCancellation, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"in-flight stream", "cancel signal"}, ExpectedEvidence: []string{"cancel outcome", "no post-cancel events"}},
		{ID: "lifecycle-close", Feature: FeatureLifecycle, Transport: TransportConnector, Prerequisites: []string{"configured session"}, ExpectedEvidence: []string{"first close success", "second close success"}},
		{ID: "close-idempotent", Feature: FeatureLifecycle, Transport: TransportConnector, Prerequisites: []string{"configured session"}, ExpectedEvidence: []string{"first close success", "second close success"}},
		{ID: "terminal-clean-text", Feature: FeatureText, Transport: TransportHTTP, Prerequisites: []string{"valid text request"}, ExpectedEvidence: []string{"canonical call", "terminal response"}},
		{ID: "terminal-clean-tool-call", Feature: FeatureTools, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityTools}}, Transport: TransportHTTP, Prerequisites: []string{"tool declaration", "tool call result"}, ExpectedEvidence: []string{"tool call lifecycle", "terminal response"}},
		{ID: "terminal-output-limit", Feature: FeatureText, Transport: TransportHTTP, Prerequisites: []string{"valid text request"}, ExpectedEvidence: []string{"canonical call", "truncated terminal response"}},
		{ID: "terminal-upstream-error", Feature: FeatureErrors, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"terminal provider error"}, ExpectedEvidence: []string{"error event", "terminal classification"}},
		{ID: "terminal-usage-reported", Feature: FeatureUsage, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportStreaming, Prerequisites: []string{"usage reporting enabled"}, ExpectedEvidence: []string{"usage event present", "terminal response"}},
		{ID: "terminal-content-filter", Feature: FeatureErrors, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportHTTP, Prerequisites: []string{"refusal-shaped upstream response"}, ExpectedEvidence: []string{"refusal lexeme preserved verbatim", "terminal response"}},
		{ID: "terminal-empty-turn", Feature: FeatureText, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportHTTP, Prerequisites: []string{"clean terminal with no visible content"}, ExpectedEvidence: []string{"no visible deltas", "clean terminal response"}},
		{ID: "terminal-error-taxonomy", Feature: FeatureErrors, Requires: aipapi.ProtocolRequirements{Capabilities: []aipapi.Capability{aipapi.CapabilityStreaming}}, Transport: TransportHTTP, Prerequisites: []string{"frontend execution failure"}, ExpectedEvidence: []string{"classified error envelope", "terminal classification"}},
	}
}
