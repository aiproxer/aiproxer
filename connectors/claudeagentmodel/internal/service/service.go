package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const PluginID = "com.aiproxer.backend.claudeagentmodel"

// ConnectorVersion is the single release identity shared by Describe,
// release.yaml and the packaged manifest template.
const ConnectorVersion = "0.1.0"

// descriptorMinor is the latest supported ABI minor. The array length is a
// compile-time guard that it never regresses below the cancellation-handshake
// minor the descriptor also advertises.
const descriptorMinor = backendplugin.ProtocolMinorReasoningEffortValues

var _ [descriptorMinor - backendplugin.ProtocolMinorCancellationHandshake]struct{}

// Service exposes the optional connector before an instance is configured.
type Service struct {
	deps *engineDeps
}

func New() *Service { return &Service{} }

func (*Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		// The proxy-owned session id names the conversation a sidecar serves and
		// binds a parked tool turn to its continuation authority.
		ProtocolMinor: descriptorMinor,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureCancellationHandshake},
			{Name: backendplugin.FeatureReasoningEffortValues},
			{Name: backendplugin.FeatureExactOpenResponsesFields},
		},
		PluginID: PluginID,
		Version:  ConnectorVersion,
		BuildID:  "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     FactoryKind,
			DisplayName:              "Claude Agent Model",
			Description:              "Local Claude Agent SDK model connector; model namespace " + ModelNamespace,
			CredentialMode:           backendplugin.CredentialModeOAuthUser,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			Experimental:             true,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Tools: true, OrderedItems: true},
			TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true},
		}},
	}, nil
}

func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("claudeagentmodel: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	secrets := make(map[string]string, len(req.Secrets.Values))
	for key, value := range req.Secrets.Values {
		if strings.TrimSpace(key) == "" {
			continue
		}
		secrets[key] = string(value)
	}
	deps := productionDeps()
	if s != nil && s.deps != nil {
		deps = *s.deps
	}
	eng, err := newEngine(ctx, cfg, secrets, deps)
	if err != nil {
		return nil, err
	}
	return &instance{eng: eng}, nil
}

type instance struct{ eng *engine }

func (i *instance) Resolve(ctx context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	return i.eng.profile(ctx, modelID)
}

func (i *instance) ListModels(ctx context.Context, max uint32) (backendplugin.ListModelsResponse, error) {
	return i.eng.listModels(ctx, max)
}

func (i *instance) Close(ctx context.Context) error { return i.eng.Close(ctx) }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, _ backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		return i.eng.Open(ctx, &call)
	})
}

var _ backendplugin.Service = (*Service)(nil)
