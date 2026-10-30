package standardplugins

import (
	frontanthropic "github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	frontgemini "github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	frontopenailegacy "github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	frontsystemone "github.com/aiproxer/aiproxer/internal/plugins/frontends/systemone"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"gopkg.in/yaml.v3"
)

// FrontendRouteClaims computes the normalized owner-aware route claims for one
// enabled frontend instance. instanceID is the immutable owner identity used
// for the route-ownership registry; cfg is the plugin-local config subtree.
// Re-exported from the cycle-neutral composition contract.
type FrontendRouteClaims = httpcontract.FrontendRouteClaims

// StandardFrontendRouteClaims returns the owner-aware route-claims providers
// for standard frontends that declare route ownership. The generic
// OpenResponses frontend declares its config-derived claims (base_path,
// WebSocket); the legacy openai-responses frontend declares its fixed /v1
// claims so canonical path takeover validation can detect base_path=/v1
// collisions before any ServeMux handler is mounted. Frontends without a
// provider participate in no route-ownership validation.
func StandardFrontendRouteClaims() map[string]FrontendRouteClaims {
	out := make(map[string]FrontendRouteClaims)
	for _, c := range standardFrontendContributions() {
		if c.routes != nil {
			out[c.id] = c.routes
		}
	}
	return out
}

// openResponsesFrontendRouteClaims decodes the generic OpenResponses frontend
// config and returns its owner-aware claims. The claims reflect the configured
// base_path (default /openresponses/v1) and whether the WebSocket route is
// enabled, so canonical takeover validation rejects a /v1 base_path that would
// collide with an already-owned legacy route.
func openResponsesFrontendRouteClaims(instanceID string, n yaml.Node) ([]httpcontract.RouteClaim, error) {
	cfg, err := frontopenresponses.DecodeConfig(n)
	if err != nil {
		return nil, err
	}
	return frontopenresponses.RouteClaimsForOwner(cfg, instanceID)
}

// openAIResponsesFrontendRouteClaims returns the fixed legacy OpenAI Responses
// default claims (/v1/responses plus cancel) for one instance, so a generic
// OpenResponses frontend configured at base_path=/v1 is rejected as a canonical
// takeover before mounting.
func openAIResponsesFrontendRouteClaims(instanceID string, _ yaml.Node) ([]httpcontract.RouteClaim, error) {
	return frontopenairesponses.RouteClaims(instanceID)
}

func staticFrontendRouteClaims(instanceID string, claims ...httpcontract.RouteClaim) ([]httpcontract.RouteClaim, error) {
	out := make([]httpcontract.RouteClaim, 0, len(claims))
	for _, claim := range claims {
		claim.OwnerID = instanceID
		normalized, err := claim.NormalizedClaim()
		if err != nil {
			return nil, err
		}
		out = append(out, normalized)
	}
	return out, nil
}

// These declarations mirror the concrete mounts and are the single composition
// source used by route characterization; callers never reconstruct a route by hand.
func openAILegacyFrontendRouteClaims(instanceID string, _ yaml.Node) ([]httpcontract.RouteClaim, error) {
	return frontopenailegacy.RouteClaims(instanceID)
}

func anthropicFrontendRouteClaims(instanceID string, _ yaml.Node) ([]httpcontract.RouteClaim, error) {
	return frontanthropic.RouteClaims(instanceID)
}

func geminiFrontendRouteClaims(instanceID string, _ yaml.Node) ([]httpcontract.RouteClaim, error) {
	return frontgemini.RouteClaims(instanceID)
}

func systemOneFrontendRouteClaims(instanceID string, n yaml.Node) ([]httpcontract.RouteClaim, error) {
	if _, err := frontsystemone.DecodeConfig(n); err != nil {
		return nil, err
	}
	return frontsystemone.RouteClaims(instanceID)
}
