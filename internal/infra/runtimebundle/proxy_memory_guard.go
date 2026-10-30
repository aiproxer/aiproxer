package runtimebundle

import (
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
)

// The composition-root facts the proxy-memory feature's pre-open guard needs
// (design §2).
//
// The guard is consulted by the core at the provider-open chokepoint, and the
// core computes the deployment facts it hands to the guard. The generation's
// frozen model-registry publication is one of them: an auxiliary background
// request carries no request-bound registry view, so the generation's own
// publication is its deployment fact.

// proxyMemoryBoundRegistry returns the generation's frozen model-registry
// publication. A generation without a registry runtime has no publication,
// and the installation fact is then unproven rather than absent.
func proxyMemoryBoundRegistry(model *modelRuntime) modelregistry.BoundView {
	if model == nil || model.RegistryRuntime == nil {
		return modelregistry.EmptyBoundView()
	}
	return model.RegistryRuntime.BoundView()
}
