# Feature plugins (official)

## Composition boundary

- **Feature packages** (`internal/plugins/features/<name>`) implement feature capabilities. In the target architecture, feature plugins own their configuration decoding and bundle construction via a feature-owned constructor or factory (e.g. `toolcallrepair.FeatureBundle(cfg)`, `secretguard.FeatureBundle(cfg)`, `reasoningpreservation.FeatureBundleWithCompanionPolicy(cfg, ...)`). Remaining standard factories where `features_install.go` still directly constructs `ContributionSet`/`FeatureBundle` (e.g. Agent Loop Guard at line 38, Pre-request Policy at line 220) are deferred with inventory tracking.
  - Feature packages must not import `internal/core/*`, `internal/infra/runtimebundle`, frontends, or backends.
- **Registry & Standard Distribution:** [`internal/pluginreg`](../../pluginreg) registers a `FeatureFactory` per feature plugin ID. Standard in-repo wiring in [`internal/standardplugins/features_install.go`](../../standardplugins/features_install.go) and [`internal/standardplugins/standard_table.go`](../../standardplugins/standard_table.go) registers feature factories explicitly in `StandardBundle().Features`.
- **No Core or Runtimebundle Branching:** Concrete feature packages are never imported by `internal/core` or `internal/infra/runtimebundle`. `runtimebundle` contains zero direct imports of `internal/plugins/features/*`. Where process- or generation-bound capabilities are required (such as background auxiliary workers or credential matchers), dedicated explicit typed composition adapters outside `runtimebundle` (such as `internal/infra/*compose`) assemble them.

## Extension plane lifecycle and FeatureBundle

All feature plugins contribute capabilities through the typed extension plane lifecycle in [`pkg/aipsdk/feature`](../../../pkg/aipsdk/feature):

1. **Staging**: Construct a mutable set using `feature.NewContributionSet()`.
2. **Contribution**: Add typed capabilities using `feature.Contribute(cs, plane, contributorID, value)`. `Contribute` tags contributions with `SourceFeature` and enforces fail-before-mutate semantics: if validation or combination fails, `cs` remains unmodified and an `*AttributedError` attributing the contributor and plane is returned.
3. **Freezing**: Call `cs.Freeze()` to produce an immutable `FrozenPlaneSet`. Freezing provides top-level collection isolation: slice backing arrays and metadata maps are isolated, while element values (e.g. interface handlers) are shallow-copied, not deep-cloned.
4. **Packaging**: Call `feature.BundleFromPlanes(frozen, lifecycles)` to wrap the frozen planes into a `FeatureBundle` with `SchemaVersionV1`.
5. **Validation**: Validate the bundle via `bundle.Validate()`.
6. **Reading**: Downstream consumers read values using `feature.Get(bundle.PlaneSet, plane)`. If a plane was not contributed or is absent, `feature.Get` returns the plane's zero value (e.g. `nil` for slice planes).

Note: `FeatureBundle` contains `SchemaVersion`, `PlaneSet FrozenPlaneSet`, and optional `Lifecycles []aipplugin.Lifecycle`. It does **not** contain individual named fields or slices for each extension plane; all extension planes are held in `bundle.PlaneSet`.

Canonical authoring example matching `testdata/external_feature_sdk`:

```go
cs := feature.NewContributionSet()
if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "my_plugin_id", []hooks.SubmitHook{hook}); err != nil {
    return feature.FeatureBundle{}, fmt.Errorf("contribute failed: %w", err)
}
bundle := feature.BundleFromPlanes(cs.Freeze(), nil)
if err := bundle.Validate(); err != nil {
    return feature.FeatureBundle{}, fmt.Errorf("bundle validate failed: %w", err)
}
```

## Closed standard manifest and policy authority

In v1, AIProxer enforces a **closed standard-plane catalog** declared in the canonical manifest (`pkg/aipsdk/feature/plane_manifest.go`):

- **No dynamic planes in v1**: Arbitrary unbound or dynamically declared `Plane[T]` instances are not supported. Contributing through an ungenerated or unbound plane fails immediately before candidate mutation with `feature.ErrUngeneratedPlane`.
- **Platform-level plane addition**: Adding a new extension plane is an upstream AIProxer SDK/runtime platform change requiring a declaration in `plane_manifest.go` and code regeneration via `go run ./scripts/generate-feature-planes.go`.
- **Canonical generated-policy authority**: Exported `Plane[T]` package variables (such as `PlaneSubmitHooks`, `PlaneRequestTransforms`, etc.) act as typed descriptors. The canonical generated binding (`plane_generated.go`) is the sole authority for production combination rules, nil handling, validator execution, and identity extraction. Copying or mutating exported fields on a `Plane[T]` descriptor (e.g., copying `PlaneSubmitHooks` and altering its `Rules` or `Combine` fields) does **not** redefine the plane; production execution always enforces the canonical generated policy. If an altered copy has a modified plane ID, contribution is rejected with `feature.ErrUngeneratedPlane`.

## Constructor naming

Exported constructors that build individual hook implementations use **`New` + the hooks interface role** so call sites read like the assembled `hooks.Config` fields:

| Return type | Constructor name |
|-------------|-------------------|
| `SubmitHook` | `NewSubmitHook` |
| `RequestPartHook` | `NewRequestPartHook` |
| `ResponsePartHook` | `NewResponsePartHook` |
| `ToolReactor` | `NewToolReactor` |

- **Zero-config features** use the names above with no parameters (or defaults only).
- **Configured features** use the same names with a `(cfg <Package>Config)` argument. An extra variant is allowed when there are two entrypoints (e.g. `NewSubmitHook` and `NewSubmitHookWithConfig` for tests vs YAML-decoded config).
- **Bundle constructors** building full feature bundles use `FeatureBundle(cfg)` or `NewFeatureBundle(cfg)`.

This matches the noop and reference plugins in this tree; out-of-repo feature plugins should follow the same pattern for consistency with `pluginreg` wiring.
