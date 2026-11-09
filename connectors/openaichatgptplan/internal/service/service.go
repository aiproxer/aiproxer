package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Service is the backend-plugin service of the openai-chatgpt-plan connector.
//
// The service owns exactly one export: the direct-inference factory. The
// SIWC-backed Codex app-server factory is reserved for the later
// agent-runtime phase and is deliberately absent here, so a deployment can
// never select it by accident.
type Service struct {
	// referenceBaseURL is the TEST-ONLY reference base URL of the Responses
	// transport. It is empty for the shipped constructor, and no configuration
	// value can set it: NewWithReferenceEndpoint is the only writer, and
	// ParseConfigYAML has no endpoint key.
	referenceBaseURL string
}

// New returns the connector service.
//
// The shipped service always sends to the documented public Responses resource;
// its configuration surface has no endpoint key.
func New() *Service { return &Service{} }

// NewWithReferenceEndpoint returns the connector service with its Responses
// transport addressed to a LOCAL REFERENCE SERVER.
//
// This is the connector's TEST-ONLY construction seam: the direct contract
// tests and the staged host integration use it to observe the exact wire
// request without a live account. It is NOT reachable from YAML or Configure -
// ParseConfigYAML has no endpoint key and refuses responses_base_url - and the
// shipped command (cmd/aip-backend-openaichatgptplan) calls New. The reference
// endpoint must still be the documented public origin or a loopback IP literal,
// the transport's own narrow allowance.
func NewWithReferenceEndpoint(baseURL string) (*Service, error) {
	if _, err := responses.EndpointForBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("openai-chatgpt-plan: the reference endpoint is unusable: %w", err)
	}
	return &Service{referenceBaseURL: baseURL}, nil
}

// Describe reports the plugin identity and the factory exports of this
// connector.
//
// What this build provides is the factory boundary, the account-scoped model
// inventory and the public Responses execution path: the connector is
// describable, negotiable and configurable, it lists the selected
// registration's public models, and it serves an attempt through the
// stream-only public route with an explicit failure when no registration can
// authorize it.
//
// The security posture below is the connector's only security declaration.
// The effective credential is the selected user's SIWC OAuth profile, so the
// export is user-scoped (oauth_user) and local-only: a personal ChatGPT-plan
// credential must never become an operator-wide credential that unrelated
// downstream principals can multiplex through a shared proxy.
//
// The connector implements no single-user/server-mode check and is never
// told the deployment access mode. Multi-user rejection is provided once by
// the generic host backend security-profile gate, which runs before backend
// construction, Configure, and any child-process launch, and by the
// host-owned multi-user approval registry, where this kind is absent. Absence
// from that registry is the default deny; the connector neither appears in it
// nor tries to override it.
//
// The capability summary declares the surface SIWC supports and that this
// factory provides. It is a declaration of the backend's contract: the
// execution path serves both downstream delivery modes over the one streaming
// public route, and the account-scoped inventory is read from the selected
// registration's public catalog. Nothing outside that declared
// surface is advertised - reasoning replay, compaction, vision and document
// input, and API-dollar charge finalization are not part of this factory's
// contract, and the optional ABI features this build cannot exercise are not
// advertised either. Completed-stream token usage does flow through the
// host-only V1 accounting evidence sideband; that is provider-reported
// evidence, not a price.
func (s *Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: backendplugin.ProtocolMajorV1,
		ProtocolMinor: backendplugin.ProtocolMinorCancellationHandshake,
		PluginID:      PluginID,
		Version:       Version,
		BuildID:       BuildID,
		// The cancellation handshake is implemented by the shared backend-plugin
		// execute helper this connector runs. The accounting evidence sideband
		// is implemented by the managed stream: the completed answer's usage is
		// bridged once through the existing V1 host-only record.
		//
		// semantic_extensions_v1 is the bounded residual carrier the preview
		// policy consumes: a client prompt-cache hint reaches the policy as one
		// carrier, so interoperable posture can omit it explicitly (or strict
		// posture can refuse it) instead of the ABI rejecting the attempt
		// before policy. Only the recognized prompt-cache alias is omitted;
		// every other residual semantic remains a projector refusal.
		//
		// exact_openresponses_fields is deliberately NOT advertised: the SIWC
		// preview route does not accept exact Responses fields, so claiming
		// that feature would promise semantics this build cannot honor. Ordered
		// items and proxy-owned session authority are likewise not advertised.
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureCancellationHandshake},
			{Name: backendplugin.FeatureAccountingEvidence},
			{Name: backendplugin.FeatureSemanticExtensions},
		},
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:        FactoryKind,
			DisplayName: DisplayName,
			Description: Description,
			// The effective credential is one human user's SIWC profile, so the
			// export is user-scoped and stays local_only.
			CredentialMode:           backendplugin.CredentialModeOAuthUser,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			StaticCapabilities: backendplugin.CapabilitySummary{
				Streaming: true,
				Tools:     true,
				// The static descriptor MIRRORS the runtime capability; it is not
				// admission authority. Runtime admission reads the resolved
				// profile below, including the exact call extension.
				OpaqueExtensions: true,
			},
			TransportCapabilities: backendplugin.TransportCapabilitySummary{
				Cancellation:        true,
				BidirectionalStream: true,
			},
		}},
	}, nil
}

// Configure creates the configured instance of the direct factory.
//
// Configure validates the requested factory kind and parses the opaque
// configuration subtree. It performs no access-mode decision: the connector
// has no local eligibility authority, and the host decides multi-user
// eligibility before it ever calls Configure.
//
// The export carries no host-delivered credential, so a delivered secret is
// refused rather than silently ignored: the effective credential is the
// selected user's SIWC profile, and accepting a token here would hide which
// credential the factory actually uses.
func (s *Service) Configure(_ context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	kind := strings.TrimSpace(req.FactoryKind)
	if kind != FactoryKind {
		return nil, fmt.Errorf(
			"openai-chatgpt-plan: unsupported factory kind %q; this connector exports only %q",
			req.FactoryKind, FactoryKind,
		)
	}
	if len(req.Secrets.Values) > 0 {
		return nil, fmt.Errorf(
			"openai-chatgpt-plan: this export carries no host-delivered credential; the effective credential is the selected user's SIWC profile store",
		)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	authority, err := newProfileAuthority(cfg)
	if err != nil {
		return nil, err
	}
	inventory, err := newInventoryFromConfig(authority)
	if err != nil {
		return nil, err
	}
	referenceBaseURL := ""
	if s != nil {
		referenceBaseURL = s.referenceBaseURL
	}
	execution, err := newExecutionFromConfig(cfg, authority, req.Negotiation, referenceBaseURL)
	if err != nil {
		return nil, err
	}
	return &instance{cfg: cfg, inventory: inventory, execution: execution}, nil
}

// instance is the configured direct factory.
type instance struct {
	// cfg is the validated per-instance configuration. Keeping it here
	// makes Configure the single place where the configuration is
	// validated.
	cfg Config
	// inventory is the account-scoped model inventory of the selected
	// registration. It is nil when the configuration names no registration
	// store, and ListModels then reports the missing store explicitly.
	inventory *profileInventory
	// execution is the direct factory's provider transport, bound to the same
	// registration authority as the inventory. It is nil when the
	// configuration names no registration store, and Execute then refuses the
	// attempt before any provider I/O.
	execution *execution
}

// Resolve reports the capability profile of the configured instance.
//
// The resolved profile is the runtime admission authority: generic core admits
// a call only when the profile declares both opaque-extension capability and
// the EXACT call extension that carries the preserved hosted web-search
// declaration. The static FactoryDescriptor mirrors the capability bit, but a
// descriptor alone cannot admit the carrier because it carries no exact
// extension identity.
func (i *instance) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{
			Streaming: true,
			Tools:     true,
			// The connector consumes the preserved hosted-declaration carrier
			// deliberately (omit in interoperable posture, refuse in strict
			// posture), so it supports the generic opaque-extension seam.
			OpaqueExtensions: true,
		},
		DialectSupport: backendplugin.DialectSupport{
			ExtensionTypes: []backendplugin.ExtensionRequirementDTO{{
				Namespace: "call",
				Type:      responses.WebSearchDeclarationsExtensionKey,
			}},
		},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{
			Cancellation:        true,
			BidirectionalStream: true,
		},
		SupportsDynamicInventory: true,
		RoutePrefixes:            []string{FactoryKind},
		EvidenceSource:           FactoryKind,
		ProfileVersion:           "1",
	}, nil
}

// Close releases the instance resources. The direct factory owns no child
// process and no open upstream connection at this point.
func (i *instance) Close(context.Context) error { return nil }
