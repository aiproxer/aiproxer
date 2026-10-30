# Requirements Document

## Introduction

AIProxer shall restore and consistently enforce the intended static API-key contract across **all eligible built-in, compatible/profile and executable backends**. Environment variables are the sole source of provider API-key values; proxy and connector configuration files contain no provider API-key values or fields that accept them. A provider has a stable, unique nonsecret environment root, with a default 20-slot numbered search and ordered, quota-aware, pre-output-only credential rotation. This is a **brownfield** repair of the existing implementation.

The preceding audit identified omitted external provisioning, incompatible YAML precedence, hard-coded limits/gap stopping, missing rotation in compatible families, 429 threshold violations, a fast-wire replay discrepancy, OpenCode file fallback, shared CommandCode root, and Ollama Cloud manifest inconsistency. All identified defects are **in scope**, not deferred.

## V1 Slice

- **Delivers:** An environment-only, host-enforced static API-key contract for all eligible backends, plus consistent key selection and safe failure behavior on both canonical and supported optimized paths.
- **First consumer:** The existing bundled API backends and manifest-discovered inference connectors installed by stock lipstd.
- **Safe degradation:** Missing/malformed credentials, unauthorized connector posture, invalid YAML secret-bearing fields or unsafe replay fail closed with bounded, secret-free errors. An exhausted pool returns a classified pre-output failure to core routing, never silently disables a backend or exposes secrets.
- **Delivery:** This is a contract-completion umbrella implemented in independently mergeable, small code slices (see tasks). All discovered discrepancies are acceptance-critical before the umbrella closes; the entire umbrella is **not** one implementation PR.

## Boundary Context

- **In scope:** Static API-key and functionally equivalent operator-owned API-token/PAT modes; optional static auth to local compatible servers; existing in-process essentials; generic compatible backends and provider profiles; external executable inference connectors; Cursor SDK's API-key mode with session-side-effect safeguards; key-bearing static modes requiring secure source remediation; configuration examples/operator migration; manifest/SDK/host admission and architecture guards.
- **Not silently generalized:** OAuth-user/subscription refresh credentials, ACP credential lifecycles, cloud workload-signing/IAM chains and truly no-auth runtimes have distinct auth contracts. They must still reject **literal static secrets in AIProxer connector YAML**; they are not made interchangeable bearer-key pools.
- **Ownership:** The composition root owns host environment capture, admission, namespace registry and secret handoff. Backend/connector adapters own provider error classification and local pre-output key attempts. Core retains route failover, commitment, lineage and attempt ownership. The public SDK owns narrow provider-neutral executable connector contracts.
- **Precedence:** This user-directed prohibition on configuration-file API keys supersedes legacy YAML compatibility and archived porting-spec allowances.

## Requirements

### Requirement 1: Environment-only discovery and namespaces
**Objective:** As an operator, I want deterministic, provider-specific environment-key discovery, so one configuration can rotate multiple keys without storing secrets in files.

#### Acceptance Criteria
1. **1.1** When an API-key-backed instance is admitted, the system shall resolve its bare canonical environment root as slot 1 and roots suffixed _2 through _N in ascending numeric order, with a proxy-wide default N of 20 and an explicitly configurable bounded value.
2. **1.2** When some numbered environment variables are absent, the system shall continue scanning through N, trim and reject empty/invalid secret values, deduplicate equal secrets without changing first-occurrence priority, and never interpret a suffix as a new routable backend.
3. **1.3** When registering an API-key backend, the system shall require an explicit canonical nonsecret provider credential namespace, reject accidental root collisions, and permit only expressly declared same-provider sharing (such as OpenAI Chat/Responses).
4. **1.4** Where a custom compatible endpoint, provider profile or separate upstream deployment requires its own credential identity, the system shall permit a validated nonsecret environment-root reference or explicit namespace binding without accepting any key value in configuration or conflating routing prefixes with credential namespaces.
5. **1.5** If the global maximum, namespace binding, key count, secret size or environment-name syntax is invalid, the system shall reject candidate composition with a stable secret-free error instead of accepting partial or ambiguous credentials.

### Requirement 2: Credential source prohibition and secure delivery
**Objective:** As a security-conscious operator, I want every static provider credential to come from the trusted host environment, never from proxy/connector configuration or implicit connector credential files.

#### Acceptance Criteria
1. **2.1** When loading any backend/connector configuration, the system shall reject literal secret-bearing YAML/config keys, including legacy singular, list, structured, interpolated and nested variants, and shall remove static key/token/PAT/private-key values from the accepted config schemas rather than retaining deprecated secret fields.
2. **2.2** While a backend operates in static API-key/token mode, the system shall not read credentials from config files, dotenv files, auth.json, credential JSON paths or other connector-side filesystem fallbacks, and shall not use connector-local direct environment discovery in place of host-controlled acquisition.
3. **2.3** When configuring an admitted executable connector, the host shall acquire approved keys and deliver only the selected connector's bounded secret material through the existing peer-authenticated private Configure secret channel, never via launch environment, argv, plaintext YAML, inspect or unauthenticated discovery.
4. **2.4** Where a connector also supports OAuth, agent, no-auth or cloud workload identity, the system shall preserve those explicitly classified paths without mistaking their provider-managed auth stores for static API-key config; literal provider secrets in AIProxer YAML shall remain prohibited for **every** path.
5. **2.5** When keys, policy or bindings change during reload, the system shall validate and isolate the complete candidate snapshot before publication, leave the active generation untouched on failure, and ensure old in-flight attempts retain their admitted credentials and policy.

### Requirement 3: Sequential quota-aware selection and safe attempts
**Objective:** As an operator, I want the first key used until long-lived quota exhaustion or invalid authentication warrants advancing, without unexpectedly consuming secondary credentials for short transient throttles.

#### Acceptance Criteria
1. **3.1** While the primary credential is valid and not classified quota-exhausted, the system shall prefer it consistently; when a credential is proven unusable or quota-exhausted, it shall advance deterministically to the next eligible key, preserve nonsecret per-key availability state and allow recovered credentials to become eligible again.
2. **3.2** When a provider returns HTTP 429 (or its documented equivalent) with a valid Retry-After greater than the configurable proxy-wide switch threshold (default 1h), the system shall mark that key quota-cooling and attempt the next eligible credential before output; when the hint is at or below the threshold, the system shall not switch credentials merely because of that throttle, including on subsequent concurrent requests.
3. **3.3** If Retry-After is absent, malformed, past, overflows, or lacks trustworthy equivalent provider quota evidence, the system shall follow a bounded conservative no-rotation policy; authentication-invalid errors and explicitly classified provider exhaustion shall be handled separately from transient throttling and from unrelated 4xx/5xx errors.
4. **3.4** When retrying across credentials on any canonical, streaming, non-streaming or optimized wire path, the system shall require pre-output, cancellation-safe and physically replayable input with no uncommitted side effect requiring at-most-once treatment; once output or a non-replayable effect begins, the system shall never transparently rotate or replay.
5. **3.5** If no credential can safely be used or a request deadline prevents same-key backoff, the adapter shall return a bounded, classified failure under core-owned routing without spinning, reattempting a consumed body or recording key material in diagnostics.

### Requirement 4: Existing connector parity and explicit exceptions
**Objective:** As a maintainer, I want every applicable existing backend migrated to one contract, with documented, policy-enforced exceptions only where its auth semantics genuinely differ.

#### Acceptance Criteria
1. **4.1** When an eligible static API-key connector is enabled, the same environment-only lookup, numeric ordering, no-config-value policy and rotation contract shall apply to in-process essentials, generic compatible backends, embedded provider profiles and executable inference connectors.
2. **4.2** Where an executable inference connector previously accepted one API key, the system shall securely provision an ordered key set without weakening agent-runtime restrictions, process isolation or per-instance credential ownership.
3. **4.3** When the OpenResponses-compatible or SystemOne-compatible backend, or the OpenAI compatible fast-wire lane, encounters a switch-eligible failure, it shall use the same guarded per-key decision contract as the canonical API paths rather than an unconditional first-key shortcut.
4. **4.4** When backend variants genuinely share a provider credential namespace or multiple instances operate under distinct quota/account/deployment scopes, the system shall preserve explicit sharing/isolation and shall not mistake a backend instance name, route prefix or a different project's 429 for an independent credential.
5. **4.5** Where an integration is ACP, OAuth-user/subscription, non-bearer workload/signing, local no-auth or a side-effecting agent API-key session, the system shall explicitly classify the permitted source/rotation mode and reject unsafe substitution; Cursor SDK API-key admission and static Codex/OpenCode legacy credential-field remediation shall not be left as undocumented exclusions.

### Requirement 5: Enforced onboarding, negative gates and migration
**Objective:** As a maintainer, I want future connector changes that violate this contract rejected automatically, not merely discouraged by prose.

#### Acceptance Criteria
1. **5.1** When registering or activating a new backend, the system shall require machine-validated credential class, environment namespace, approved source, secret slot shape and runtime descriptor/manifest posture agreement, including every exported kind of a multi-export artifact.
2. **5.2** When an in-tree connector adds a secret-valued configuration field, API-key filesystem/env fallback, unsupported manifest, unclassified static auth mode or bypass of the approved key-selection path, the existing build/architecture/connector contract gates shall fail deterministically; the acceptance guard shall include a proof fixture that fails for a deliberately noncompliant connector.
3. **5.3** Where an optional third-party executable cannot be statically audited, the host shall still reject undeclared/unsupported static-key contracts at admission, supply only capability-scoped Configure secrets and an isolated launch environment, and shall not claim to prove absence of arbitrary filesystem reads without OS sandboxing.
4. **5.4** When a legacy YAML/file-key deployment is migrated, the system shall fail closed with actionable field paths and required environment-variable names (never key values), provide updated configuration/docs and validate no plaintext secret crosses logs, process launch, probes or output.
5. **5.5** When the completed feature is exercised by hermetic contract fixtures, the system shall demonstrate sparse indexes, configurable bounds, namespace collision handling, rejected file/config key sources, first-key persistence across short 429s, long-429 progression, safe no-replay, full plugin export parity and candidate reload isolation.

## Deferred

- Distributed cross-node quota synchronization, durable credential-state restoration after host restart and external vault/KMS integration are **not** necessary to implement the audited single-host contract; track separately if required.
- Provider introspection APIs for dynamic organization/project quota discovery and provider-specific advanced error taxonomies beyond proven adapter signals are future refinements; the present policy must remain conservative where evidence is ambiguous.
- General-purpose OS-level confinement that can prove arbitrary third-party binaries never read unrelated host files is a separate connector sandbox initiative. This specification still enforces the current host admission, IPC and launch-environment guarantees and requires accurate trust-boundary documentation.
