# Implementation Plan

## Current Implementation Checkpoint

The uncommitted worktree contains completed implementation and certification work for Phases 1–7, including Tasks 7.1–7.4. All phases are complete; the final commit follows.

Completed Phase 1–2 work:

- **1.2:** complete. The deterministic five-frontend/1,000-profile fixture explicitly proves profile #1000 identity, four-family binding, and scans the representative structure for frontend×profile collections, nested materializers, per-profile factories/registrations/goroutines, central-list edits, and sentinel growth. Clean footprint evidence is obtained from an isolated temporary Git repository and Git-produced diff; isolated positive/negative mutation fixtures cover every structural category.
- **1.4:** complete. Ordinary architecture tests pass and the tagged target reports only enumerated current debts. Authority-aware AST mutation fixtures preserve distinct same-symbol paths and cover arbitrary contribution/diagnostic row/projector/state false positives; structural Go/protobuf ABI checks reject arbitrary synthetic qualifiers while preserving neutral semantic carriers and exact v1.0-v1.3 declarations/legacy OpenResponses exception, including multiline/comment resilience.
- **2.2:** complete. Five mounted frontends independently certify their declared semantic surfaces with explicit positive canonical-call and wire-output evidence; OpenResponses lifecycle remains protocol-owned.
- **2.3:** complete. Every standard in-process family passes the reusable capability-driven suite with tool lifecycle, usage/zero presence, errors/cancellation, hard-negative zero-upstream, and false-capability evidence. The suite uses structured request controls and bounded cancellation probes.
- **2.4:** complete. `pkg/lipsdk/backendplugin/host` is the single public host implementation; the internal adapter delegates to it, ACP/OpenRouter/NVIDIA use it, and a separate external-style module compiles the public host path. The host retains Resolve/ListModels/Execute/Cancel/Close, negotiation, terminal/error/bounds behavior, CountTokens, and FinalizeBilling; compile-time and real bufconn regressions prove both optional capabilities remain visible through the internal alias and adapter. The shared contract result remains machine-readable.

Current fresh validation evidence:

- Green: Phases 1–7, including Tasks 7.1–7.4, with architecture, scale, TCK, build, formatting, documentation, lint, fuzz, parity, quality, QA, and diff checks as recorded in `release-evidence.md`.
- `go test -tags=architecture_red -run 'TestRED_' ./internal/archtest` is an opt-in legacy-debt ratchet and is expected to remain independent of the release task checkbox.
- The aggregate `make quality-checks`, `make parity-checks`, `make test`, `make lint`, and `make qa` release gates are green; `make test-race` reports the actual Windows platform limitation documented below.

## 1. Freeze Brownfield Behavior and Add RED Architecture Contracts

- [x] 1.1 Freeze current conformance, contribution, route, diagnostics, ABI, and continuation baselines
  - Characterize `BundledFrontendIDs`, `BundledBackendIDs`, the legacy `AllCells` inventory, current required feature IDs/scenario IDs, standard/essential/compatible backend lists, frontend route claims, compatible/OpenResponses diagnostics, backend-plugin v1.0–v1.3 negotiation, and continuation store/recorder behavior. The live `AllCells` executable characterization was intentionally retired during Phase 6; its durable replacement is the pinned inventory under `internal/testkit/conformance/testdata/baseline_cartesian_inventory.json`.
  - Commit a machine-readable baseline inventory of Cartesian-only files/functions/lines at baseline SHA `95089eb4b74d5cf8d062f238a1121124ce0da878`.
  - Record current diagnostic JSON shapes before generic projection changes; the historical Phase 1 row contract is durably locked by `internal/core/diag/testdata/phase1_inventory_baseline.json`, and the current full compatibility projection by `internal/core/diag/testdata/current_inventory_snapshot.json` with regressions in `internal/core/diag/inventory_test.go`.
  - Observable completion: characterization passes on unmodified baseline behavior and produces stable legacy-surface evidence.
  - _Requirements: 1.1–1.10, 10.1–10.2, 11.6–11.7, 12.1–12.2, 12.6, 13.11_
  - _Design rules: D1, D12–D14, D16–D18_
  - _Boundary: tests/evidence only_
  - _Depends: none_
  - _Validation: go test ./internal/testkit/conformance/... ./internal/standardplugins/... ./internal/stdhttp/contract/... ./pkg/lipsdk/backendplugin/... ./pkg/lipsdk/continuation/... ./internal/core/continuation/..._

- [x] 1.2 Add RED non-Cartesian scale and extension-footprint contracts
  - Build synthetic five-frontend / 1,000-provider-profile fixtures.
  - Assert profile additions do not create frontend×profile pair collections, family factories, central compatible/essential list edits, or sentinel growth.
  - Add a provider-profile-only fixture whose expected shared-boundary footprint is zero.
  - Observable completion: tests fail against the current matrix/parallel-registry architecture for the intended reasons.
  - _Requirements: 2.1–2.10, 6.11, 7.5, 12.8, 13.3, 13.7_
  - _Design rules: D2, D5, D7, D15, D18_
  - _Boundary: architecture/test fixtures_
  - _Depends: 1.1_
  - _Validation: go test ./internal/archtest/... ./internal/testkit/..._

- [x] 1.3 Add RED contract-test-kit interfaces and semantic scenario metadata
  - Define test-only/supported dependency-neutral semantic feature IDs, scenario IDs, subject descriptors, certification DTOs, and narrow frontend/backend harness contracts.
  - Keep executable scenario builders out of production runtime packages.
  - Add compile-time tests proving third-party connector-facing contract types do not import `internal` or concrete backend packages.
  - Observable completion: interfaces/metadata compile and RED runner tests specify expected scenario selection/evidence before runners exist.
  - _Requirements: 3.1–3.3, 3.7–3.10, 4.1–4.3, 4.9–4.10, 5.1, 10.10, 13.1_
  - _Design rules: D1–D4, D8, D14_
  - _Boundary: test contract/API definitions_
  - _Depends: 1.1_
  - _Validation: go test ./internal/testkit/contract/... ./pkg/lipsdk/backendplugin/contracttest/..._

- [x] 1.4 Add RED contribution/profile/ABI/mirror architecture guards
  - Add tests that reject duplicate authoritative extension registries, central protocol-specific route kinds, protocol-ID diagnostic switches, new protocol-named backend-plugin features/proto fields beyond the v1.3 compatibility allowlist, provider profiles imported by core, and duplicate continuation authorities.
  - Add explicit allowlist for current legacy v1.3 OpenResponses ABI symbols only.
  - Observable completion: current known gaps fail the new guards while existing legitimate provider dialect IDs remain allowed.
  - _Requirements: 7.1–7.10, 8.1–8.8, 10.3, 10.11, 11.1–11.9, 13.1–13.4_
  - _Design rules: D7–D13, D18_
  - _Boundary: internal/archtest_
  - _Depends: 1.1_
  - _Validation: go test ./internal/archtest/..._


### Phase 1–2 completion evidence (tasks 1.1–1.4 and 2.1–2.4)
- `go test -count=1 ./internal/testkit/scale/...` passes; the scale suite uses deterministic temporary Git repositories and Git-produced diffs plus isolated mutation fixtures.
- `go test -count=1 ./internal/archtest` passes, including the separate external-style public-host compile gate. The root-only enterprise gate is separately blocked on Linux by its administrative-user refusal.
- Opt-in RED command: `go test -count=1 -tags=architecture_red -run 'TestRED_' ./internal/archtest` is a migration ratchet for current architecture debt and the bounded sentinel policy. Its non-Cartesian test no longer enforces the retired 45-cell/AllCells release invariant; the historical 45-cell count remains pinned in `baseline_cartesian_inventory.json` and is checked by the deletion gate.
- Focused suites: `go test -count=1 ./internal/testkit/contract/... ./pkg/lipsdk/backendplugin/...`; `go test -count=1 ./internal/refbackend/... ./internal/plugins/backends/protocols/geminigenerate/...`; and `go test -count=1 ./...` in ACP, OpenRouter, and NVIDIA connector modules.

## 2. Build Frontend, Core, Backend, and Connector TCKs

- [x] 2.1 Implement the canonical-core contract suite
  - Move/reuse canonical semantic fixtures to test requirement derivation, exact matching, item↔legacy projectors, transport admission, frozen failover requirements, output commitment, and terminal validation without real provider adapters.
  - Add deliberate mutation tests proving each core invariant turns RED.
  - Observable completion: core semantic proof is executable without frontend/backend Cartesian cells.
  - _Requirements: 5.1–5.4, 9.3–9.5, 9.9_
  - _Design rules: D1, D3–D4, D9–D10_
  - _Boundary: internal/testkit/contract/core and focused pkg/lipapi/core tests_
  - _Depends: 1.3_
  - _Validation: go test ./internal/testkit/contract/core/... ./pkg/lipapi/... ./internal/core/capabilities/... ./internal/core/runtime/..._

- [x] 2.2 Implement the frontend TCK runner
  - Implement capturing executor, scripted canonical event stream, common request/error/stream contracts, and certification output.
  - Migrate reusable text/tools/multimodal/reasoning/structured-output/error scenarios from matrix code without importing backends.
  - Keep OpenResponses continuation/WebSocket/compaction lifecycle in protocol-owned tests composed alongside the TCK.
  - Observable completion: all bundled frontends obtain frontend certification without constructing real provider backends.
  - _Requirements: 4.1–4.10, 1.1, 1.7, 5.10_
  - _Design rules: D1–D3, D14, D16_
  - _Boundary: internal/testkit/contract/frontend + frontend tests_
  - _Depends: 1.3, 2.1_
  - _Validation: go test ./internal/testkit/contract/frontend/... ./internal/plugins/frontends/..._

- [x] 2.3 Implement the backend-family TCK runner with zero-upstream proof
  - Implement capability/dialect-driven positive selection and hard-negative scenarios.
  - Add request/process probe support and canonical event/usage/error/cancel/lifecycle assertions.
  - Provide harness adapters for essential built-ins and compatible-family adapters using current refbackends/httptest providers.
  - Observable completion: each current in-process backend family passes one reusable semantic suite; false capability mutation is caught.
  - _Requirements: 3.1–3.7, 3.9–3.10, 1.2–1.4_
  - _Design rules: D1–D4, D14_
  - _Boundary: internal/testkit/contract/backend + backend tests_
  - _Depends: 1.3, 2.1_
  - _Validation: go test ./internal/testkit/contract/backend/... ./internal/plugins/backends/..._

- [x] 2.4 Implement the supported executable-connector contract test entry point
  - Drive negotiation/configure/execute/cancel/close through the real backend-plugin host adapter.
  - Reuse the same semantic scenario source as 2.3; do not fork a connector-specific feature catalog.
  - Add at least ACP/OpenRouter/NVIDIA/current connector representatives and a fake external-style connector fixture.
  - Observable completion: connector authors can invoke one supported SDK contract test and receive machine-readable certification evidence.
  - _Requirements: 3.7–3.10, 10.9–10.10, 13.1_
  - _Design rules: D3–D4, D11–D14_
  - _Boundary: pkg/lipsdk/backendplugin/contracttest + connector-host test harness_
  - _Depends: 2.3_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./tools/backendplugin/... ./internal/infra/backendplugins/adapter/..._

### Phase 2 completion evidence (tasks 2.1–2.4)
- **Frontend Authentication-Ordering Evidence:** Proved in `TestBundledFrontends_ProveAuthenticationBeforeSensitiveWork` (`internal/testkit/contract/frontend/standard_frontends_test.go`), verifying unauthenticated request rejection with 4xx before reaching `Exec.Execute`.
- **Core Sticky-Affinity Cleanup Evidence:** Verified via `StickyCleanup` hook in `RunSemanticInvariantSuite` (`internal/testkit/contract/core/harness.go`), asserting affinity cleanup on candidate admission rejection.
- **In-Flight Cancellation Evidence:** Realized in `harness.go` (`internal/testkit/contract/frontend/harness.go`), executing `context.WithCancel` mid-flight cancellation during HTTP stream handling.
- **Shared Scenario Vocabulary Evidence:** Added `FeatureParallelTools`, `FeatureVideo`, `FeatureAnnotations`, `FeatureAssistantPhase`, and `FeatureAssistantMedia` vocabulary constants to `pkg/lipsdk/contract/scenarios.go`.

## 3. Introduce Provider Families and Typed Provider Profiles

- [x] 3.1 Define the provider-profile v1 schema and RED validation suite
  - Define typed versioned profile contracts for family, endpoint/path policy, credential references, safe headers, model discovery/namespace, tokenizer/accounting, capability/dialect overrides, and closed family quirks.
  - Add refusals for arbitrary transformations, unsafe auth/header combinations, unknown quirks/versions, capability elevation beyond family support, invalid endpoints, and unbounded data.
  - Observable completion: schema tests define safe declarative boundaries before catalog/runtime implementation.
  - _Requirements: 6.1–6.5, 6.7–6.8, 6.12_
  - _Design rules: D5–D6, D9–D10_
  - _Boundary: internal/providerprofiles contract/tests_
  - _Depends: 1.2_
  - _Validation: go test ./internal/providerprofiles/..._

- [x] 3.2 Implement deterministic embedded provider-profile catalog and family binding
  - Add one source-of-truth profile catalog loaded through `go:embed` or deterministic generated typed values.
  - Bind profiles to existing compatible family adapters without one Go factory registration per provider.
  - Preserve arbitrary custom-compatible user configuration independently from the shipped profile catalog.
  - Observable completion: current/synthetic profiles resolve to family configurations through one compiler and require no provider network/process activation.
  - _Requirements: 6.2–6.7, 6.9, 6.12, 7.5_
  - _Design rules: D5–D8_
  - _Boundary: provider profile catalog + standard composition_
  - _Depends: 3.1_
  - _Validation: go test ./internal/providerprofiles/... ./internal/standardplugins/... ./internal/infra/runtimebundle/..._

- [x] 3.3 Implement profile certification and 1,000-profile scale proof
  - Certify family binding, endpoint/auth/header/model behavior, effective capability/dialect calculation, and declared quirks without frontend multiplication.
  - Add 1,000-profile synthetic catalog load/validation tests and assert zero provider network/process starts and no per-profile goroutine.
  - Observable completion: profile #1000 costs one profile certification path and does not affect frontend TCK/sentinel counts.
  - _Requirements: 2.2–2.7, 6.7–6.11, 12.8_
  - _Design rules: D2–D6, D15_
  - _Boundary: profile TCK/scale tests_
  - _Depends: 3.2, 2.3_
  - _Validation: go test ./internal/providerprofiles/... ./internal/testkit/contract/backend/..._

- [x] 3.4 Integrate provider-profile changes with generation compile/reload and diagnostics source data
  - Reuse existing immutable candidate/generation compilation and startup security validation.
  - Prove profile catalog changes rebuild affected family instances without a second mutable registry.
  - Keep profile values secret-safe and provider processes dormant during validation.
  - Observable completion: startup/check-config/reload characterization remains coherent for profile-backed compatible instances.
  - _Requirements: 1.5–1.6, 6.7–6.9, 8.5–8.7_
  - _Design rules: D5–D8_
  - _Boundary: runtimebundle/configreload composition only_
  - _Depends: 3.2_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/core/configreload/... ./internal/standardplugins/..._

### Phase 3 completion evidence (tasks 3.1-3.4)
- `internal/providerprofiles` now owns the typed v1 contract, closed JSON/YAML decoding, bounds, endpoint/auth/header/model/accounting/dialect/quirk validation, deterministic catalog ordering, immutable family compilation, embedded catalog, and comprehensive `Certify()` verification covering endpoint URL format, auth credential references, safe headers, and model mapping/namespaces (Req 6.10). Unsupported quirks remain fail-closed until a family adapter implements and certifies their behavior.
- `internal/standardplugins` exposes one profile compiler/family binding path and secret-safe operator diagnostics via `ProviderProfileDiagnostics()` and `ProjectProviderProfileDiagnostics` registered with `StandardDiagnosticProjectors()` (Req 6.8).
- The 1,000-profile scale test in `profile_test.go` certifies 1,000 profiles through a pure deterministic catalog/compiler path, checks stable goroutine counts, and statically verifies that the compiled value contains no runtime handles or activation hooks. This is a no-activation boundary proof for the pure profile path; process/network absence is not claimed as an observational runtime measurement (Req 6.11).
- Runtime-bundle characterization tests verify the provider-profile lifecycle in bounded stages: `TestProviderProfile_ReloadPublishesExpandedFamilyRow` exercises real host reload publication, while `TestProviderProfile_CompileReloadAndDiagnosticsEndToEnd` exercises structural validation, compilation, and inventory diagnostics. Together they cover provider-profile compilation, reload, and diagnostics without provider activation.
- Focused green evidence: `go test ./internal/providerprofiles/... ./internal/standardplugins/... ./internal/core/configreload/... ./internal/infra/runtimebundle/... ./internal/testkit/contract/backend/...`.

## 4. Single-Source Contribution Metadata and Generic Projections

- [x] 4.1 Add focused frontend/backend contribution facets behind RED derivation tests
  - Define registration, route, diagnostic, contract, and compatible-family facets without carrying runtime service bags.
  - Add synthetic contributions proving deterministic derivation, uniqueness, and automatic appearance in declared views.
  - Observable completion: tests specify one-authority behavior before central lists are removed.
  - _Requirements: 7.1–7.9, 13.7_
  - _Design rules: D7–D8_
  - _Boundary: internal/standardplugins or cycle-neutral contribution package_
  - _Depends: 1.4, 2.2, 2.3_
  - _Validation: go test ./internal/standardplugins/... ./internal/pluginreg/..._

- [x] 4.2 Migrate standard/essential/compatible registration to contribution-derived views
  - Derive `StandardBundle`, essential IDs, compatible family IDs, contract subjects, and provider-profile family bindings from one source.
  - Delete parallel authoritative lists/switches superseded by contribution facets.
  - Preserve current optional-connector rule: optional connector identities are not promoted into essential built-ins.
  - Observable completion: all existing built-ins resolve identically and provider profile additions require no central Go list edits.
  - _Requirements: 7.1–7.10, 2.2–2.5_
  - _Design rules: D5, D7–D8, D17_
  - _Boundary: internal/standardplugins/internal/pluginreg composition_
  - _Depends: 4.1, 3.2_
  - _Validation: go test ./internal/standardplugins/... ./internal/pluginreg/... ./internal/infra/runtimebundle/..._

- [x] 4.3 Generalize route-kind ownership and derive route claims from frontend contributions
  - Keep normalized owner/method/path conflict validation.
  - Move concrete operation IDs to frontend-owned contributions/protocol packages and replace central closed route-kind additions with bounded opaque IDs.
  - Derive standard route-claim providers from frontend contributions.
  - Observable completion: current route collision tests pass and a synthetic new frontend route requires no `stdhttp/contract` enum edit.
  - _Requirements: 8.1–8.2, 8.7–8.8, 7.4_
  - _Design rules: D7–D8, D18_
  - _Boundary: stdhttp contract + frontend contribution metadata_
  - _Depends: 4.1_
  - _Validation: go test ./internal/stdhttp/contract/... ./internal/standardplugins/... ./internal/stdhttp/..._

- [x] 4.4 Replace protocol-specific central diagnostics rows/switches with bounded generic contribution projectors
  - Characterize existing HTTP JSON first and retain compatibility serialization/versioning where needed.
  - Introduce common instance diagnostics plus bounded sanitized extension fields.
  - Move protocol-specific projection to contribution-owned side-effect-free projectors; remove OpenResponses-compatible/frontend selection switches from central composition.
  - Observable completion: operator information remains equivalent while a synthetic protocol adds diagnostics without a new core diagnostic DTO/switch.
  - _Requirements: 8.3–8.8, 7.4, 7.10_
  - _Design rules: D7–D10, D17_
  - _Boundary: internal/core/diag + standardplugins projection adapters_
  - _Depends: 4.1, 4.2_
  - _Validation: go test ./internal/core/diag/... ./internal/standardplugins/... ./internal/stdhttp/..._

### Phase 4 completion evidence (tasks 4.1–4.4)
- **Single-Source Metadata & Contribution Facets:** `internal/standardplugins/contrib` defines `FrontendContribution` and `BackendContribution` with focused facets (`Registration`, `Routes`, `Diagnostics`, `Contract`, `Compatible`).
- **Contribution-Derived Diagnostic Projectors:** Central composition (`validate_structural.go`) executes contribution-derived diagnostic projectors (`validateDiagnosticProjectors`) instead of protocol-specific switches/hooks. `standard_contributions.go` declares backend diagnostic ownership (`ProjectCompatibleBackendInstanceRows`) and `StandardDiagnosticProjectors` collects both frontend and backend diagnostic projectors cleanly.
- **Protocol-Neutral Route Claims & Opaque Operation IDs:** `internal/stdhttp/contract/route_claim.go` is completely free of protocol-specific default claim builders. Route claims and operation IDs are owned by protocol packages/contributions. Tested via `TestSyntheticOpaqueRoute_ContributionDerivedAndRegistered` in `frontend_route_claims_test.go`.
- **Focused Green Validation:** `go test ./internal/core/diag/... ./internal/standardplugins/... ./internal/stdhttp/contract/... ./internal/infra/runtimebundle/...` passes cleanly.

## 5. Harden Canonical and Backend-Plugin Extension Boundaries

- [x] 5.1 Audit protocol-named canonical fields with characterization tests before any migration
  - Trace all readers/writers of `PromptCacheKey`, reasoning Summary/Content/EncryptedContent presence, compaction encrypted content, and related OpenResponses/Codex carriers.
  - Classify each as core/shared semantic or adapter-only fidelity using Requirement 9 promotion rules.
  - Add characterization round-trips and projection/admission tests for every migration candidate.
  - Observable completion: classification/evidence is complete before changing public canonical shape.
  - _Requirements: 9.1–9.10, 1.9_
  - _Design rules: D9–D10, D14, D16_
  - _Boundary: canonical audit/tests_
  - _Depends: 2.1_
  - _Validation: go test ./pkg/lipapi/... ./internal/plugins/backends/openresponsescompat/... ./internal/plugins/frontends/openresponses/... ./connectors/codex/..._

- [x] 5.2 Migrate only proven adapter-only canonical fidelity to bounded negotiated carriers
  - Reuse existing extension carriers where sufficient; add at most one generic presence-bearing semantic residual carrier if evidence proves a gap.
  - Keep shared/core-consumed reasoning/compaction semantics first-class.
  - Preserve source compatibility through aliases/transitional fields where practical and forbid raw request/response tunneling. The audit evidence records why `Call.Extensions`, item/content carriers, and existing backend-plugin fields cannot preserve call-level presence/identity safely.
  - Observable completion: canonical first-class surface is smaller or no larger for adapter-only fidelity, with all characterization tests green.
  - _Requirements: 9.2–9.9, 1.9_
  - _Design rules: D9–D10, D17_
  - _Boundary: pkg/lipapi + adapter conversions only_
  - _Depends: 5.1_
  - _Validation: go test ./pkg/lipapi/... ./internal/core/... ./internal/plugins/frontends/... ./internal/plugins/backends/..._

- [x] 5.3 Add semantic backend-plugin carrier/negotiation support without breaking v1.3
  - First add RED v1.0–v1.3 compatibility and unknown-carrier tests.
  - If required by 5.2, add one protocol-neutral semantic extension feature/carrier with strict bounds and exact requirement matching.
  - Preserve `exact_openresponses_fields` as legacy compatibility vocabulary; bridge without dual authority or silent loss.
  - Observable completion: current connectors negotiate exactly as before unless opting into the additive generic semantic feature.
  - _Requirements: 10.1–10.10, 1.2, 1.9_
  - _Design rules: D10–D12_
  - _Boundary: api/backendplugin/v1 + pkg/lipsdk/backendplugin + host adapter_
  - _Depends: 5.2, 2.4_
  - _Validation: go test ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/adapter/... ./tools/backendplugin/..._

- [x] 5.4 Enforce future canonical/ABI promotion policy in architecture/docs
  - Add architecture tests for new protocol-named backend-plugin feature/proto additions outside the legacy allowlist.
  - Document the canonical promotion checklist and semantic-ABI rule with positive/negative examples.
  - Observable completion: a synthetic protocol-named ABI feature fails architecture tests while a semantic carrier extension passes.
  - _Requirements: 9.10, 10.3, 10.8, 10.11, 13.8–13.10_
  - _Design rules: D9–D12, D18_
  - _Boundary: archtest + docs_
  - _Depends: 5.3_
  - _Validation: go test ./internal/archtest/... && make docs-check_

### Phase 5 completion evidence (tasks 5.1–5.4)
- **Canonical Surface & Characterization Audit:** Documented canonical promotion policy in `docs/adr/0010-extension-scalability-and-architecture-simplification.md`. Characterized PromptCacheKey alias bridging and residual semantic carriers in `pkg/lipapi/semantic_extension_test.go`.
- **Bounded Semantic Carrier (`semantic_extensions_v1`):** Added bounded `SemanticExtension` carrier to `lipapi` and `backendplugin` ABI (minor 6). Preserves source-compatible `PromptCacheKey` alias while bridging to single semantic authority.
- **Additive Backend-Plugin Minor 6 & v1.3 Compatibility:** Tested v1.0–v1.3 backward compatibility in `pkg/lipsdk/backendplugin/semantic_extensions_test.go` and `pkg/lipsdk/backendplugin/contracttest/host_adapter_test.go`. Connectors negotiating minor 6 carry `SemanticExtension` without breaking legacy clients.
- **Architecture Guards & Promotion Policy:** Added architecture tests in `internal/archtest/extension_architecture_guards_test.go` guarding against raw unnegotiated protocol-named ABI additions.
- **Focused Green Validation:** `go test ./pkg/lipapi/... ./pkg/lipsdk/backendplugin/... ./internal/infra/backendplugins/adapter/... ./pkg/lipsdk/backendplugin/contracttest/... ./internal/archtest/...` passes cleanly (100% green).

## 6. Converge Continuation Ownership and Cut Over Conformance

- [x] 6.1 Collapse continuation store/recorder mirrors to one authority
  - Run parity characterization against SDK/core copies first.
  - Retain `pkg/lipsdk/continuation` as default protocol-neutral contract/utility authority, retain durable infra adapters, and reduce core to orchestration/delegation.
  - Delete duplicate mutable state machines and add mirror regression guards.
  - Observable completion: one `MemoryStore` and one `StreamRecorder` authority remain with unchanged security/lifecycle behavior.
  - _Requirements: 11.1–11.9, 1.5_
  - _Design rules: D13, D17_
  - _Boundary: pkg/lipsdk/continuation + internal/core/continuation + internal/infra/continuation_
  - _Depends: 1.4_
  - _Validation: go test ./pkg/lipsdk/continuation/... ./internal/core/continuation/... ./internal/infra/continuation/..._

- [x] 6.2 Build the bounded real-stack sentinel and prove provider-count independence
  - Select representative built-in, compatible-family, connector-host, stateful frontend, and negative admission paths with explicit `Protects` rationale.
  - Add bound tests and verify 1,000 additional profiles in existing families do not change sentinel count.
  - Observable completion: sentinel catches deliberately broken composition wiring but is independent of provider population.
  - _Requirements: 5.5–5.9, 2.6–2.9, 12.8_
  - _Design rules: D2, D15–D16_
  - _Boundary: integration/conformance sentinel tests_
  - _Depends: 2.2–2.4, 3.3, 4.2_
  - _Validation: go test -tags=precommit,integration ./internal/testkit/contract/... ./internal/integration/..._

- [x] 6.3 Dual-run legacy matrix and TCK model with feature traceability/mutation proof
  - Map every current release-critical matrix feature to frontend/core/backend/profile/protocol/sentinel ownership.
  - Run both systems at the same implementation head.
  - Inject representative decode, projector, false-capability, connector-field-loss, and composition faults and prove the new owner suite catches each.
  - Observable completion: no required feature has matrix-only ownership.
  - _Requirements: 12.1–12.3, 12.9, 5.10_
  - _Design rules: D14–D16_
  - _Boundary: migration evidence/tests_
  - _Depends: 2.1–2.4, 6.2_
  - _Validation: make parity-checks && go test ./internal/testkit/contract/..._

- [x] 6.4 Retire Cartesian completeness and delete obsolete evidence scaffolding
  - Switch parity/release correctness to TCK certifications + protocol-owned suites + bounded sentinel.
  - Remove authoritative `AllCells` completeness, OpenResponses row/column/general-cell feature evidence used only by Cartesian proof, and pairwise metadata.
  - Preserve independent emulators/compliance and reusable scenarios.
  - Enforce ≥80% deletion of baseline Cartesian-only non-generated Go lines and no net growth in reviewed affected shared surfaces.
  - Observable completion: no mandatory release path constructs all frontend×backend pairs; deletion gates pass.
  - _Requirements: 2.1, 5.10, 12.3–12.10_
  - _Design rules: D2, D14–D17_
  - _Boundary: internal/testkit/conformance, Makefile/workflows as needed_
  - _Depends: 6.3_
  - _Validation: make test && make parity-checks && make quality-checks_

### Phase 6 completion evidence (tasks 6.1–6.4)
- **Continuation Ownership Convergence:** Retained `pkg/lipsdk/continuation` as the single protocol-neutral contract and utility authority. Deleted duplicate core continuation stores/recorders (`internal/core/continuation`). Verified via `go test ./pkg/lipsdk/continuation/...`.
- **Bounded Real-Stack Sentinel & Scale Independence:** Implemented `BoundedSentinelCases()` in `internal/testkit/conformance/sentinel.go` and verified 1,000 profile scale independence in `internal/testkit/scale/profile_scale_test.go`.
- **Dual-Run 45-Cell Matrix Equivalence & TCK Cutover:** Documented same-head dual-run baseline characterization in `internal/testkit/conformance/testdata/baseline_cartesian_inventory.json` (capturing all 45 cells across 5 frontends x 9 backends). Mapped all 17 release-critical features to explicit frontend/core/backend/profile/protocol/sentinel owners in `internal/testkit/contract/traceability.go`. Added real connector field-loss mutation execution test in `owner_mutation_test.go`.
- **Cartesian Completeness Retirement & Retained Parity:** Switched release gates from 45-cell matrix enumeration to TCK certifications + protocol suites + bounded sentinel. Achieved 82.3% deletion of legacy Cartesian-only non-generated Go lines.
- **Focused Green Validation:** `make parity-checks && go test ./internal/testkit/contract/... ./pkg/lipsdk/continuation/...` passes cleanly (100% green).

## 7. Add Change-Surface Ratchets, Documentation, and Release Evidence

- [x] 7.1 Implement extension change-surface reporting and hard profile-only boundary checks
  - Classify diffs into extension-owned, provider-profile, shared composition, canonical, core routing/runtime, ABI, generated, tests/reference, and docs/spec categories.
  - Add fixture tests showing generated/test breadth does not equal architectural coupling.
  - Enforce zero canonical/core/frontend/ABI/shared-registry edits for provider-profile-only fixture additions.
  - Observable completion: report explains blast radius and profile-only contract fails on synthetic shared-boundary edits.
  - _Requirements: 13.5–13.8, 2.2–2.3_
  - _Design rules: D5, D7, D18_
  - _Boundary: tooling/internal/archtest_
  - _Depends: 4.2, 6.4_
  - _Validation: go test ./internal/archtest/... ./tools/... && make quality-checks_

- [x] 7.2 Consolidate architecture rules and remove protocol-specific guard duplication where generic rules suffice
  - Replace OpenResponses-only import/registry guards with zone/contribution/ABI rules where semantics are generic.
  - Retain protocol-specific guards only for real wire/provenance/emulator-independence constraints.
  - Observable completion: architecture suite becomes smaller or equal while catching representative violations for arbitrary synthetic protocols/providers.
  - _Requirements: 13.1–13.4, 12.7_
  - _Design rules: D7–D12, D17–D18_
  - _Boundary: internal/archtest_
  - _Depends: 4.2–4.4, 5.4, 6.4_
  - _Validation: go test ./internal/archtest/..._

- [x] 7.3 Publish extension/provider authoring and conformance documentation
  - Document decision tree: provider profile vs family adapter vs executable connector.
  - Document profile schema/security, contribution facets, frontend/backend/connector TCK entry points, sentinel purpose, canonical promotion checklist, semantic ABI evolution, and change-surface review.
  - Remove docs that describe 5×9 Cartesian completeness as permanent architecture.
  - Observable completion: a provider author can add a profile/family/connector without discovering hidden central registries.
  - _Requirements: 6.5, 9.10, 13.9–13.10_
  - _Design rules: D5–D12, D15, D18_
  - _Boundary: docs/steering updates where durable_
  - _Depends: 7.2_
  - _Validation: make docs-check_

- [x] 7.4 Run final certification, scale, race/fuzz, shrinkage, and release gates
  - Run frontend/core/backend/connector/profile TCK certifications, bounded sentinel, protocol-specific compliance, synthetic 1,000-profile scale, architecture/change-surface report, continuation security/race, relevant fuzz, and full repository quality gates.
  - Record baseline SHA, certification subjects/scenarios, legacy surface deletion %, affected-surface line delta, synthetic scale counts, wall-clock evidence, and any environmental limitations without claiming skipped evidence.
  - Observable completion: all deterministic gates pass at exact implementation head and spec tasks can be closed with auditable evidence.
  - _Requirements: 1.1–1.10, 2.1–2.10, 3.1–3.10, 4.1–4.10, 5.1–5.10, 6.1–6.12, 7.1–7.10, 8.1–8.8, 9.1–9.10, 10.1–10.11, 11.1–11.9, 12.1–12.10, 13.1–13.12_
  - _Design rules: D1–D18_
  - _Boundary: repository release evidence_
  - _Depends: 7.1–7.3_
  - _Validation: make quality-checks && make test && make qa; make test-race where supported; targeted provider-profile/semantic-carrier fuzz campaigns_
