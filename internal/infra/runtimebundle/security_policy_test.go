package runtimebundle_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/accessmode"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func registerBackendWithProfile(t *testing.T, reg *pluginreg.Registry, factoryID string, profile pluginreg.BackendSecurityProfile) {
	t.Helper()
	err := reg.RegisterBackendWithProfile(factoryID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, nil
			},
		}, nil
	}, profile)
	if err != nil {
		t.Fatal(err)
	}
}

type billingBackendOptions struct {
	Finalizer bool
	Supported bool
}

func registerBillingBackend(t *testing.T, reg *pluginreg.Registry, factoryID string, opts billingBackendOptions) {
	t.Helper()
	err := reg.RegisterBackend(factoryID, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		be := execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{factoryID},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
			BillingFinalizationSupported: opts.Supported,
		}
		if opts.Finalizer {
			be.FinalizeBilling = func(context.Context, execbackend.BillingFinalizationInput) (aipapi.Event, error) {
				return aipapi.Event{Kind: aipapi.EventUsageDelta, CostSource: "provider_reported"}, nil
			}
		}
		return be, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuild_strictAuthoritativeAccountingRequiresBackendBillingFinalizer(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		finalizer bool
		supported bool
		wantErr   bool
	}{
		{name: "missing finalizer", finalizer: false, wantErr: true},
		{name: "flag without finalizer", supported: true, finalizer: false, wantErr: true},
		{name: "has finalizer", finalizer: true, wantErr: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			factoryID := "billing-" + strings.ReplaceAll(t.Name(), "/", "-")
			reg := pluginreg.NewRegistry()
			registerBillingBackend(t, reg, factoryID, billingBackendOptions{
				Finalizer: tt.finalizer,
				Supported: tt.supported,
			})
			cfg := &config.Config{
				Routing:    config.RoutingConfig{MaxAttempts: 3},
				Continuity: config.ContinuityConfig{InMemory: true},
				Accounting: config.AccountingConfig{StrictAuthoritative: true},
				Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
					Kind: factoryID, ID: "be", Enabled: true,
				}}},
			}

			_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
				PluginRegistry: reg,
			})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "strict_authoritative requires billing finalizer") {
					t.Fatalf("Build err = %v, want strict_authoritative billing finalizer error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build err = %v", err)
			}
		})
	}
}

// buildWithProfiledBackend exercises credential-posture validation in the
// production candidate-compilation seam. Multi-user credential checks only run for
// backends the host approval registry permits (see multi_user_approval_policy_test.go
// for the default-deny boundary), so multi-user cases register under a centrally
// approved kind and single-user cases use a synthetic kind.
func buildWithProfiledBackend(t *testing.T, address string, authMode config.AuthMode, mode pluginreg.BackendCredentialMode) error {
	t.Helper()
	factoryID := "profiled-" + strings.ReplaceAll(t.Name(), "/", "-")
	if !config.IsExplicitLoopbackListenAddress(address) {
		factoryID = approvedFixtureKind(t)
	}
	reg := pluginreg.NewRegistry()
	registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{CredentialMode: mode})
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: address, AuthMode: authMode},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	opts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	if !config.IsExplicitLoopbackListenAddress(address) {
		cfg.Access = config.AccessConfig{Mode: "multi_user"}
		cfg.Auth = config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"}
		opts.Auth.RemoteDecider = &testkit.StubRemoteDecider{}
	}
	_, _, err := processAndCandidateErr(t, cfg, opts)
	return err
}

// approvedFixtureKind returns an essential backend kind that the host-owned
// multi-user approval policy approves, so a test can isolate one posture check
// without also exercising the approval registry.
func approvedFixtureKind(t *testing.T) string {
	t.Helper()
	for _, kind := range standardplugins.EssentialBackendKinds() {
		if standardplugins.HostMultiUserBackendPolicy().IsApproved(kind) {
			return kind
		}
	}
	t.Fatal("no approved essential backend kind available for fixture")
	return ""
}

func TestBuild_oauthUserBackend_allowsOnSingleUserLoopback(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "127.0.0.1:8080", "", pluginreg.CredentialOAuthUser); err != nil {
		t.Fatalf("loopback single-user should allow oauth_user backend: %v", err)
	}
}

func TestBuild_oauthUserBackend_rejectsOnNonLoopbackMultiUser(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialOAuthUser)
	if err == nil || !errors.Is(err, runtimebundle.ErrOAuthUserDisallowedMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrOAuthUserDisallowedMultiUser, err)
	}
}

func TestBuild_oauthUserBackendAllowedWhenSingleUserAccessExternalAuthLoopback(t *testing.T) {
	t.Parallel()
	factoryID := "profiled-oauth-single-user-external-loopback"
	reg := pluginreg.NewRegistry()
	registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialOAuthUser})
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.SingleUserLocalMode() {
		t.Fatal("precondition: SingleUserLocalMode must be false when server.auth_mode is external")
	}
	mode, err := cfg.EffectiveAccessMode()
	if err != nil || mode != accessmode.ModeSingleUser {
		t.Fatalf("EffectiveAccessMode: want single_user, got mode=%v err=%v", mode, err)
	}
	_, _, err = processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
	})
	if err != nil {
		t.Fatalf("single_user access with external auth on loopback must allow oauth_user backend: %v", err)
	}
}

func TestBuild_localOnlyBackend_allowsOnSingleUserLoopback(t *testing.T) {
	t.Parallel()
	factoryID := "profiled-local-only-single-user"
	reg := pluginreg.NewRegistry()
	registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialStatic,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	})
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "localhost:8080"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, _ = mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
}

func TestBuild_localOnlyBackend_rejectsOnMultiUser(t *testing.T) {
	t.Parallel()
	factoryID := "profiled-local-only-multi-user"
	reg := pluginreg.NewRegistry()
	registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialStatic,
		AccessScope:    pluginreg.BackendAccessLocalOnly,
	})
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser, err)
	}
	if !strings.Contains(err.Error(), `instance "be"`) || !strings.Contains(err.Error(), `factory "`+factoryID+`"`) {
		t.Fatalf("error should include instance and factory: %v", err)
	}
}

func TestBuild_unsupportedBackendAccessScope_rejects(t *testing.T) {
	t.Parallel()
	factoryID := "profiled-unsupported-access-scope"
	reg := pluginreg.NewRegistry()
	registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialStatic,
		AccessScope:    pluginreg.BackendAccessScope("totally_bogus"),
	})
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "multi_user"},
		Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
		Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
	})
	if err == nil || !errors.Is(err, runtimebundle.ErrUnsupportedBackendAccessScope) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrUnsupportedBackendAccessScope, err)
	}
}

func TestBuild_unknownBackendCredentialMode_rejectsOnNonLoopbackMultiUser(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialUnknown)
	if err == nil || !errors.Is(err, runtimebundle.ErrUnknownCredentialMultiUser) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrUnknownCredentialMultiUser, err)
	}
}

func TestBuild_staticBackendCredentialModeAllowsExternalAuth(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialStatic); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_noneBackendCredentialModeAllowsExternalAuth(t *testing.T) {
	t.Parallel()
	if err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.CredentialNone); err != nil {
		t.Fatal(err)
	}
}

func TestBuild_unsupportedBackendCredentialMode_rejects(t *testing.T) {
	t.Parallel()
	err := buildWithProfiledBackend(t, "0.0.0.0:8080", config.AuthModeExternal, pluginreg.BackendCredentialMode("totally_bogus"))
	if err == nil || !errors.Is(err, runtimebundle.ErrUnsupportedBackendCredentialMode) {
		t.Fatalf("want %v, got %v", runtimebundle.ErrUnsupportedBackendCredentialMode, err)
	}
}

// TestBuild_localOnlyBackend_allowsLoopbackVariantsAcceptedByAccessPolicy freezes
// the access-mode predicate: every loopback spelling the access policy accepts must
// still admit a local_only backend. This guards against accidentally substituting a
// narrower predicate than EffectiveAccessMode + loopback validation.
func TestBuild_localOnlyBackend_allowsLoopbackVariantsAcceptedByAccessPolicy(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"} {
		if !config.IsExplicitLoopbackListenAddress(address) {
			t.Fatalf("precondition: %q must be an accepted explicit loopback address", address)
		}
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			factoryID := "profiled-local-only-" + strings.NewReplacer(":", "-", "[", "", "]", "").Replace(address)
			reg := pluginreg.NewRegistry()
			registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{
				CredentialMode: pluginreg.CredentialStatic,
				AccessScope:    pluginreg.BackendAccessLocalOnly,
			})
			cfg := &config.Config{
				Server:     config.ServerConfig{Address: address},
				Routing:    config.RoutingConfig{MaxAttempts: 3},
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
					Kind: factoryID, ID: "be", Enabled: true,
				}}},
			}
			mode, err := cfg.EffectiveAccessMode()
			if err != nil || mode != accessmode.ModeSingleUser {
				t.Fatalf("precondition: EffectiveAccessMode = %v, %v; want single_user", mode, err)
			}
			_, _ = mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
		})
	}
}

// TestBuild_singleUserLocalOnlyBroadBindStillFailsAccessPosture proves a broad or
// non-loopback listener cannot masquerade as single-user. The access-posture gate in
// internal/core/accessmode owns that invariant, so this asserts it directly: the
// effective access mode still reads single_user (which is why backend eligibility
// must never be the only defense) while access-posture validation rejects the bind.
func TestBuild_singleUserLocalOnlyBroadBindStillFailsAccessPosture(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"0.0.0.0:8080", "192.0.2.10:8080", "::"} {
		t.Run(address, func(t *testing.T) {
			t.Parallel()
			if config.IsExplicitLoopbackListenAddress(address) {
				t.Fatalf("precondition: %q must not classify as explicit loopback", address)
			}
			factoryID := "profiled-broad-bind-" + strings.NewReplacer(".", "-", ":", "-").Replace(address)
			reg := pluginreg.NewRegistry()
			registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{
				CredentialMode: pluginreg.CredentialStatic,
				AccessScope:    pluginreg.BackendAccessLocalOnly,
			})
			cfg := &config.Config{
				Access:     config.AccessConfig{Mode: "single_user"},
				Server:     config.ServerConfig{Address: address, AuthMode: config.AuthModeExternal},
				Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
				Routing:    config.RoutingConfig{MaxAttempts: 3},
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
					Kind: factoryID, ID: "be", Enabled: true,
				}}},
			}
			mode, err := cfg.EffectiveAccessMode()
			if err != nil || mode != accessmode.ModeSingleUser {
				t.Fatalf("precondition: EffectiveAccessMode = %v, %v; want single_user", mode, err)
			}
			if err := config.Validate(cfg); err == nil {
				t.Fatalf("broad/non-loopback single_user bind %q must fail access-posture validation", address)
			}
		})
	}
}

// TestBuild_multiUserBackendSecurityErrorPrecedence pins the typed-error ordering so
// the operator always sees the most specific actionable reason:
//
//	local_only (access) -> oauth_user/unknown (credential) -> approval registry
func TestBuild_multiUserBackendSecurityErrorPrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		factory  string
		profile  pluginreg.BackendSecurityProfile
		wantErrs []error
	}{
		{
			name:    "local_only wins over oauth_user and approval",
			factory: "prec-local-only-oauth",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialOAuthUser, AccessScope: pluginreg.BackendAccessLocalOnly},
			wantErrs: []error{
				runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser,
				runtimebundle.ErrOAuthUserDisallowedMultiUser,
				runtimebundle.ErrBackendNotApprovedForMultiUser,
			},
		},
		{
			name:    "local_only wins over unknown credential and approval",
			factory: "prec-local-only-unknown",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialUnknown, AccessScope: pluginreg.BackendAccessLocalOnly},
			wantErrs: []error{
				runtimebundle.ErrLocalOnlyBackendDisallowedMultiUser,
				runtimebundle.ErrUnknownCredentialMultiUser,
				runtimebundle.ErrBackendNotApprovedForMultiUser,
			},
		},
		{
			name:    "oauth_user wins over approval",
			factory: "prec-oauth-any",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialOAuthUser, AccessScope: pluginreg.BackendAccessAny},
			wantErrs: []error{
				runtimebundle.ErrOAuthUserDisallowedMultiUser,
				runtimebundle.ErrBackendNotApprovedForMultiUser,
			},
		},
		{
			name:    "unknown credential wins over approval",
			factory: "prec-unknown-any",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialUnknown, AccessScope: pluginreg.BackendAccessAny},
			wantErrs: []error{
				runtimebundle.ErrUnknownCredentialMultiUser,
				runtimebundle.ErrBackendNotApprovedForMultiUser,
			},
		},
		{
			name:    "approval is the only remaining rejection",
			factory: "prec-approved-shape-unapproved-kind",
			profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessAny},
			wantErrs: []error{
				runtimebundle.ErrBackendNotApprovedForMultiUser,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg := pluginreg.NewRegistry()
			registerBackendWithProfile(t, reg, tc.factory, tc.profile)
			cfg := &config.Config{
				Access:     config.AccessConfig{Mode: "multi_user"},
				Server:     config.ServerConfig{Address: "0.0.0.0:8080", AuthMode: config.AuthModeExternal},
				Auth:       config.AuthConfig{Handler: "remote", RequiredLevel: "api_key"},
				Routing:    config.RoutingConfig{MaxAttempts: 3},
				Continuity: config.ContinuityConfig{InMemory: true},
				Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
					Kind: tc.factory, ID: "be", Enabled: true,
				}}},
			}
			_, _, err := processAndCandidateErr(t, cfg, &runtimebundle.BuildOptions{
				PluginRegistry: reg,
				Auth:           runtimebundle.AuthOptions{RemoteDecider: &testkit.StubRemoteDecider{}},
			})
			if err == nil {
				t.Fatal("want rejection in multi_user, got nil")
			}
			if !errors.Is(err, tc.wantErrs[0]) {
				t.Fatalf("want highest-precedence error %v, got %v", tc.wantErrs[0], err)
			}
			for _, forbidden := range tc.wantErrs[1:] {
				if errors.Is(err, forbidden) {
					t.Fatalf("lower-precedence error %v must not be reported: %v", forbidden, err)
				}
			}
		})
	}
}

// TestBuild_singleUserIgnoresMultiUserApprovalRegistry proves absence from the
// approval registry only denies shared operation: an unapproved factory still runs
// in an otherwise valid single-user loopback deployment.
func TestBuild_singleUserIgnoresMultiUserApprovalRegistry(t *testing.T) {
	t.Parallel()
	const factoryID = "unapproved-single-user-ok"
	if standardplugins.HostMultiUserBackendPolicy().IsApproved(factoryID) {
		t.Fatal("precondition: fixture factory must be unapproved")
	}
	reg := pluginreg.NewRegistry()
	registerBackendWithProfile(t, reg, factoryID, pluginreg.BackendSecurityProfile{
		CredentialMode: pluginreg.CredentialStatic,
		AccessScope:    pluginreg.BackendAccessAny,
	})
	cfg := &config.Config{
		Server:     config.ServerConfig{Address: "127.0.0.1:8080"},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Plugins: config.PluginsConfig{Backends: []config.PluginConfig{{
			Kind: factoryID, ID: "be", Enabled: true,
		}}},
	}
	_, _ = mustProcessAndCandidate(t, cfg, &runtimebundle.BuildOptions{PluginRegistry: reg})
}
