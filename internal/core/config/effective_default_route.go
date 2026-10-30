package config

import (
	"strconv"
	"strings"
)

// DefaultFallbackWireModel is the compile-time model token used when synthesizing a
// default route selector and no WireModelForBackend mapping is available.
const DefaultFallbackWireModel = "gpt-4o-mini"

// DefaultFallbackRouteSelector is used only when wireModel is nil (tests / degenerate bootstrap).
const DefaultFallbackRouteSelector = "openai-responses:" + DefaultFallbackWireModel

// WireModelForBackend resolves the default model token for a backend factory id (plugin kind)
// when synthesizing a fallback route selector. Supplied at composition time (typically
// standardplugins.DefaultWireModel) so policy stays independent of HTTP mounting.
//
// An empty result means the factory kind has no canonical default model, for example a
// dynamic-inventory backend whose models are discovered at runtime. Such a kind is never
// given a placeholder model: the model the instance discovers at runtime is used instead,
// and the configuration names the route explicitly when it wants a specific one.
type WireModelForBackend func(factoryID string) string

// DiscoveredModelForBackend resolves the default model a backend instance
// published in its runtime inventory: the models it discovered at runtime, such
// as the models an executable connector reports.
//
// The provider owns the choice of the default model: an empty result means the
// instance designates no default, so no default route can be derived from it.
// The resolver is supplied by the composition root, which owns the inventory;
// the configuration layer never loads it.
type DiscoveredModelForBackend func(instanceID string) string

// DefaultRouteSource names where an effective default route came from.
type DefaultRouteSource string

const (
	// DefaultRouteSourceConfigured marks a route the configuration named.
	DefaultRouteSourceConfigured DefaultRouteSource = "configured"
	// DefaultRouteSourceCanonicalModel marks the compile-time canonical default
	// model of the first enabled backend kind.
	DefaultRouteSourceCanonicalModel DefaultRouteSource = "canonical_default_model"
	// DefaultRouteSourceDiscoveredInventory marks the model a backend instance
	// published in its runtime inventory.
	DefaultRouteSourceDiscoveredInventory DefaultRouteSource = "discovered_inventory"
)

// EffectiveDefaultRoute is the default route a configuration resolves to.
type EffectiveDefaultRoute struct {
	// Selector is the route selector clients fall back to. It is empty when the
	// configuration resolves to no default route.
	Selector string
	// Source names where the selector came from.
	Source DefaultRouteSource
	// Instance is the backend instance the selector names, and Model is the
	// model it names. Both are set when the route was derived from a backend
	// kind or instance, and empty for a route the configuration named: the
	// routing layer owns the grammar and validates a derived route against
	// them.
	Instance string
	Model    string
}

// EffectiveDefaultRouteSelector returns the selector used when clients omit explicit routing
// (e.g. X-AIP-Route). Single implementation for the proxy; routing alias validation stays in
// package routing (see routing.ValidateModelAliasesConfig).
//
// The selector is resolved from the configuration alone. A configuration whose backend
// discovers its models at runtime has no route here: use
// [ResolveEffectiveDefaultRoute] with the discovered inventory to derive one.
func EffectiveDefaultRouteSelector(cfg *Config, wireModel WireModelForBackend) string {
	return ResolveEffectiveDefaultRoute(cfg, wireModel, nil).Selector
}

// ResolveEffectiveDefaultRoute resolves the default route a configuration uses when a
// client omits an explicit route, consulting the models a backend instance discovered
// at runtime.
//
// Resolution order:
//  1. cfg.Routing.DefaultRoute when non-empty after trim. The configured route is
//     authoritative: it wins over any discovered model.
//  2. the first enabled backend row, as "<instance_id>:<model>", where the model is
//     a. the canonical default model of the row's kind, when the kind has one
//     b. otherwise the default model the instance published in its runtime
//     inventory
//  3. "" when the first enabled backend's kind has no canonical default model and the
//     instance designates no default model: the kind discovers its models at runtime,
//     so no default can be known before the inventory is discovered. The search never
//     falls through to a later backend: that would route a request to a provider the
//     operator did not put first, and the operator names the route explicitly when
//     they want one
//  4. compile-time fallback "openai-responses:<wireModel(openai-responses)>" when wireModel is set
//  5. literal "openai-responses:gpt-4o-mini" only if wireModel is nil (tests / degenerate bootstrap)
func ResolveEffectiveDefaultRoute(cfg *Config, wireModel WireModelForBackend, discovered DiscoveredModelForBackend) EffectiveDefaultRoute {
	if cfg != nil {
		if s := strings.TrimSpace(cfg.Routing.DefaultRoute); s != "" {
			return EffectiveDefaultRoute{Selector: s, Source: DefaultRouteSourceConfigured}
		}
		for _, p := range cfg.Plugins.Backends {
			if !p.Enabled {
				continue
			}
			instance := p.InstanceID()
			if instance == "" {
				continue
			}
			if wireModel != nil {
				model := strings.TrimSpace(wireModel(p.FactoryID()))
				if model != "" {
					return EffectiveDefaultRoute{
						Selector: instance + ":" + model,
						Source:   DefaultRouteSourceCanonicalModel,
						Instance: instance,
						Model:    model,
					}
				}
			}
			// The kind has no canonical default model: it discovers its models at
			// runtime, so the default model the instance designated is the default.
			if model := discoveredModelFor(discovered, instance); model != "" {
				return EffectiveDefaultRoute{
					Selector: instance + ":" + model,
					Source:   DefaultRouteSourceDiscoveredInventory,
					Instance: instance,
					Model:    model,
				}
			}
			if wireModel == nil {
				return EffectiveDefaultRoute{
					Selector: instance + ":" + DefaultFallbackWireModel,
					Source:   DefaultRouteSourceCanonicalModel,
					Instance: instance,
					Model:    DefaultFallbackWireModel,
				}
			}
			// No canonical default model and no designated default model: never invent
			// one, and never route to another backend instead.
			return EffectiveDefaultRoute{}
		}
	}
	if wireModel != nil {
		m := strings.TrimSpace(wireModel("openai-responses"))
		if m == "" {
			m = DefaultFallbackWireModel
		}
		return EffectiveDefaultRoute{
			Selector: "openai-responses:" + m,
			Source:   DefaultRouteSourceCanonicalModel,
			Instance: "openai-responses",
			Model:    m,
		}
	}
	return EffectiveDefaultRoute{
		Selector: DefaultFallbackRouteSelector,
		Source:   DefaultRouteSourceCanonicalModel,
		Instance: "openai-responses",
		Model:    DefaultFallbackWireModel,
	}
}

// discoveredModelFor returns the model an instance published, or the empty string
// when no inventory was consulted or the instance published no model.
func discoveredModelFor(discovered DiscoveredModelForBackend, instance string) string {
	if discovered == nil {
		return ""
	}
	return strings.TrimSpace(discovered(instance))
}

// BackendsWithoutCanonicalDefaultModel lists the enabled backend instances whose
// factory kind has no canonical default model. Such a kind discovers its models
// at runtime, so no default route can be known for it before the inventory is
// discovered.
func BackendsWithoutCanonicalDefaultModel(cfg *Config, wireModel WireModelForBackend) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		instance := p.InstanceID()
		if instance == "" {
			continue
		}
		if wireModel != nil && strings.TrimSpace(wireModel(p.FactoryID())) == "" {
			out = append(out, instance)
		}
	}
	return out
}

// DefaultRouteGuidance explains a configuration that resolves to no default
// route and names the remedy. The operator is the only one who knows which
// model the backend serves, so the message points at routing.default_route
// instead of guessing a model.
//
// The remedy depends on what is known: with an inventory in hand the instance
// designated no default model, so the operator names one; without one the
// instance may still designate one later, and naming one stays the way to be
// sure.
func DefaultRouteGuidance(cfg *Config, wireModel WireModelForBackend, discovered DiscoveredModelForBackend) string {
	instances := BackendsWithoutCanonicalDefaultModel(cfg, wireModel)
	if len(instances) == 0 {
		return "no default route: set routing.default_route to a concrete route " +
			"(for example \"backend:model\")"
	}
	instance := instances[0]
	if discovered != nil {
		return "no default route: backend " + strconv.Quote(instance) +
			" has no canonical default model and designated no default model; set " +
			"routing.default_route to a concrete route for it (for example " +
			strconv.Quote(instance+":<model>") + ")"
	}
	return "no default route: backend " + strconv.Quote(instance) +
		" has no canonical default model; the default route is the default model " +
		"the instance designates at runtime, or set routing.default_route to a concrete " +
		"route for it (for example " + strconv.Quote(instance+":<model>") + ")"
}
