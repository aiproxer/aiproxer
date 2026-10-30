package backendplugin

import (
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// MetaProtocolRequirements is deprecated; use Invocation.ProtocolRequirements.
	MetaProtocolRequirements = "aip.protocol_requirements"
)

// ApplyItemAuthorityMetadata is deprecated; ordered item wire uses first-class invocation fields via ApplyOrderedItemWire.
func ApplyItemAuthorityMetadata(inv *Invocation, call aipapi.Call) {
	ApplyOrderedItemWire(inv, call)
}

// HasItemAuthorityMetadata reports whether the invocation declares item authority.
func HasItemAuthorityMetadata(meta map[string]string) bool {
	_ = meta
	return false
}

// HasItemAuthorityInvocation reports item authority from first-class invocation fields.
func HasItemAuthorityInvocation(inv Invocation) bool {
	return HasItemAuthorityWire(inv)
}

// ProtocolRequirementsFromInvocation decodes requirements from first-class invocation fields.
func ProtocolRequirementsFromInvocation(inv Invocation) (aipapi.ProtocolRequirements, bool) {
	if !inv.ItemAuthority {
		return aipapi.ProtocolRequirements{}, false
	}
	req := aipapi.ProtocolRequirements{}
	for _, c := range inv.ProtocolRequirements.Capabilities {
		req.Capabilities = append(req.Capabilities, aipapi.Capability(c))
	}
	for _, d := range inv.ProtocolRequirements.ItemDialects {
		req.ItemDialects = append(req.ItemDialects, aipapi.DialectRequirement{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, d := range inv.ProtocolRequirements.ReasoningDialects {
		req.ReasoningDialects = append(req.ReasoningDialects, aipapi.DialectRequirement{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, d := range inv.ProtocolRequirements.CompactionDialects {
		req.CompactionDialects = append(req.CompactionDialects, aipapi.DialectRequirement{
			Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor,
		})
	}
	for _, e := range inv.ProtocolRequirements.ExtensionTypes {
		req.ExtensionTypes = append(req.ExtensionTypes, aipapi.ExtensionRequirement{
			Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor,
		})
	}
	return aipapi.NormalizeProtocolRequirements(req), true
}

// ProtocolRequirementsFromMetadata is deprecated; requirements are carried on Invocation.ProtocolRequirements.
func ProtocolRequirementsFromMetadata(meta map[string]string) (aipapi.ProtocolRequirements, bool) {
	_ = meta
	return aipapi.ProtocolRequirements{}, false
}

// CapabilitySummaryFromAipapi maps canonical backend caps into plugin DTO form.
func CapabilitySummaryFromAipapi(caps aipapi.BackendCaps) CapabilitySummary {
	return CapabilitySummary{
		Streaming:          capsHas(caps, aipapi.CapabilityStreaming),
		Tools:              capsHas(caps, aipapi.CapabilityTools),
		Vision:             capsHas(caps, aipapi.CapabilityVision),
		Documents:          capsHas(caps, aipapi.CapabilityDocuments),
		StructuredOutputs:  capsHas(caps, aipapi.CapabilityStructuredOutputs),
		Reasoning:          capsHas(caps, aipapi.CapabilityReasoning),
		ReasoningReplay:    capsHas(caps, aipapi.CapabilityReasoningReplay),
		ParallelToolCalls:  capsHas(caps, aipapi.CapabilityParallelToolCalls),
		OrderedItems:       capsHas(caps, aipapi.CapabilityOrderedItems),
		ItemReferences:     capsHas(caps, aipapi.CapabilityItemReferences),
		Compaction:         capsHas(caps, aipapi.CapabilityCompaction),
		AssistantPhase:     capsHas(caps, aipapi.CapabilityAssistantPhase),
		OpaqueExtensions:   capsHas(caps, aipapi.CapabilityOpaqueExtensions),
		VideoInput:         capsHas(caps, aipapi.CapabilityVideoInput),
		Annotations:        capsHas(caps, aipapi.CapabilityAnnotations),
		AssistantMediaRefs: capsHas(caps, aipapi.CapabilityAssistantMediaRefs),
	}
}

// DialectSupportFromAipapi maps canonical dialect support into plugin DTO form.
func DialectSupportFromAipapi(s aipapi.DialectSupport) DialectSupport {
	out := DialectSupport{}
	for _, d := range s.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, DialectRequirementDTO{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range s.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, DialectRequirementDTO{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range s.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, DialectRequirementDTO{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, e := range s.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, ExtensionRequirementDTO{Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor})
	}
	return out
}

// DialectSupportToAipapi maps plugin dialect support into canonical form.
func DialectSupportToAipapi(d DialectSupport) aipapi.DialectSupport {
	out := aipapi.DialectSupport{}
	for _, d := range d.ItemDialects {
		out.ItemDialects = append(out.ItemDialects, aipapi.DialectRequirement{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range d.ReasoningDialects {
		out.ReasoningDialects = append(out.ReasoningDialects, aipapi.DialectRequirement{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, d := range d.CompactionDialects {
		out.CompactionDialects = append(out.CompactionDialects, aipapi.DialectRequirement{Kind: d.Kind, Dialect: d.Dialect, Implementor: d.Implementor})
	}
	for _, e := range d.ExtensionTypes {
		out.ExtensionTypes = append(out.ExtensionTypes, aipapi.ExtensionRequirement{Namespace: e.Namespace, Type: e.Type, Implementor: e.Implementor})
	}
	return aipapi.NormalizeDialectSupport(out)
}

func capsHas(caps aipapi.BackendCaps, targetCap aipapi.Capability) bool {
	if caps == nil {
		return false
	}
	_, ok := caps[targetCap]
	return ok
}
