package openaicompat

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type capsResolverFunc func(ctx context.Context, model string, cand routing.AttemptCandidate) aipapi.BackendCaps

// OpenAIResponsesProfileID is the certified profile identifier for OpenAI Responses (Requirement 4, 8).
const OpenAIResponsesProfileID = "openai_responses_v1"

// OpenAIChatProfileID is the certified profile identifier for OpenAI Chat (Requirements 4, 8, 17).
const OpenAIChatProfileID = "openai_chat_v1"

// OpenResponsesProfileID is the certified profile identifier for OpenResponses (Requirements 4, 8, 17.2).
const OpenResponsesProfileID = "openresponses_v1"

func staticModelsFromInventory(inv modelinventory.Provider) (map[string]struct{}, bool) {
	if inv == nil {
		return nil, false
	}
	sp, ok := inv.(modelinventory.StaticProvider)
	if !ok || len(sp.Models) == 0 {
		return nil, false
	}
	models := make(map[string]struct{}, len(sp.Models)*2)
	for _, m := range sp.Models {
		if c := strings.TrimSpace(m.CanonicalID); c != "" {
			models[c] = struct{}{}
		}
		if n := strings.TrimSpace(m.NativeID); n != "" {
			models[n] = struct{}{}
		}
	}
	return models, true
}

func isBackendUniversal(spec BackendSpec, inv modelinventory.Provider) bool {
	if spec.WireDomainAnyAcceptedModel != nil {
		return *spec.WireDomainAnyAcceptedModel
	}
	_, hasStatic := staticModelsFromInventory(inv)
	return !hasStatic
}

func resolveResponsesWireRequest(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
	resolveCaps capsResolverFunc,
) largebody.WireRequestSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorResponses {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != aipapi.OperationOpenAIResponses {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenAIResponsesProfileID {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != aipapi.DeliveryModeStreaming {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}

	targetModel := strings.TrimSpace(facts.CandidateModel)
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.WireModel())
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.Model)
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(facts.ClientModel)
	}
	if targetModel == "" {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonModelUnsupported,
		}
	}

	staticModels, hasStatic := staticModelsFromInventory(inv)
	if hasStatic {
		if _, ok := staticModels[targetModel]; !ok {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonModelUnsupported,
			}
		}
	}

	needsRewrite := targetModel != strings.TrimSpace(facts.ClientModel)
	if needsRewrite && !facts.Rewrite.NeedsModelRewrite() {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	if len(facts.RequiredCapabilities) > 0 && resolveCaps != nil {
		backendCaps := resolveCaps(ctx, targetModel, cand)
		if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonCapabilityUnsupported,
			}
		}
	}

	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: needsRewrite,
		Reason:            largebody.WireSupportReasonNone,
	}
}

func resolveResponsesWireDomain(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireDomainFacts,
	resolveCaps capsResolverFunc,
) largebody.WireDomainSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorResponses {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != aipapi.OperationOpenAIResponses {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenAIResponsesProfileID {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != aipapi.DeliveryModeStreaming {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}
	if facts.Rewrite.Kind() != largebody.RewriteKindNone && facts.Rewrite.Kind() != largebody.RewriteKindModelToken {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	universal := isBackendUniversal(spec, inv)
	if facts.UniversalModel {
		if !universal {
			return largebody.WireDomainSupport{
				Compatible:       false,
				AnyAcceptedModel: false,
				Reason:           largebody.WireSupportReasonModelUnsupported,
			}
		}
		if len(facts.RequiredCapabilities) > 0 {
			staticModels, hasStatic := staticModelsFromInventory(inv)
			if hasStatic && resolveCaps != nil {
				for m := range staticModels {
					backendCaps := resolveCaps(ctx, m, routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID, Model: m}})
					if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
						return largebody.WireDomainSupport{
							Compatible:       false,
							AnyAcceptedModel: false,
							Reason:           largebody.WireSupportReasonCapabilityUnsupported,
						}
					}
				}
			} else {
				if resolveCaps != nil {
					backendCaps := resolveCaps(ctx, "", routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID}})
					if backendCaps == nil || aipapi.Negotiate(facts.RequiredCapabilities, backendCaps).Kind != aipapi.NegotiationLossless {
						return largebody.WireDomainSupport{
							Compatible:       false,
							AnyAcceptedModel: false,
							Reason:           largebody.WireSupportReasonCapabilityUnsupported,
						}
					}
				}
				for _, c := range facts.RequiredCapabilities {
					if c != aipapi.CapabilityStreaming {
						return largebody.WireDomainSupport{
							Compatible:       false,
							AnyAcceptedModel: false,
							Reason:           largebody.WireSupportReasonCapabilityUnsupported,
						}
					}
				}
			}
		}
		return largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: true,
			Reason:           largebody.WireSupportReasonNone,
		}
	}

	// Finite model domain: check every candidate model against declared static inventory
	if !universal {
		staticModels, _ := staticModelsFromInventory(inv)
		for _, m := range facts.CandidateModels {
			m = strings.TrimSpace(m)
			if _, ok := staticModels[m]; !ok {
				return largebody.WireDomainSupport{
					Compatible:       false,
					AnyAcceptedModel: false,
					Reason:           largebody.WireSupportReasonModelUnsupported,
				}
			}
		}
	}
	if len(facts.RequiredCapabilities) > 0 && resolveCaps != nil {
		for _, m := range facts.CandidateModels {
			m = strings.TrimSpace(m)
			backendCaps := resolveCaps(ctx, m, routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID, Model: m}})
			if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
				return largebody.WireDomainSupport{
					Compatible:       false,
					AnyAcceptedModel: false,
					Reason:           largebody.WireSupportReasonCapabilityUnsupported,
				}
			}
		}
	}
	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: false,
		Reason:           largebody.WireSupportReasonNone,
	}
}

func resolveOpenResponsesWireRequest(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
	resolveCaps capsResolverFunc,
) largebody.WireRequestSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorResponses {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != aipapi.OperationOpenResponsesCreate {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenResponsesProfileID {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != aipapi.DeliveryModeStreaming {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}

	targetModel := strings.TrimSpace(facts.CandidateModel)
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.WireModel())
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.Model)
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(facts.ClientModel)
	}
	if targetModel == "" {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonModelUnsupported,
		}
	}

	staticModels, hasStatic := staticModelsFromInventory(inv)
	if hasStatic {
		if _, ok := staticModels[targetModel]; !ok {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonModelUnsupported,
			}
		}
	}

	needsRewrite := targetModel != strings.TrimSpace(facts.ClientModel)
	if needsRewrite && !facts.Rewrite.NeedsModelRewrite() {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	if len(facts.RequiredCapabilities) > 0 && resolveCaps != nil {
		backendCaps := resolveCaps(ctx, targetModel, cand)
		if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonCapabilityUnsupported,
			}
		}
	}

	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: needsRewrite,
		Reason:            largebody.WireSupportReasonNone,
	}
}

func resolveOpenResponsesWireDomain(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireDomainFacts,
	resolveCaps capsResolverFunc,
) largebody.WireDomainSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorResponses {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != aipapi.OperationOpenResponsesCreate {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenResponsesProfileID {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != aipapi.DeliveryModeStreaming {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}
	if facts.Rewrite.Kind() != largebody.RewriteKindNone && facts.Rewrite.Kind() != largebody.RewriteKindModelToken {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	// Requirement 8, 17.2: Universal-only domain proof for OpenResponses.
	universal := isBackendUniversal(spec, inv)
	if !universal {
		return largebody.WireDomainSupport{
			Compatible:       false,
			AnyAcceptedModel: false,
			Reason:           largebody.WireSupportReasonModelUnsupported,
		}
	}

	if len(facts.RequiredCapabilities) > 0 {
		staticModels, hasStatic := staticModelsFromInventory(inv)
		if hasStatic && resolveCaps != nil {
			for m := range staticModels {
				backendCaps := resolveCaps(ctx, m, routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID, Model: m}})
				if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
					return largebody.WireDomainSupport{
						Compatible:       false,
						AnyAcceptedModel: false,
						Reason:           largebody.WireSupportReasonCapabilityUnsupported,
					}
				}
			}
		} else {
			if resolveCaps != nil {
				backendCaps := resolveCaps(ctx, "", routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID}})
				if backendCaps == nil || aipapi.Negotiate(facts.RequiredCapabilities, backendCaps).Kind != aipapi.NegotiationLossless {
					return largebody.WireDomainSupport{
						Compatible:       false,
						AnyAcceptedModel: false,
						Reason:           largebody.WireSupportReasonCapabilityUnsupported,
					}
				}
			}
			for _, c := range facts.RequiredCapabilities {
				if c != aipapi.CapabilityStreaming && c != aipapi.CapabilityOrderedItems {
					return largebody.WireDomainSupport{
						Compatible:       false,
						AnyAcceptedModel: false,
						Reason:           largebody.WireSupportReasonCapabilityUnsupported,
					}
				}
			}
		}
	}

	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: true,
		Reason:           largebody.WireSupportReasonNone,
	}
}

func resolveChatWireRequest(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireRequestFacts,
	cand routing.AttemptCandidate,
	resolveCaps capsResolverFunc,
) largebody.WireRequestSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorChat {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != aipapi.OperationOpenAIChatCompletions {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenAIChatProfileID {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != aipapi.DeliveryModeStreaming {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}

	targetModel := strings.TrimSpace(facts.CandidateModel)
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.WireModel())
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(cand.Primary.Model)
	}
	if targetModel == "" {
		targetModel = strings.TrimSpace(facts.ClientModel)
	}
	if targetModel == "" {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonModelUnsupported,
		}
	}

	staticModels, hasStatic := staticModelsFromInventory(inv)
	if hasStatic {
		if _, ok := staticModels[targetModel]; !ok {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonModelUnsupported,
			}
		}
	}

	needsRewrite := targetModel != strings.TrimSpace(facts.ClientModel)
	if needsRewrite && !facts.Rewrite.NeedsModelRewrite() {
		return largebody.WireRequestSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	if len(facts.RequiredCapabilities) > 0 && resolveCaps != nil {
		backendCaps := resolveCaps(ctx, targetModel, cand)
		if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonCapabilityUnsupported,
			}
		}
	}

	return largebody.WireRequestSupport{
		Compatible:        true,
		NeedsModelRewrite: needsRewrite,
		Reason:            largebody.WireSupportReasonNone,
	}
}

func resolveChatWireDomain(
	ctx context.Context,
	spec BackendSpec,
	inv modelinventory.Provider,
	facts largebody.WireDomainFacts,
	resolveCaps capsResolverFunc,
) largebody.WireDomainSupport {
	if ctx != nil && ctx.Err() != nil {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonUnsupported,
		}
	}
	if specFlavor(spec) != FlavorChat {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.Operation != aipapi.OperationOpenAIChatCompletions {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonOperationUnsupported,
		}
	}
	if facts.ProfileID != OpenAIChatProfileID {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonProfileUnsupported,
		}
	}
	if facts.Delivery != aipapi.DeliveryModeStreaming {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonDeliveryUnsupported,
		}
	}
	if facts.BodyMode != largebody.BodyModeIdentityJSON {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonBodyModeUnsupported,
		}
	}
	if facts.Rewrite.Kind() != largebody.RewriteKindNone && facts.Rewrite.Kind() != largebody.RewriteKindModelToken {
		return largebody.WireDomainSupport{
			Compatible: false,
			Reason:     largebody.WireSupportReasonRewriteUnsupported,
		}
	}

	universal := isBackendUniversal(spec, inv)
	if facts.UniversalModel {
		if !universal {
			return largebody.WireDomainSupport{
				Compatible:       false,
				AnyAcceptedModel: false,
				Reason:           largebody.WireSupportReasonModelUnsupported,
			}
		}
		if len(facts.RequiredCapabilities) > 0 {
			staticModels, hasStatic := staticModelsFromInventory(inv)
			if hasStatic && resolveCaps != nil {
				for m := range staticModels {
					backendCaps := resolveCaps(ctx, m, routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID, Model: m}})
					if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
						return largebody.WireDomainSupport{
							Compatible:       false,
							AnyAcceptedModel: false,
							Reason:           largebody.WireSupportReasonCapabilityUnsupported,
						}
					}
				}
			} else {
				if resolveCaps != nil {
					backendCaps := resolveCaps(ctx, "", routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID}})
					if backendCaps == nil || aipapi.Negotiate(facts.RequiredCapabilities, backendCaps).Kind != aipapi.NegotiationLossless {
						return largebody.WireDomainSupport{
							Compatible:       false,
							AnyAcceptedModel: false,
							Reason:           largebody.WireSupportReasonCapabilityUnsupported,
						}
					}
				}
				for _, c := range facts.RequiredCapabilities {
					if c != aipapi.CapabilityStreaming {
						return largebody.WireDomainSupport{
							Compatible:       false,
							AnyAcceptedModel: false,
							Reason:           largebody.WireSupportReasonCapabilityUnsupported,
						}
					}
				}
			}
		}
		return largebody.WireDomainSupport{
			Compatible:       true,
			AnyAcceptedModel: true,
			Reason:           largebody.WireSupportReasonNone,
		}
	}

	// Finite model domain: check every candidate model against declared static inventory
	if !universal {
		staticModels, _ := staticModelsFromInventory(inv)
		for _, m := range facts.CandidateModels {
			m = strings.TrimSpace(m)
			if _, ok := staticModels[m]; !ok {
				return largebody.WireDomainSupport{
					Compatible:       false,
					AnyAcceptedModel: false,
					Reason:           largebody.WireSupportReasonModelUnsupported,
				}
			}
		}
	}
	if len(facts.RequiredCapabilities) > 0 && resolveCaps != nil {
		for _, m := range facts.CandidateModels {
			m = strings.TrimSpace(m)
			backendCaps := resolveCaps(ctx, m, routing.AttemptCandidate{Primary: routing.Primary{Backend: spec.ID, Model: m}})
			if res := aipapi.Negotiate(facts.RequiredCapabilities, backendCaps); res.Kind != aipapi.NegotiationLossless {
				return largebody.WireDomainSupport{
					Compatible:       false,
					AnyAcceptedModel: false,
					Reason:           largebody.WireSupportReasonCapabilityUnsupported,
				}
			}
		}
	}

	return largebody.WireDomainSupport{
		Compatible:       true,
		AnyAcceptedModel: false,
		Reason:           largebody.WireSupportReasonNone,
	}
}

func specFlavor(spec BackendSpec) Flavor {
	if spec.Flavor != "" {
		return spec.Flavor
	}
	if spec.ResolveFlavor != nil {
		defer func() {
			_ = recover()
		}()
		return spec.ResolveFlavor(aipapi.Call{})
	}
	return FlavorChat
}

func attachWireProof(be execbackend.Backend, spec BackendSpec, pool *credpool.Pool, inv modelinventory.Provider) execbackend.Backend {
	flavor := specFlavor(spec)
	if flavor != FlavorResponses && flavor != FlavorChat {
		be.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
			return largebody.WireRequestSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonOperationUnsupported,
			}
		}
		be.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
			return largebody.WireDomainSupport{
				Compatible: false,
				Reason:     largebody.WireSupportReasonOperationUnsupported,
			}
		}
		return be
	}

	capsResolver := func(ctx context.Context, model string, cand routing.AttemptCandidate) aipapi.BackendCaps {
		if model != "" {
			cand.Primary.Model = model
		}
		if be.ResolveCaps != nil && be.ResolveWireCaps == nil {
			return nil
		}
		if be.ResolveWireCaps != nil {
			return be.ResolveWireCaps(ctx, cand)
		}
		if len(be.Caps) > 0 {
			return be.Caps
		}
		return openaicaps.ForHostedModelCompatibleReplay(model, be.BackendPrefixes)
	}

	if flavor == FlavorResponses {
		be.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
			if facts.Operation == aipapi.OperationOpenResponsesCreate {
				return resolveOpenResponsesWireRequest(ctx, spec, inv, facts, cand, capsResolver)
			}
			return resolveResponsesWireRequest(ctx, spec, inv, facts, cand, capsResolver)
		}
		be.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
			if facts.Operation == aipapi.OperationOpenResponsesCreate {
				return resolveOpenResponsesWireDomain(ctx, spec, inv, facts, capsResolver)
			}
			return resolveResponsesWireDomain(ctx, spec, inv, facts, capsResolver)
		}
	} else {
		be.ResolveWireRequest = func(ctx context.Context, facts largebody.WireRequestFacts, cand routing.AttemptCandidate) largebody.WireRequestSupport {
			return resolveChatWireRequest(ctx, spec, inv, facts, cand, capsResolver)
		}
		be.ResolveWireDomain = func(ctx context.Context, facts largebody.WireDomainFacts) largebody.WireDomainSupport {
			return resolveChatWireDomain(ctx, spec, inv, facts, capsResolver)
		}
	}

	fallback := spec.RateLimitFallback
	if fallback <= 0 {
		fallback = credpool.DefaultRateLimitFallback
	}
	prims := WireOpenPrimitives{
		ProviderID:           spec.ID,
		BaseURL:              spec.BaseURL,
		Flavor:               flavor,
		Pool:                 pool,
		HTTPClient:           spec.HTTPClient,
		RateLimitFallback:    fallback,
		QuotaSwitchThreshold: spec.QuotaSwitchThreshold,
		MaxPending:           50,
	}
	be.OpenWire = prims.OpenWire
	return be
}
