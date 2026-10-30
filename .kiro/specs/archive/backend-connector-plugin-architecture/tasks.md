# Implementation Plan

Implementation follows TDD throughout: architecture gates, interfaces, protobuf fixtures, manifests, conformance tests, fake processes, and migration parity tests are written before production behavior. A task is complete only when its focused validation passes and its observable completion condition is demonstrated.

## Phase 1: Lock the Public Contract and Dependency Gates

- [x] 1. Establish the versioned backend plugin contract and proof harness

- [x] 1.1 Revalidate the executable-plugin process substrate before adopting it
  - Start from the source-anchored limitations of stock `go-plugin` v1.8.0: pathname checksum followed by path execution, no Unix expected-peer credential enforcement, Windows loopback TCP rather than named pipes, environment-carried AutoMTLS bootstrap material, no Go-LIP process-model supervisor, and effectively unbounded default gRPC message ceilings.
  - Write an isolated substrate **decision spike** and tests comparing a customized `go-plugin` path and a narrow project-owned host against the mandatory matrix (protocol negotiation, bidirectional streaming, disabled transport retries, exact-byte launch profiles, expected-process peer identity, protected bootstrap, env/handle control, reattach prohibition, process-tree cleanup, bounded logs/messages, declared process models). This task records honest source/design feasibility and replacement cost; it does **not** claim those OS controls were executed here (runtime proof remains Tasks 2.3/3.1/3.2/3.4).
  - Record MPL-2.0 obligations, security posture, unsupported/unverified platforms, and the evidence for selecting one substrate.
  - Accept customized `go-plugin` only when every launch, peer, bootstrap, bounds, lifecycle, and cleanup contract is proven without replacing those subsystems; otherwise select the same public ABI on the project-owned host without weakening requirements.
  - Observable completion: a reviewed decision record and deterministic decision tests select one process substrate and one approved secure transport/launch profile per supported platform without changing requirements or leaking internal types.
  - _Requirements: 2.4, 2.5, 4.3, 5.3, 5.4, 7.2, 7.3, 7.4, 7.6, 7.10, 11.10, 12.2_
  - _Boundary: Infrastructure research and tests_
  - _Depends: none_
  - _Validation: `go test ./internal/infra/backendplugins/processhost/...`_

- [x] 1.2 Define protobuf and public Go DTO contracts with tests first
  - Write failing golden and round-trip tests for protocol negotiation, plugin descriptors, factory exports, opaque YAML, absent-versus-zero and absent-versus-empty scalars, explicit `UsagePresence`, absent/`null`/empty raw JSON, canonical invocation parts, capabilities, transport capabilities, reasoning replay, inventory, count evidence, billing finalization, errors, cancellation outcomes, and terminal events.
  - Add `api/backendplugin/v1/backend.proto` and public `pkg/lipsdk/backendplugin` authoring types without aliasing or importing `internal/...` packages.
  - Define compatibility rules for major versions, minor feature negotiation, retained unknown fields, fail-closed unknown enums/frame kinds/mandatory features, optional-versus-required operations, and bounded message sizes; prohibit transport-level automatic retries.
  - Observable completion: public packages compile independently, golden fixtures preserve canonical presence and ordering, and an incompatible major version is rejected before configuration.
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.9, 2.10, 9.1, 9.4, 9.5, 9.6, 9.7_
  - _Boundary: SDK public contract_
  - _Depends: 1.1_
  - _Validation: `go test ./pkg/lipsdk/backendplugin/... ./api/backendplugin/v1/...`_

- [x] 1.3 Add executable architecture and module-isolation tests before implementation
  - Write failing tests proving `internal/core` cannot import concrete connectors, plugin host code, generated plugin RPC packages, gRPC, protobuf, or the process substrate.
  - Write failing tests proving the root `go.mod` cannot require or replace external connector modules, public ABI packages cannot import provider SDKs or `internal/...`, and generic factory dependencies cannot name Codex, OpenCode, ACP-product, or other provider-specific types.
  - Add a test that permits exactly the five essential families plus `custom-openai-responses-compatible`, `custom-openai-legacy-compatible`, and `custom-anthropic-compatible`, and fails when any other fixed backend kind enters the essential table or mandatory requirements.
  - Add `GOWORK=off` root proofs for `go list ./...`, `go test ./...`, `go build ./cmd/lipstd`, and `go list -m all` that can run with connector module directories hidden or copied out of the checkout.
  - Observable completion: all target boundary violations are detected by focused tests before package movement begins.
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 10.9, 11.2, 11.3, 12.4, 12.5_
  - _Boundary: Architecture tests_
  - _Depends: 1.2_
  - _Validation: `go test ./internal/archtest && GOWORK=off go build ./cmd/lipstd`_

- [x] 1.4 Build the conformance kit and deterministic fake plugin
  - Write failing conformance cases for descriptor/configure lifecycle, capability honesty, inventory bounds, text/reasoning/tool/multimodal events, usage, errors, cancellation, one-terminal ordering, counting, billing idempotency, and instance close.
  - Add a minimal public server helper and a configurable fake executable that scripts malformed frames, slow output, blocked cancellation, process exit, duplicate terminal, oversized messages, unauthorized peer attempts, and shutdown behavior.
  - Ensure the conformance harness runs against a connector process without importing Go-LIP internals or requiring provider network access.
  - Observable completion: a third-party-shaped executable passes the advertised-capability conformance suite and intentionally broken modes fail with stable diagnostics.
  - _Requirements: 2.8, 5.1, 6.1, 6.2, 6.3, 6.4, 6.7, 6.8, 7.3, 9.1, 9.2, 9.4, 9.5, 9.6, 12.2, 12.3_
  - _Boundary: SDK and tests_
  - _Depends: 1.2_
  - _Validation: `go test ./pkg/lipsdk/backendplugin/conformance/... ./internal/testkit/backendplugin/...` (includes process smoke for `cmd/lip-backendplugin-fake`)_

## Phase 2: Implement Trusted Non-Executing Discovery

- [x] 2. Add manifest, trust, and discovery infrastructure

- [x] 2.1 Define manifest v1 and strict parser tests
  - Write failing tests and fuzz targets for schema version, plugin ID, native executable path, SHA-256, protocol range, exported kinds, security profiles, process-sharing declaration, platform matrix, every unknown v1 field, explicitly supported versioned extension blocks, duplicate exports, nesting, and file-size bounds.
  - Implement a closed versioned public manifest model and strict decoder that rejects every unknown field by default and treats manifests as installation metadata only; future metadata must use a new schema version or an explicitly standardized versioned extension mechanism.
  - Reject secret fields, arbitrary arguments, environment maps, install hooks, shell commands, script or interpreter entrypoints, download URLs, connector model catalogs, and arbitrary pass-through metadata.
  - Observable completion: valid fixtures round-trip deterministically and malformed, unknown-field, unsupported-extension, or security-expanding manifests fail without executing files.
  - _Requirements: 3.3, 3.4, 3.9, 7.5, 7.9, 11.4, 12.2_
  - _Boundary: SDK manifest contract and infrastructure parser_
  - _Depends: 1.2_
  - _Validation: `go test ./pkg/lipsdk/backendplugin/manifest/... ./internal/infra/backendplugins/manifest/... && go test -fuzz=FuzzManifest -fuzztime=30s ./internal/infra/backendplugins/manifest`_

- [x] 2.2 Implement deterministic trusted-directory discovery without process launch
  - Write failing tests for explicit paths; upstream defaults `/opt/go-lip/plugins`, `/Library/Application Support/Go-LIP/plugins`, and `%ProgramFiles%\Go-LIP\plugins`; packager-injected alternatives; development mode requiring explicit paths; stable ordering; duplicate manifest paths; no current-working-directory, implicit per-user, or ambient `PATH` search; no network access; and no executable invocation.
  - Implement discovery as an O(manifests plus bytes) scan that produces immutable artifact descriptors and safe status records.
  - Add a one-hundred-synthetic-manifest test proving bounded memory, file descriptors, goroutines, and zero child processes.
  - Observable completion: discovery registers valid metadata for one hundred manifests while process-spawn counters remain zero.
  - _Requirements: 3.1, 3.2, 3.7, 3.10, 4.1, 4.2, 4.6, 12.1, 12.7_
  - _Boundary: Infrastructure discovery_
  - _Depends: 2.1_
  - _Validation: `go test ./internal/infra/backendplugins/discovery/... -run 'Discovery|Hundred|NoLaunch'`_

- [x] 2.3 Enforce path containment, artifact type, platform, and digest-bound launch trust
  - Write failing tests for path traversal, absolute-path policy, symlink/junction escape and replacement, scripts/interpreter entrypoints, directories, devices, sockets, non-executable files, unsupported platform, digest mismatch, replacement after discovery, replacement after final pathname check, Linux sealed descriptor execution, protected macOS/Windows digest staging, staging substitution, and upgrade/rollback cleanup.
  - Implement trusted-root containment and SHA-256 verification from an opened file identity, then atomically bind verified native bytes to Linux descriptor-bound execution or protected macOS/Windows digest-addressed staging; a pathname rehash immediately before process creation is not sufficient.
  - Fail closed on a platform where neither approved strategy preserves the verified identity, and keep signature verification as an additive future feature that cannot disable the digest and trusted-directory baseline.
  - Observable completion: the exact executable bytes launched are the bytes whose digest was accepted, and every substitution or unsupported-binding attempt returns a bounded stable reason code.
  - _Requirements: 3.4, 3.8, 7.1, 7.2, 7.10, 12.2_
  - _Boundary: Infrastructure trust policy_
  - _Depends: 1.1, 2.2_
  - _Validation: `go test ./internal/infra/backendplugins/trust/... -run 'Digest|Handle|Staging|Substitution|Rollback'`_

- [x] 2.4 Build discovery catalog conflict handling and safe diagnostics
  - Write failing tests for duplicate plugin IDs, two manifests claiming one factory kind, collisions with built-ins, protocol incompatibility, unused invalid artifacts, strict mode, and enabled config referencing an unavailable kind.
  - Implement deterministic conflict resolution that never silently chooses one artifact.
  - Expose bounded states for discovered, incompatible, invalid, untrusted, digest mismatch, conflict, configured, active, failed, and stopped without secrets or full user paths.
  - Observable completion: inspect data identifies both owners of a conflict, unused invalid plugins remain non-fatal outside strict mode, and configured invalid plugins fail before serving.
  - _Requirements: 3.5, 3.6, 3.7, 3.8, 8.5, 8.6, 12.1_
  - _Boundary: Infrastructure catalog and diagnostics_
  - _Depends: 2.2, 2.3_
  - _Validation: `go test ./internal/infra/backendplugins/catalog/... ./internal/core/diag/...`_

## Phase 3: Implement the Process Host and Internal Adapter

- [x] 3. Add lazy process lifecycle and canonical stream adaptation

- [x] 3.1 Implement lazy supervised process activation and secure local channel establishment
  - Windows production profile proven after repair: per-instance pending/configured/failed activation gate + serialized DialAndConfigure; generation-owned duplicated `FILE_SHARE_READ` launch handle; suspended CreateProcess + job `KILL_ON_JOB_CLOSE` + image FileId check (path locates only); private named pipe (`PIPE_REJECT_REMOTE_CLIENTS`, PID/SID/job peer auth) + negotiate/configure after peer; PID-reuse and cookie/plaintext fail-closed evidence. Darwin remains fail-closed. Linux socketpair path cross-compiles.
  - Write failing tests for no launch during discovery, first-configure launch, launch singleflight keyed by the declared process model, shared artifact processes, per-instance processes, declared multi-instance sharing, instance and secret isolation, missing runtime, failed handshake, unauthorized and same-UID wrong-process clients, PID reuse and stale-generation peers, plaintext/cookie-only fallback rejection, environment bootstrap-key rejection, configuration attempted before both executable and peer identity gates, and later-operation restart.
  - Launch the exact verified executable directly without a shell, with an explicit working directory, process generation, minimal non-secret bootstrap environment, and an approved OS-specific confidential peer-authenticated local channel; refuse configuration when no approved profile is available.
  - Configure only the requested instance. Later instances reuse a process only when sharing, isolation, and concurrency are declared; otherwise they receive independent supervised processes.
  - Do not health-check or initialize an installed plugin until an enabled backend row requires its exported kind, and never deliver connector configuration or secrets before expected-peer authentication and protocol compatibility succeed.
  - Observable completion: an unconfigured installed plugin never starts; concurrent first configuration starts exactly the process generations permitted by the declared model; and unauthorized local peers receive no configure access or secret material.
  - _Requirements: 4.1, 4.2, 4.3, 4.4, 4.5, 4.7, 4.8, 5.3, 5.7, 7.3, 7.4, 7.5, 7.6, 7.11, 7.12, 7.13_
  - _Boundary: Infrastructure process host and local transport_
  - _Depends: 1.1, 2.3, 2.4_
  - _Validation: `go test -race ./internal/infra/backendplugins/processhost/... -run 'Lazy|ProcessModel|Instance|Peer|SecureChannel|Restart'`_

- [x] 3.2 Implement instance ownership, rollback, shutdown, and process reaping
  - Write failing tests for configure failure after process start, multiple instance closes, reverse-order build rollback, normal runtime close, rejected new work during shutdown, graceful timeout, hard termination, descendant cleanup, and exactly-once wait.
  - Introduce a composition-owned backend build result containing `execbackend.Backend` plus idempotent cleanup; do not add process lifecycle to the core-consumed backend struct.
  - Register instance and process closers immediately during runtime assembly so later inventory, accounting, or server startup failure cannot leak resources (`pluginreg.BackendBuildResult` / `RegisterLifecycleBackend` → `buildBackends` → `RegisterPluginBuildCleanup` before inventory/accounting).
  - Observable completion: every partial-build and shutdown scenario leaves no process, goroutine, pipe, socket, staged executable, or unreaped child in race and leak tests.
  - _Requirements: 5.3, 5.4, 5.5, 5.6, 5.9, 8.8, 12.2_
  - _Boundary: Internal backend lifecycle and composition root_
  - _Depends: 3.1_
  - _Validation: `go test -race ./internal/infra/backendplugins/processhost ./internal/infra/runtimebundle && go test ./internal/infra/backendplugins/processhost -run TestLeak`_

- [x] 3.3 Adapt capabilities, inventory, counting, and billing operations
  - Write failing tests mapping static and model-aware capabilities, transport capabilities, replay support, route prefixes, max-output enforcement, inventory provenance, refresh failures, token count evidence, and idempotent billing finalization.
  - Implement the host adapter so unadvertised optional methods remain nil or unsupported and strict accounting continues to fail closed.
  - Add independent deadlines and result bounds for metadata and auxiliary calls.
  - Observable completion: a fake plugin can populate every existing backend seam field, and omissions never fabricate support.
  - _Requirements: 5.1, 5.2, 6.7, 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7, 9.8_
  - _Boundary: Infrastructure anti-corruption adapter_
  - _Depends: 1.4, 3.1_
  - _Validation: `go test ./internal/infra/backendplugins/adapter ./internal/core/modelregistry ./internal/core/execbackend -run 'Capability|Inventory|CountToken|FinalizeBilling'`_

- [x] 3.4 Implement bounded bidirectional execution streaming and cancellation
  - Write failing tests for accepted, incremental events, sequence numbers, frame bounds, pending-event bounds, slow consumers, EOF, one terminal, duplicate terminal, events after terminal, malformed events, provider cancellation, transport cancellation, cancellation timeout, and disabled automatic gRPC retries.
  - Implement a managed stream that pulls RPC events incrementally and propagates backpressure instead of collecting a provider response.
  - Enforce full `ServerFrame` size (`ServerFrameSizeBytes` / `ValidateServerFrameSize`) on every inbound frame against `MaxStreamFrame` (opaque/tool/diagnostic, not delta-only).
  - Separate bounded stderr diagnostics from protocol transport and sanitize all plugin-originated errors.
  - Observable completion: streaming conformance passes under slow consumption with bounded memory and no post-terminal delivery.
  - _Requirements: 6.1, 6.2, 6.3, 6.4, 6.7, 6.8, 6.10, 7.3, 7.7, 12.8_
  - _Boundary: Infrastructure stream adapter_
  - _Depends: 1.4, 3.1_
  - _Validation: `go test -race ./internal/infra/backendplugins/adapter -run 'Stream|Cancel|Backpressure|Terminal'`_

- [x] 3.5 Enforce pre-output versus post-output failure ownership
  - Write failing integration tests for process exit, protocol violation, provider error, and cancellation before and after the first decoded event satisfying `lipapi.OutputCommitted`; prove that headers, handshake, `accepted`, diagnostics, and usage-only events do not commit output.
  - Return classified pre-output failures to the existing executor so core alone decides retry or failover (`ExecuteFailure` kinds at Execute boundary).
  - After output commitment, terminate the current attempt without process restart, request replay, model switch, credential switch, or hidden fallback.
  - Invalidate every instance of a crashed generation on transport death / protocol violation (once, via `InvalidateGeneration`); context cancel does not invalidate; restart remains external/core-selected.
  - Observable completion: instrumentation proves the host opens one provider attempt per core attempt and never replays a committed attempt.
  - _Requirements: 5.5, 5.6, 6.5, 6.6, 6.9, 7.7, 12.2_
  - _Boundary: Core integration and infrastructure adapter_
  - _Depends: 3.4_
  - _Validation: `go test -race ./internal/core/runtime ./internal/infra/backendplugins/adapter -run 'PreOutput|PostOutput|NoReplay|Crash'`_

## Phase 4: Integrate Discovery with Standard Composition

- [x] 4. Replace the fixed optional table with hybrid composition

- [x] 4.1 Split the standard backend bundle into essential built-ins and migration-only optional entries
  - Write failing registration tests asserting that only OpenAI Responses, OpenAI legacy, Anthropic, Gemini, Bedrock, and approved dependency-free compatible modes belong to the essential bundle.
  - Refactor composition so essential packages are imported only by `internal/standardplugins`, never by core.
  - Temporarily isolate current optional static registrations behind an explicitly named migration bundle so they cannot be mistaken for the final architecture.
  - Observable completion: a minimal registry contains the five essential families and no non-essential fixed kind.
  - _Requirements: 1.3, 1.4, 1.5, 10.1, 10.7_
  - _Boundary: Composition root_
  - _Depends: 1.3_
  - _Validation: `go test ./internal/standardplugins ./internal/pluginreg ./pkg/lipsdk -run 'Essential|StandardDistribution|Optional'`_

- [x] 4.2 Register discovered exports through one generic factory path
  - Write failing tests for registration of arbitrary manifest-exported kinds, duplicate rejection, configured instance IDs, opaque YAML delivery, security profile propagation, and absence of connector-specific switch statements.
  - Add a registry installation function that converts validated exports into host-backed factories.
  - Resolve enabled backend rows against the union of essential and discovered factories before runtime construction.
  - Wire `BuildBootstrap` serve: discover→trust→catalog→`InstallDiscoveredExports` before `Build` (production path populates install; no manual `DiscoveredPlugins` required).
  - Observable completion: a synthetic factory kind unknown to Go-LIP source is discovered, registered, configured, and invoked successfully; Windows production `BuildBootstrap` path activates a real fake `.exe`.
  - _Requirements: 2.7, 3.5, 3.6, 5.1, 8.1, 8.2, 8.4_
  - _Boundary: Registry and composition root_
  - _Depends: 2.4, 3.3, 4.1_
  - _Validation: `go test ./internal/pluginreg ./internal/infra/runtimebundle -run 'Discovered|Dynamic|UnknownKind'`_

- [x] 4.3 Add generic discovery config and inspect or doctor behavior
  - Write failing config tests for defaults, paths, strict mode, development mode, unknown keys, production rejection of development overrides, and unchanged existing backend rows.
  - Extend bootstrap so inspect performs manifest validation without process launch and reports built-in, discovered, configured, missing, incompatible, and conflicting states.
  - Inspect and serve share `ResolveCatalog` / `ResolvePluginCatalog` (same snapshot + conflict policy).
  - Add an explicit doctor path that may launch only selected configured plugins and validates exact executable identity plus secure peer-authenticated channel establishment; never launch every discovered plugin implicitly or send credentials after channel failure.
  - Observable completion: existing configurations decode unchanged, minimal installations inspect cleanly, and a missing or insecure configured plugin gives actionable guidance without exposing secrets.
  - _Requirements: 3.1, 3.7, 3.8, 4.2, 4.6, 7.2, 7.3, 8.1, 8.3, 8.5, 8.6, 8.7_
  - _Boundary: Config, CLI, and composition root_
  - _Depends: 2.4, 4.2_
  - _Validation: `go test ./internal/core/config ./internal/infra/runtimebundle ./cmd/lipstd -run 'Discovery|Inspect|Doctor|MissingPlugin|SecureChannel'`_

- [x] 4.4 Remove connector-specific collaborators from generic factory dependencies
  - Write failing architecture tests forbidding Codex catalog, Codex source, OpenCode vendor resolver, ACP product profiles, and provider-specific key pools from `BackendFactoryDeps` or its replacement.
  - Move built-in-only dependencies into closures owned by the essential composition bundle.
  - Define the stable runtime-policy projection passed to external plugins instead of sharing `*http.Client`, internal identity config, or mutable registries across the process boundary.
  - Observable completion: generic plugin registration compiles without importing any connector family and the current static connectors continue to pass while awaiting migration.
  - _Requirements: 1.5, 2.2, 5.2, 5.7, 8.3, 9.8, 10.3_
  - _Boundary: Registry contracts and composition root_
  - _Depends: 4.1, 4.2_
  - _Validation: `go test ./internal/pluginreg ./internal/standardplugins ./internal/archtest -run 'FactoryDeps|ProviderSpecific'`_

- [x] 4.5 Prove minimal distribution and inactive-plugin scale behavior end to end
  - Add end-to-end tests for no plugin directory, one unused installed plugin, one configured external plugin, one invalid unused plugin, one invalid configured plugin, and one hundred inactive manifests.
  - Assert that built-in traffic remains functional and no external process starts unless an enabled backend requires it.
  - Assert inspect and serving use the same resolved catalog and conflict policy.
  - Windows production unknown-kind e2e via `BuildBootstrap` (real `.exe`, processhost, secure channel; no TestLauncher/FakeService DialSession).
  - Observable completion: the standard binary serves through built-ins with zero optional artifacts and activates exactly the required external artifacts in mixed configurations.
  - _Requirements: 3.7, 3.8, 4.1, 4.2, 4.5, 4.6, 4.7, 8.7, 12.7_
  - _Boundary: End-to-end tests_
  - _Depends: 4.2, 4.3_
  - _Validation: `go test ./internal/stdhttp ./internal/infra/runtimebundle ./cmd/lipstd -run 'Minimal|Inactive|Hundred|Mixed'`_

## Phase 5: Prove Independent Module and Packaging Workflows

- [x] 5. Establish the first external connector artifact and release topology

- [x] 5.1 Move the production local-stub connector into an independent reference module
  - Write module-local conformance and packaging tests before moving production behavior.
  - Create `connectors/localstub` with its own `go.mod`, entry command, connector implementation, manifest template, and no imports from Go-LIP internals.
  - Keep test-only in-process stubs under `internal/testkit`; remove the production local-stub factory from the essential root bundle after external parity passes.
  - Observable completion: the local-stub executable is discovered and invoked from an installed artifact while its module directory can be removed without breaking the root build.
  - _Requirements: 2.8, 10.6, 11.1, 11.2, 11.4, 11.8, 12.3, 12.6_
  - _Boundary: External backend plugin module_
  - _Depends: 1.4, 4.5_
  - _Validation: `(cd connectors/localstub && GOWORK=off go test ./... && go build ./cmd/lip-backend-localstub) && GOWORK=off go build ./cmd/lipstd`_

- [x] 5.2 Add dynamic multi-module CI discovery and root-isolation gates
  - Write a script test that discovers connector modules from repository structure or manifests rather than a maintained connector-name list.
  - Run root `go list ./...`, `go test ./...`, `go build ./cmd/lipstd`, and `go list -m all` with `GOWORK=off`, then run list/test/build for each structurally discovered connector module independently with `GOWORK=off` and its own dependency graph.
  - Add checks that root `go list -m all` contains no external connector module and connector modules contain no Go-LIP internal import.
  - Observable completion: adding a synthetic connector module automatically adds it to CI without modifying a source or workflow connector list.
  - _Requirements: 1.2, 1.5, 11.1, 11.2, 11.3, 11.5, 11.7, 11.12, 12.4, 12.5_
  - _Boundary: Build and CI_
  - _Depends: 5.1_
  - _Validation: `make backend-plugin-module-checks && GOWORK=off make quality-checks`_

- [x] 5.3 Build minimal and curated-full package assembly from manifests
  - Write packaging tests proving the minimal artifact includes no optional connector executable and the curated-full profile installs selected artifacts into `/opt/go-lip/plugins`, `/Library/Application Support/Go-LIP/plugins`, or `%ProgramFiles%\Go-LIP\plugins` with installer/admin ownership and proxy read/execute access only.
  - Generate package indexes and install layouts from connector release metadata rather than Go registration code.
  - Ensure installed but unconfigured plugins remain inactive and private Node, Python, or native companions remain inside their connector package.
  - Observable completion: the same root binary runs both distribution profiles and full-package removal of one plugin does not affect unrelated backends.
  - _Requirements: 4.7, 11.4, 11.6, 11.7, 11.8, 11.9_
  - _Boundary: Packaging and release infrastructure_
  - _Depends: 5.1, 5.2_
  - _Validation: `make package-minimal package-full package-plugin-smoke`_

- [x] 5.4 Publish the connector authoring and compatibility documentation with executable examples
  - Document closed manifest creation, SDK server helpers, advertised capabilities, process-sharing declarations, configuration ownership, exact executable trust, secure local IPC, secret handling, conformance, module versioning, release tags, installation, compatibility, and rollback.
  - Include a minimal connector based on local-stub and a skeleton for connectors that own a private Node or Python bridge.
  - Add documentation tests or sample builds so examples cannot drift from the public API.
  - Observable completion: a clean external module generated from the guide passes conformance and packages without importing the root module's internals.
  - _Requirements: 2.8, 3.4, 4.3, 7.1, 7.2, 7.3, 7.6, 9.8, 11.4, 11.5, 11.9, 12.9_
  - _Boundary: SDK documentation and examples_
  - _Depends: 5.1, 5.3_
  - _Validation: `make docs-check backend-plugin-example-check`_

## Phase 6: Extract and Migrate the ACP Family

- [x] 6. Move shared ACP support and concrete ACP products out of root composition

- [x] 6.1 Extract dependency-light ACP connector support with contract tests
  - Write failing parity tests for JSON-RPC framing, initialize/authenticate, session creation, prompt streaming, server requests, cancellation, history divergence, subprocess pooling, idle reaping, and PID reuse protection through a public support boundary.
  - Move only stable protocol and runtime support into an independently versioned `connector-support/acp` module or equivalent public package with no `internal/core`, registry, runtimebundle, or concrete product imports; replace internal lifecycle cancellation types with public DTOs, require caller-owned HTTP policy instead of `internal/infra/httpclient`, and make executable lookup caching instance-owned.
  - Where a helper cannot be made stable without internal leakage, keep it product-local rather than broadening the public API.
  - Observable completion: ACP support builds independently and its API contains no Cursor, Gemini, Agy, routing candidate, or internal backend types.
  - _Requirements: 1.6, 2.2, 10.3, 10.4, 10.5, 11.1, 12.6_
  - _Boundary: Connector-support module_
  - _Depends: 5.2_
  - _Validation: `(cd connector-support/acp && GOWORK=off go test -race ./...) && go test ./internal/archtest -run ACP`_

- [x] 6.2 Implement and validate the external generic ACP plugin
  - Write plugin-module tests for existing `acp` configuration, security profile, route prefix, model inventory, HTTP and subprocess modes as applicable, stream mapping, cancellation, and lifecycle.
  - Implement the `acp` factory kind using the extracted support and public backend plugin SDK.
  - Run external versus current-static differential and conformance evidence before removing the static registration.
  - Observable completion: existing `kind: acp` YAML routes through the external artifact with accepted parity and no root dependency on ACP support.
  - Evidence (2026-07-20): `connectors/acp` TestParity_* + `conformance.RunWith` against named `testemu` HTTP emulator; root golden `connectors/acp/testdata/parity_profile.json` + `TestExternalParity_ProfileFixture`; `make parity-acp-plugin` named runs. Vision treated as input-only in conformance execute proof; usage events not required for ACP HTTP.
  - _Requirements: 8.1, 8.2, 8.4, 10.1, 10.2, 10.4, 10.5, 10.8, 12.6_
  - _Boundary: External backend plugin module_
  - _Depends: 6.1_
  - _Validation: `(cd connectors/acp && GOWORK=off go test -race ./...) && make parity-acp-plugin`_

- [x] 6.3 Implement and validate the external Cursor CLI ACP plugin
  - Write tests preserving `cursorcliacp` factory kind, CLI login posture, model discovery, workspace and session identity, subprocess pooling, cancellation, stale termination, and local-only security.
  - Implement the external connector without embedding Cursor product behavior in the generic host or ACP support package.
  - Prove process-tree cleanup on Linux, macOS, and Windows before static cutover.
  - Observable completion: existing Cursor CLI ACP configurations pass parity and cross-platform lifecycle gates through the external artifact.
  - Evidence (2026-07-28, complete): `TestParity_*` with deterministic `ScriptedStdioAgent` (not live Cursor CLI); instance-owned `ExecutableCache`; native Windows descendant cleanup and Unix process-group cleanup. Local `make parity-cursorcliacp-plugin` PASS. GitHub Actions `ACP process-tree` run `30382805122` PASS on Windows, Ubuntu, and macOS for SHA `e796998cc8f7095bce65e450211cb1aef3b76def`, including native process-tree and Cursor connector jobs. Resolved blocker retained as deprecated historical evidence in `phase6-task63-macos-process-tree-blocker.md`.
  - Historical note (2026-07-27): no local macOS host was available. Superseded by exact-SHA three-OS `ACP process-tree` run `30382805122` PASS on 2026-07-28.
  - _Requirements: 1.6, 7.8, 8.2, 10.1, 10.2, 10.5, 10.8, 11.4, 12.6_
  - _Boundary: External backend plugin module_
  - _Depends: 6.1, 6.2_
  - _Validation: `(cd connectors/cursorcliacp && GOWORK=off go test -race ./...) && make parity-cursorcliacp-plugin`_

- [x] 6.4 Implement and validate Gemini CLI and Agy CLI ACP plugins
  - Write connector-local TDD suites for the existing `geminicliacp` and `agycliacp` factory kinds, authentication differences, model profiles, commands, environment policy, inventory, cancellation, and local-only security.
  - Reuse only the stable ACP support contract; keep each product's launch and auth behavior inside its module.
  - Produce independent manifests and package artifacts.
  - Observable completion: both existing kinds resolve dynamically and pass their accepted parity, lifecycle, and packaging gates.
  - Evidence (2026-07-20): gemini/agy `TestParity_*` + conformance with scripted ACP stdio; inventory max honored; `make parity-cli-acp-plugins`.
  - _Requirements: 1.6, 7.8, 8.2, 10.1, 10.2, 10.5, 10.8, 11.4, 12.6_
  - _Boundary: External backend plugin modules_
  - _Depends: 6.1_
  - _Validation: `(cd connectors/geminicliacp && GOWORK=off go test ./...) && (cd connectors/agycliacp && GOWORK=off go test ./...) && make parity-cli-acp-plugins`_

- [x] 6.5 Remove root ACP product registrations after atomic cutover
  - Add failing root tests asserting `acp`, `cursorcliacp`, `geminicliacp`, and `agycliacp` are absent from static tables and root mandatory requirements.
  - Delete concrete ACP product packages or move remaining source fully under their external modules.
  - Run minimal distribution and configured-missing-plugin tests to prove absence tolerance and actionable errors.
  - Observable completion: the root module builds and tests with all ACP connector module directories unavailable, while curated packages continue to install them.
  - _Requirements: 1.2, 1.5, 10.1, 10.5, 10.8, 10.9, 11.2_
  - _Boundary: Root composition cleanup_
  - _Depends: 6.2, 6.3, 6.4_
  - _Validation: `GOWORK=off go test ./... && GOWORK=off go build ./cmd/lipstd && make package-plugin-smoke`_

## Phase 7: Migrate OpenAI-Compatible Provider and Local Runtime Families

- [x] 7. Externalize provider subclasses and local OpenAI-compatible runtimes
  - _Notes (2026-07-20):_ Repair pass after honesty audit. Validated: module `GOWORK=off go test ./...`, `make parity-openrouter-plugin parity-hosted-compatible-plugins parity-ollama-plugins parity-local-compatible-plugins`, Windows `TestPhase7_OpenRouter*` BuildBootstrap e2e (repeated), `make backend-plugin-absence-checks`, archtest `Phase7_|OpenRouter`. Residual: `internal/core/identity` OpenRouter policy schema + `internal/plugins/openrouterwire` frontend extension capture remain for canonical wire compatibility (not connector factories). Phase 6 macOS process-tree CI evidence still external. Phase 8 not started.

- [x] 7.1 Move OpenRouter to an independent external connector
  - Write differential tests for current OpenRouter headers, identity attribution, API key handling, chat and Responses behavior, extra body, model inventory, provider errors, capabilities, route prefixes, and billing or usage evidence.
  - Extract a stable provider-neutral OpenAI mapping support surface where justified; do not let the external module import the existing internal helper.
  - Preserve factory kind `openrouter` and remove its static registration only after parity and packaging pass.
  - Observable completion: OpenRouter-specific behavior exists only in its connector module and the root essential bundle has no OpenRouter import or dependency.
  - _Notes:_ FinalizeBilling capability removed (fail-closed; not advertised). Connector-local attribution modes cover identity headers. Production Windows BuildBootstrap e2e stages real `lip-backend-openrouter` against deterministic HTTP emulator.
  - _Requirements: 1.5, 8.2, 10.1, 10.2, 10.3, 10.7, 10.8, 11.1, 12.6_
  - _Boundary: External backend plugin and connector-support modules_
  - _Depends: 5.2, 5.3_
  - _Validation: `(cd connectors/openrouter && GOWORK=off go test ./...) && make parity-openrouter-plugin && go test ./internal/archtest -run OpenRouter`_

- [x] 7.2 Move NVIDIA and Hugging Face connectors
  - Write connector-local tests preserving API key defaults, endpoints, model inventory, transport restrictions, parameter remapping, headers, capabilities, and canonical stream behavior.
  - Consume only the public provider-neutral mapping support proven by OpenRouter migration.
  - Cut over `nvidia` and `huggingface` kinds independently after parity.
  - Observable completion: both modules release independently and root dependency graphs contain neither provider's connector code.
  - _Notes:_ HF `?provider=` route param restored via Invocation SafeMetadata `route.param.provider`. NVIDIA remaps + bounded `nvidia.extra_body.` / `openai.extra_body.` via support helpers. Inventory errors propagate.
  - _Requirements: 1.5, 8.2, 10.1, 10.2, 10.8, 11.1, 12.6_
  - _Boundary: External backend plugin modules_
  - _Depends: 7.1_
  - _Validation: `(cd connectors/nvidia && GOWORK=off go test ./...) && (cd connectors/huggingface && GOWORK=off go test ./...) && make parity-hosted-compatible-plugins`_

- [x] 7.3 Move Ollama and Ollama Cloud connectors
  - Write tests for existing local and cloud factory kinds, endpoint defaults, authentication, model discovery, capabilities, usage, stream cancellation, and access scope.
  - Package local and cloud exports in one artifact only if shared process and dependency ownership is explicit; otherwise use separate modules.
  - Preserve `ollama` and `ollama-cloud` kinds through dynamic registration.
  - Observable completion: both configurations work with no root static registration and an absent local Ollama installation does not affect unrelated startup.
  - _Notes:_ Single module, dual exports, per-instance sharing. Local: localhost + dummy key + LocalOnly. Cloud: ollama.com defaults + required api_key + tags URL; no localhost/dummy. Caps via `/api/show` for local.
  - _Requirements: 4.5, 8.2, 10.1, 10.2, 10.8, 11.1, 11.8, 12.6_
  - _Boundary: External backend plugin module or modules_
  - _Depends: 5.3_
  - _Validation: `(cd connectors/ollama && GOWORK=off go test ./...) && make parity-ollama-plugins`_

- [x] 7.4 Move llama.cpp, LM Studio, and vLLM connectors
  - Write TDD suites for each existing kind, endpoint and model discovery behavior, compatible transport differences, capabilities, cancellation, and static or dynamic inventory.
  - Create independent artifacts or a carefully justified compatible-runtime artifact with multiple exported kinds and explicit process sharing.
  - Preserve `llamacpp`, `lmstudio`, and `vllm` configuration and route semantics.
  - Observable completion: all three kinds resolve from manifests and pass current accepted parity without appearing in root registration code.
  - _Notes:_ Independent modules; chat-only transport; LocalOnly; distinct DefaultURL; inventory errors propagate; scaffold `if true` dead code removed.
  - _Requirements: 2.7, 8.2, 10.1, 10.2, 10.8, 11.1, 12.6_
  - _Boundary: External backend plugin modules_
  - _Depends: 5.3_
  - _Validation: `make test-local-compatible-plugin-modules parity-local-compatible-plugins`_

- [x] 7.5 Delete migrated provider and local-runtime static entries
  - Add failing architecture assertions that OpenRouter, NVIDIA, Hugging Face, Ollama, Ollama Cloud, llama.cpp, LM Studio, and vLLM cannot reappear in the essential bundle or mandatory list.
  - Remove obsolete root wrappers, provider-specific key fields used only by migrated connectors, and internal support packages after all consumers use public support modules.
  - Re-run minimal, full-package, and root-module isolation gates.
  - Observable completion: root build and test succeed after migrated connector directories and package artifacts are removed.
  - _Notes:_ `DefaultWireModel` migrated cases removed; archtests forbid reintroduction + non-OR `openrouter.*` coupling. Scaffold tool deleted. Absence + package-full metadata OK. Residual identity OpenRouter YAML schema / frontend `openrouterwire` capture kept for wire compatibility (not static connector registration).
  - _Requirements: 1.2, 1.5, 10.1, 10.8, 10.9, 11.2, 12.4_
  - _Boundary: Root composition and dependency cleanup_
  - _Depends: 7.1, 7.2, 7.3, 7.4_
  - _Validation: `GOWORK=off make quality-checks && make backend-plugin-absence-checks`_

## Phase 8: Migrate OpenCode, Codex, and Cursor SDK Boundaries

- [x] 8. Remove the remaining connector-specific core and factory dependencies

- [x] 8.1 Move OpenCode Go and Zen with vendor-resolution ownership
  - Write tests preserving both factory kinds, model vendor resolution, inventory, credentials, and canonical behavior; select shared-artifact process semantics only if module-local tests prove configuration, secret, concurrency, and failure isolation, otherwise declare per-instance processes.
  - Move vendor-resolution, provider metadata, caching, and fallback behavior into the OpenCode connector module.
  - Remove OpenCode-specific resolver fields and root key defaults after external cutover.
  - Observable completion: `opencode-go` and `opencode-zen` run from an external artifact and generic factory dependencies no longer know OpenCode concepts.
  - Evidence (2026-07-20T03:24:00+02:00): `connectors/opencode` closed artifact exports both kinds (`per_instance`); module-local vendor/inventory/execute/isolation parity; root static OpenCode packages deleted; `StandardDistributionRequirements` excludes `opencode-go`/`opencode-zen`; Windows `TestPhase8_OpenCode*` BuildBootstrap e2e (`-count=3`); `make parity-opencode-plugins` repeated; archtest `OpenCode|Phase8_`; package index includes `io.golip.backend.opencode`; `lipstd check-config` on dogfood example; absence scripts cover Phase8 root isolation. The former Phase 6.3 blocker was resolved by run `30382805122`.
  - _Requirements: 1.5, 2.7, 8.2, 9.8, 10.1, 10.2, 10.3, 10.8, 11.1_
  - _Boundary: External backend plugin module and registry cleanup_
  - _Depends: 4.4, 5.3_
  - _Validation: `(cd connectors/opencode && GOWORK=off go test ./...) && make parity-opencode-plugins && go test ./internal/archtest -run OpenCode`_

- [x] 8.2 Move OpenAI Codex and Codex App Server with their model catalog
  - Write differential tests for current factory kinds, credential and local-only posture, model catalog discovery and fallback, inventory provenance, app-server lifecycle, stream mapping, and capability declarations.
  - Move `internal/core/codexcatalog` into the Codex connector module or independent Codex support module with no core imports.
  - Remove Codex-specific fields from `BackendFactoryDeps`, root config comments, startup discovery, and standard key resolution after cutover.
  - Observable completion: both Codex kinds share catalog behavior inside their external boundary and core has no Codex package or type.
  - Evidence (2026-07-28, complete): connector-local catalog/token estimation, real plugin-host HTTP + app-server execution, fake Codex CLI process-tree cleanup, full module/archtest/runtime parity, and removal of root Codex catalog ownership remain green. Local `make parity-codex-plugins` PASS. GitHub Actions `Codex connector race` run `30382811954` PASS on Ubuntu for SHA `e796998cc8f7095bce65e450211cb1aef3b76def` using `GOWORK=off go test -race ./...`. Resolved blocker retained as deprecated historical evidence in `phase8-task82-linux-race-blocker.md`.
  - _Requirements: 1.1, 1.5, 7.8, 8.2, 9.8, 10.1, 10.2, 10.3, 10.8, 11.1_
  - _Boundary: External backend plugin module and core cleanup_
  - _Depends: 4.4, 5.3_
  - _Validation: `(cd connectors/codex && GOWORK=off go test -race ./...) && make parity-codex-plugins && go test ./internal/archtest -run Codex`_

- [x] 8.3 Revalidate and update the active Cursor SDK backend specification
  - Add a spec-level validation test or review checklist proving the Cursor SDK Go wrapper and project-owned Node bridge belong inside an external `cursorsdk` connector artifact, not `internal/plugins/backends` or the root module.
  - Update its requirements, research, design, tasks, file plan, packaging, configuration, lifecycle, and architecture tests to consume the backend plugin SDK and closed manifest model, declare its shared or per-instance process model, and use the approved secure local channel and exact-executable launch contracts.
  - Preserve its Cursor-specific bridge, agent pool, canonical history, safety, and evidence decisions while removing any root Node or SDK dependency.
  - Observable completion: the Cursor SDK spec passes design validation against this architecture and is blocked from root-tree implementation.
  - Evidence (2026-07-20T10:42:51+02:00): Revalidated `.kiro/specs/archive/cursor-sdk-backend/` for external `connectors/cursorsdk` + `bridge-node` private companion; public `pkg/lipsdk/backendplugin` ABI; closed `golip.backendplugin.manifest/v1`; `per_instance` with secret/concurrency/failure isolation justification; digest-bound exact executable + approved secure local IPC + lazy activation; forbidden root paths listed; research Recommended Design Direction withdraws old `internal/plugins/backends/cursorsdk` path. Added `AGENTS.md`, `file-plan.md`, `packaging.md`, `validation-checklist.md`; `make kiro-spec-check SPEC=cursor-sdk-backend` (`scripts/kiro-spec-check.{ps1,sh}` + `tools/kiro/speccheck`); archtest `TestCursorSDK_*` absence gates. Subsequent implementation and release evidence closed Phase 8 on 2026-07-28.
  - _Requirements: 1.6, 3.4, 4.3, 4.4, 4.8, 7.2, 7.3, 7.4, 8.8, 10.1, 11.8, 12.10_
  - _Boundary: Kiro specification and external connector design_
  - _Depends: 5.4_
  - _Validation: `make kiro-spec-check SPEC=cursor-sdk-backend`_

- [x] 8.4 Remove the final optional fixed registration table and compatibility scaffolding
  - Write failing tests asserting the standard source tree contains no fixed non-essential backend kind list, generated import list, blank-import list, build-tag list, or compatibility switch.
  - Delete the migration-only static bundle after all current kinds have external replacements.
  - Keep essential registration explicit and external registration manifest-driven.
  - Observable completion: adding or removing an optional connector artifact requires no root Go source edit and no standard binary rebuild when protocol-compatible.
  - Evidence (2026-07-26T14:05:44+02:00): Migration scaffolding gone (`migration_bundle.go`/`migration_deps.go`); `StandardBackendBundle` == essential allowlist only; archtests `NoFixedOptional*|EssentialOnly*|Dynamic*` pass (repeated); registry provenance (`BackendSourceBuiltin`/`Discovered`) fixes inspect self-`builtin_collision`; staged OpenRouter/OpenCode/Codex inspect tests + real collision diagnosis; `go build ./cmd/lipstd` + `lipstd inspect` (reference config, essentials only); package tests `runtimebundle`/`standardplugins`/`pluginreg`/`cmd/lipstd`; `make backend-plugin-absence-checks`; `go vet`/`go mod tidy`/`git diff --check` clean on touched paths. Subsequent exact-SHA CI evidence closed the former Phase 6.3 and 8.2 blockers.
  - _Requirements: 1.5, 3.5, 10.1, 10.8, 11.7, 11.9, 12.4_
  - _Boundary: Composition root cleanup_
  - _Depends: 6.5, 7.5, 8.1, 8.2_
  - _Validation: `go test ./internal/archtest ./internal/standardplugins ./internal/pluginreg -run 'NoFixedOptional|EssentialOnly|Dynamic'`_

- [x] 8.5 Prove root isolation after complete connector removal
  - Build a CI fixture that copies only the root module while excluding `connectors`, connector-support modules, Node packages, and plugin artifacts.
  - Run root format, vet, unit, architecture, quality, and standard binary build with `GOWORK=off`.
  - Separately install selected released plugin artifacts and run mixed built-in/external smoke tests against the unchanged binary.
  - Observable completion: the isolated root succeeds alone, and the same binary gains optional kinds solely by installing manifests and artifacts.
  - Evidence (2026-07-26T14:21:34+02:00): `make isolated-root-qa` (temp copy excludes connectors/connector-support/Node/artifacts; GOWORK=off gofmt/vet/safe unit+archtest/`go build ./cmd/lipstd`; no connector imports in production) and `make installed-plugin-smoke` (one lipstd hash; release.yaml structural select stub+multi-export; same-binary inspect/doctor/check-config/serve invoke; remove stub keeps multi + essentials) both pass repeated; archtest `Phase85_`; CI qa.yml steps added without duplicating full QA; `make backend-plugin-absence-checks` OK. Parent Phase 8 unchecked; 8.2 Linux-race + Phase 6.3 macOS blockers untouched; Phase 9 not started.
  - _Requirements: 1.2, 1.5, 1.6, 10.9, 11.2, 11.3, 11.6, 11.8, 11.9, 12.5_
  - _Boundary: CI and release verification_
  - _Depends: 8.4_
  - _Validation: `make isolated-root-qa installed-plugin-smoke`_

## Phase 9: Finalize Security, Cross-Platform Release, and Documentation

- [x] 9. Complete release-grade validation and architecture documentation

- [x] 9.1 Update ADRs, steering, package maps, and architecture knowledge
  - Add a superseding ADR that records hybrid composition, executable gRPC plugins, rejection of Go native plugins, closed manifest schema, digest-bound exact-executable launch, approved secure local IPC, lazy activation, declared process models, and independent modules.
  - Update `AGENTS.md`, `.kiro/steering/{structure,tech,testing}.md`, architecture docs, backend adapter boundaries, and EchoesVault plugin/package pages.
  - Add architecture tests or documentation links that prevent stale claims of static-only backend plugins.
  - Observable completion: all authoritative architecture sources describe one consistent final boundary and no source instructs maintainers to add optional connectors to a fixed table.
  - Evidence (2026-07-26T14:31:43+02:00): ADR `docs/adr/0008-hybrid-backend-connector-plugins.md` supersedes ADR 0001 for optional backends; AGENTS/steering/architecture/backend-adapter-boundaries/README/EchoesVault (plugin-system, package-map, architecture-overview, backend-connector-plugins, codex-app-server-backend) describe hybrid essential+executable gRPC connectors; stale in-tree optional paths and root env-pool notes closed; `make docs-check knowledge-check` + `go test ./internal/archtest` (incl. `Phase91_`) pass; `git diff --check` clean. Parent Phase 9 unchecked; 9.2+ not started; 8.2 Linux-race + Phase 6.3 macOS blockers untouched.
  - _Requirements: 1.4, 3.4, 4.3, 7.1, 7.2, 7.3, 11.10, 12.9_
  - _Boundary: Architecture documentation_
  - _Depends: 8.4_
  - _Validation: `make docs-check knowledge-check && go test ./internal/archtest`_

- [x] 9.2 Publish operator installation, trust, diagnostics, upgrade, and rollback guides
  - Document minimal and curated-full layouts, trusted directories, closed manifest behavior, digest-bound launch and private staging, approved platform IPC profiles, peer-authentication failures, development mode, configured-missing behavior, inspect/doctor output, secret posture, local-only connectors, compatibility, upgrades, rollback, and troubleshooting.
  - Include platform-specific installation directories and executable permission guidance without adding runtime download behavior.
  - Validate every configuration example through tests.
  - Observable completion: an operator can install, inspect, activate, upgrade, roll back, and remove one plugin without rebuilding Go-LIP or exposing secrets.
  - Evidence (2026-07-26T14:41:23+02:00): `docs/backend-plugins/operator.md` + `examples/operator/*` + `config/examples/plugin-operator-{minimal,full-discovery,upgrade,rollback}.yaml`; `make example-config-check` (docs operator/example tests + `TestConfigExamples_passBootstrapInspect`) and `make docs-check` and `make package-plugin-smoke` pass repeated; README/EchoesVault/dogfood/authoring links; trust posture notes process isolation ≠ sandbox without claiming 9.3 complete; `git diff --check` clean. Parent Phase 9 unchecked; 9.3+ not started; 8.2 Linux-race + Phase 6.3 macOS blockers untouched.
  - _Requirements: 3.4, 7.1, 7.2, 7.3, 7.5, 7.6, 8.5, 8.6, 11.4, 11.6, 11.9, 12.9_
  - _Boundary: Operator documentation and example tests_
  - _Depends: 5.3, 8.5_
  - _Validation: `make example-config-check docs-check package-plugin-smoke`_

- [x] 9.3 Perform a dedicated executable-plugin threat model and hardening pass
  - Review trusted-path assumptions, strict manifest evolution, symlink and replacement races, atomic exact-executable binding, digest staging and cleanup, local endpoint exposure, OS peer credentials, named-pipe ACLs, mutual TLS fallback, unauthorized and stale-generation clients, environment inheritance, file descriptors, descendant processes, stderr injection, resource exhaustion, secret transport, development overrides, and plugin-originated canonical events.
  - Add adversarial tests for every accepted control, including proving that an untrusted local client cannot authenticate, invoke configure, or obtain credential-bearing responses.
  - Document explicitly that process isolation is not a malicious-code sandbox and define the trust equivalence of installed plugins.
  - Observable completion: security review has no unresolved P0/P1 issue and all accepted controls have executable tests or explicit platform evidence.
  - Evidence (2026-07-26T15:09:16+02:00): P1 repair on audit findings — shared `internal/infra/diagredact` (redact-before-truncate, `[redacted]`, recognized credential formats + control stripping); adapter+doctor wired; stderr isolation adversarial via processhost `TestLauncher`/`LastProcess.Stderr` + adapter drain; `make backend-plugin-security-checks` now executes `runtimebundle` `TestBuild_localOnly|…Credential…` (+ diagredact). Local re-audit: no remaining local P0/P1. **Unchecked** only for external Linux race/security CI observation and Darwin peer-cred (fail-closed); blocker `phase9-task93-external-security-blocker.md`. Parent Phase 9 unchecked; 9.4+ not started; 8.2 Linux-race + Phase 6.3 macOS blockers untouched.
  - Evidence (2026-07-28, complete): local `make backend-plugin-security-checks` PASS after fail-closed unmapped ABI-part handling and cancellation-safe shared Execute pump integration. `ACP process-tree` run `30382805122`, `Backend plugin cross-platform` run `30382807401`, `Codex connector race` run `30382811954`, and Ubuntu `Backend plugin release gates` run `30382809655` all PASS on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`. Darwin connector IPC remains deliberately unsupported/fail-closed and no manifest claims Darwin runtime support. Resolved blocker retained as deprecated historical evidence.
  - _Requirements: 3.4, 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 7.7, 7.8, 7.9, 7.10, 12.2_
  - _Boundary: Security review and tests_
  - _Depends: 3.1, 3.2, 3.4, 5.3_
  - _Validation: `make backend-plugin-security-checks test-fuzz test-race`_

- [x] 9.4 Validate cross-platform lifecycle, packaging, exact-executable launch, and secure IPC
  - Run Linux-amd64/arm64, macOS-amd64/arm64, and Windows-amd64/arm64 native or architecture-appropriate compile/package gates, with native runtime tests for discovery paths, strict manifests, descriptor-bound or protected-staging launch, digest checks, unauthorized peer rejection, approved local transport, configuration secrecy, streaming, cancellation, process trees, hard kill, reaping, file replacement, upgrade, rollback, and uninstall wherever runners exist.
  - Validate each first-party artifact's declared platform matrix and reject false manifest claims or platforms that cannot satisfy the required launch and local-channel profiles.
  - Record unsupported connector-platform pairs without making the root binary depend on them.
  - Observable completion: supported platform matrices match release artifacts, the launched bytes are the accepted bytes, unauthorized local peers cannot configure plugins, and no process, channel, staged artifact, or locked source artifact survives tested shutdown and upgrade paths.
  - Evidence (2026-07-28, complete): local `make backend-plugin-cross-platform-qa` PASS; all claimed Linux/Windows amd64/arm64 connector targets compile and package matrices match; Darwin stays explicitly unsupported/fail-closed with zero false manifest claims. GitHub Actions `Backend plugin cross-platform` run `30382807401` PASS on Ubuntu, macOS, and Windows for SHA `e796998cc8f7095bce65e450211cb1aef3b76def`. ACP native process-tree run `30382805122` also PASS on all three OS runners. Resolved blocker retained as deprecated historical evidence.
  - Historical note (2026-07-27): no local macOS host was available. Superseded by exact-SHA Ubuntu/macOS/Windows `Backend plugin cross-platform` run `30382807401` PASS on 2026-07-28.
  - _Requirements: 3.3, 3.4, 4.5, 5.4, 7.2, 7.3, 7.6, 11.4, 11.8, 11.9, 11.11, 12.2, 12.5, 12.11_
  - _Boundary: Cross-platform QA and packaging_
  - _Depends: 5.3, 6.5, 7.5, 8.5_
  - _Validation: `make backend-plugin-cross-platform-qa`_

- [x] 9.5 Run final conformance, scale, race, leak, architecture, security, and release gates
  - Run every connector module against the conformance suite using only advertised capabilities.
  - Run root isolation, one-hundred-manifest discovery, unknown-field rejection, exact-executable substitution, unauthorized peer, mixed built-in/external routing, pre/post-output failure, strict accounting, model inventory refresh, race, leak, fuzz, security, package, upgrade, and rollback suites.
  - Confirm the PR or release contains no optional connector source or dependency in the root module and no fixed optional registration list.
  - Observable completion: `make qa` plus backend-plugin release gates pass, and the final traceability review maps every requirement to passing evidence.
  - Evidence (2026-07-28, complete): local full `make backend-plugin-release-gates` PASS across 17 structurally discovered modules (GOWORK=off list/vet/tidy/test/build, advertised-capability conformance, root isolation, security, packaging, installed-plugin smoke, deterministic 116-criterion traceability); `make quality-checks` and `make parity-checks` PASS. GitHub Actions Ubuntu `Backend plugin release gates` run `30382809655` PASS on SHA `e796998cc8f7095bce65e450211cb1aef3b76def`; cross-platform run `30382807401`, ACP process-tree run `30382805122`, and Codex Linux race run `30382811954` also PASS on that SHA. Resolved blocker retained as deprecated historical evidence.
  - Historical note (2026-07-27): no local macOS host was available. Superseded by exact-SHA Ubuntu release-gates run `30382809655` plus three-OS cross-platform run `30382807401`, three-OS ACP run `30382805122`, and Ubuntu Codex race run `30382811954`, all PASS on 2026-07-28.
  - _Requirements: 1.7, 3.4, 4.3, 6.8, 7.2, 7.3, 7.6, 10.8, 10.9, 11.2, 11.7, 12.1, 12.2, 12.3, 12.4, 12.5, 12.6, 12.7, 12.8, 12.9, 12.10, 12.11_
  - _Boundary: Release validation_
  - _Depends: 9.1, 9.2, 9.3, 9.4_
  - _Validation: `make qa backend-plugin-release-gates`_
