package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	FactoryKind = "claudeagentacp"
	// ModelNamespace preserves provider identity independently of the backend instance.
	ModelNamespace = "anthropic/"
)

// Service exposes connector identity without enabling an uncertified runtime.
type Service struct{ deps *engineDeps }

func New() *Service { return &Service{} }

func (*Service) Describe(context.Context) (backendplugin.PluginDescriptor, error) {
	return backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		// The proxy-owned session id names the conversation the agent process
		// serves. The host injects it only when the connector advertises the
		// feature, so the connector can key one agent process per conversation
		// instead of one per request.
		ProtocolMinor: backendplugin.ProtocolMinorReasoningEffortValues,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureProxyOwnedSessionID},
			{Name: backendplugin.FeatureReasoningEffortValues},
		},
		PluginID: "com.aiproxer.backend.claudeagentacp",
		Version:  "0.1.0",
		BuildID:  "localdev",
		Factories: []backendplugin.FactoryDescriptor{{
			Kind:                     FactoryKind,
			DisplayName:              "Claude Agent ACP",
			Description:              "Local Claude Agent ACP connector; model namespace " + ModelNamespace,
			CredentialMode:           backendplugin.CredentialModeStatic,
			AccessScope:              backendplugin.AccessScopeLocalOnly,
			RoutePrefixes:            []string{FactoryKind},
			SupportsDynamicInventory: true,
			ProcessSharing:           backendplugin.ProcessSharingPerInstance,
			Experimental:             true,
			StaticCapabilities:       backendplugin.CapabilitySummary{Streaming: true, Reasoning: true},
		}},
	}, nil
}

// Configure validates immutable operator settings and certifies the deployment.
// Agent processes remain lazy; the configured engine owns the existing pool.
func (s *Service) Configure(ctx context.Context, req backendplugin.ConfigureRequest) (backendplugin.ConfiguredInstance, error) {
	if req.FactoryKind != "" && req.FactoryKind != FactoryKind {
		return nil, fmt.Errorf("claudeagentacp: unexpected factory kind %q", req.FactoryKind)
	}
	cfg, err := ParseConfigYAML(req.ConfigYAML)
	if err != nil {
		return nil, err
	}
	secrets := make(map[string]string, len(req.Secrets.Values))
	for key, value := range req.Secrets.Values {
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

func (i *instance) Resolve(_ context.Context, modelID *string) (backendplugin.ResolvedProfile, error) {
	i.eng.mu.RLock()
	defer i.eng.mu.RUnlock()
	// The existing ABI has no provider-specific fields; carry safe disclosure in
	// its evidence string rather than expanding the public plugin contract.
	evidence, err := json.Marshal(i.eng.disclosure)
	if err != nil {
		return backendplugin.ResolvedProfile{}, err
	}
	profile := backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{Streaming: true, Reasoning: true, OpaqueExtensions: true},
		DialectSupport: backendplugin.DialectSupport{ExtensionTypes: []backendplugin.ExtensionRequirementDTO{
			{Namespace: "call", Type: "workspace_path"},
		}},
		SupportsDynamicInventory: true, RoutePrefixes: []string{FactoryKind}, EvidenceSource: string(evidence), ProfileVersion: i.eng.profileID,
	}
	// Provider-owned graded effort facts are read-only and model-specific.
	// Model-agnostic queries, aliases, undiscovered models, and snapshots
	// without a graded fact publish nothing (Unknown). Completeness of the
	// upstream supportedEffortLevels list is not proven, so members are
	// advertised with exhaustive=false; all other values stay Unknown.
	if modelID != nil {
		if requested := strings.TrimSpace(*modelID); requested != "" && requested != "auto" {
			if levels, ok := i.eng.spec.GradedEffortLevels(requested); ok {
				decl := &backendplugin.ReasoningEffortValuesV1{ModelID: requested, Supported: levels}
				if err := decl.Validate(); err == nil {
					profile.ReasoningEffortValues = decl
				}
			}
		}
	}
	return profile, nil
}

func (i *instance) ListModels(ctx context.Context, max uint32) (backendplugin.ListModelsResponse, error) {
	snapshot, err := i.eng.LoadModels(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	result := backendplugin.ListModelsResponse{InventorySource: string(snapshot.Source), FetchedUnixMS: snapshot.LoadedAt.UnixMilli()}
	for _, model := range snapshot.Models {
		result.Models = append(result.Models, backendplugin.ModelDescriptor{
			CanonicalModelID: model.CanonicalID,
			NativeModelID:    model.NativeID,
			DisplayName:      model.DisplayName,
			FactoryKind:      FactoryKind,
			// The connector designates its current model as the instance's
			// default, so the host derives its default route from that
			// designation instead of the enumeration order.
			IsDefault:    model.IsDefault,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true, Reasoning: true},
		})
		if max > 0 && uint32(len(result.Models)) >= max {
			break
		}
	}
	return result, nil
}

func (i *instance) Close(context.Context) error { return i.eng.Close() }

func (i *instance) Execute(stream backendplugin.ExecuteStream) error {
	return backendplugin.ForwardExecute(stream, func(ctx context.Context, _ backendplugin.Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error) {
		return i.eng.Open(ctx, &call)
	})
}

var _ backendplugin.Service = (*Service)(nil)
