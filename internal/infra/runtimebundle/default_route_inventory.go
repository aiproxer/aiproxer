package runtimebundle

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// defaultModelBinding is the generation-owned canonical-to-native binding of
// the default route a generation derived from a discovered model.
//
// The model registry keeps publishing refreshed inventories, and a refresh can
// withdraw a model the generation's default route names. The generation keeps
// the mapping it was compiled with, so its own default route keeps addressing
// the model the backend published. A model the backend no longer serves then
// fails at the backend, which reports it, instead of the proxy sending a
// canonical id the backend never published.
type defaultModelBinding struct {
	bindings routing.DefaultRouteBindings
}

// discoveredModelResolver resolves the default model a backend instance
// designated in the published model registry.
//
// The designation is the provider's: a model it marked as the default, or
// its only published model. Several unmarked models have no reliable
// preference order, so the instance designates no default and the resolver
// answers with no model.
//
// The resolver is nil when no inventory is available, so the default route
// resolution keeps its configuration-only behavior and an undiscovered
// inventory stays an absent default route.
func discoveredModelResolver(rt *modelregistry.Runtime) config.DiscoveredModelForBackend {
	reg := publishedRegistry(rt)
	if reg == nil {
		return nil
	}
	return func(instanceID string) string {
		model, ok := reg.DefaultModel(strings.TrimSpace(instanceID))
		if !ok {
			return ""
		}
		return strings.TrimSpace(model.CanonicalID)
	}
}

// publishedRegistry returns the registry the runtime currently publishes, or
// nil when the runtime published none.
func publishedRegistry(rt *modelregistry.Runtime) *modelregistry.Registry {
	if rt == nil {
		return nil
	}
	return rt.ActiveRegistry()
}

// frozenDefaultRouteBindings freezes the canonical-to-native bindings the
// generation's derived default route needs.
//
// The model registry keeps publishing refreshed inventories, and a refresh can
// withdraw a model the generation's default route names. The generation keeps
// the mapping it was compiled with, so its own default route keeps addressing
// the model the backend published: a model the backend no longer serves then
// fails at the backend, which reports it, instead of the proxy sending a
// canonical id the backend never published.
//
// The bindings are per leaf, so a default route an operator expanded into a
// composite route keeps every model's binding.
func frozenDefaultRouteBindings(
	reg *modelregistry.Registry,
	leaves []routing.DefaultRouteLeaf,
) routing.DefaultRouteBindings {
	if reg == nil || len(leaves) == 0 {
		return nil
	}
	bindings := make(routing.DefaultRouteBindings, 0, len(leaves))
	for _, leaf := range leaves {
		native, ok := nativeModelFor(reg, leaf.Backend, leaf.Model)
		if !ok {
			continue
		}
		bindings = append(bindings, routing.DefaultModelBinding{
			Backend: leaf.Backend,
			Model:   leaf.Model,
			Native:  native,
		})
	}
	if len(bindings) == 0 {
		return nil
	}
	return bindings
}

// defaultRouteBindings returns the generation-owned frozen model bindings, and
// whether the generation has any.
func defaultRouteBindings(binding *defaultModelBinding) routing.DefaultRouteBindings {
	if binding == nil {
		return nil
	}
	return binding.bindings
}

// nativeModelFor returns the native id the backend serves the canonical model
// as, and whether the registry knows the mapping.
func nativeModelFor(reg *modelregistry.Registry, backend, canonical string) (string, bool) {
	if reg == nil {
		return "", false
	}
	rows, ok := reg.Lookup(canonical)
	if !ok {
		return "", false
	}
	for _, row := range rows {
		if strings.TrimSpace(row.BackendID) != strings.TrimSpace(backend) {
			continue
		}
		native := strings.TrimSpace(row.NativeID)
		if native == "" {
			return "", false
		}
		return native, true
	}
	return "", false
}
