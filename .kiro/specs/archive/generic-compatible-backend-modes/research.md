# Current-State Review, Requirements Gap Analysis, Architecture Research, and Design Validation

Generated: 2026-07-19T16:20:00+02:00
Task 1.1 revalidation evidence updated: 2026-07-28

## Status

- Repository: `matdev83/go-llm-interactive-proxy` (worktree branch `feat/generic-backends`)
- Feature: `generic-compatible-backend-modes`
- Source feature request: issue `#187`
- Workflow completed: initialization, requirements generation, mandatory brownfield gap analysis, requirements remediation, design generation, design validation, design correction, task generation, Task 1.1 brownfield revalidation against landed plugin architecture
- Change scope: active specification plus Task 1.1/1.2 implementation
- Implementation readiness: requirements, design, and tasks approved; `ready_for_implementation: true`

## Task 1.1 Landed Interface / Package Mapping (2026-07-28)

Dependency: archived `.kiro/specs/archive/backend-connector-plugin-architecture/` (landed; do not treat transitional paths as final).

| Concern | Landed package / symbol | Notes for this spec |
|---|---|---|
| Built-in essential + compatible kinds | `internal/standardplugins.EssentialBackendKinds`, `EssentialBackendBundle`, `IsEssentialBackendKind` | Three kinds already registered: `custom-openai-legacy-compatible`, `custom-openai-responses-compatible`, `custom-anthropic-compatible` |
| Transitional construction | `internal/standardplugins/custom_backends.go` (`customCompatibleBackendYAML`, factories) | Still accepts literal `api_key` / `api_keys` / `credentials`; replaced by strict `config.CompatibleModeConfig` decode (Task 1.2+) then composition cutover |
| Registry / factory ABI | `internal/pluginreg.Registry`, `BackendFactoryDeps` (= `GenericBackendFactoryDeps`) | Generic host deps only (`Identity`); no provider-specific fields |
| Executable plugin ABI | `pkg/lipsdk/backendplugin`, `pkg/lipsdk/backendplugin/manifest` (schema `golip.backendplugin.manifest/v1`) | Manifest exports factory kinds before launch; route prefixes arrive from resolved profiles after activation |
| Discovery (no launch) | `internal/infra/backendplugins/discovery.Discover` | Manifest-available catalog for `check-config` / first-stage ownership |
| Host ownership | `internal/infra/runtimebundle.Host` | Owns reload coordinator, manager, process services, active generation publication |
| Immutable generation | `internal/infra/runtimebundle.GenerationRuntime` / `GenerationBundle` | Published request plane; no mutable config retained on the generation |
| Credentials (shared) | `internal/plugins/backends/credpool`, numbered env keys via `collectNumberedEnvKeys` | Compatible modes: env-root only after Task 2.1; no-auth is valid |
| Inventory | `internal/plugins/backends/modeldiscover`, `pkg/lipsdk/modelinventory` | Reuse OpenAI-compatible / static providers; provenance stays instance-scoped |
| Tokenizer / accounting | `internal/infra/tokenizers`, `internal/core/tokenaccounting` | Spec adds composition-edge resolver; **no** `internal/core/tokenization` package |
| Concurrency / admission | `internal/core/config` accounting concurrency rules + runtime terminal ownership | Per-instance `max_concurrent_requests` maps to concurrency-authority dimensions; not codec semaphore / reload `attemptGate` |
| Diagnostics / CLI | `cmd/lipstd`, `internal/core/diag`, runtimebundle inspect/check-config paths | Must distinguish `built_in_compatible` origin later |
| Architecture gates | `internal/archtest` (`TestEssentialBackendBundle_ExactAllowlist`, `TestCoreExcludesBackendPluginHostAndWire`, Phase 7/8 connector gates) plus Task 1.1 probes in `generic_compatible_boundaries_test.go` | Built-in membership, no core kind naming, no host/manifest externalization |

### Two-stage external ownership (ABI preserved)

1. **Manifest-available (check-config / pre-activation):** built-in kinds+prefixes, enabled generic prefixes, discovered external **factory kinds** from validated manifests — no process launch.
2. **Post-activation (startup/reload):** merge advertised **route prefixes** from resolved external profiles; reject collisions before publishing the candidate `GenerationRuntime` through `Host`.

### Boundary evidence recorded for Task 1.1

- Existing: `TestEssentialBackendBundle_ExactAllowlist` locks essential membership including the three generic kinds.
- Existing: Phase 8 optional-connector gates reject essential kinds in connector manifests.
- Added: `TestGenericCompatible_remainBuiltIn`, `TestGenericCompatible_absentFromConnectorManifestsAndHostPackages`, `TestGenericCompatible_notNamedByInternalCore` in `internal/archtest/generic_compatible_boundaries_test.go`.
- Validation: `go test ./internal/archtest -run GenericCompatible` and full `go test ./internal/archtest` plus `GOWORK=off go build ./cmd/lipstd`.

## Reviewed Steering, Rules, Templates, and Patterns

The workflow and artifact shape follow repository conventions observed in completed specifications, especially `backend-connector-plugin-architecture`:

- `AGENTS.md` and `.kiro/AGENTS.md`
- `.kiro/steering/{product,structure,tech,api-standards,routing-and-orchestration,testing}.md`
- `.kiro/rules/{ears-format,gap-analysis,design-principles,design-review}.md`
- `.kiro/settings/templates/specs/{init.json,requirements.md,design.md,tasks.md}`
- completed `.kiro/specs/archive/backend-connector-plugin-architecture/*`
- active and archived connector specifications
- architecture, adapter-boundary, plugin-system, and operator documentation

The final artifact set matches the established pattern: `spec.json`, `requirements.md`, `research.md`, `design.md`, and `tasks.md`.

## Executive Assessment

The desired operator capability already exists in partial form. Three compatible factory kinds support multiple instances, base URLs, route prefixes, environment-variable-root credentials, model discovery/static inventory, and duplicate/reserved-prefix validation.

Material gaps remain:

1. Literal `api_key`, `api_keys`, and inline credential secrets are accepted from YAML.
2. Requested tokenizer and per-instance concurrency settings are absent.
3. Reserved-prefix validation derives from the current standard bundle and cannot see the final discovered external-plugin catalog.
4. Implementation is tied to transitional `internal/standardplugins/custom_backends.go`.
5. Documentation advertises literal YAML credentials.
6. The modes need explicit classification after connector externalization: built-in dependency-free protocol aliases, not executable plugins.

The selected direction is completion and migration, not a new connector subsystem:

- retain the existing factory kinds and configuration rows;
- place their factories in the final built-in protocol-family composition layer;
- reuse shared protocol adapters and common runtime services;
- reject literal configuration secrets and permit no-auth endpoints;
- add common tokenizer and per-instance admission integration;
- validate prefixes against immutable composed ownership state;
- preserve canonical parity and inventory provenance;
- explicitly exclude OpenRouter and provider-specific behavior.

## Reviewed Current Assets

### Current generic implementation

- `internal/standardplugins/custom_backends.go`
- generic registrations in `internal/standardplugins/standard_table.go`
- `internal/plugins/backends/openaicompat`
- Anthropic backend construction
- `docs/custom-compatible-backends.md`
- `config/config.yaml`

### Shared seams

- `config.PluginConfig` and strict YAML decoding
- `execbackend.Backend`
- `pluginreg.Registry` and the future built-in composition mechanism
- model inventory and registry
- tokenization/accounting abstractions
- runtime admission/concurrency mechanisms
- managed streams and terminal ownership
- diagnostics, routes, check-config, and inventory commands

### Controlling adjacent architecture

`backend-connector-plugin-architecture` establishes that:

- OpenAI Responses, OpenAI legacy, Anthropic, Gemini, and Bedrock remain essential built-ins;
- dependency-free generic OpenAI/Anthropic-compatible aliases may remain built-in;
- OpenRouter and other provider-specific optional connectors are external;
- core remains independent from concrete connectors and plugin-host mechanics;
- composition combines built-in factories with discovered external factories;
- external plugin presence is not required for the minimal root build.

## Existing Strengths to Preserve

1. Stable generic factory-kind strings already represent the intended API families.
2. `plugins.backends` supports multiple instances through `id` plus `kind`.
3. Opaque config preserves connector-private schema.
4. Shared OpenAI-compatible mapping avoids duplicate wire implementations.
5. Compatible model discovery and backend-qualified inventory already exist.
6. Prefix validation already covers empty values, `/`, `:`, duplicates, and known standard prefixes.
7. Numbered environment keys already support credential pools.
8. Routing, failover, commitment, and streaming semantics remain outside connector configuration.
9. Architecture, race, parity, and leak tests are established practices.

## Mandatory Brownfield Requirements Gap Analysis

| ID | Severity | Finding | Classification | Required disposition |
|---|---:|---|---|---|
| G-01 | P0 | Literal `api_key`, `api_keys`, and inline credential secrets are accepted. | Security conflict | Reject literal secret-bearing fields; allow env-root references only. |
| G-02 | P0 | No-key behavior and empty auth-header behavior are not a firm contract. | Behavior gap | Make no-key valid and omit credential headers entirely. |
| G-03 | P0 | Implementation is tied to a transitional centralized file. | Architecture risk | Target the final built-in protocol-family composition boundary. |
| G-04 | P0 | Prefix reservation cannot see future external-plugin exports. | Correctness gap | Validate against immutable fully composed ownership. |
| G-05 | P0 | Dynamic instances may be misclassified as executable plugins. | Classification risk | Define them as built-in protocol aliases. |
| G-06 | P0 | OpenRouter appears transport-compatible but is provider-specific. | Scope ambiguity | Explicitly exclude it and use its external connector. |
| G-07 | P1 | `max_concurrent_requests` is absent. | Missing capability | Integrate through common per-instance admission. |
| G-08 | P1 | `tokenizer` override is absent. | Missing capability | Resolve through shared tokenizer/capability/accounting. |
| G-09 | P1 | Base URL/path joining is not a stable contract. | Partial | Require absolute http(s), no userinfo, and deterministic joins. |
| G-10 | P1 | Documentation recommends literal YAML secrets. | Security debt | Replace with env-root-only and no-auth examples. |
| G-11 | P1 | Collision errors do not cover composed built-in/external ownership. | Operability gap | Return deterministic two-owner diagnostics. |
| G-12 | P1 | Permit release across stream terminal paths is unspecified. | Lifecycle gap | Use common terminal-aware admission and test all paths. |
| G-13 | P1 | Tokenizer selection could leak into protocol branching. | Boundary risk | Keep tokenizer resolution outside wire adapters. |
| G-14 | P1 | Canonical parity is assumed rather than executable. | Test gap | Add differential parity fixtures for all three families. |
| G-15 | P1 | Inventory provenance may regress during composition refactor. | Migration risk | Preserve instance/kind/prefix/source/freshness via common contracts. |
| G-16 | P1 | Check-config network-free behavior is implicit. | Operability gap | Require structural validation only. |
| G-17 | P1 | Minimal distribution availability is not proven. | Release gap | Add no-plugin and `GOWORK=off` gates. |
| G-18 | P2 | Disabled-row collision semantics are implicit. | Ambiguity | Follow and document established enabled-instance policy. |
| G-19 | P1 | Diagnostics may confuse built-in modes with plugin processes. | UX gap | Introduce explicit built-in-compatible origin. |
| G-20 | P1 | Narrow secret-policy change could leak to other connector classes. | Regression risk | Scope rejection only to these three kinds. |

## Requirements Remediation Performed

The requirements were corrected after gap analysis to:

- make the post-plugin architecture a normative dependency;
- classify all three modes as built-in aliases of essential protocol families;
- prohibit the external plugin ABI for these modes;
- preserve kinds, IDs, routes, and opaque configuration;
- reject literal YAML secrets while retaining numbered env-key pooling;
- define no-credential operation and empty-header prohibition;
- require deterministic URL validation and joining;
- validate prefixes against composed built-in and discovered ownership;
- add common per-instance concurrency and tokenizer requirements;
- require canonical parity and explicit capability honesty;
- preserve inventory provenance through common providers;
- distinguish built-in compatible modes in diagnostics;
- add `GOWORK=off`, no-plugin, architecture, race, leak, and parity gates;
- explicitly exclude OpenRouter and provider-specific extensions.

## Architecture Options Considered

### A. Patch the current centralized custom factory

Small immediate diff, but it optimizes for a transitional composition structure scheduled for removal. Rejected as final architecture; retained only as characterization input.

### B. External executable plugin per configured provider

Misuses the plugin architecture and imposes artifact/process complexity where no provider-specific code exists. Rejected.

### C. One generic external protocol plugin exporting all three kinds

Still externalizes essential dependency-free codecs and weakens the minimal distribution. Rejected.

### D. Built-in protocol-family modes composed at the application edge

Selected. Each kind is a thin validated configuration/factory mode over its essential adapter. Common infrastructure supplies credentials, endpoint policy, inventory, tokenizer, admission, ownership validation, and diagnostics. Core remains unaware.

## Selected Direction

1. Preserve the three existing factory kinds.
2. Register them through the final built-in composition mechanism.
3. Treat each configured row as an independent runtime backend instance.
4. Decode strict config incapable of carrying literal secrets.
5. Resolve optional numbered credentials through a shared reference abstraction.
6. Use one validated endpoint descriptor for execution and inventory.
7. Validate prefixes after built-in and external descriptors are composed but before serving.
8. Attach tokenizer through shared capability/accounting seams.
9. Attach per-instance concurrency through common terminal-aware admission.
10. Reuse canonical adapters without provider-specific branches.
11. Publish inventory/diagnostics with complete provenance.
12. Keep check-config structural and non-networking.
13. Prove minimal root operation with no external plugins and `GOWORK=off`.

## Design Validation

### Validation method

The generated design was checked against all requirements, repository design rules, the adjacent plugin architecture, current configuration/adapter/inventory/runtime seams, and security/cancellation/race/leak/migration failure modes.

### Initial findings and corrections

| ID | Severity | Finding | Correction applied |
|---|---:|---|---|
| DV-01 | P0 | Prefix validation occurred before external manifests were composed. | Moved validation after composed descriptor assembly and before activation. |
| DV-02 | P0 | Credential abstraction could still carry literal decoded values. | Made successful config env-reference-only with explicit forbidden-field detection. |
| DV-03 | P1 | A wrapper-local semaphore duplicated admission and risked leaks. | Replaced with common terminal-aware per-instance admission. |
| DV-04 | P1 | Tokenizer was modeled as adapter config. | Moved resolution to shared tokenizer/capability/accounting composition. |
| DV-05 | P1 | Inventory URL joins could diverge from execution joins. | Added one immutable endpoint descriptor for all operations. |
| DV-06 | P1 | Diagnostics could classify modes as external plugins. | Added `built_in_compatible` origin and prohibited process states. |
| DV-07 | P1 | Static inventory precedence and no-network check-config were weak. | Bound them to shared inventory policy and structural-only validation. |
| DV-08 | P1 | Parity omitted reasoning, multimodal, usage, and terminal ordering. | Expanded the differential matrix. |
| DV-09 | P1 | Migration could retain a manual reserved-prefix set. | Required composed ownership descriptors and anti-drift architecture tests. |
| DV-10 | P2 | Disabled-row ownership was unstated. | Bound behavior to established enabled-instance policy. |

### Final validation verdict

**GO after corrections.**

The final design preserves core/plugin-host boundaries, keeps dependency-free modes in the minimal binary, prevents literal secret persistence, composes with discovered external plugins, uses common lifecycle/tokenization/inventory/admission seams, and provides a deterministic TDD path.

## Open Implementation Decisions

Resolved during Task 1.1/1.2:

1. Exact final package names follow landed packages listed in the Task 1.1 mapping table.
2. Strict compatible config lives in `internal/core/config` as `CompatibleModeConfig` / `DecodeCompatibleModeConfig` (kind-agnostic schema; factories remain in `internal/standardplugins` until cutover).

Still open without changing requirements:

3. Exact admission API name and zero-limit default.
4. Supported tokenizer identifier vocabulary.
5. Disabled-row visibility in diagnostics.
6. Exact bounded diagnostic fields and reason codes.

Externalizing these modes, adding provider-specific behavior, accepting YAML secrets, or introducing core branches requires requirements and design revalidation.
