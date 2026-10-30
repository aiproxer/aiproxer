# Requirements Document

## Introduction

Go-LIP currently calls its backend adapters “plugins,” but the standard distribution statically imports every concrete backend through one registration table. That model is adequate for a small connector set, but it makes every bundled connector and its transitive runtime requirements part of the root module and standard binary. It also prevents a connector implemented in another Go module from satisfying the current factory contract because that contract exposes `internal/...` types and connector-specific composition dependencies.

This feature establishes a true backend connector plugin architecture suitable for more than one hundred independently evolving inference backends. A small built-in distribution retains the five essential hosted connectors—OpenAI Responses, OpenAI legacy completions/chat completions, Anthropic, Gemini, and Amazon Bedrock—without allowing `internal/core` to depend on any of them. All provider-specific and local-agent connectors outside that set become optional executable plugins discovered from trusted manifests and started only when an enabled backend instance requires them.

OpenRouter is deliberately classified as non-essential. Although it uses OpenAI-compatible transports, it is an independently evolving provider with provider-specific authentication, attribution headers, model routing, extensions, and inventory behavior. Generic OpenAI- and Anthropic-compatible protocol modes may remain implemented with the built-in protocol-family codecs when they add no provider-specific dependency, but OpenRouter itself belongs in an external connector module.

## Boundary Context

- **In scope**: backend connector ABI and SDK; executable-plugin process host; trusted manifest discovery; dynamic backend factory registration; lazy activation; connector lifecycle and streaming adaptation; root-module dependency isolation; independently buildable first-party connector modules; migration of every existing non-essential backend, including ACP-family and Codex-family connectors; packaging, diagnostics, conformance, and architectural enforcement.
- **Out of scope**: dynamically loading Go shared objects; remote/network plugin services; downloading or installing plugins at Go-LIP runtime; marketplace/catalog UI; frontend or feature plugin externalization; changing canonical request/event semantics; moving routing, failover, output commitment, secure-session authority, or accounting policy out of core.
- **Adjacent expectations**: active connector specs, especially `cursor-sdk-backend`, must be revalidated against this architecture before implementation. Installer and release work may ship a curated “full” plugin bundle for convenience, but the root binary remains able to build and run without it.
- **Boundary ownership**: public ABI and authoring kit in SDK; discovery/process supervision and internal adaptation in infrastructure/composition; concrete provider behavior in backend plugin modules; orchestration and canonical semantics in core.
- **Optional hexagonal lens**: `internal/core` consumes its existing backend port; the backend plugin host is a driven adapter and anti-corruption layer; executable connectors are out-of-process driven adapters; `cmd/lipstd` and `runtimebundle` remain the composition roots.
- **Revalidation triggers**: backend factory or `execbackend.Backend` shape changes; canonical call/event changes; capability negotiation; model inventory; accounting/finalization hooks; process lifecycle; startup security; plugin trust policy; connector packaging; active connector specifications.

## Requirements

### Requirement 1: Dependency and Ownership Boundaries

**Objective:** As a Go-LIP maintainer, I want enforceable module and import boundaries, so that connector growth cannot increase core coupling or silently add optional SDK runtimes to the proxy.

#### Acceptance Criteria

1. The `internal/core` package tree shall not import any concrete backend connector package, provider SDK, plugin host implementation, or executable-plugin transport library.
2. The Go-LIP root module shall compile, test, and produce the standard binary without requiring source code, generated code, package-manager files, or runtime installations from any non-essential connector module.
3. The standard binary shall statically include only the essential backend connector families: OpenAI Responses, OpenAI legacy, Anthropic, Gemini, and Bedrock, plus dependency-free protocol-compatible aliases implemented by those same codec families.
4. The composition root, and not core, shall own imports and construction of the essential backend connectors.
5. The root module shall not import a non-essential connector module directly or through a blank import, generated import table, build tag, or hidden transitive wrapper.
6. If a non-essential connector adds Node, Python, Java, native libraries, vendor SDKs, or other runtime dependencies, then those dependencies shall remain confined to that connector’s independently buildable and installable artifact.
7. The architecture test suite shall fail when a forbidden connector import, root-module dependency, or fixed non-essential registration is introduced.
8. The essential bundle shall name its dependency-free protocol-compatible aliases explicitly as `custom-openai-responses-compatible`, `custom-openai-legacy-compatible`, and `custom-anthropic-compatible`; adding any other built-in alias shall require architecture review and proof that it adds no provider-specific dependency or behavior.

### Requirement 2: Versioned Public Backend Plugin Contract

**Objective:** As a connector author, I want a stable public contract that contains no Go-LIP internal types, so that a connector can be developed and released independently from the root module.

#### Acceptance Criteria

1. The system shall define a versioned backend plugin protocol and Go authoring API under public, documented packages that do not expose `internal/...` types.
2. The plugin contract shall use protocol-neutral DTOs for backend identity, instance configuration, canonical invocation, attempt context, capability profiles, model inventory, usage/accounting evidence, errors, cancellation, and lifecycle.
3. The plugin contract shall preserve canonical optional-value and event-order semantics without exposing provider SDK or provider wire types.
4. The protocol shall negotiate a major compatibility version and a minor feature set before any connector instance is configured or invoked.
5. If the host and plugin have no compatible protocol major version, then startup of that configured backend shall fail before provider work begins with an actionable compatibility error.
6. Where a method or semantic is optional, the plugin shall advertise it explicitly and the host shall leave the corresponding internal capability unset rather than emulating unsupported behavior.
7. The public contract shall support one plugin executable exporting one or more backend factory kinds without requiring a host code change for each exported kind.
8. The SDK shall include a conformance harness and a minimal reference executable plugin that third-party authors can run without importing Go-LIP internals.
9. The protobuf contract shall preserve absent, explicit zero, explicit empty, and JSON `null` states wherever the canonical contract distinguishes them by using explicit field presence and bounded raw-JSON envelopes rather than implicit scalar, repeated, or map presence models that cannot distinguish absence from zero or empty.
10. Unknown protobuf fields may be tolerated for forward compatibility, but an unknown enum, feature, frame kind, or operation that affects required behavior shall fail closed before provider execution unless it was explicitly negotiated as optional.

### Requirement 3: Trusted Manifest Discovery and Dynamic Registration

**Objective:** As an operator, I want installed backend plugins to be discovered automatically, so that the core and standard bundle never maintain a fixed list of optional connectors.

#### Acceptance Criteria

1. When Go-LIP starts, the backend plugin discovery layer shall enumerate versioned manifests from operator-configured paths and installation-owned default plugin directories.
2. The discovery layer shall not search the current working directory, execute arbitrary `PATH` matches, contact a network service, or download software by default.
3. The plugin manifest shall declare the plugin identity, executable, protocol compatibility, exported backend factory kinds, build/version metadata, platform constraints, security posture, and artifact digest.
4. When a manifest is considered for registration, the discovery layer shall strictly validate the manifest schema, reject every unknown v1 field, require future metadata to use an explicit versioned extension mechanism, and validate path containment, executable file type, digest, uniqueness of plugin identity, uniqueness of exported factory ownership, and compatibility with the host.
5. When a valid manifest exports a backend factory kind, the integration layer shall register a host-backed factory dynamically without adding a connector-specific branch or table entry.
6. If two manifests claim the same factory kind, then startup shall fail deterministically and identify both conflicting manifests.
7. If an invalid or incompatible plugin is not referenced by any enabled backend configuration, then inspect/diagnostic output shall report it without preventing unrelated built-in or external backends from operating, unless strict discovery mode is configured.
8. If an enabled backend references a missing, invalid, incompatible, or untrusted plugin, then startup shall fail for that configuration with an actionable error before serving traffic.
9. Manifest v1 shall reference a native executable artifact only; scripts, shell command files, and interpreter entrypoints shall be rejected because v1 does not attest the interpreter runtime as part of executable identity.
10. The upstream standard distribution shall supply machine-scoped defaults of `/opt/go-lip/plugins` on Linux, `/Library/Application Support/Go-LIP/plugins` on macOS, and `%ProgramFiles%\Go-LIP\plugins` on Windows; another packager may replace those defaults explicitly, and development mode shall not infer a per-user directory unless the operator configures it in `paths`.

### Requirement 4: Lazy Activation and Optional Presence

**Objective:** As an operator, I want external connector processes and runtimes activated only when configured, so that installed or absent plugins do not impose unrelated startup or runtime costs.

#### Acceptance Criteria

1. The discovery layer shall read and validate manifests without starting plugin executables.
2. When no enabled backend instance uses an external factory kind, the system shall not start, health-check, initialize, or require that plugin executable or its language runtime.
3. When the first enabled backend instance for a plugin is constructed, the plugin host shall lazily start the supervised process required by the plugin’s declared process model and configure the requested instance; later instances shall reuse that process only when process sharing is declared, otherwise each instance shall receive its own supervised process.
4. Where one plugin exports multiple enabled backend instances, the plugin host shall share a supervised process only when the plugin contract declares instance isolation and concurrency support.
5. If a configured plugin runtime is unavailable, then the failure shall affect only startup of configurations that require that plugin and shall not turn plugin presence into a prerequisite for compiling the root module.
6. The minimal distribution shall remain fully functional with only built-in backends and no external plugin directory.
7. Where a curated full distribution installs optional plugins by default, the installed plugins shall remain inactive until referenced by enabled backend configuration.
8. The host shall not perform runtime package-manager installation, dependency resolution, or self-update for a plugin.

### Requirement 5: Host Integration and Lifecycle Ownership

**Objective:** As a runtime maintainer, I want one explicit integration layer between external plugins and the internal backend port, so that process mechanics and provider behavior remain outside core orchestration.

#### Acceptance Criteria

1. The backend plugin host shall adapt a configured plugin instance to the existing internal backend execution seam without changing routing ownership.
2. The integration layer shall map public plugin DTOs to and from `lipapi` and the internal attempt view while preventing plugin access to mutable routing, secure-session, database, registry, or executor state.
3. The composition root shall own plugin process creation, instance configuration, health state, shutdown, and rollback when later runtime assembly fails.
4. The plugin host shall give every started plugin process and configured instance idempotent close semantics, bounded graceful shutdown, hard termination fallback, and exactly-once wait/reap behavior.
5. If a plugin process exits unexpectedly, then every instance from that process shall be invalidated and subsequent operations shall receive a classified transport failure.
6. The host may restart a failed plugin for a later inventory refresh, request, or core-selected attempt, but it shall not replay an already opened attempt or hide a retry from core.
7. The integration layer shall pass a stable host-runtime policy projection—timeouts, proxy posture, identity presentation, size limits, and allowed environment—not a shared in-process HTTP client or internal configuration object.
8. The plugin process shall own provider SDK clients, provider transport plumbing, and provider-specific retries that comply with the advertised contract and core output-commitment rules.
9. Backend construction shall return the internal backend value together with composition-owned, idempotent cleanup; process and instance lifecycle shall not be added to the core-consumed `execbackend.Backend` port or exposed through provider-specific lifecycle branches.

### Requirement 6: Canonical Streaming, Cancellation, and Error Semantics

**Objective:** As a client and routing maintainer, I want external connectors to behave like built-in connectors at the canonical seam, so that plugin isolation does not weaken streaming or failover correctness.

#### Acceptance Criteria

1. The execution protocol shall be streaming-first and shall carry ordered canonical events incrementally without buffering a complete provider response.
2. The host shall enforce bounded frame sizes, bounded pending events, one terminal outcome, and no event delivery after terminal completion.
3. The plugin shall not emit frontend wire shapes, provider SDK objects, or provider-specific retry/failover decisions across the ABI.
4. When downstream cancellation occurs, the host shall propagate cancellation to the plugin instance and record whether provider-level or transport-level cancellation was achieved.
5. If a plugin fails before the first client-visible output event, then the host shall return a classified error that allows existing core policy to decide whether another candidate may be attempted.
6. If a plugin fails after the first client-visible output event, then the host shall preserve the failure as committed-attempt termination and shall not restart, replay, or fail over transparently.
7. If a capability mismatch or lossy canonical mapping is detected, the plugin host shall fail before provider execution rather than silently dropping required inputs or events.
8. The external plugin path shall preserve the same canonical conformance expectations as built-in connectors for text, reasoning, tools, multimodal content, usage, errors, and terminal ordering where those capabilities are advertised.
9. Transport headers, handshake completion, an `accepted` frame, and non-client-visible canonical events shall not mark output commitment; commitment shall occur only when the decoded canonical event satisfies the same `lipapi.OutputCommitted` predicate used by built-in execution.
10. The host shall disable automatic gRPC retries for execution and auxiliary operations; all retry or failover decisions shall remain explicit and owned by existing core policy.

### Requirement 7: Security, Trust, and Secret Handling

**Objective:** As a security-conscious operator, I want executable plugins treated as explicit trusted code with constrained launch behavior, so that automated discovery does not become arbitrary code execution or secret leakage.

#### Acceptance Criteria

1. The system shall document that an installed executable plugin has operating-system-level trust equivalent to other code executed by the proxy account.
2. The host shall launch only executables referenced by validated manifests from trusted directories and matching the expected cryptographic digest, and shall bind that verification atomically to the exact executable bytes launched through an approved platform strategy, unless an explicit development-mode override is enabled.
3. The host shall use an approved OS-specific local IPC profile that provides confidentiality and peer authentication; if no approved profile is available, the host shall fail before plugin configuration or secret delivery, and shall not use unauthenticated, cookie-only, or plaintext loopback transport.
4. The host shall construct a minimal child environment and shall not inherit the complete proxy environment by default.
5. The system shall ensure secrets are not placed in command-line arguments, manifest files, discovery diagnostics, process titles, metric labels, or normal logs.
6. The plugin host shall deliver plugin configuration and credential material only through the authenticated confidential local protocol after peer authentication and compatibility negotiation; an environment allowlist may contain non-secret bootstrap values only.
7. The host shall bound plugin stdout/stderr capture and sanitize plugin-originated errors and diagnostics before exposing them to operators.
8. The startup validator shall continue to enforce each backend’s declared credential mode and access scope, including local-only restrictions.
9. The core shall not auto-download, auto-update, or execute installation hooks from plugin manifests.
10. When a security-sensitive manifest or launch-policy change is proposed, the system shall require dedicated threat-model and startup-security revalidation.
11. Exact executable-byte identity and local transport peer identity shall be validated as separate mandatory gates; a digest, operating-system user identity, process ID, named-pipe token, or TLS certificate shall not be treated as proof of the other gate.
12. Unix peer authentication shall bind the connection to the expected spawned process generation; same-user identity alone shall not authorize configuration or secret delivery.
13. Where loopback mutual TLS is used, private bootstrap key material shall not be placed in the child environment and shall be delivered through an inherited private handle or an equivalently protected one-shot operating-system channel.

### Requirement 8: Configuration and Operator Compatibility

**Objective:** As an existing operator, I want connector externalization to preserve backend IDs and configuration intent, so that migration does not require redesigning routing and model aliases.

#### Acceptance Criteria

1. The system shall preserve existing `plugins.backends` rows that continue to use `kind`, runtime `id`, `enabled`, and opaque connector configuration.
2. When an existing connector is externalized, the system shall preserve its factory kind and runtime instance semantics unless a connector-specific migration explicitly documents a breaking change.
3. The system shall add generic backend plugin discovery/trust configuration without adding provider-specific fields to core configuration.
4. If an existing optional connector kind is configured after it becomes external, then Go-LIP shall resolve it from discovered manifests using the same kind rather than a connector-specific compatibility switch.
5. The inspect and doctor commands shall distinguish built-in, discovered, configured, active, incompatible, and failed backend plugin states.
6. If a minimal installation omits optional plugins, the system shall provide actionable installation guidance only when their configuration references an omitted kind.
7. The example configurations shall not enable every discovered connector or cause all plugin processes to start.
8. The active `cursor-sdk-backend` specification and any other unimplemented connector spec shall be updated or explicitly blocked until it conforms to this plugin architecture.

### Requirement 9: Capabilities, Inventory, Accounting, and Auxiliary Contracts

**Objective:** As a routing and accounting maintainer, I want the external ABI to cover the complete backend seam, so that connector extraction does not reduce model discovery or economic correctness.

#### Acceptance Criteria

1. The plugin shall describe static and model-aware canonical capabilities, transport capabilities, reasoning replay support, backend route prefixes, and max-output enforcement posture.
2. The plugin shall expose bounded model inventory with canonical IDs, native IDs, backend provenance, display metadata, capability evidence, source, freshness, and refresh behavior.
3. The host shall retain backend instance identity and plugin factory kind when publishing inventory rows and resolving routes.
4. If a plugin does not implement dynamic capability or inventory methods, then the host shall use only explicitly declared static data and shall not infer provider support.
5. Where provider token counting is supported, the ABI shall expose bounded counting with evidence quality and error classification.
6. Where billing finalization is supported, the ABI shall expose idempotent finalization with the same attempt lineage and accounting evidence expected by core.
7. If strict accounting requires a method that a plugin does not advertise, then startup or candidate admission shall fail through existing accounting policy rather than fabricating support.
8. The generic plugin metadata shall not contain connector-specific model catalogs, vendor resolvers, credential logic, or provider SDK objects.

### Requirement 10: Migration of Existing Non-Essential Connectors

**Objective:** As a maintainer, I want every current non-essential connector moved behind the external ABI, so that the architecture is proven on the existing heterogeneous connector set.

#### Acceptance Criteria

1. The final standard registration bundle shall contain no fixed registrations for ACP, Cursor CLI ACP, Gemini CLI ACP, Agy CLI ACP, Codex App Server, OpenAI Codex, OpenRouter, NVIDIA, Hugging Face, OpenCode Go, OpenCode Zen, Ollama, Ollama Cloud, llama.cpp, LM Studio, vLLM, or future Cursor SDK connectors.
2. The migration shall preserve for each migrated connector its existing factory kind, security profile, route-prefix behavior, canonical conformance, and documented configuration unless a separately approved migration states otherwise.
3. The migration shall move provider-specific helpers used only by non-essential connectors, including the Codex model catalog and OpenCode vendor-resolution wiring, out of generic core/factory dependencies and into the owning plugin module or a connector-support module.
4. The shared ACP protocol, JSON-RPC, subprocess, session, cancellation, and mapping functionality may remain in the Go-LIP repository only as a dependency-light connector-support package or independently versioned module that does not import `internal/core` and is not required by the root build; extraction shall replace internal lifecycle cancellation types with public plugin cancellation DTOs, require caller-owned HTTP transport policy instead of `internal/infra/httpclient`, and make executable lookup caches instance-owned rather than package-global.
5. The concrete ACP products and authentication profiles shall live in executable connector modules and shall not be enumerated by the root binary.
6. The migration shall use a reference external `local-stub` connector to prove the ABI, discovery, lifecycle, and conformance path before high-complexity provider migrations begin; test-only in-process stubs may remain under `internal/testkit`.
7. The migration shall move OpenRouter as an external provider connector; generic OpenAI-compatible protocol modes may remain with built-in codecs only when they carry no OpenRouter-specific behavior or dependency.
8. The migration shall remove a static connector only after its external replacement passes parity, lifecycle, security, packaging, and upgrade/rollback gates.
9. When the final migration is complete, deleting all optional connector module directories from a checkout shall not prevent `GOWORK=off go build ./cmd/lipstd` or root-module tests from succeeding.

### Requirement 11: Independent Modules, Packaging, and Release Topology

**Objective:** As a release maintainer, I want connectors versioned and shipped independently, so that optional dependencies and release cadence do not contaminate the proxy root module.

#### Acceptance Criteria

1. The first-party external connectors shall be separate Go modules or separate repositories with their own dependency graphs, tests, lockfiles, generated artifacts, and release metadata.
2. The root `go.mod` shall not require or replace first-party external connector modules.
3. Where local multi-module development uses a generated or developer-only Go workspace, the root CI and release builds shall run with `GOWORK=off` to prove isolation.
4. The plugin release shall publish an executable, manifest, digest, protocol compatibility range, platform/architecture metadata, and dependency/runtime prerequisites.
5. The submodule release tags and module paths shall follow Go module rules and permit independent semantic versioning.
6. The release system may produce minimal and curated-full installation bundles without creating different core behavior or hardcoded connector registration lists.
7. The build and test automation for connector modules shall discover modules from repository structure or manifests rather than maintain a second fixed connector list.
8. Where a connector artifact bundles a private Node/Python/native companion, the root distribution shall not install or require that companion when the connector package is absent.
9. The plugin upgrade and rollback process shall replace the external artifact and manifest without rebuilding the Go-LIP root binary, subject to protocol compatibility.
10. The architecture decision records and steering documentation shall be updated to supersede the current static-only backend plugin model while preserving explicit construction and rejecting Go native shared-object plugins.
11. The first-party release matrix shall cover `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64`, `windows-amd64`, and `windows-arm64` for the host architecture; each connector manifest may advertise only the subset for which its artifact passes launch, IPC, lifecycle, and conformance gates.
12. The root release workflow shall verify `GOWORK=off go list ./...`, `GOWORK=off go test ./...`, `GOWORK=off go build ./cmd/lipstd`, and `GOWORK=off go list -m all`, while connector workflows shall discover nested modules structurally and execute their checks with `GOWORK=off` from each module root.

### Requirement 12: Diagnostics, Testing, and Scale Readiness

**Objective:** As a maintainer and operator, I want deterministic evidence for plugin discovery, lifecycle, and connector parity, so that the architecture can safely scale beyond the current connector count.

#### Acceptance Criteria

1. The system shall expose bounded, low-cardinality diagnostics for plugin discovery state, negotiated protocol, process generation, configured instances, health, restarts, and last safe error code without exposing secrets or full config.
2. The test suite shall include manifest parser fuzzing, unknown-field and extension-version tests, duplicate/conflict tests, digest/path and launch-substitution tests, unauthorized-local-client and peer-authentication tests, compatibility negotiation tests, fake process tests, stream contract tests, cancellation tests, crash/restart tests, shutdown/rollback tests, and race/leak checks.
3. The SDK conformance suite shall be runnable by every connector module and shall validate only capabilities the connector advertises.
4. The architecture tests shall prove core import isolation, root module graph isolation, absence tolerance, no fixed optional registration list, and no connector-specific fields in generic plugin contracts.
5. The CI system shall build the root module independently and run a dynamically discovered matrix of first-party plugin modules on supported platforms.
6. The migration CI shall compare each external connector against its existing implementation or accepted golden/refbackend evidence before static removal.
7. The discovery and registration process for at least one hundred synthetic plugin manifests shall remain bounded and shall not launch plugin processes.
8. The plugin data plane shall add no unbounded queues or whole-response buffering and shall preserve backpressure from the canonical consumer.
9. The documentation shall include authoring, installation, trust, configuration, troubleshooting, compatibility, upgrade, rollback, and minimal-versus-full distribution guidance.
10. The full design shall pass Kiro design validation against architecture alignment, maintainability, type safety, security, and complete requirement traceability before tasks are approved.
11. CI shall execute native plugin security, launch, IPC, lifecycle, packaging, and upgrade/rollback gates on Linux, macOS, and Windows rather than treating the existing Ubuntu-only workflows as cross-platform evidence.
