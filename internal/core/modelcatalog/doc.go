// Package modelcatalog holds source-neutral model capability facts, match metadata, and consumer-owned
// snapshot ports used by the core catalog runtime. It must not import provider SDKs, backend plugins,
// frontend plugins, or concrete HTTP/filesystem adapters (those live under internal/infra).
//
// Matching and resolution: [DefaultMatcher] (exact then normalized catalog ids), [OverrideResolver] for
// administrator pair/model overrides, [CatalogResolverImpl] (from [NewCatalogResolver]) for effective capabilities vs backend caps,
// and [DefaultVendorResolver] (from [NewVendorResolver]) for catalog-backed vendor/model canonicalization
// with optional [VendorPolicy] hooks for provider-specific alias and keyword rules.
//
// Lifecycle: [CatalogRuntime] loads the local cache; the composition root runs periodic refresh via
// [SnapshotSource] / [SnapshotCache], and publishes an immutable active [Snapshot] for readers.
//
// Request sizing and routing: [DefaultSizeEstimator] and [NewEligibilityResolver] implement conservative
// pre-upstream context-limit eligibility on top of already-resolved [EffectiveFacts].
//
// Constructor shape: [NewCatalogResolver], [NewEligibilityResolver], and [NewOverrideResolver] return narrow
// interface types (ports) rather than concrete structs. That intentionally trades the usual “return structs,
// accept interfaces” rule for a smaller, sealed substitution surface at the composition root.
package modelcatalog
