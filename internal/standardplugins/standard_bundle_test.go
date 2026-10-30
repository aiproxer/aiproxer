package standardplugins

import (
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestInstallBundleOnCustomBundleDoesNotTouchOtherRegistries(t *testing.T) {
	t.Parallel()

	custom := Bundle{Backends: []BackendRegistration{{
		ID: "custom-backend",
		Factory: func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
			return execbackend.Backend{Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming)}, nil
		},
		Profile: pluginreg.BackendSecurityProfile{CredentialMode: pluginreg.CredentialWorkload},
	}}}

	withCustom := pluginreg.NewRegistry()
	if err := InstallBundleOn(withCustom, custom); err != nil {
		t.Fatal(err)
	}
	if _, err := withCustom.BuildBackend("custom-backend", yaml.Node{}, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("custom bundle backend missing: %v", err)
	}

	empty := pluginreg.NewRegistry()
	if _, err := empty.BuildBackend("custom-backend", yaml.Node{}, nil, pluginreg.BackendFactoryDeps{}); err == nil {
		t.Fatal("custom bundle leaked into another registry")
	}
}

func TestStandardBundleIsValueOriented(t *testing.T) {
	t.Parallel()

	a := StandardBundle()
	b := StandardBundle()
	if len(a.Frontends) == 0 || len(a.Features) == 0 {
		t.Fatal("standard bundle must expose frontend and feature registrations")
	}
	a.Frontends[0].ID = "mutated"
	a.Features[0].ID = "mutated"
	if b.Frontends[0].ID == "mutated" || b.Features[0].ID == "mutated" {
		t.Fatal("standard bundle returned shared mutable slices")
	}
}

func TestStandardBackendBundleIsValueOriented(t *testing.T) {
	t.Parallel()

	a := StandardBackendBundle(UpstreamAPIKeys{})
	b := StandardBackendBundle(UpstreamAPIKeys{})
	if len(a.Backends) == 0 {
		t.Fatal("standard backend bundle must expose backend registrations")
	}
	a.Backends[0].ID = "mutated"
	if b.Backends[0].ID == "mutated" {
		t.Fatal("standard backend bundle returned shared mutable slices")
	}
}

func TestStandardViewsMatchRuntimeRegistrationAndRouteProjection(t *testing.T) {
	t.Parallel()
	bundle := StandardBundle()
	views, err := DerivedViews()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(bundle.Frontends), len(views.Frontends); got != want {
		t.Fatalf("frontend registrations=%d, contributions=%d", got, want)
	}
	for i, registration := range bundle.Frontends {
		if registration.ID != views.Frontends[i].Registration.ID {
			t.Fatalf("frontend %d = %q, contribution = %q", i, registration.ID, views.Frontends[i].Registration.ID)
		}
	}
	if got, want := len(StandardFrontendRouteClaims()), len(views.Routes); got != want {
		t.Fatalf("route providers=%d, route facets=%d", got, want)
	}
	if len(views.RouteClaims) == 0 {
		t.Fatal("derived views route claims must be populated from frontend contributions")
	}
	totalClaims := 0
	for id, provider := range StandardFrontendRouteClaims() {
		claims, err := provider(id, yaml.Node{})
		if err != nil {
			t.Fatalf("provider %s failed: %v", id, err)
		}
		totalClaims += len(claims)
	}
	if got, want := len(views.RouteClaims), totalClaims; got != want {
		t.Fatalf("derived route claims=%d, expected total=%d", got, want)
	}
	if got, want := len(StandardDiagnosticProjectors()), len(views.Diagnostics); got != want {
		t.Fatalf("diagnostic projectors=%d, diagnostic facets=%d", got, want)
	}
	if got := len(StandardDiagnosticProjectors()); got != 2 {
		t.Fatalf("diagnostic projectors=%d, want one frontend and one profile-catalog projector", got)
	}
}

func TestInstallBundleOnNilRegistry(t *testing.T) {
	t.Parallel()
	if err := InstallBundleOn(nil, Bundle{}); err == nil {
		t.Fatal("expected nil registry error")
	}
}
