package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"gopkg.in/yaml.v3"
)

// factoryProbe records every backend-factory construction so a test can prove the
// security gate rejected before any factory ran.
type factoryProbe struct{ built atomic.Int32 }

func (p *factoryProbe) factory(id string) pluginreg.BackendFactory {
	return func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		p.built.Add(1)
		return stubProbeBackend(id), nil
	}
}

func multiUserConfig(kind, instanceID string) *config.Config {
	return &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: kind, ID: instanceID, Enabled: true,
		}}},
	}
}

func multiUserBuildOptions(reg *pluginreg.Registry) *runtimebundle.BuildOptions {
	return &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
	}
}

// TestBuild_multiUserRejectsLocalOnlyBeforeBackendConstruction is the generic
// production-path regression: a local_only backend must fail startup/candidate
// compilation with the typed error and the backend factory must never run.
func TestBuild_multiUserRejectsLocalOnlyBeforeBackendConstruction(t *testing.T) {
	t.Parallel()
	const factoryID = "local-only-generic-probe"
	reg := pluginreg.NewRegistry()
	probe := &factoryProbe{}
	if err := reg.RegisterBackendWithProfilesAndSource(factoryID, probe.factory(factoryID),
		pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialStatic,
			AccessScope:    pluginreg.BackendAccessLocalOnly,
		},
		pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionAgentRuntime},
		pluginreg.BackendSourceBuiltin); err != nil {
		t.Fatal(err)
	}

	_, _, err := processAndCandidateErr(t, multiUserConfig(factoryID, "be"), multiUserBuildOptions(reg))
	if !errors.Is(err, runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser, err)
	}
	assertFactoryNotBuilt(t, probe, "local_only")
}

// TestBuild_multiUserRejectsUnapprovedAnyStaticFactoryBeforeBackendConstruction is
// the fail-closed default-deny regression: a brand-new factory that LIES with
// access_scope: any + credential_mode: static + execution_class: inference is still
// rejected in multi_user because the host-owned approval registry does not list it.
func TestBuild_multiUserRejectsUnapprovedAnyStaticFactoryBeforeBackendConstruction(t *testing.T) {
	t.Parallel()
	const factoryID = "brand-new-lying-factory"
	if standardplugins.HostMultiUserBackendPolicy().IsApproved(factoryID) {
		t.Fatal("precondition: a brand-new factory must be unapproved")
	}
	reg := pluginreg.NewRegistry()
	probe := &factoryProbe{}
	if err := reg.RegisterBackendWithProfilesAndSource(factoryID, probe.factory(factoryID),
		pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialStatic,
			AccessScope:    pluginreg.BackendAccessAny,
		},
		pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionInference},
		pluginreg.BackendSourceBuiltin); err != nil {
		t.Fatal(err)
	}

	_, _, err := processAndCandidateErr(t, multiUserConfig(factoryID, "be"), multiUserBuildOptions(reg))
	if !errors.Is(err, runtimebundle.ErrBackendNotApprovedForMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrBackendNotApprovedForMultiUser, err)
	}
	// The typed error must stay actionable: instance, factory, and the operator fix.
	for _, want := range []string{`instance "be"`, `factory "` + factoryID + `"`, "single-user loopback", "approval policy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must contain %q: %v", want, err)
		}
	}
	assertFactoryNotBuilt(t, probe, "unapproved any/static")
}

// TestBuild_multiUserAllowsCentrallyApprovedFactory is the positive control: only an
// explicitly approved, compatible factory may run in a valid multi-user deployment.
func TestBuild_multiUserAllowsCentrallyApprovedFactory(t *testing.T) {
	t.Parallel()
	factoryID := standardplugins.EssentialBackendKinds()[0]
	approval, ok := standardplugins.HostMultiUserBackendPolicy().Lookup(factoryID)
	if !ok {
		t.Fatalf("essential kind %q must be approved", factoryID)
	}
	reg := pluginreg.NewRegistry()
	probe := &factoryProbe{}
	if err := reg.RegisterBackendWithProfilesAndSource(factoryID, probe.factory(factoryID),
		pluginreg.BackendSecurityProfile{
			CredentialMode: pluginreg.CredentialStatic,
			AccessScope:    pluginreg.BackendAccessAny,
		},
		pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionInference},
		pluginreg.BackendSourceBuiltin); err != nil {
		t.Fatal(err)
	}
	cfg := multiUserConfig(factoryID, "be")
	cfg.Plugins.Backends[0].Config = yaml.Node{}
	_, _ = mustProcessAndCandidate(t, cfg, multiUserBuildOptions(reg))
	if got := probe.built.Load(); got != 1 {
		t.Fatalf("approved factory must be constructed exactly once, got %d", got)
	}
	t.Logf("approved control factory %q (%s / %s)", approval.FactoryKind, approval.CredentialClass, approval.ExecutionLocation)
}

// TestBuild_multiUserDiscoveredConnectorNeverDialsOrConfigures proves rejection
// happens before connector activation on the real discovered-export composition
// seam: the trusted manifest export becomes the registry security profile, the host
// gate rejects it, and Dial/Configure is never reached.
func TestBuild_multiUserDiscoveredConnectorNeverDialsOrConfigures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		kind    string
		profile pluginreg.BackendSecurityProfile
		exec    pluginreg.BackendExecutionProfile
		wantErr error
	}{
		{
			name:    "local_only discovered export",
			kind:    "discovered-local-only-acp",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessLocalOnly},
			exec:    pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionAgentRuntime},
			wantErr: runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser,
		},
		{
			name:    "unapproved any/static discovered export",
			kind:    "discovered-lying-any-static",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessAny},
			exec:    pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionInference},
			wantErr: runtimebundle.ErrBackendNotApprovedForMultiUser,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := pluginreg.NewRegistry()
			host := processhost.NewHost(processhost.Config{
				Launcher: &processhost.TestLauncher{PID: 9100 + len(tc.kind)},
				Channel:  &processhost.TestChannel{},
			})
			t.Cleanup(func() { _ = host.Close() })

			var dials atomic.Int32
			err := runtimebundle.InstallDiscoveredExports(reg, host, []runtimebundle.ValidatedExport{{
				Kind:        tc.kind,
				Profile:     tc.profile,
				ExecProfile: tc.exec,
				Artifact:    &trust.VerifiedArtifact{DigestHex: "digest-" + tc.kind},
				Model:       processhost.ProcessModelPerInstance,
			}}, runtimebundle.DiscoveredInstallOptions{
				DialSession: func(context.Context, runtimebundle.DialSessionRequest) (runtimebundle.ExecuteSession, backendplugin.ResolvedProfile, error) {
					dials.Add(1)
					return nil, backendplugin.ResolvedProfile{}, errors.New("dial must never be reached")
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			_, _, buildErr := processAndCandidateErr(t, multiUserConfig(tc.kind, "be"), multiUserBuildOptions(reg))
			if !errors.Is(buildErr, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, buildErr)
			}
			if got := dials.Load(); got != 0 {
				t.Fatalf("connector Dial/Configure reached %d times; rejection must precede activation", got)
			}
		})
	}
}

// TestCompileCandidate_securityGatePrecedesBackendConstruction freezes the ordering
// through the existing candidate fault-injection seam rather than source text: the
// "model" fault boundary sits immediately after buildModelRuntime, so a security
// rejection must win over it.
func TestCompileCandidate_securityGatePrecedesBackendConstruction(t *testing.T) {
	t.Parallel()

	// Control: an approved factory reaches the model-construction boundary.
	controlKind := standardplugins.EssentialBackendKinds()[0]
	controlReg := pluginreg.NewRegistry()
	controlProbe := &factoryProbe{}
	if err := controlReg.RegisterBackendWithProfilesAndSource(controlKind, controlProbe.factory(controlKind),
		pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessAny},
		pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionInference},
		pluginreg.BackendSourceBuiltin); err != nil {
		t.Fatal(err)
	}
	controlErr := compileCandidateWithFault(t, controlKind, multiUserBuildOptions(controlReg))
	if !errors.Is(controlErr, runtimebundle.ErrCandidateFaultInjected) {
		t.Fatalf("control: approved factory must pass the security gate and reach model build, got %v", controlErr)
	}

	// Subject: an unapproved factory must fail the gate before that boundary.
	const subjectKind = "unapproved-ordering-probe"
	subjectReg := pluginreg.NewRegistry()
	subjectProbe := &factoryProbe{}
	if err := subjectReg.RegisterBackendWithProfilesAndSource(subjectKind, subjectProbe.factory(subjectKind),
		pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessAny},
		pluginreg.BackendExecutionProfile{Class: pluginreg.BackendExecutionInference},
		pluginreg.BackendSourceBuiltin); err != nil {
		t.Fatal(err)
	}
	subjectErr := compileCandidateWithFault(t, subjectKind, multiUserBuildOptions(subjectReg))
	if !errors.Is(subjectErr, runtimebundle.ErrBackendNotApprovedForMultiUser) {
		t.Fatalf("security gate must win over the model fault boundary, got %v", subjectErr)
	}
	assertFactoryNotBuilt(t, subjectProbe, "ordering probe")
}

func compileCandidateWithFault(t *testing.T, kind string, opts *runtimebundle.BuildOptions) error {
	t.Helper()
	cfg := multiUserConfig(kind, "be")
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  testkit.DiscardLogger(),
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps,
		Bus:     hooks.New(hooks.Config{}),
		FaultInject: runtimebundle.CandidateFaultInject{
			After: "model",
			Hook:  func() {},
		},
	})
	if err != nil {
		return err
	}
	t.Cleanup(func() { _ = cand.Close() })
	return nil
}

func assertFactoryNotBuilt(t *testing.T, probe *factoryProbe, label string) {
	t.Helper()
	if got := probe.built.Load(); got != 0 {
		t.Fatalf("%s: backend factory ran %d time(s); the security gate must reject before construction", label, got)
	}
}

func stubProbeBackend(factoryID string) execbackend.Backend {
	return execbackend.Backend{
		Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		BackendPrefixes: []string{factoryID},
		ModelInventory:  testModelInventory(),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
		},
	}
}
