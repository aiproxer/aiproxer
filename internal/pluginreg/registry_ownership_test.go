package pluginreg

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func TestRegistriesDoNotShareFactories(t *testing.T) {
	t.Parallel()
	a := NewRegistry()
	b := NewRegistry()
	id := "isolated-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := a.RegisterBackend(id, func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.BuildBackend(id, yaml.Node{}, nil, BackendFactoryDeps{}); err == nil {
		t.Fatal("expected empty registry b to miss factory registered only on a")
	}
}

func TestHasBackend(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	id := "has-backend-" + strings.ReplaceAll(t.Name(), "/", "-")
	if r.HasBackend(id) {
		t.Fatal("empty registry should not report HasBackend")
	}
	if (*Registry)(nil).HasBackend(id) {
		t.Fatal("nil registry HasBackend should be false")
	}
	if err := r.RegisterBackend(id, func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !r.HasBackend(id) {
		t.Fatal("registered factory should report HasBackend")
	}
	if !r.HasBackend("  " + id + "  ") {
		t.Fatal("HasBackend should trim factoryID")
	}
	if r.HasBackend("") || r.HasBackend("missing-"+id) {
		t.Fatal("HasBackend should be false for empty/missing ids")
	}
}

func TestDuplicateRegistrationScopedPerRegistry(t *testing.T) {
	t.Parallel()
	r1 := NewRegistry()
	r2 := NewRegistry()
	id := "dup-scope-" + strings.ReplaceAll(t.Name(), "/", "-")
	fn := func(yaml.Node, *http.Client, BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{}, nil
	}
	if err := r1.RegisterBackend(id, fn); err != nil {
		t.Fatal(err)
	}
	if err := r1.RegisterBackend(id, fn); err == nil {
		t.Fatal("expected duplicate error on same registry")
	}
	if err := r2.RegisterBackend(id, fn); err != nil {
		t.Fatalf("same id on independent registry: %v", err)
	}
}
