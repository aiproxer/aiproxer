package backendplugin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// RequireExactOpenResponsesABISupport rejects calls that require exact OpenAI
// Responses semantics (PromptCacheKey, inline FileData, opaque extension content
// parts, reasoning Summary/Content/EncryptedContent presence, compaction
// EncryptedContent) when negotiation did not reach minor 3 with the feature
// enabled. Plain calls without those semantics remain backward-compatible.
func RequireExactOpenResponsesABISupport(neg Negotiation, call aipapi.Call) error {
	if !callRequiresExactOpenResponses(call) || promptCacheAliasUsesSemanticCarrier(neg, call) {
		return nil
	}
	return requireExactOpenResponsesNegotiation(neg)
}

func promptCacheAliasUsesSemanticCarrier(neg Negotiation, call aipapi.Call) bool {
	return strings.TrimSpace(call.PromptCacheKey) != "" && len(call.SemanticExtensions) == 0 && ProxyOwnedSemanticExtensionsSupported(neg)
}

// RequireSemanticExtensionsABISupport rejects a required residual carrier when
// the negotiated ABI cannot carry its exact identity/presence/data.
func RequireSemanticExtensionsABISupport(neg Negotiation, call aipapi.Call) error {
	if len(call.SemanticExtensions) == 0 {
		return nil
	}
	if ProxyOwnedSemanticExtensionsSupported(neg) {
		return nil
	}
	return fmt.Errorf("%w: semantic carrier requires minor %d and feature %q", ErrExactOpenResponsesUnsupported, ProtocolMinorSemanticExtensions, FeatureSemanticExtensions)
}

func ProxyOwnedSemanticExtensionsSupported(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorSemanticExtensions && slices.Contains(neg.EnabledFeatures, FeatureSemanticExtensions)
}

// RequireExactOpenResponsesEventABISupport rejects canonical events that carry
// exact OpenAI Responses reasoning fields when negotiation cannot represent them,
// so hosts never silently drop encrypted content or summary/content presence.
func RequireExactOpenResponsesEventABISupport(neg Negotiation, ev *CanonicalEvent) error {
	if !eventRequiresExactOpenResponses(ev) {
		return nil
	}
	return requireExactOpenResponsesNegotiation(neg)
}

func requireExactOpenResponsesNegotiation(neg Negotiation) error {
	if !neg.Compatible {
		return fmt.Errorf("%w: negotiation incompatible", ErrExactOpenResponsesUnsupported)
	}
	if neg.NegotiatedMinor < ProtocolMinorExactOpenResponsesFields {
		return fmt.Errorf("%w: negotiated minor %d", ErrExactOpenResponsesUnsupported, neg.NegotiatedMinor)
	}
	if slices.Contains(neg.EnabledFeatures, FeatureExactOpenResponsesFields) {
		return nil
	}
	return fmt.Errorf("%w: feature %q not enabled", ErrExactOpenResponsesUnsupported, FeatureExactOpenResponsesFields)
}

// ProxyOwnedSessionIDSupported reports whether the negotiated ABI can carry
// proxy-owned session authority. It is intentionally optional: an older
// connector must continue the request without native compaction state.
func ProxyOwnedSessionIDSupported(neg Negotiation) bool {
	if !neg.Compatible || neg.NegotiatedMinor < ProtocolMinorProxyOwnedSessionID {
		return false
	}
	return slices.Contains(neg.EnabledFeatures, FeatureProxyOwnedSessionID)
}

func callRequiresExactOpenResponses(call aipapi.Call) bool {
	// PromptCacheKey is bridged to semantic_extensions_v1 when that explicit
	// carrier is present; otherwise retain the v1.3 compatibility gate.
	if strings.TrimSpace(call.PromptCacheKey) != "" && len(call.SemanticExtensions) == 0 {
		return true
	}
	for i := range call.Instructions {
		if slices.ContainsFunc(call.Instructions[i].Parts, partRequiresExactOpenResponses) {
			return true
		}
	}
	for i := range call.Messages {
		if slices.ContainsFunc(call.Messages[i].Parts, partRequiresExactOpenResponses) {
			return true
		}
	}
	return slices.ContainsFunc(call.Items, itemRequiresExactOpenResponses)
}

func partRequiresExactOpenResponses(p aipapi.Part) bool {
	return p.Kind == aipapi.PartReasoning && aipapi.ReasoningHasExactResponsesFields(p.Reasoning)
}

func itemRequiresExactOpenResponses(item aipapi.Item) bool {
	if item.Reasoning != nil && item.Reasoning.Reasoning != nil && aipapi.ReasoningHasExactResponsesFields(item.Reasoning.Reasoning) {
		return true
	}
	if item.Compaction != nil && strings.TrimSpace(item.Compaction.EncryptedContent) != "" {
		return true
	}
	if itemContentRequiresExactOpenResponses(item.Content) {
		return true
	}
	if item.ToolResult != nil && itemContentRequiresExactOpenResponses(item.ToolResult.Parts) {
		return true
	}
	return false
}

func itemContentRequiresExactOpenResponses(parts []aipapi.ContentPart) bool {
	for _, cp := range parts {
		switch cp.Kind {
		case aipapi.ContentPartExtension:
			return true
		case aipapi.ContentPartFileRef:
			if cp.FileData != "" {
				return true
			}
		case aipapi.ContentPartReasoning:
			if aipapi.ReasoningHasExactResponsesFields(cp.Reasoning) {
				return true
			}
		}
	}
	return false
}

func eventRequiresExactOpenResponses(ev *CanonicalEvent) bool {
	if ev == nil {
		return false
	}
	return ev.ReasoningSummary.State() != RawJSONAbsent ||
		ev.ReasoningContent.State() != RawJSONAbsent ||
		ev.ReasoningEncryptedContent.State() != RawJSONAbsent
}
