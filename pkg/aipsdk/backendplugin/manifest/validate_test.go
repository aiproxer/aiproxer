package manifest_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/manifest"
)

func TestManifestValidate_ExecutionClass(t *testing.T) {
	t.Parallel()
	validBase := func(exec aipsdk.BackendExecutionClass) manifest.Manifest {
		return manifest.Manifest{
			Schema: manifest.SchemaV1, PluginID: "io.x.y", Version: "1", BuildID: "b",
			Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProtocolMajor: 1, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}},
			Exports: []manifest.Export{{
				Kind: "k", CredentialMode: backendplugin.CredentialModeNone,
				AccessScope: backendplugin.AccessScopeLocalOnly, ProcessSharing: backendplugin.ProcessSharingPerInstance,
				ExecutionClass: exec,
			}},
		}
	}

	if err := validBase(aipsdk.BackendExecutionUnknown).Validate(); err != nil {
		t.Fatalf("omitted/unknown execution class should be valid, got: %v", err)
	}
	if err := validBase(aipsdk.BackendExecutionInference).Validate(); err != nil {
		t.Fatalf("inference execution class should be valid, got: %v", err)
	}
	if err := validBase(aipsdk.BackendExecutionAgentRuntime).Validate(); err != nil {
		t.Fatalf("agent_runtime execution class should be valid, got: %v", err)
	}
	if err := validBase("invalid_class").Validate(); err == nil {
		t.Fatal("invalid execution class should fail validation, got nil")
	}
}

func TestManifestValidate_OAuthUserImpliesLocalOnly(t *testing.T) {
	t.Parallel()
	base := func(cred backendplugin.CredentialMode, scope backendplugin.AccessScope) manifest.Manifest {
		m := manifest.Manifest{
			Schema: manifest.SchemaV1, PluginID: "io.x.y", Version: "1", BuildID: "b",
			Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ProtocolMajor: 1, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}},
			Exports: []manifest.Export{{
				Kind: "k", CredentialMode: cred, AccessScope: scope,
				ProcessSharing: backendplugin.ProcessSharingPerInstance,
				ExecutionClass: aipsdk.BackendExecutionInference,
			}},
		}
		if cred == backendplugin.CredentialModeStatic {
			m.Exports[0].CredentialContract = staticContract()
		}
		return m
	}

	for _, tc := range []struct {
		name  string
		cred  backendplugin.CredentialMode
		scope backendplugin.AccessScope
	}{
		{name: "oauth_user local_only", cred: backendplugin.CredentialModeOAuthUser, scope: backendplugin.AccessScopeLocalOnly},
		{name: "static any", cred: backendplugin.CredentialModeStatic, scope: backendplugin.AccessScopeAny},
		{name: "workload any", cred: backendplugin.CredentialModeWorkload, scope: backendplugin.AccessScopeAny},
		{name: "none any", cred: backendplugin.CredentialModeNone, scope: backendplugin.AccessScopeAny},
		{name: "none local_only", cred: backendplugin.CredentialModeNone, scope: backendplugin.AccessScopeLocalOnly},
		{name: "unknown any", cred: backendplugin.CredentialModeUnknown, scope: backendplugin.AccessScopeAny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := base(tc.cred, tc.scope).Validate(); err != nil {
				t.Fatalf("valid combination rejected: %v", err)
			}
		})
	}

	// oauth_user with a non-local_only scope is internally contradictory: the host
	// runtime already refuses it in multi-user mode, so advertising broad
	// eligibility would be misleading.
	t.Run("oauth_user any", func(t *testing.T) {
		t.Parallel()
		err := base(backendplugin.CredentialModeOAuthUser, backendplugin.AccessScopeAny).Validate()
		if err == nil {
			t.Fatal("oauth_user without local_only access scope must be rejected")
		}
		if !errors.Is(err, manifest.ErrInconsistentExportSecurityPosture) {
			t.Fatalf("want %v, got %v", manifest.ErrInconsistentExportSecurityPosture, err)
		}
		if !errors.Is(err, manifest.ErrInvalidManifest) {
			t.Fatalf("inconsistent posture must still classify as invalid manifest, got %v", err)
		}
	})

	// An omitted scope stays fail-closed on its own closed-enum gate; the
	// posture rule must not relax it into an inferred local_only.
	t.Run("oauth_user unspecified stays fail-closed", func(t *testing.T) {
		t.Parallel()
		err := base(backendplugin.CredentialModeOAuthUser, backendplugin.AccessScopeUnspecified).Validate()
		if !errors.Is(err, manifest.ErrInvalidManifest) {
			t.Fatalf("want %v, got %v", manifest.ErrInvalidManifest, err)
		}
	})
}

func TestManifestValidate_OAuthUserImpliesLocalOnlyIsPerExport(t *testing.T) {
	t.Parallel()
	m := manifest.Manifest{
		Schema: manifest.SchemaV1, PluginID: "io.x.y", Version: "1", BuildID: "b",
		Executable: "bin/x", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ProtocolMajor: 1, Platforms: []manifest.Platform{{OS: "linux", Arch: "amd64"}},
		Exports: []manifest.Export{
			{
				Kind: "safe", CredentialMode: backendplugin.CredentialModeStatic,
				AccessScope:        backendplugin.AccessScopeAny,
				ProcessSharing:     backendplugin.ProcessSharingPerInstance,
				ExecutionClass:     aipsdk.BackendExecutionInference,
				CredentialContract: staticContract(),
			},
			{
				Kind: "lying", CredentialMode: backendplugin.CredentialModeOAuthUser,
				AccessScope:    backendplugin.AccessScopeAny,
				ProcessSharing: backendplugin.ProcessSharingPerInstance,
				ExecutionClass: aipsdk.BackendExecutionAgentRuntime,
			},
		},
	}
	err := m.Validate()
	if err == nil || !errors.Is(err, manifest.ErrInconsistentExportSecurityPosture) {
		t.Fatalf("want %v, got %v", manifest.ErrInconsistentExportSecurityPosture, err)
	}
	if !containsKind(err.Error(), "lying") {
		t.Fatalf("error must name the offending export kind: %v", err)
	}
}

func containsKind(msg, kind string) bool { return strings.Contains(msg, kind) }
