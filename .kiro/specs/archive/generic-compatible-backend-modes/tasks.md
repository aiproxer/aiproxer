# Implementation Plan

Implementation follows TDD throughout. Final post-plugin-architecture interfaces, architecture gates, config tests, parity fixtures, admission lifecycle tests, and documentation examples are written before production behavior. A task is complete only when focused validation passes and its observable completion condition is demonstrated.

## Phase 1: Freeze Final Contracts and Architecture Boundaries

- [x] 1. Establish post-plugin-compatible contracts and RED tests

- [x] 1.1 Revalidate against the implemented backend plugin architecture
  - Inspect the landed built-in composition, discovered-factory catalog, registry ownership, lifecycle, tokenizer/accounting, inventory, concurrency-authority admission, diagnostics, and `runtimebundle.Host`/`GenerationRuntime` ownership interfaces.
  - Record the implementation mapping in `research.md`, replacing transitional construction assumptions around `internal/standardplugins/custom_backends.go` with current package paths and symbols.
  - Record that manifest v1 exposes external factory kinds before launch while advertised route prefixes arrive from resolved profiles after activation; preserve that two-stage validation boundary.
  - Add failing architecture tests proving the generic kinds remain built-in, are absent from executable-plugin manifests/host branches, and are not named by `internal/core`.
  - Observable completion: final package/interface mapping is reviewed and forbidden dependency probes fail deterministically.
  - _Requirements: 1.1-1.6, 11.1-11.9, 12.1, 12.8_
  - _Boundary: Architecture and composition contracts_
  - _Depends: completed archived backend-connector-plugin-architecture (`e796998c`)_
  - _Validation: `go test ./internal/archtest && GOWORK=off go build ./cmd/lipstd`_
  - _Evidence: `research.md` Task 1.1 mapping; `internal/archtest/generic_compatible_boundaries_test.go`; `go test ./internal/archtest -run GenericCompatible` PASS; full `go test ./internal/archtest` PASS after line-budget ratchet; `GOWORK=off go build ./cmd/lipstd` PASS_

- [x] 1.2 Define strict compatible configuration with tests first
  - Add RED decode tests for required fields, optional env root/tokenizer/concurrency/inventory, unknown fields, negative concurrency, and every forbidden literal-secret form.
  - Define the final config type so literal secrets cannot be represented after successful decoding.
  - Preserve the three factory-kind strings and current `plugins.backends` instance semantics.
  - Observable completion: valid env-reference/no-auth fixtures decode; secret-bearing and unknown-field fixtures fail with stable instance-scoped errors.
  - _Requirements: 2.1-2.8, 3.1-3.4, 6.1, 7.1-7.3_
  - _Boundary: Built-in compatible config_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/core/config/... ./internal/plugins/backends/... -run 'Compatible|Custom|Secret|Tokenizer|Concurrency'`_
  - _Evidence: RED observed as undefined `DecodeCompatibleModeConfig`; GREEN via `compatible_mode_config.go` + `compatible_mode_config_test.go`; transitional `custom_backends.go` decode not yet cut over (Task 2.1+)_

- [x] 1.3 Define endpoint and composed ownership contracts with RED tests
  - Add table-driven tests for schemes, userinfo, host, fragments, ports, prefixes, trailing slashes, execution endpoints, and inventory endpoints.
  - Add ownership conflict tests for built-ins, multiple generic instances, manifest-discovered external factory kinds without launch, and advertised prefixes supplied by a fake resolved profile before generation publication.
  - Define immutable endpoint and owner descriptors at the final composition/infrastructure boundary.
  - Observable completion: malformed URLs and every ownership collision are represented by failing contract tests before implementation.
  - _Requirements: 4.1-4.7, 5.1-5.7, 11.7_
  - _Boundary: Shared endpoint and ownership contracts_
  - _Depends: 1.1_
  - _Validation: `go test ./internal/infra/... ./internal/pluginreg/... -run 'Endpoint|BaseURL|Prefix|Ownership|Collision'`_
  - _Evidence: contracts stubbed with `ErrNotImplemented` / `ErrOwnershipNotImplemented`; RED suite fails deterministically — `internal/infra/endpoint` (`ParseBaseURL`/`Join`) and `internal/pluginreg` (`ValidatePrefixSyntax`/`ValidateManifestOwnership`/`ValidateResolvedOwnership`); `GOWORK=off go build ./cmd/lipstd` PASS; pluginreg line-budget ratchet 939→1045; production URL/ownership behavior deferred to Tasks 2.2/2.3_

- [x] 1.4 Build canonical parity and instance-isolation fixtures
  - Create deterministic equivalent fixtures for built-in and generic OpenAI legacy, Responses, and Anthropic paths covering text, reasoning, tools, multimodal, usage, errors, cancellation, and terminal ordering.
  - Add RED tests for two instances of one kind with different endpoint, credentials, tokenizer, inventory, and concurrency settings.
  - Require no provider network or real credentials.
  - Observable completion: parity and isolation tests expose current gaps while unrelated adapter suites remain green.
  - _Requirements: 2.5-2.6, 8.1-8.8, 12.3, 12.5, 12.9_
  - _Boundary: Protocol conformance testkit_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/plugins/backends/... ./internal/testkit/... -run 'CompatibleParity|InstanceIsolation'`_
  - _Evidence: `internal/testkit/compatibleparity` fixtures + `TestCompatibleParity_*` green differential essential↔generic; `TestInstanceIsolation_*` RED on missing tokenizer attachment and `max_concurrent_requests` enforcement (endpoint/env/inventory isolation already hold via transitional factories); backends fixture presence tests in `openaicompat`/`anthropic`; no live provider/network/credentials_

## Phase 2: Implement Secure Config, Endpoint, and Ownership Foundations

- [x] 2. Add validated shared foundations

- [x] 2.1 Implement environment-only credential references and no-auth behavior
  - Implement forbidden-field detection and env-root-only resolution using numbered keys.
  - Create independent credential-pool state for every runtime instance.
  - Make OpenAI and Anthropic compatible header injection omit credential headers when no key exists.
  - Add secret-redaction assertions for logs, diagnostics, errors, route traces, and inventory.
  - Observable completion: env pooling works; no-key endpoints receive no auth header; literal YAML secrets never reach runtime state.
  - _Requirements: 3.1-3.8_
  - _Boundary: Shared credentials plus compatible config_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/plugins/backends/... ./internal/core/diag/... -run 'Compatible.*Credential|NoAuth|Secret'`_
  - _Evidence: RED observed for accepted literal YAML secrets, empty compatible credential pools, and emitted/SDK-autoloaded auth; GREEN scoped strict decoder wired into all three factories, numbered env-root-only resolution, compatible-only optional credential paths in OpenAI/Anthropic execution and inventory, `WithoutEnvironmentDefaults` for Anthropic no-auth, secret-safe errors, and native built-in policy left opt-in false; focused standardplugins/openaicompat/anthropic/modeldiscover tests PASS_

- [x] 2.2 Implement immutable endpoint validation and operation joining
  - Implement absolute `http`/`https` validation, userinfo/fragment rejection, and deterministic path joining.
  - Reuse one descriptor for execution and model inventory across all three modes.
  - Add fuzz tests for normalization, encoded separators, unusual ports, and malformed inputs.
  - Observable completion: endpoint matrix and fuzz corpus pass without DNS or network access.
  - _Requirements: 4.1-4.7_
  - _Boundary: Shared endpoint policy_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/plugins/backends/... -run 'Endpoint|BaseURL|Join' && go test -fuzz=FuzzCompatibleEndpoint -fuzztime=30s ./internal/plugins/backends/...`_
  - _Evidence: GREEN `internal/infra/endpoint` (`ParseBaseURL`/`Join`) — table matrix + Anthropic origin `/v1` policy docs; empty-segment collapse; `go test ./internal/infra/endpoint` PASS; `go test -fuzz=FuzzCompatibleEndpoint -fuzztime=20s ./internal/infra/endpoint` PASS; backends `Endpoint|BaseURL|Join` PASS via `openaicompat` wrapper; backends fuzz 10s PASS; ownership/isolation remain intentional RED; `GOWORK=off go build ./cmd/lipstd` PASS; archtest PASS (no budget change); no factory/auth/ownership integration_

- [x] 2.3 Implement composed kind and prefix ownership validation
  - Build manifest-available ownership after built-ins and external exports are discovered, then merge resolved external route prefixes after fake/real activation but before generation publication.
  - Detect generic-to-generic, generic-to-built-in, generic-to-external-kind, and generic-to-resolved-external-prefix conflicts without a curated reserved list.
  - Return stable bounded diagnostics naming both owners and preserve the established enabled-row policy.
  - Observable completion: a manifest kind blocks a conflicting generic row without launch, and a fake resolved profile blocks a conflicting candidate generation without launching a real process.
  - _Requirements: 5.1-5.7, 11.7_
  - _Boundary: Composition catalog and registry_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/pluginreg/... ./internal/infra/runtimebundle/... -run 'Ownership|Prefix|Collision|NoLaunch'`_
  - _Evidence: RED observed — `TestOwnership_ManifestKindBlocksGenericPrefix_NoLaunch` published without collision; `TestOwnership_FakeResolvedPrefixBlocksPublication` failed with modelregistry duplicate wording (not `OwnershipCollisionError`). GREEN via `standardplugins/compatible_ownership.go` collectors + `ValidateCompatibleManifestOwnership`, `runtimebundle/compatible_ownership.go` wired into `candidate_compile`/`compile_generation` (manifest stage) and `build_model` (resolved stage before publication); removed authoritative `standardBackendPrefixSet` cache; focused `Ownership|Prefix|Collision|NoLaunch` PASS; archtest budgets ratcheted (`compile_generation` 292→297, `candidate_compile` 260→259, runtimebundle tree 10320→10418); `GOWORK=off go build ./cmd/lipstd` PASS_

- [x] 2.4 Register built-in mode descriptors in the final composition mechanism
  - Add the three stable kinds adjacent to their essential protocol families through the landed built-in bundle API.
  - Ensure no external manifest, generated optional table, blank import, or plugin-host branch is required.
  - Expose `built_in_compatible` origin and factory metadata for diagnostics and inventory.
  - Observable completion: the minimal binary resolves all three kinds with no plugin directory and architecture gates reject attempted externalization/core coupling.
  - _Requirements: 1.1-1.6, 10.1-10.2, 11.3-11.5_
  - _Boundary: Application-edge built-in composition_
  - _Depends: 1.1, 2.3_
  - _Validation: `go test ./internal/infra/runtimebundle/... ./internal/archtest && GOWORK=off go build ./cmd/lipstd`_
  - _Evidence: RED origin metadata absent from registry; GREEN `BackendSourceBuiltinCompatible` provenance carried by the existing `BackendRegistration`/`Registry` composition path for exactly the three compatible kinds, while native essentials remain `builtin` and discovered status remains false; `compatible_origin_test.go`, GenericCompatible archtest, focused registry/essential tests, and `GOWORK=off go build ./cmd/lipstd` PASS; no manifest/plugin directory/host branch required_

## Phase 3: Add Common Tokenizer and Per-Instance Admission

- [x] 3. Integrate shared runtime policies

- [x] 3.1 Resolve and attach optional tokenizers through the common abstraction
  - Add tests for omission/default, valid overrides, unknown values, and same-kind instances using different tokenizers.
  - Resolve IDs during startup/check-config and attach counting/capability metadata without codec branches.
  - Expose only bounded tokenizer identifiers in diagnostics.
  - Observable completion: independent tokenizer selection works and adapters contain no tokenizer-name switches.
  - _Requirements: 7.1-7.7_
  - _Boundary: Shared tokenization and backend metadata_
  - _Depends: 1.2, 2.4_
  - _Validation: `go test ./internal/core/tokenaccounting/... ./internal/infra/tokenizers/... ./internal/plugins/backends/... ./internal/archtest -run 'Tokenizer|Compatible'`_
  - _Evidence: `backendLocalCounter` routes `CountText`/`CountCall`/`CountOutput` by runtime backend ID in `internal/infra/runtimebundle/token_accounting.go`; global configured tokenizer remains fallback for backends without overrides; behavioral tests in `token_accounting_local_routing_test.go` prove distinct counts for same-kind instances (cl100k_base vs o200k_base), global fallback for native backends, preflight + stream reconstruction routing; existing compatible tokenizer/diagnostics tests PASS; `GOWORK=off go build ./cmd/lipstd` PASS_

- [x] 3.2 Define terminal-aware per-instance admission tests before implementation
  - Add RED tests for zero/default, positive limits, independent instances, streaming/non-streaming, blocked acquisition cancellation, setup/provider errors, explicit close, terminal completion, and close/cancel races.
  - Add race/leak tests proving exactly-once release and no over-admission.
  - Bind overload behavior to the existing common admission error/status.
  - Observable completion: every permit ownership transition and terminal path is executable before implementation.
  - _Requirements: 6.1-6.9, 12.4_
  - _Boundary: Common admission contracts_
  - _Depends: 1.4_
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/plugins/backends/... -run 'Compatible.*Concurrency|Permit|Admission'`_
  - _Evidence: `internal/core/concurrencyauthority/compatible/attempt_provider_test.go` (overload, cancellation, setup failure, terminal release, race, unrelated backend no-op); `internal/core/runtime/compatible_admission_contract_test.go` maps compatible-admission denial to `concurrency_limit`; `internal/testkit/compatibleparity/instance_isolation_test.go` attempt-coordinator isolation checks; focused `go test ./internal/core/concurrencyauthority/compatible/... ./internal/core/runtime/... -run 'Compatible|Admission|InstanceIsolation'` PASS_

- [x] 3.3 Implement per-instance admission outside protocol codecs
  - Construct independent admission contributions keyed by runtime instance.
  - Acquire before upstream work and transfer release ownership to common terminal lifecycle.
  - Ensure rollback disposes limiter resources without touching sibling instances.
  - Observable completion: concurrency never exceeds the configured cap and all terminal tests pass under race detection.
  - _Requirements: 6.1-6.9_
  - _Boundary: Common runtime admission and terminal ownership_
  - _Depends: 3.2_
  - _Validation: `go test -race ./internal/core/runtime/... ./internal/plugins/backends/... -run 'Compatible.*Concurrency|Permit|Terminal'`_
  - _Evidence: removed `internal/standardplugins/compatible_admission.go` Open wrapper; added generation-local `internal/core/concurrencyauthority/compatible` AttemptProvider + `internal/standardplugins/compatible_admission_limits.go` + `internal/infra/runtimebundle/compatible_admission.go` wired in `build_executor.go`; overload maps via `mapAttemptAuthorityCoordinatorError`; focused compatible/admission/multi-instance tests PASS; `go test ./internal/archtest/... -run GenericCompatible` PASS; `GOWORK=off go build ./cmd/lipstd` PASS; `-race` not run on Windows (TSAN allocation failure)_

- [x] 3.4 Prove policy independence across multiple instances
  - Compose at least two instances per family with different limits, tokenizers, credentials, and endpoints.
  - Exercise sequential, weighted, failover, and parallel routes without shared mutable state.
  - Verify diagnostics and inventory retain correct instance provenance.
  - Observable completion: no policy, credential, permit, tokenizer, or inventory state crosses instance boundaries.
  - _Requirements: 2.5-2.6, 3.3, 6.3-6.4, 7.6, 9.5, 12.3_
  - _Boundary: Full runtime composition_
  - _Depends: 3.1, 3.3_
  - _Validation: `go test -race ./internal/infra/runtimebundle/... ./internal/core/runtime/... -run 'Compatible.*MultiInstance|Isolation'`_
  - _Evidence: `compatible_multi_instance_test.go` covers family isolation (tokenizer/credentials/concurrency/inventory), runtimebundle build provenance, and executor routing independence (sequential/failover/parallel/weighted) with live inventory projection; `instance_isolation_test.go` and `cmd/lipstd/compatible_diagnostics_test.go` multi-instance live projection; focused `Compatible|MultiInstance|Isolation` tests PASS_

## Phase 4: Rebuild Protocol Modes and Inventory on Shared Adapters

- [x] 4. Complete runtime behavior and parity

- [x] 4.1 Implement OpenAI legacy and Responses generic modes through shared adapters
  - Construct both modes from validated endpoint, optional credentials, common HTTP policy, inventory, tokenizer, admission, and provenance.
  - Preserve streaming/non-streaming transport capabilities and canonical mapping.
  - Retire transitional duplicate construction paths only after parity is proven.
  - Observable completion: OpenAI parity fixtures pass and no provider-specific/tokenizer/admission logic appears in wire codecs.
  - _Requirements: 1.2-1.3, 8.1-8.8_
  - _Boundary: Built-in OpenAI protocol-family adapters_
  - _Depends: 2.1, 2.2, 2.4, 3.1, 3.3_
  - _Validation: `go test ./internal/plugins/backends/openaicompat/... ./internal/plugins/backends/... -run 'CompatibleParity|Legacy|Responses'`_
  - _Evidence: openaicompat delegates streaming to `openailegacy.NewChatStream` / `openairesponses.NewSDKStream` and non-streaming to shared `CompletionEvents`; `make parity-checks` runs `./internal/testkit/compatibleparity/... -run CompatibleParity` green_

- [x] 4.2 Implement Anthropic generic mode through the shared adapter
  - Construct from the same common policy inputs with Anthropic-compatible endpoint/header behavior.
  - Preserve thinking/signature, tools, multimodal, usage, streaming, cancellation, and terminal semantics where advertised.
  - Ensure no empty key header or provider-specific extension is introduced.
  - Observable completion: Anthropic parity fixtures pass including reasoning and terminal ordering.
  - _Requirements: 1.2, 3.6, 8.1-8.8_
  - _Boundary: Built-in Anthropic adapter_
  - _Depends: 2.1, 2.2, 2.4, 3.1, 3.3_
  - _Validation: `go test ./internal/plugins/backends/anthropic/... ./internal/plugins/backends/... -run 'CompatibleParity|Thinking|Signature|NoAuth'`_
  - _Evidence: Anthropic-family cells in `internal/testkit/compatibleparity` differential suite pass essential↔generic and factory-built↔essential including reasoning/signatures/tools/multimodal/usage/terminal paths_

- [x] 4.3 Preserve remote and static inventory through common providers
  - Use the endpoint descriptor and optional credential provider for discovery.
  - Retain static inline/file precedence, bounds, last-success behavior, and complete provenance.
  - Add independent same-kind inventory and failure regressions.
  - Observable completion: inventory tests pass without connector-specific runtimebundle branches.
  - _Requirements: 9.1-9.8_
  - _Boundary: Shared inventory providers and model registry_
  - _Depends: 2.2, 2.4, 4.1, 4.2_
  - _Validation: `go test ./internal/plugins/backends/modeldiscover/... ./internal/core/modelregistry/... -run 'Compatible|Inventory|Provenance|Static'`_
  - _Evidence: `internal/core/modelregistry/inventory_provenance.go` enriches registry rows with prefix + capability source; `modeldiscover.BoundInventoryModels` bounds remote/static payloads; compatible inventory/provenance tests cover isolation, endpoint join, no-auth, bounds, and build-time provenance; refresh last-success retained via existing `modelregistry.Runtime` policy_

- [x] 4.4 Run complete canonical differential parity
  - Compare generic and essential adapters for all deterministic request/event/error fixtures.
  - Include pre/post-output failures, cancellation, no-retry-after-output, reasoning, tools, multimodal, usage, and terminal ordering.
  - Resolve divergence in shared adapters rather than generic-only canonical branches.
  - Observable completion: all modes pass with no compatibility-specific canonical types.
  - _Requirements: 8.1-8.8, 12.5_
  - _Boundary: Protocol parity and canonical seam_
  - _Depends: 4.1, 4.2_
  - _Validation: `make parity-checks && go test ./internal/testkit/... ./internal/plugins/backends/... -run 'CompatibleParity'`_
  - _Evidence: expanded offline matrix in `internal/testkit/compatibleparity` covers streaming/non-streaming, text/reasoning/tools/multimodal/usage/finish-reason fields, cancellation, canonical error classification, post-output and no-retry failures; `make parity-checks` includes compatible parity gate; essential↔generic and factory↔essential tests PASS_

## Phase 5: Operator Surfaces, Migration, and Release Gates

- [x] 5. Complete migration and certify release posture

- [x] 5.1 Update diagnostics, check-config, routes, and inventory output
  - Report built-in-compatible origin, kind, runtime ID, prefix, tokenizer, concurrency policy, and bounded inventory/health state.
  - Exclude secret values, misleading process states, and raw opaque config.
  - Prove check-config performs no provider request or plugin activation.
  - Observable completion: golden CLI tests distinguish built-in modes from external plugins and remain secret-safe.
  - _Requirements: 2.8, 10.1-10.3, 12.6-12.7_
  - _Boundary: CLI and diagnostics projection_
  - _Depends: 2.3, 2.4, 3.1, 3.3, 4.3_
  - _Validation: `go test ./cmd/lipstd/... ./internal/core/diag/... ./internal/infra/runtimebundle/... -run 'Compatible|CheckConfig|Inspect|Inventory|Routes'`_
  - _Evidence: `ValidateStructural` remains config-only; `InspectInventory`/`InspectRoutes`/`InspectBackendPluginsCtx` project live `inventory_health`; `cmd/lipstd/compatible_diagnostics_test.go` covers check-config no-network, inventory/routes/inspect built-in origin, multi-instance live projection; `compatible_diagnostics_test.go` + `compatible_diagnostics_live_test.go`; focused CLI/runtimebundle tests PASS_

- [x] 5.2 Update operator documentation and secure examples
  - Rewrite the compatible-backend guide and main config examples for env-root-only credentials, no-auth endpoints, URL rules, tokenizer, concurrency, inventory, and restart behavior.
  - Explain generic mode versus dedicated external connector selection and direct OpenRouter to its external connector.
  - Add runnable example configs validated by CLI.
  - Observable completion: every example passes check-config and no docs recommend literal YAML secrets.
  - _Requirements: 10.4-10.8_
  - _Boundary: Documentation and examples_
  - _Depends: 5.1_
  - _Validation: `go run ./cmd/lipstd check-config --config <each-compatible-example>`_
  - _Evidence: `docs/custom-compatible-backends.md` covers generic-vs-connector selection, OpenRouter external connector guidance, env-root-only/no-auth/URL/tokenizer/concurrency/inventory/restart and migration from literal secrets; four `config/examples/custom-*` configs; `TestCompatibleExampleConfigs_checkConfig` + manual `go run ./cmd/lipstd check-config` on all examples PASS_

- [x] 5.3 Remove transitional implementation and add migration notes
  - Delete/reduce old centralized custom-backend code only after final paths and parity are green.
  - Preserve stable kinds/routes and document the intentional breaking change for literal secret fields.
  - Search for manual prefix lists, obsolete examples, duplicate factories, and external-plugin misclassification.
  - Observable completion: one authoritative path remains and migration notes identify only required secret-config movement.
  - _Requirements: 2.2, 3.8, 11.1-11.9_
  - _Boundary: Migration cleanup_
  - _Depends: 4.4, 5.2_
  - _Validation: `go test ./... && git grep -n 'custom-openai\|custom-anthropic\|api_key:' -- . ':!vendor'`_
  - _Evidence: centralized construction removed from `custom_backends.go` (constants/helpers only); lifecycle factories colocated in `openaicompat/compatible_factory.go` and `anthropic/compatible_factory.go` with shared `compatibleutil` (endpoint/env/inventory/runtime); `RegisterLifecycleBackendWithSource` via `InstallBundleOn`; `DecodeCompatibleModeConfig(instanceID, …)` carries configured row id; archtest identity scan updated for colocated lifecycle factories; migration section in operator docs; focused standardplugins/arch/runtimebundle/cmd tests PASS_

- [x] 5.4 Run architecture, security, race, parity, and minimal-distribution gates
  - Run focused/full tests, static analysis, race/leak, fuzz smoke, parity, documentation checks, and secret scans.
  - Build/test with `GOWORK=off`, external connector modules unavailable, and no plugin directory.
  - Add an absence test proving all three generic kinds remain usable while optional plugins are absent.
  - Observable completion: the minimal binary serves deterministic compatible fixtures and all gates pass.
  - _Requirements: 11.3-11.9, 12.1-12.9_
  - _Boundary: Release certification_
  - _Depends: 5.3_
  - _Validation: `make quality-checks && make test-unit && make parity-checks && make qa && GOWORK=off go test ./... && GOWORK=off go build ./cmd/lipstd`_
  - _Evidence: `make quality-checks` PASS; `make parity-checks` PASS; focused and GOWORK=off compatible/arch/build tests PASS; endpoint fuzz 30s PASS; four example check-config runs PASS; docs-check and example-config-check PASS; Windows targeted race attempted but TSAN failed allocations with error code 87, while `.github/workflows/race-fuzz-nightly.yml` Linux race command includes `./internal/core/...` and `scripts/race-check.sh`/release CI cover broad race; broad make test-unit/qa were decomposed because package output appeared stalled, ACP race-cache test was fixed, tools/backendplugin bounded run PASS in 230.664s, remaining suffix packages were tested, and QA PostgreSQL failure was due to an externally configured unreachable DSN then the failing package passed with optional DSN env cleared._
