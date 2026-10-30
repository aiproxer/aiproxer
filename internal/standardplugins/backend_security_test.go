package standardplugins

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestBackendSecurityProfile_roundTrip(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("oauth", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialOAuthUser, AccessScope: pluginreg.BackendAccessLocalOnly}); err != nil {
		t.Fatal(err)
	}
	profile, ok := reg.BackendSecurityProfile("oauth")
	if !ok {
		t.Fatal("expected profile")
	}
	if profile.CredentialMode != pluginreg.CredentialOAuthUser {
		t.Fatalf("credential mode: got %q", profile.CredentialMode)
	}
	if profile.AccessScope != pluginreg.BackendAccessLocalOnly {
		t.Fatalf("access scope: got %q", profile.AccessScope)
	}
}

func TestRegisterBackend_defaultsUnknownCredentialMode(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackend("legacy", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	profile, ok := reg.BackendSecurityProfile("legacy")
	if !ok {
		t.Fatal("expected profile")
	}
	if profile.CredentialMode != pluginreg.CredentialUnknown {
		t.Fatalf("credential mode: got %q", profile.CredentialMode)
	}
	if profile.AccessScope != pluginreg.BackendAccessAny {
		t.Fatalf("access scope: got %q", profile.AccessScope)
	}
}

func TestRegisterBackendWithProfile_defaultsAnyAccessScope(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("static", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}); err != nil {
		t.Fatal(err)
	}
	profile, ok := reg.BackendSecurityProfile("static")
	if !ok {
		t.Fatal("expected profile")
	}
	if profile.AccessScope != pluginreg.BackendAccessAny {
		t.Fatalf("access scope: got %q", profile.AccessScope)
	}
}

func TestBackendSecurityProfile_unregisteredFactoryIsNotFound(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	_, ok := reg.BackendSecurityProfile("factory-never-registered")
	if ok {
		t.Fatal("expected no profile for factory id that was never registered")
	}
}

// TestBackendAccessAny_defaultIsUnsafeForLocalTrustBackends anchors the security contract
// for the BackendAccessAny default: a backend registered without an explicit AccessScope
// is recorded as BackendAccessAny, which permits multi-user deployments. Process-spawning
// backends and backends that depend on a local-user trust boundary MUST declare
// BackendAccessLocalOnly explicitly instead of relying on this default; otherwise they
// would bypass the local-trust boundary in multi-user mode. The standard bundle enforces
// this for known local-trust connectors via TestStandardBackends_localOnlyConnectorsDeclareLocalOnlyScope.
// Do not relax this contract without an approved spec change.
func TestBackendAccessAny_defaultIsUnsafeForLocalTrustBackends(t *testing.T) {
	t.Parallel()
	reg := pluginreg.NewRegistry()
	if err := reg.RegisterBackendWithProfile("local-trust-default", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic}); err != nil {
		t.Fatal(err)
	}
	profile, ok := reg.BackendSecurityProfile("local-trust-default")
	if !ok {
		t.Fatal("expected profile")
	}
	if profile.AccessScope != pluginreg.BackendAccessAny {
		t.Fatalf("empty AccessScope must default to BackendAccessAny for compatibility, got %q", profile.AccessScope)
	}
	// Local-trust backends must opt into BackendAccessLocalOnly explicitly; the registry
	// does not infer it, which is exactly why the default is unsafe for such backends.
	if err := reg.RegisterBackendWithProfile("local-trust-explicit", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}, pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialStatic, AccessScope: pluginreg.BackendAccessLocalOnly}); err != nil {
		t.Fatal(err)
	}
	explicit, ok := reg.BackendSecurityProfile("local-trust-explicit")
	if !ok {
		t.Fatal("expected profile")
	}
	if explicit.AccessScope != pluginreg.BackendAccessLocalOnly {
		t.Fatalf("explicit local-only declaration must round-trip, got %q", explicit.AccessScope)
	}
}
