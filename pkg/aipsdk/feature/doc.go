// Package feature defines typed extension planes and the contribution lifecycle for
// feature plugins in AIProxer.
//
// # Contribution Lifecycle
//
// A feature plugin contributes capabilities by assembling typed extension plane values
// into a [ContributionSet]. The lifecycle proceeds as follows:
//
//  1. Construct a mutable set using [NewContributionSet].
//  2. Add typed values using the package-level [Contribute] function. [Contribute] tags
//     contributions with [SourceFeature] and enforces fail-before-mutate semantics: if
//     validation or combination fails, the set remains unmodified and an [AttributedError]
//     attributing the contributor ID and plane ID is returned.
//  3. Once all contributions are accumulated, call [ContributionSet.Freeze] to produce an
//     immutable [FrozenPlaneSet].
//  4. Wrap the frozen planes and any optional plugin lifecycles into a [FeatureBundle] by
//     calling [BundleFromPlanes]. [BundleFromPlanes] assigns [SchemaVersionV1] and
//     defensively clones the frozen plane set and lifecycle slices, preserving nil vs
//     explicit empty slice semantics without validation side effects.
//
// The frozen plane set lifecycle transitions through well-defined stages:
//
//   - Accumulation: [ContributionSet] provides mutable staging with typed generated storage.
//   - Freezing: [ContributionSet.Freeze] creates an immutable [FrozenPlaneSet], providing top-level
//     collection isolation: slice backing arrays and metadata maps are isolated; element values
//     (e.g. interface handlers) are shallow-copied, not deep-cloned.
//   - Bundle packaging: [BundleFromPlanes] bundles the [FrozenPlaneSet] into a versioned
//     [FeatureBundle]. Note that [FeatureBundle] contains no per-plane named fields or slices;
//     all extension planes are stored in [FeatureBundle.PlaneSet].
//   - Reading: Values are read from [FrozenPlaneSet] via [Get]. If an ungenerated plane is
//     requested or a plane was not contributed, [Get] returns the zero value of the plane type
//     and does not search dynamic fallback storage.
//   - Replay & Thaw: An existing set can be replayed to another set via [FrozenPlaneSet.ReplayTo]
//     or thawed back into a mutable staging set via [FrozenPlaneSet.ToContributions].
//   - Request snapshot: [FreezeRequestPlanes] evaluates declared request materializers to produce
//     an immutable request-scoped execution snapshot.
//
// Plugin lifecycles ([plugin.Lifecycle]) are managed on a dedicated runtime side channel
// and are distinct from extension planes. (Historical note: named extension plane fields
// on [FeatureBundle] were removed in favor of [FrozenPlaneSet].)
//
// # Reading Values
//
// Plane values are read from a [FrozenPlaneSet] using the generic package-level function
// [Get]:
//
//	values := feature.Get(bundle.PlaneSet, feature.PlaneRequestTransforms)
//
// Package-level generic functions are used because Go methods on structs cannot introduce
// type parameters. Key read semantics include:
//
//   - If a plane was not contributed or is absent from the set, [Get] returns the zero value
//     of the plane's type (e.g. nil for slice-valued planes, 0 for scalar planes).
//   - Slice-valued planes return defensive copies on ordinary [Get] calls to guarantee that
//     caller modifications do not corrupt the frozen snapshot.
//   - Runtime borrowed request execution views ([RequestExecutionView]) are internal generated
//     optimizations used on executor hot paths and must not be used by plugin authors.
//
// # Plane Declarations
//
// An extension plane is declared as a [Plane] specification defining its types, combination
// rules, validation, and diagnostics metadata:
//
//   - ID: A globally unique, non-empty, stable string identifier.
//   - Multiplicity: Must be [MultOrdered] (multiple ordered contributions combined in
//     registration order) or [MultExclusive] (at most one occupant permitted, failing fast
//     on conflict).
//   - Rules ([SourceRules]): Explicit per-source combination rules for [SourceFeature],
//     [SourceHost], and [SourceGenerationBinder]. Unsupported sources remain the zero value
//     [CombUnsupported].
//   - NilPolicy: Defines handling of nil contributions ([NilNotApplicable], [NilReject],
//     [NilSkip]), evaluated before validation or combination. For interface-valued planes,
//     an explicit IsNil predicate must be provided to detect typed-nil pointers boxed in
//     interfaces without runtime reflection.
//   - Validate: Optional validator function for incoming contribution values.
//   - Combine: Folding function that combines incoming values with the accumulated state
//     and returns the retained value for the specified source rule. It must not mutate
//     caller-owned or current stored state on failure; [ContributionSet] supplies defensive
//     clones so failed combination leaves the set unchanged.
//   - Identity & ValidateIdentity: Required for exclusive planes and replace-by-identity
//     sources to extract and validate stable identity strings.
//   - ExclusiveConflictError: Optional compatibility error; valid only when at least one
//     source rule is [CombExclusive]; conflict still preserves generic exclusive conflict
//     classification ([ErrExclusiveConflict]).
//   - Diagnostics ([DiagnosticDescriptor]): Configures operator inventory and privilege
//     projection. Descriptor fields include:
//   - StageID: Identifies a legal lifecycle stage ([ValidateStageID]).
//   - Materialize: Creates diagnostic occupants from plane values.
//   - Privileges: Projects privilege flags from plane values.
//   - CoalesceGroup: Groups compatible stage occupancy across planes.
//   - Order: Gives deterministic ordering across stage diagnostics.
//     When a StageID is set, Materialize must be non-nil, Order must be > 0, and the stage ID
//     must be valid. If StageID is absent, descriptor metadata must be empty.
//   - RequestMaterializer & RequestBorrow: An optional sorting/materialization transform for
//     request execution snapshots. When RequestBorrow is enabled, a non-nil RequestMaterializer
//     is required.
//   - [Plane.ValidateDeclaration] and [ValidateManifest] enforce these rules and check catalog
//     consistency across all declarations.
//
// # Closed Manifest and Policy Authority
//
// In v1, AIProxer enforces a closed standard-plane catalog declared in the canonical manifest
// (pkg/aipsdk/feature/plane_manifest.go). The closed manifest guarantees that every supported
// extension plane has generated typed storage, deterministic dispatch, and full freeze/replay
// coverage.
//
// Key contract rules for the closed catalog include:
//
//   - No dynamic planes in v1: Arbitrary unbound or dynamically declared planes are not
//     supported. Contributing through an ungenerated or unbound plane fails immediately before
//     candidate mutation with [ErrUngeneratedPlane].
//   - Canonical generated-policy authority: Exported [Plane] descriptors (such as [PlaneSubmitHooks])
//     act as typed descriptors and identifiers. The generated binding is the sole authority for
//     production combination, source rules, nil policy, validator, and identity extraction.
//     Copying or mutating exported fields on a [Plane] descriptor (e.g. copying PlaneX and changing
//     its rules or combiner) does not redefine the plane; production contribution always executes
//     the canonical generated policy. If an altered copy has a modified plane ID, contribution is
//     rejected with [ErrUngeneratedPlane].
//   - Adding a new extension plane requires an upstream manifest and platform change: declaring
//     the plane in plane_manifest.go and regenerating code via scripts/generate-feature-planes.go.
//
// # Generated-File Policy
//
// The extension plane catalog and dispatch machinery follow a strict code generation policy:
//
//   - pkg/aipsdk/feature/plane_manifest.go is the canonical hand-authored catalog of standard
//     plane declarations.
//
//   - pkg/aipsdk/feature/plane_generated.go is checked in and must not be edited manually.
//
//   - After modifying or adding plane declarations, run:
//
//     go run ./scripts/generate-feature-planes.go
//
//   - Verify that generated files are up to date with:
//
//     go run ./scripts/generate-feature-planes.go -check
//
// The generator produces typed storage and ordinal dispatch adapters that eliminate runtime
// reflection, unsafe type conversions, and request-path key lookup.
//
// # Standard Distribution Registration
//
// Adding a standard in-process feature implementation to AIProxer involves:
//
//  1. The feature implementation package lives under internal/plugins/features/<feature>.
//     Feature plugins own their configuration decoding and bundle construction via a feature-owned
//     constructor or factory (e.g. NewFeatureBundle or FeatureBundle) in the target architecture
//     (demonstrated by migrated plugins toolcallrepair, secretguard, and reasoningpreservation).
//     Standard factories where internal/standardplugins/features_install.go still directly constructs
//     the [ContributionSet] and [FeatureBundle] (such as Agent Loop Guard at features_install.go:38
//     and Pre-request Policy at features_install.go:220) are deferred with inventory tracking
//     rather than universally completed; new features follow the feature-owned model.
//  2. Standard distribution wiring in internal/standardplugins/features_install.go provides only
//     explicit registration and adaptation to connect the feature factory to the standard bundle.
//  3. The sole registration table edit is adding exactly one FeatureRegistration row to
//     internal/standardplugins/standard_table.go in StandardBundle().Features.
//  4. Do not add feature-specific branches or types to core/runtime or any other registry.
//     Features own their policy; neither internal/core nor internal/infra/runtimebundle imports
//     concrete feature packages. If a feature requires process- or generation-bound capabilities,
//     explicit typed composition adapters outside runtimebundle (such as internal/infra/*compose)
//     are used.
//  5. Optional executable backend connectors use an independent gRPC manifest discovery
//     mechanism and are out of this feature-registration path.
package feature
