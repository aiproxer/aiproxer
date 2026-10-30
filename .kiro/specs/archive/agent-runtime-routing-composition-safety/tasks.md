# Implementation Plan

## 1. Freeze Brownfield Behavior and Add RED Contracts

- [x] 1.1 Define RED execution-metadata and routing-policy contracts
  - Add table-driven tests for `inference`, `agent_runtime`, omitted/effective `unknown`, invalid authored class, and execution profile validation without changing production registration behavior first.
  - Add routing config tests proving omitted `execution_composition_policy` resolves to `safe`, `safe` and `unrestricted` are accepted, and any other value fails configuration validation.
  - Characterize current backend security/source/process metadata separately so the new tests cannot be made green by deriving class from `local_only`, `discovered`, process sharing, credentials, or canonical tool capability.
  - Observable completion: new tests compile against the intended focused contracts and are RED until metadata/config production types are implemented.
  - _Requirements: 1.1, 1.2, 1.3, 1.5, 1.8, 2.1, 2.2, 7.4, 8.1, 10.1, 10.11_
  - _Design rules: D1, D2, D4_
  - _Boundary: pkg/lipsdk + internal/core/config tests_
  - _Depends: none_
  - _Validation: go test ./pkg/lipsdk/... ./internal/core/config/..._

- [x] 1.2 Define the RED pure execution-composition validator matrix
  - Add one named table covering direct inference/agent/unknown, inference-only weighted/parallel/failover/thinker, agent mixed and agent+agent composition, configured-unknown composition, unrestricted behavior, global parameters, and absent-backend preservation.
  - Include the existing thinker hybrid whose executor branch contains an embedded parallel group.
  - Add alias-expanded and model-only-default cases proving classification occurs on the compiled AST rather than raw selector text.
  - Add a fake resolver that distinguishes absent backend from configured backend with unknown class.
  - Observable completion: tests fail because the execution-composition validator/error does not yet exist; existing parser tests remain unchanged.
  - _Requirements: 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 2.10, 2.11, 3.1, 3.2, 5.1, 5.2, 5.3, 5.6, 5.7, 5.8, 7.1, 10.2, 10.3, 10.4_
  - _Design rules: D5, D6, D7_
  - _Boundary: internal/core/routing tests only_
  - _Depends: 1.1_
  - _Validation: go test ./internal/core/routing/..._

- [x] 1.3 Define RED no-side-effect runtime and route-override contracts
  - Add deterministic fake counters/barriers proving an unsafe request must fail before backend `Open`, upstream attempt creation, billing authorization, weighted-first consumption, affinity mutation, and interleaved planning-state access.
  - Characterize `internal/testkit.NewTestExecutor`/direct executor construction so a missing execution view cannot become a production fail-open default; define the test-only helper behavior that explicitly classifies ordinary fake backends as inference.
  - Add a route-override service/generation-validator test proving an unsafe PUT is rejected before `Replace`/store mutation.
  - Characterize current inference-only pre-output failover and no-failover-after-output behavior so the implementation cannot “solve” the feature by disabling ordinary failover globally.
  - Observable completion: unsafe-composition expectations are RED while existing inference routing behavior remains green.
  - _Requirements: 3.3, 3.4, 3.5, 4.9, 5.4, 5.5, 6.4, 6.5, 8.7, 10.8, 10.9, 10.12, 10.14_
  - _Design rules: D6, D8, D9_
  - _Boundary: internal/core/runtime + internal/core/routeoverride/runtimebundle tests_
  - _Depends: 1.2_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/routeoverride/... ./internal/infra/runtimebundle/..._

- [x] 1.4 Define RED metadata ownership and architecture regressions
  - Add manifest tests for omitted/explicit/invalid `execution_class`, including the Codex dual-export fixture with `openai-codex` and `openai-codex-app-server` assigned different classes.
  - Add factory-vs-instance tests requiring configured instance IDs to receive factory execution metadata, including two instances sharing one factory.
  - Add architecture tests preventing execution classification authority from being implemented as provider-name lists/imports in core and preventing the feature from entering canonical `pkg/lipapi`.
  - Add a local/discovered inference fixture that must remain composable to prevent future source/access/process heuristics.
  - Observable completion: the intended ownership contracts are RED before manifest/registry/generation production changes.
  - _Requirements: 1.4, 1.6, 1.7, 4.1, 4.2, 4.3, 4.4, 8.2, 8.3, 8.5, 8.6, 8.8, 10.5, 10.6, 10.7, 10.10_
  - _Design rules: D2, D3, D10, D13_
  - _Boundary: backendplugin manifest/pluginreg/runtimebundle/archtest tests_
  - _Depends: 1.1_
  - _Validation: go test ./pkg/lipsdk/backendplugin/manifest/... ./internal/infra/backendplugins/manifest/... ./internal/pluginreg/... ./internal/infra/runtimebundle/... ./internal/archtest/..._

## 2. Implement Execution Metadata at the Plugin Boundary

- [x] 2.1 Add the focused SDK execution profile and plugin-registry storage
  - Implement `BackendExecutionClass`/`BackendExecutionProfile` (or equivalent reviewed names) with explicit inference/agent-runtime values and effective unknown for omitted legacy metadata.
  - Extend plugin registration through the smallest source-compatible API shape; keep security profile and execution profile as separate concerns.
  - Preserve legacy registration helpers by defaulting their execution metadata to unknown while making new/project-owned registrations explicit.
  - Add defensive read/access helpers needed by generation assembly; do not expose a generic metadata bag or mutable registry to core.
  - Make the RED contracts from 1.1 green.
  - _Requirements: 1.1, 1.2, 1.3, 1.5, 1.8, 4.1, 8.1, 8.9_
  - _Design rules: D1, D2, D4_
  - _Boundary: pkg/lipsdk + internal/pluginreg_
  - _Depends: 1.1_
  - _Validation: go test ./pkg/lipsdk/... ./internal/pluginreg/..._

- [x] 2.2 Extend the closed executable manifest with per-export execution class
  - Add `execution_class` to the strict manifest wire/export types and validation without changing the manifest schema identifier or backend-plugin runtime protocol.
  - Preserve omitted legacy field as unknown; reject invalid non-empty classes.
  - Propagate class through discovered `ValidatedExport` and registration installation.
  - Prove strict unknown-field behavior, existing old-manifest parsing, and current digest/security/process validation remain unchanged.
  - Make the manifest portions of 1.4 green.
  - _Requirements: 1.4, 1.8, 8.2, 8.5, 8.7_
  - _Design rules: D2, D3, D4, D10_
  - _Boundary: pkg/lipsdk/backendplugin/manifest + internal/infra/backendplugins/manifest + runtimebundle discovery metadata_
  - _Depends: 2.1, 1.4_
  - _Validation: go test ./pkg/lipsdk/backendplugin/manifest/... ./internal/infra/backendplugins/manifest/... ./internal/infra/runtimebundle/..._

- [x] 2.3 Classify all project-owned backend factories/exports explicitly
  - Add focused execution metadata to essential/compatible backend contributions and every project-owned executable manifest export.
  - Mark whole-agent ACP/App-Server/Cursor-SDK style exports as agent runtime based on actual execution semantics.
  - Mark direct inference/compatible/local inference exports explicitly as inference; ensure `openai-codex` differs from `openai-codex-app-server`, and OpenCode inference exports are not classified from connector provenance.
  - Add a derived completeness/contract test requiring maintained official registrations to avoid effective unknown without creating a duplicate central production allowlist.
  - _Requirements: 1.6, 1.7, 8.3, 8.4, 8.5, 10.6, 10.7_
  - _Design rules: D2, D3, D13_
  - _Boundary: internal/standardplugins contribution metadata + project-owned connectors/* manifest templates/tests_
  - _Depends: 2.1, 2.2_
  - _Validation: go test ./internal/standardplugins/... ./internal/infra/backendplugins/... ./internal/archtest/... && go test ./connectors/... where the repository's connector test command supports it_

- [x] 2.4 Compile an immutable configured-instance execution view
  - During candidate generation assembly, resolve each enabled backend's factory ID and configured instance ID and project the factory execution profile to that instance ID.
  - Preserve a distinct configured-unknown state versus absent backend identity.
  - Freeze/defensively own the view for the generation; do not scan manifests or plugin registries per request.
  - Wire the narrow resolver/view into `RoutingRuntime` without exposing concrete plugin registry types to core routing.
  - Make internal/direct executor construction conservative when the resolver is omitted (configured backends effective unknown), and update test-only builders/options to mark ordinary fake inference backends explicitly rather than adding a production fail-open fallback.
  - Make the factory-vs-instance RED tests from 1.4 green.
  - _Requirements: 4.2, 4.3, 4.4, 4.5, 4.8, 4.9, 10.5, 10.14_
  - _Design rules: D3, D11, D14_
  - _Boundary: internal/infra/runtimebundle generation assembly + internal/core/runtime routing configuration_
  - _Depends: 2.1, 2.2_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/core/runtime/..._

## 3. Implement Core Safe-Composition Policy

- [x] 3.1 Implement typed `safe` / `unrestricted` routing configuration
  - Add the routing configuration field and effective-default helper; omitted means safe and invalid values fail validation.
  - Project the effective policy into each runtime generation/`RoutingRuntime`.
  - Keep the setting operator-owned; add tests proving no client selector/header/annotation path changes it.
  - Make config RED tests from 1.1 green.
  - _Requirements: 2.1, 2.2, 7.3, 7.4, 10.11_
  - _Design rules: D1, D5, D11_
  - _Boundary: internal/core/config + runtimebundle wiring_
  - _Depends: 1.1, 2.4_
  - _Validation: go test ./internal/core/config/... ./internal/infra/runtimebundle/..._

- [x] 3.2 Implement the pure recursive AST validator and typed routing error
  - Add the direct-primary predicate and one recursive primary walker covering failover, weighted, parallel, and thinker embedded-parallel shapes.
  - Under safe, return nil for direct primary; otherwise require every configured reachable class to equal explicit inference.
  - Preserve absent-backend handling for its existing authority; deny configured unknown/agent runtime.
  - Under unrestricted, bypass class rejection without changing any other selector validation.
  - Return a bounded typed `ErrUnsafeExecutionComposition` family error containing composition/backend/class/policy facts but not the raw selector.
  - Make 1.2 green without modifying parser grammar.
  - _Requirements: 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 2.10, 2.11, 3.1, 3.2, 5.2, 5.3, 5.6, 5.7, 5.8, 7.1, 9.1, 9.2, 9.3_
  - _Design rules: D5, D6, D7, D12_
  - _Boundary: internal/core/routing_
  - _Depends: 2.1, 3.1_
  - _Validation: go test ./internal/core/routing/..._

- [x] 3.3 Establish one shared generation semantic-preflight sequence
  - Compose existing `CompileSelector`, existing per-entry-point unknown-backend checks, and new execution-composition validation without turning `CompileSelector` into a stateful policy service.
  - Ensure aliases and model-only defaulting precede class validation.
  - Keep native-model binding, catalog/capability admission, health, affinity, and dynamic planner state outside this pure preflight.
  - Add equivalence tests so runtime/admin/default consumers cannot reorder the execution-composition check differently.
  - _Requirements: 5.1, 5.6, 6.1, 6.2, 6.3, 6.4, 6.9, 10.4_
  - _Design rules: D7, D8, D9_
  - _Boundary: internal/core/routing + thin runtimebundle preflight adapter_
  - _Depends: 3.2_
  - _Validation: go test ./internal/core/routing/... ./internal/infra/runtimebundle/..._

- [x] 3.4 Validate configured/default routes against candidate generation metadata
  - Run pure compile/known-backend/class validation for the effective configured/default selector using the candidate generation's aliases/default backend/class view/policy.
  - Fail candidate build/publication for an unsafe static default with no request-time backend work.
  - Characterize the earliest safe assembly point before connector activation; use it when compatible with current resource-ledger ownership, otherwise prove no unsafe generation publishes and no request-attributable backend work occurs.
  - Keep regexp alias safety as use-time validation rather than attempting impossible exhaustive alias expansion.
  - _Requirements: 6.2, 6.3, 4.6_
  - _Design rules: D8, D9, D11_
  - _Boundary: internal/infra/runtimebundle candidate generation validation_
  - _Depends: 2.4, 3.3_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/core/config/..._

## 4. Enforce the Policy at Runtime and Admin Boundaries

- [x] 4.1 Enforce safe composition immediately after request selector compilation
  - In `buildRoutePlan`, call the generation-bound execution validator immediately after compile and before native/dynamic route-plan work.
  - Prove rejection happens before weighted RNG/`[first]`, affinity, interleaved planning state, billing authorization, B-leg allocation, and backend open.
  - Preserve all existing inference-only planning behavior and current no-failover-after-output semantics.
  - Make the runtime portion of 1.3 green.
  - _Requirements: 3.3, 3.4, 3.5, 5.4, 5.5, 6.1, 10.8, 10.12_
  - _Design rules: D6, D8_
  - _Boundary: internal/core/runtime route-plan construction_
  - _Depends: 2.4, 3.2, 3.3_
  - _Validation: go test ./internal/core/runtime/... ./internal/core/routing/..._

- [x] 4.2 Enforce the same policy before A-leg route-override persistence
  - Extend `generationSelectorValidator` with the candidate generation's execution resolver and policy.
  - Preserve order `CompileSelector -> RejectUnknownBackends -> ValidateExecutionComposition`.
  - Prove an unsafe PUT performs no store mutation and leaves previous selector/revision intact.
  - Keep admin HTTP enablement independent from request-time validation of already persisted selectors.
  - _Requirements: 6.4, 6.5, 6.8, 6.9, 10.9_
  - _Design rules: D9, D11_
  - _Boundary: internal/infra/runtimebundle route-override generation validation + routeoverride tests_
  - _Depends: 3.3, 4.1_
  - _Validation: go test ./internal/core/routeoverride/... ./internal/infra/runtimebundle/... ./internal/stdhttp/admin/..._

- [x] 4.3 Prove reload and persisted-override isolation
  - Add deterministic generation tests for `unrestricted -> safe`, safe policy/class metadata changes, and old-turn/new-turn behavior.
  - Hold an in-flight turn on generation N, publish N+1, and prove N continues unchanged while a later N+1 turn uses the new class/policy.
  - Prove persisted raw override state is neither rewritten nor cleared on reload; if newly unsafe, the later turn fails semantic preflight.
  - Include a failed candidate-generation validation case proving the last-good generation remains active under existing reload guarantees.
  - _Requirements: 4.6, 4.7, 6.6, 6.7, 7.2, 10.9_
  - _Design rules: D9, D11_
  - _Boundary: runtimebundle generation/reload + core runtime integration tests_
  - _Depends: 3.4, 4.1, 4.2_
  - _Validation: go test ./internal/infra/runtimebundle/... ./internal/core/runtime/..._

- [x] 4.4 Map typed policy failures to bounded client/operator diagnostics
  - Add/extend standard frontend execution-error classification so unsafe routing composition maps to invalid-request/HTTP-400-family behavior, never retryable upstream/server failure.
  - Emit bounded route-policy diagnostic fields where current diagnostics support them; do not emit raw selectors as metric labels or mark the backend unhealthy.
  - Ensure error text says direct agent-runtime routing is supported and points to operator-owned unrestricted policy without naming provider brands as the policy basis.
  - Add leak tests for raw selector, prompt, workspace, MCP/tool details, credentials, and hidden agent IDs.
  - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7_
  - _Design rules: D12_
  - _Boundary: frontend execution-error mapping + core route diagnostics/metrics_
  - _Depends: 3.2, 4.1_
  - _Validation: go test ./internal/plugins/frontends/... ./internal/core/diag/... ./internal/core/runtime/... ./internal/infra/metrics/..._

## 5. Complete Compatibility and Semantic Regression Coverage

- [x] 5.1 Prove inference-only and unrestricted legacy compatibility
  - Run direct/weighted/parallel/failover/thinker/affinity/TTFT/`[first]` inference cases through the real planner path and compare with pre-feature behavior.
  - Under unrestricted, prove agent-runtime/unknown compositions reach the same existing planning/open behavior as before this feature, while post-output retry prohibition remains unchanged.
  - Verify global/leaf route parameters do not misclassify a direct primary as composition.
  - _Requirements: 2.9, 2.10, 3.4, 3.5, 7.1, 7.2, 7.7, 10.12_
  - _Design rules: D5, D6_
  - _Boundary: routing/runtime regression tests_
  - _Depends: 4.1_
  - _Validation: go test ./internal/core/routing/... ./internal/core/runtime/..._

- [x] 5.2 Prove official dual-mode and non-heuristic classifications end to end
  - Build generation fixtures from official metadata showing direct `openai-codex` participates in safe inference composition while `openai-codex-app-server` does not.
  - Cover Cursor SDK/ACP-style agent runtime metadata and at least one OpenCode/local/discovered inference backend.
  - Verify no production routing assertion depends on hard-coded kind names: the same fake factory with changed metadata must change policy outcome.
  - _Requirements: 1.5, 1.6, 1.7, 8.3, 8.4, 8.5, 10.6, 10.7, 10.10_
  - _Design rules: D2, D3, D13_
  - _Boundary: runtimebundle/pluginreg/connector metadata integration tests_
  - _Depends: 2.3, 2.4, 4.1_
  - _Validation: go test ./internal/pluginreg/... ./internal/standardplugins/... ./internal/infra/runtimebundle/... ./internal/archtest/..._

- [x] 5.3 Prove legacy third-party unknown-class migration behavior
  - Parse/register an old-style manifest/registration with no execution class and prove direct route remains valid under safe.
  - Prove the same backend is rejected from weighted/parallel/failover/thinker under safe, becomes composable after explicit inference metadata, and remains composable under operator unrestricted.
  - Ensure absent backend identity still produces existing unknown-backend behavior rather than configured-unknown-class error.
  - _Requirements: 1.2, 1.3, 2.11, 4.5, 7.5, 7.6, 8.2, 10.11_
  - _Design rules: D4, D5_
  - _Boundary: manifest/pluginreg/routing/runtime integration tests_
  - _Depends: 2.2, 3.2, 4.1_
  - _Validation: go test ./pkg/lipsdk/backendplugin/manifest/... ./internal/pluginreg/... ./internal/core/routing/... ./internal/core/runtime/..._

- [x] 5.4 Exercise nested selector and dynamic-state bypass resistance
  - Cover thinker+embedded-parallel nested agent leaves, sticky affinity pointing at an inference leaf while another agent leaf remains possible, unhealthy/excluded agent branches, and `[first]` preferences.
  - Prove safe validation checks the possible compiled execution graph before health/stickiness/dynamic branch selection and rejects even when the unsafe branch would not be selected on this request.
  - Prove no rejection consumes `[first]` or thinker-cycle state.
  - _Requirements: 2.7, 5.7, 5.8, 10.3, 10.8_
  - _Design rules: D5, D7, D8_
  - _Boundary: internal/core/routing + runtime dynamic-state tests_
  - _Depends: 4.1, 5.1_
  - _Validation: go test ./internal/core/routing/... ./internal/core/runtime/..._

## 6. Architecture Ratchets, Documentation, and Release-Grade Verification

- [x] 6.1 Add permanent architecture and metadata completeness ratchets
  - Gate project-owned production backends against missing execution metadata without using a second hand-maintained provider list.
  - Gate core routing/runtime against concrete connector/provider imports or provider-name class authority.
  - Gate canonical `pkg/lipapi` and backend-plugin runtime ABI from accidental execution-class leakage required only by this feature.
  - Extend change-surface/architecture evidence only where it detects the intended boundary, not to encode implementation filenames unnecessarily.
  - _Requirements: 8.3, 8.6, 8.7, 8.8, 10.10_
  - _Design rules: D10, D13_
  - _Boundary: internal/archtest + metadata contract tests_
  - _Depends: 2.3, 3.2, 5.2_
  - _Validation: go test ./internal/archtest/..._

- [x] 6.2 Run routing/plugin/reload concurrency and quality verification
  - Run focused routing, runtime, routeoverride, pluginreg, manifest, standardplugins, and runtimebundle tests.
  - Run race-enabled routing/runtime/reload tests where supported to prove immutable generation metadata has no mutable cross-generation state.
  - Run `make quality-checks`, `make test`, and routing/plugin parity or broader `make qa` as required by repository policy for this cross-cutting change.
  - Record any environment-gated connector/PostgreSQL/live tests as blocked/skipped rather than claiming they passed.
  - _Requirements: 4.6, 4.7, 10.13_
  - _Design rules: D11, D13_
  - _Boundary: verification only_
  - _Depends: 4.3, 5.1, 5.2, 5.3, 5.4, 6.1_
  - _Validation: make quality-checks && make test; make test-race where supported; make qa or documented project-equivalent release gate_

- [x] 6.3 Document operator and connector-author migration
  - Document `routing.execution_composition_policy`, safe default, unrestricted risk, direct-agent behavior, and why failover/parallel are restricted.
  - Update connector/extension authoring docs with `execution_class`, per-export classification guidance, examples, and the rule that local/process/tool capability is not a classifier.
  - Document legacy omitted metadata behavior and migration to explicit inference/agent-runtime.
  - Update relevant agent-runtime backend docs only to explain composition policy; do not duplicate a central class list in documentation that can drift from manifests.
  - _Requirements: 7.3, 7.5, 7.6, 8.2, 8.3, 9.5_
  - _Design rules: D2, D3, D4, D6_
  - _Boundary: docs/config examples/extension authoring_
  - _Depends: 2.3, 3.1, 4.4, 5.3_
  - _Validation: documentation links/examples + repository doc/quality checks_

- [x] 6.4 Perform final implementation review against the spec
  - Produce a requirement-to-test/change trace proving all ten requirement groups are satisfied.
  - Confirm no selector grammar change, no canonical `lipapi` execution-class field, no provider-name classification authority, and no backend-plugin runtime ABI feature were introduced.
  - Confirm the final diff stays within repository change-surface limits and separates any unrelated cleanup.
  - Re-run the highest-value zero-dispatch, Codex dual-export, unknown-class, alias, admin-override, and reload tests before declaring implementation complete.
  - _Requirements: 1.1-1.8, 2.1-2.11, 3.1-3.6, 4.1-4.8, 5.1-5.8, 6.1-6.9, 7.1-7.7, 8.1-8.9, 9.1-9.7, 10.1-10.13_
  - _Design rules: D1, D2, D3, D4, D5, D6, D7, D8, D9, D10, D11, D12, D13, D14_
  - _Boundary: final review/evidence only_
  - _Depends: 6.2, 6.3_
  - _Validation: go test focused suites + make quality-checks + make test + change-surface report_
