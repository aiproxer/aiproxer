package standardplugins

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"gopkg.in/yaml.v3"
)

func TestRegistry_zeroValueRegisterBackend(t *testing.T) {
	t.Parallel()
	var r pluginreg.Registry
	id := "zero-value-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistry_zeroValueRegisterFrontend(t *testing.T) {
	t.Parallel()
	var r pluginreg.Registry
	id := "zero-value-frontend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterFrontend(id, func(*http.ServeMux, aipsdk.FrontendMountOptions) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistry_zeroValueRegisterFeature(t *testing.T) {
	t.Parallel()
	var r pluginreg.Registry
	id := "zero-value-feature-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := r.RegisterFeature(id, func(yaml.Node) (aipfeature.FeatureBundle, error) {
		return aipfeature.FeatureBundle{SchemaVersion: aipfeature.SchemaVersionV1}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

type noopWireAuthRenderer struct{}

func (noopWireAuthRenderer) RenderAuthError(ctx context.Context, in aipsdk.AuthErrorRenderInput) aipsdk.AuthErrorRenderResult {
	_ = ctx
	_ = in
	return aipsdk.AuthErrorRenderResult{}
}

func TestRegistry_RegisterAuthErrorRenderer_duplicate(t *testing.T) {
	t.Parallel()
	r := pluginreg.NewRegistry()
	if err := r.RegisterAuthErrorRenderer("gemini", noopWireAuthRenderer{}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterAuthErrorRenderer("gemini", noopWireAuthRenderer{}); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistry_RegisterAuthErrorRenderer_nilSkips(t *testing.T) {
	t.Parallel()
	r := pluginreg.NewRegistry()
	if err := r.RegisterAuthErrorRenderer("anthropic", nil); err != nil {
		t.Fatal(err)
	}
	if r.AuthErrorRenderers() != nil {
		t.Fatalf("expected nil map, got %#v", r.AuthErrorRenderers())
	}
}
