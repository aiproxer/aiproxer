# Requirements Document

## Project Description (Input)
Extract the Cursor SDK integration into a standalone, independently released executable backend plugin so building, testing, and running the standard Go-LIP distribution require neither Node nor npm. Move the Cursor-specific Go connector, JavaScript SDK bridge, instrumentation, fixtures, tests, packaging, and CI into a separate repository, retaining the existing manifest-driven backend-plugin ABI and an unchanged host binary.

The plugin owns the JavaScript SDK bridge and the JavaScript runtime it ships. Evaluate plugin packaging so operators do not need a system-wide Node installation, and do not redistribute the proprietary Cursor SDK: operators provision it themselves against the shipped runtime. Preserve existing SDK semantics, streaming, cancellation, process supervision and cleanup, credential redaction, and SDK contract evidence. Keep generic orchestration, routing, B2BUA continuity, discovery, and diagnostics contracts in Go-LIP. Remove sibling-directory dependency assumptions and establish independently consumable public contracts and compatibility verification before removing the in-tree connector.

## Introduction

Make the standard Go-LIP repository and distribution independent of Cursor's JavaScript toolchain while preserving Cursor SDK support as an optional, separately maintained product integration. Repository extraction must preserve the current integration's externally observable behavior rather than become an SDK migration.

## Boundary Context

- **In scope:** independent Cursor SDK plugin source, releases, installation, diagnostics, dependency maintenance, compatibility evidence, and migration from the in-tree integration; removal of Cursor-specific build and CI coupling from Go-LIP.
- **Out of scope:** rewriting the Cursor SDK in Go; upgrading the SDK as part of extraction; relocating other connectors; changing frontend protocols, routing, billing, or continuity policy; introducing a new plugin mechanism.
- **Adjacent expectations:** Go-LIP retains its existing executable-plugin support and public canonical contracts. Cursor credentials and provider availability remain prerequisites for live provider operations. An optional plugin may contain a JavaScript runtime even though the standard host does not require one.
- **Packaging expectation:** evaluate self-contained packaging and record a tested release decision. Extraction does not assert that the SDK can run without a JavaScript runtime or prematurely promise a single-file executable.
- **Redistribution posture:** the Cursor SDK is proprietary and is **not** redistributed by the plugin archive. Operators provision it out of band using the shipped private runtime, accepting Cursor's terms themselves. The archive ships no Cursor SDK code and none of its dependency closure. It does ship the pinned JavaScript runtime (MIT) together with that runtime's own bundled npm, which is separately licensed (npm itself is Artistic-2.0, and the packages npm bundles are licensed on their respective terms), so each shipped component must be attributed to its own license text rather than to the runtime's.
- **Migration baseline:** SDK 1.0.23 with the Undici 6.28.1 security override; any later SDK migration is a separately reviewed change.

## Requirements

### Requirement 1: Node-independent standard host
**Objective:** As a Go-LIP developer or operator, I want the standard host to work without the Cursor toolchain, so that an optional provider does not add development or runtime prerequisites.

#### Acceptance Criteria
1. When a developer builds the standard distribution from a clean Go-LIP checkout without Node or npm installed, the Go-LIP build shall succeed with the documented non-Cursor prerequisites.
2. When a developer runs the default unit, quality, and comprehensive verification commands without Node or npm installed, the Go-LIP verification tooling shall complete without invoking either tool or requiring Cursor plugin source.
3. When an operator starts the standard distribution without the Cursor plugin installed or configured, Go-LIP shall operate its configured non-Cursor backends without checking for a JavaScript runtime.
4. The Go-LIP repository shall exclude active Cursor SDK implementation source, npm dependency manifests, and Cursor-specific build, release, and dependency-update jobs after migration.

### Requirement 2: Independently maintained and released integration
**Objective:** As a plugin maintainer, I want a standalone Cursor integration, so that its dependencies and releases evolve independently of Go-LIP.

#### Acceptance Criteria
1. The Cursor SDK plugin shall provide its own source repository containing its connector, SDK bridge, Cursor-specific instrumentation, fixtures, tests, operator documentation, and release tooling.
2. When a maintainer builds or verifies the Cursor SDK plugin from a clean checkout, the plugin tooling shall consume published, explicitly versioned host contracts without requiring a sibling Go-LIP checkout or unpublished host implementation packages.
3. When a maintainer releases a compatible Cursor SDK plugin version, the plugin release process shall produce installable artifacts without requiring a simultaneous Go-LIP release.
4. The Cursor SDK plugin release metadata shall identify the plugin version, supported host compatibility, required SDK version, JavaScript runtime requirements, provisioning expectations, and artifact checksums.
5. The Cursor SDK plugin maintenance workflow shall own SDK and JavaScript dependency security verification independently of Go-LIP's standard verification workflow.

### Requirement 3: Optional installation and explicit compatibility
**Objective:** As an operator, I want to install and upgrade Cursor support separately, so that the standard host binary remains unchanged.

#### Acceptance Criteria
1. When an operator installs a trusted compatible Cursor SDK plugin through the existing plugin installation mechanism, Go-LIP shall discover and use Cursor support without recompiling or replacing the host binary.
2. When an operator inspects installed plugins without configuring a Cursor instance, Go-LIP shall report the plugin's declared metadata without starting the Cursor SDK runtime.
3. If an installed plugin is untrusted or incompatible with the host's supported plugin contract, Go-LIP shall reject its use with an explicit diagnostic before executing provider work.
4. If Cursor support is configured but the plugin, its required runtime resources, or its provisioned SDK are unavailable, Go-LIP and the plugin shall report the missing prerequisite with the exact remediation, without automatically installing packages or falling back to another Cursor integration.
5. When maintainers choose the plugin release packaging, the plugin project shall publish tested installation and provisioning instructions for each supported operating system, explicitly stating whether system Node is required and how the Cursor SDK is obtained. Those instructions shall also record the platform-specific companion-path contract: on Windows the operator shall supply the full `bridge_executable` path of the installed private bridge launcher, because the host launches a digest-addressed staging copy of the plugin executable and an executable-relative default therefore cannot reach the installed launcher; on Linux the executable-relative default remains supported and an explicit override is optional. The plugin shall not silently rewrite, search for, or auto-provision that path on either platform, and an unset Windows path shall fail with remediation naming `bridge_executable`.
6. The released plugin archive shall not contain the Cursor SDK or its dependency closure, and the shipped provenance shall not assert a redistribution right for them.

### Requirement 4: Preserve provider and stream semantics
**Objective:** As a Cursor user, I want extraction to preserve the integration's behavior, so that repository separation does not change my workloads.

#### Acceptance Criteria
1. When the extracted plugin executes existing supported Cursor requests, the plugin shall preserve model discovery, configuration validation, text and reasoning events, tool activity, warnings, and terminal usage semantics represented by the pre-extraction contract evidence.
2. When a request requires a capability unavailable in the installed plugin or SDK, the integration shall return an explicit capability error without silently dropping the required behavior.
3. When a Cursor run completes, fails, or is cancelled, the plugin shall preserve ordered events and a single terminal outcome for that run.
4. When the plugin executes session reuse and concurrency scenarios, the plugin shall preserve the existing isolation and configured resource-limit behavior.
5. While an attempt has emitted downstream content, Go-LIP shall preserve its prohibition on transparent retry or failover; extracting the plugin shall not introduce provider-local transparent replay after that commitment.

### Requirement 5: Preserve lifecycle and secret-safe diagnostics
**Objective:** As an operator, I want extraction to retain bounded cleanup and safe diagnostics, so that optional plugin failures do not compromise the host or expose credentials.

#### Acceptance Criteria
1. When a run is cancelled or the host shuts down, the integration shall preserve its configured cancellation and shutdown deadlines and release owned plugin resources within the existing bounded cleanup policy.
2. If the SDK runtime exits or is replaced, the plugin shall fail affected work explicitly and prevent stale run or cancellation messages from affecting a newer runtime generation.
3. When the integration emits diagnostics or errors, the integration shall retain existing credential redaction and bounded diagnostic output without exposing credentials through process arguments or retained logs.
4. When an operator queries the extracted plugin's health and diagnostics, the integration shall retain the existing supported readiness, runtime-version, and failure information without requiring a new Cursor-specific branch in the host.
5. Where sandboxing is required by configuration, the plugin shall reject execution if verified sandbox support is unavailable.

### Requirement 6: Verified migration and independent compatibility evidence
**Objective:** As a maintainer or existing operator, I want a verified cutover, so that removing the in-tree connector does not strand installations or lose regression evidence.

#### Acceptance Criteria
1. Before removing the in-tree Cursor SDK implementation, the migration process shall establish a buildable standalone plugin and verify its compatibility with a released host contract baseline.
2. When maintainers certify the extracted integration, the certification shall preserve SDK contract fixtures and cover existing bridge tests, plugin contracts, lifecycle regressions, and platform smoke on each supported operating system.
3. When maintainers certify Go-LIP's independence after extraction, the certification shall demonstrate its standard build and verification commands in an environment where Node and npm are unavailable.
4. When an existing operator migrates to the standalone plugin, the migration documentation shall identify installation steps, supported configuration continuity, changed executable paths, and any required adjustments without assuming an automatic upgrade.
5. If an operator needs to roll back a plugin update, the plugin documentation shall identify a compatible prior artifact and the configuration steps needed to restore it without an SDK downgrade being performed implicitly.
6. The migration process shall retain historical specifications and provenance while replacing active in-tree build and installation instructions with the standalone plugin's documented location.
