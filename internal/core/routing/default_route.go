package routing

import (
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// DefaultRouteResolution is the resolved default route of a configuration.
type DefaultRouteResolution struct {
	// Selector is the effective default route: the configured route with the
	// model aliases applied once, exactly as the executor applies them to any
	// selector it receives.
	Selector string
	// RawSelector is the configured default route before the aliases are
	// applied. The frontends fall back to it and the executor expands it, so
	// the route the executor runs is the effective selector.
	RawSelector string
	// DefaultBackend is the backend the effective default route names. It is
	// empty when the configuration resolves to no default route.
	DefaultBackend string
	// Aliases is the resolver the effective selector was expanded with.
	Aliases *AliasResolver
	// Warning explains an absent default route and names the remedy.
	Warning string
	// Source names where the effective default route came from. It is empty
	// when the configuration resolves to no default route.
	Source config.DefaultRouteSource
	// Model is the model the effective default route names. It is empty when
	// the configuration resolves to no default route, and for a composite
	// route (failover, weighted, parallel), which can run more than one
	// model.
	Model string
	// Leaves are the leaves of the effective default route: every model the
	// route can run. A generation freezes their native bindings, so the
	// models its own default route names keep their binding when a later
	// inventory refresh withdraws them.
	Leaves []DefaultRouteLeaf
}

// DefaultRouteLeaf is one leaf of a resolved default route.
type DefaultRouteLeaf struct {
	// Backend is the backend instance the leaf names.
	Backend string
	// Model is the canonical model the leaf names.
	Model string
}

// ResolveDefaultRoute resolves the default route selector a configuration uses
// when a client omits an explicit route.
//
// The operator check, the serving runtime and the routes read model share this
// resolution, so all three agree on the route that is in effect.
//
// The resolution is configuration-only: a backend kind that discovers its
// models at runtime has no default route here. Use
// [ResolveDefaultRouteWithInventory] to derive one from the discovered
// inventory.
func ResolveDefaultRoute(cfg *config.Config, wireModel config.WireModelForBackend) (DefaultRouteResolution, error) {
	return ResolveDefaultRouteWithInventory(cfg, wireModel, nil)
}

// ResolveDefaultRouteWithInventory resolves the default route selector a
// configuration uses when a client omits an explicit route, consulting the
// models a backend instance discovered at runtime.
//
// A backend kind without a canonical default model takes the model its instance
// published in the runtime inventory, so a deployment that discovers its models
// works without the operator copying a model id into the configuration. The
// configured route stays authoritative: routing.default_route wins over any
// discovered model.
//
// The model aliases are applied exactly once, to the configured route. The
// executor expands every selector it receives, including the default route the
// frontends fall back to, so the effective default route is what the executor
// runs: a second expansion would let another alias rule rewrite a route that
// was already validated, and could route the request to a backend the operator
// was not told about.
//
// An absent default route stays absent: an alias rule must not turn it into a
// route to some provider. A backend kind without a canonical default model
// never receives an invented model, and an effective route that names a model
// no backend serves, or no backend at all, is refused.
func ResolveDefaultRouteWithInventory(
	cfg *config.Config,
	wireModel config.WireModelForBackend,
	discovered config.DiscoveredModelForBackend,
) (DefaultRouteResolution, error) {
	res := DefaultRouteResolution{}
	aliases, err := NewAliasResolver(ModelAliasRulesFromConfig(cfg))
	if err != nil {
		return res, fmt.Errorf("model_aliases: %w", err)
	}
	res.Aliases = aliases
	effective := config.ResolveEffectiveDefaultRoute(cfg, wireModel, discovered)
	raw := strings.TrimSpace(effective.Selector)
	if raw == "" {
		res.Warning = config.DefaultRouteGuidance(cfg, wireModel, discovered)
		return res, nil
	}
	if effective.Source == config.DefaultRouteSourceDiscoveredInventory {
		// A discovered model identifier is external data. It must never be able
		// to change the shape of the route the proxy falls back to.
		if err := validateDiscoveredRoute(raw, effective.Instance, effective.Model); err != nil {
			res.Warning = "no default route: " + err.Error()
			return res, nil
		}
	}
	res.RawSelector = raw
	res.Selector = aliases.Resolve(raw)
	if strings.TrimSpace(res.Selector) == "" {
		// A configured route that expands to nothing is a configuration error,
		// not an absent default route: an alias replacement may name a capture
		// group its pattern does not have, and the expansion is then empty.
		return res, fmt.Errorf(
			"routing.default_route: the model alias expansion of %q is empty; "+
				"the alias rule must expand to a route that names a backend and a model",
			raw)
	}
	if err := RejectPlaceholderModel(res.Selector); err != nil {
		return res, err
	}
	backend, err := DefaultBackendFromRouteSelector(res.Selector)
	if err != nil {
		return res, err
	}
	res.DefaultBackend = backend
	res.Source = effective.Source
	res.Model = singleLeafModel(res.Selector)
	res.Leaves = defaultRouteLeaves(res.Selector, res.DefaultBackend)
	return res, nil
}

// validateDiscoveredRoute checks that a route derived from a discovered model
// is one leaf that names the publishing instance and exactly that model.
//
// A discovered model identifier is external data: a catalog must not be able
// to change the topology of the route the proxy falls back to. A selector
// that parses to more than one leaf, to another backend, to another model, or
// that carries route parameters is refused, so failover, parallel execution,
// weights and query parameters can never be injected through a model id.
func validateDiscoveredRoute(selector, instance, model string) error {
	sel, err := Parse(selector)
	if err != nil {
		return fmt.Errorf("the discovered model %q is not a valid route model: %w", model, err)
	}
	if len(sel.Alternatives) != 1 {
		return fmt.Errorf("the discovered model %q is not a valid route model: it must name exactly one route", model)
	}
	alt := sel.Alternatives[0]
	if alt.Primary == nil {
		return fmt.Errorf("the discovered model %q is not a valid route model: it must name one backend and one model", model)
	}
	primary := alt.Primary
	if got := strings.TrimSpace(primary.Backend); got != instance {
		return fmt.Errorf("the discovered model %q is not a valid route model: it names backend %q", model, got)
	}
	if got := strings.TrimSpace(primary.Model); got != model {
		return fmt.Errorf("the discovered model %q is not a valid route model: the route names %q", model, got)
	}
	if len(primary.Params) > 0 {
		return fmt.Errorf("the discovered model %q is not a valid route model: it must not carry route parameters", model)
	}
	return nil
}

// singleLeafModel returns the model the route names when the route is exactly
// one leaf, and the empty string for a composite route. A composite route
// (failover, weighted, parallel) can run more than one model, so it has no
// single model to publish.
func singleLeafModel(selector string) string {
	sel, err := Parse(selector)
	if err != nil || len(sel.Alternatives) != 1 {
		return ""
	}
	alt := sel.Alternatives[0]
	if alt.Primary == nil {
		return ""
	}
	return strings.TrimSpace(alt.Primary.Model)
}

// defaultRouteLeaves returns every leaf of a route selector: the models the
// route can run. A composite route (failover, weighted, parallel) has one leaf
// per branch, and each of them is a model the generation's default route can
// address.
//
// The leaves are collected after the model-only expansion, with the same
// default backend the request-time selector compilation uses, so a branch that
// names only a model resolves to the backend the executor would give it.
func defaultRouteLeaves(selector, defaultBackend string) []DefaultRouteLeaf {
	sel, err := Parse(selector)
	if err != nil {
		return nil
	}
	ApplyModelOnlyBackends(sel, defaultBackend)
	var leaves []DefaultRouteLeaf
	for _, primary := range routeLeaves(sel) {
		backend := strings.TrimSpace(primary.Backend)
		model := strings.TrimSpace(primary.Model)
		if backend == "" || model == "" {
			continue
		}
		leaves = append(leaves, DefaultRouteLeaf{Backend: backend, Model: model})
	}
	return leaves
}
