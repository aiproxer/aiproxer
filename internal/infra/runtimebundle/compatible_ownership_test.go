package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/internal/testkit"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

// Task 2.3: generation-scoped ownership integration through runtimebundle composition.

func TestOwnership_ManifestKindBlocksGenericPrefix_NoLaunch(t *testing.T) {
	t.Parallel()

	const (
		manifestKind  = "openrouter"
		genericPrefix = "openrouter"
		genericInst   = "compat-openrouter"
	)

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	launcher := &processhost.TestLauncher{PID: 9201}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	// Manifest-available registration only: factory kind is catalogued without
	// configuring/activating an external instance.
	if err := reg.RegisterDiscoveredBackend(manifestKind, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		t.Fatal("manifest-only ownership must not build/activate the external factory")
		return execbackend.Backend{}, errors.New("unexpected build")
	}, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialNone,
		AccessScope:    pluginreg.BackendAccessAny,
	}, pluginreg.BackendReloadPolicy{AllowsCandidateOverlap: true}); err != nil {
		t.Fatal(err)
	}

	cfg := ownershipCandidateConfig(t, []config.PluginConfig{{
		Kind:    standardplugins.CustomOpenAILegacyCompatibleID,
		ID:      genericInst,
		Enabled: true,
		Config: genYAMLNode(t, ""+
			"backend_prefix: "+genericPrefix+"\n"+
			"base_url: http://127.0.0.1:9/v1\n"),
	}})

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if bundle != nil {
		t.Cleanup(func() { _ = bundle.Close() })
		t.Fatal("expected ownership collision to block GenerationRuntime publication")
	}
	assertOwnershipCollision(t, err, genericPrefix, manifestKind, genericInst)
	if launcher.Launches.Load() != 0 {
		t.Fatalf("process launches=%d, want 0 (manifest-only, no activation)", launcher.Launches.Load())
	}
}

func TestOwnership_FakeResolvedPrefixBlocksPublication(t *testing.T) {
	t.Parallel()

	const (
		extKind        = "vllm"
		extInst        = "ext-vllm-1"
		genericPrefix  = "local-openai"
		genericInst    = "compat-local"
		resolvedPrefix = "local-openai"
	)

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	launcher := &processhost.TestLauncher{PID: 9202}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind: extKind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "ownership-resolved-digest"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{
		DialSession: func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
			inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
				InstanceID:    req.InstanceID,
				FactoryKind:   req.FactoryKind,
				ConfigYAML:    req.ConfigYAML,
				Secrets:       req.Secrets,
				Negotiation:   backendplugin.Negotiation{Compatible: true},
				RuntimePolicy: req.Policy,
			})
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			return inst, backendplugin.ResolvedProfile{
				RoutePrefixes:            []string{resolvedPrefix},
				Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
				TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true},
				SupportsCountTokens:      true,
				SupportsFinalizeBilling:  true,
				SupportsDynamicInventory: true,
				EvidenceSource:           "fake-ownership",
				ProfileVersion:           "1",
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := ownershipCandidateConfig(t, []config.PluginConfig{
		{
			Kind:    standardplugins.CustomOpenAIResponsesCompatibleID,
			ID:      genericInst,
			Enabled: true,
			Config: genYAMLNode(t, ""+
				"backend_prefix: "+genericPrefix+"\n"+
				"base_url: http://127.0.0.1:9/v1\n"),
		},
		{
			Kind:    extKind,
			ID:      extInst,
			Enabled: true,
			Config:  genYAMLNode(t, "x: 1\n"),
		},
	})

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if bundle != nil {
		t.Cleanup(func() { _ = bundle.Close() })
		t.Fatal("expected resolved-prefix ownership collision before GenerationRuntime publication")
	}
	assertOwnershipCollision(t, err, resolvedPrefix, genericInst, extInst)
	if launcher.Launches.Load() < 1 {
		t.Fatalf("expected fake activation before collision gate, launches=%d", launcher.Launches.Load())
	}
}

func TestOwnership_CleanComposition_OK(t *testing.T) {
	t.Parallel()

	const (
		extKind        = "ollama"
		extInst        = "ext-ollama"
		genericPrefix  = "provider-a"
		genericInst    = "compat-a"
		resolvedPrefix = "ollama-local"
	)

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	launcher := &processhost.TestLauncher{PID: 9203}
	host := processhost.NewHost(processhost.Config{
		Launcher: launcher,
		Channel:  &processhost.TestChannel{},
	})
	t.Cleanup(func() { _ = host.Close() })

	fake := &bpkit.FakeService{Mode: bpkit.ModeValid}
	if err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
		Kind: extKind,
		Profile: pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialNone,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		Artifact: &trust.VerifiedArtifact{DigestHex: "ownership-clean-digest"},
		Model:    processhost.ProcessModelPerInstance,
	}}, runtimebundle.DiscoveredInstallOptions{
		DialSession: func(ctx context.Context, req runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
			inst, err := fake.Configure(ctx, backendplugin.ConfigureRequest{
				InstanceID:    req.InstanceID,
				FactoryKind:   req.FactoryKind,
				ConfigYAML:    req.ConfigYAML,
				Secrets:       req.Secrets,
				Negotiation:   backendplugin.Negotiation{Compatible: true},
				RuntimePolicy: req.Policy,
			})
			if err != nil {
				return nil, backendplugin.ResolvedProfile{}, err
			}
			return inst, backendplugin.ResolvedProfile{
				RoutePrefixes:            []string{resolvedPrefix},
				Capabilities:             backendplugin.CapabilitySummary{Streaming: true},
				TransportCapabilities:    backendplugin.TransportCapabilitySummary{Cancellation: true},
				SupportsCountTokens:      true,
				SupportsFinalizeBilling:  true,
				SupportsDynamicInventory: true,
				EvidenceSource:           "fake-ownership-clean",
				ProfileVersion:           "1",
			}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	cfg := ownershipCandidateConfig(t, []config.PluginConfig{
		{
			Kind:    standardplugins.CustomAnthropicCompatibleID,
			ID:      genericInst,
			Enabled: true,
			Config: genYAMLNode(t, ""+
				"backend_prefix: "+genericPrefix+"\n"+
				"base_url: http://127.0.0.1:9\n"),
		},
		{
			Kind:    extKind,
			ID:      extInst,
			Enabled: true,
			Config:  genYAMLNode(t, "x: 1\n"),
		},
	})

	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  processBaseConfig(),
		Log:  testkit.DiscardLogger(),
		Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	bundle, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: cfg,
		Compose:   stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("clean composition must publish: %v", err)
	}
	if bundle == nil {
		t.Fatal("expected GenerationRuntime")
	}
	t.Cleanup(func() { _ = bundle.Close() })
}

func ownershipCandidateConfig(t *testing.T, backends []config.PluginConfig) *config.Config {
	t.Helper()
	cfg := processBaseConfig()
	cfg.Plugins.Backends = backends
	cfg.Plugins.Frontends = []config.PluginConfig{{ID: "openai-responses", Enabled: true}}
	if len(backends) > 0 {
		cfg.Routing.DefaultRoute = backends[0].InstanceID() + ":stub-default"
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate ownership candidate: %v", err)
	}
	return cfg
}

func assertOwnershipCollision(t *testing.T, err error, key string, ownerA, ownerB string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected ownership collision error")
	}
	var coll *pluginreg.OwnershipCollisionError
	if !errors.As(err, &coll) {
		t.Fatalf("error type %T (%v) is not OwnershipCollisionError", err, err)
	}
	if coll.Key != key {
		t.Fatalf("collision key = %q, want %q", coll.Key, key)
	}
	msg := coll.Error()
	if !strings.Contains(msg, ownerA) || !strings.Contains(msg, ownerB) {
		t.Fatalf("collision error %q must identify owners %q and %q", msg, ownerA, ownerB)
	}
}
