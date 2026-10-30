# Requirements Document

## Base API connector porting

Spec directory: `base-api-connector-porting`

## Project description (input)

porting-of-base-api-connectors

## Scope and intent

This spec defines the requirements for porting the official hosted-provider backend connectors from the Python LIP into the Go implementation without reproducing Python-side architectural complexity.

The immediate target is the official API connector set only:

- OpenAI Responses backend
- legacy OpenAI-compatible chat completions backend
- Anthropic Messages backend
- Gemini generateContent backend

Bedrock, ACP, and OAuth-based connectors are out of scope for this spec.

The goal is not a line-by-line port. The goal is to preserve protocol parity, streaming correctness, and explicit capability handling while adopting a simpler Go-native architecture in which backend instance identity is separated from API-key usefulness state.

## In scope

- Official hosted-provider backend plugins for OpenAI Responses, OpenAI legacy-compatible chat completions, Anthropic Messages, and Gemini generateContent
- Emulator-first and contract-first porting approach for those backends
- Canonical request to provider request mapping
- Provider stream to canonical event stream mapping
- Capability negotiation and explicit mismatch failures
- Pre-output recoverable failure behavior consistent with core-owned routing and failover
- Backend configuration rules for base URL, provider-specific parameters, and credential pools
- Requirements for separating backend instance identity from credential state
- Use of shared base protocol implementations and thin provider-specific overlays where providers are materially protocol-compatible

## Out of scope

- OAuth-based connectors and any connector that depends on borrowed native-client credentials
- Bedrock and ACP implementation work
- Frontend protocol work except where backend requirements require cross-checking existing canonical contracts
- Broad core-runtime refactors unrelated to backend connector porting
- Dynamic plugin loading, out-of-process plugins, or non-official community connectors

## Architectural constraints

1. Backend connector work SHALL preserve the existing ownership model: the core owns orchestration, retry eligibility, failover policy, and attempt lineage; backend plugins own upstream protocol translation and provider-specific transport details.
2. No requirement in this spec authorizes provider SDK types, provider wire payloads, or credential-management details to leak into `pkg/lipapi`, `pkg/lipsdk`, or `internal/core`.
3. This spec SHALL prefer small local seams that emerge from connector pressure over broad speculative refactors.

## Requirements

### Requirement ID convention

Each acceptance criterion is labeled **`N.M`** (requirement **N**, criterion **M**). These IDs are the stable handles used in later `design.md` and `tasks.md` traceability. To find a criterion, search this file for `**N.M**`.

---

### Requirement 1: Official backend scope shall be explicit

**Objective:** As a maintainer, I want this phase to target only the official hosted-provider backend families, so implementation effort stays focused on the highest-value protocol surfaces first.

#### Acceptance criteria

**1.1.** The standard distribution shall treat OpenAI Responses, legacy OpenAI-compatible chat completions, Anthropic Messages, and Gemini generateContent as the only backend families required for implementation in this phase.

**1.2.** If a connector depends on OAuth token borrowing, native harness credentials, or other non-official authentication tricks, then the system shall treat that connector family as out of scope for this specification.

**1.3.** If future work references Bedrock or ACP within this spec, then the system shall document them only as deferred backend families and shall not require their implementation in this phase.

---

### Requirement 2: Backend porting shall be protocol-parity driven, not Python-topology driven

**Objective:** As an architect, I want the Go implementation to port protocol behavior rather than Python connector structure, so historical complexity is not reintroduced into the new codebase.

#### Acceptance criteria

**2.1.** When porting an official backend connector, the system shall treat protocol semantics, canonical mappings, streaming behavior, capability handling, and error behavior as the required parity targets rather than the Python class hierarchy, registry shape, or env-discovery topology.

**2.2.** If a Python connector pattern would couple routing identity to credential identity, then the system shall not treat that Python pattern as normative for the Go design.

**2.3.** When historical Python captures or fixtures are used during porting, the system shall use them as regression evidence only and shall not treat Python implementation details as the architectural source of truth.

---

### Requirement 3: Backend instance identity shall be separate from credential identity

**Objective:** As an operator, I want backend instances to represent stable upstream targets rather than individual API keys, so routing and diagnostics remain understandable and maintainable.

#### Acceptance criteria

**3.1.** When configuring an official backend instance, the system shall define that instance by protocol implementation, base URL, and any backend-specific deployment parameters rather than by a single API key.

**3.2.** The system shall not require a unique routable backend instance identifier for each API key attached to the same upstream deployment.

**3.3.** When a backend instance has multiple API keys for the same upstream target, the system shall treat those keys as credentials attached to that instance rather than as separate backend instances.

**3.4.** Where selectors, diagnostics, or route planning refer to a backend, the system shall use stable backend instance identifiers that represent upstream deployments or operator-meaningful targets rather than auto-generated per-key identities.

---

### Requirement 4: Credential usefulness state shall be modeled separately from routing identity

**Objective:** As an implementer, I want rate-limit and credential-health state handled as credential-level state, so the core routing model stays clean and provider credential behavior remains manageable.

#### Acceptance criteria

**4.1.** When an official backend instance uses one or more API keys, the system shall maintain credential usefulness state separately from backend instance identity.

**4.2.** While a credential is temporarily unusable because of rate limiting or quota cooldown, the system shall represent that state as credential-level state rather than as a requirement to create or select a different backend instance id.

**4.3.** If an upstream response provides a retry-after or equivalent cooldown hint for a credential, the system shall preserve enough information to determine the nearest time when that credential becomes usable again.

**4.4.** If a credential becomes permanently invalid because of authentication failure, the system shall allow that credential to be marked unusable without implying that the entire backend instance identity is invalid unless no usable credentials remain.

---

### Requirement 5: Retry and failover ownership shall remain explicit

**Objective:** As an architect, I want credential rotation and backend failover to occur at different layers, so the no-retry-after-first-output invariant and core-owned orchestration rules remain intact.

#### Acceptance criteria

**5.1.** When an official backend instance has multiple usable credentials, the backend adapter may switch credentials only before it returns or yields the first canonical output event for the selected attempt.

**5.2.** Once client-visible output has begun for an attempt, the system shall not silently switch credentials or backend instances for that attempt.

**5.3.** If all credentials attached to the selected backend instance are unusable before output begins, the backend adapter shall return a classified pre-output failure to the core rather than inventing a new routing identity.

**5.4.** The core runtime shall remain responsible for inter-backend failover decisions, attempt sequencing, and lineage recording, while backend adapters remain responsible only for provider-local credential selection and provider-local transport handling.

---

### Requirement 6: Backend configuration shall support deployment identity and credential pools

**Objective:** As an operator, I want backend configuration to express real upstream deployments cleanly, so configuration remains understandable as connectors grow.

#### Acceptance criteria

**6.1.** When configuring an official backend instance, the system shall support backend-specific deployment inputs such as base URL and other provider-specific parameters needed to address an upstream target.

**6.2.** When a backend uses static API credentials, the system shall support attaching multiple API keys to a single backend instance without requiring multiple duplicate backend definitions solely for credential rotation.

**6.3.** If environment variables are used to supply multiple credentials for a backend family, the system shall ingest numbered environment variables for operator convenience and shall map them into a credential pool for a backend instance rather than auto-creating separate routable backend instances per key.

**6.4.** The system shall continue to allow explicit multiple backend instances of the same protocol family when those instances represent genuinely different upstream targets, accounts, regions, projects, or operator-distinct routing identities.

---

### Requirement 7: Protocol implementations shall be reusable where providers are materially compatible

**Objective:** As a maintainer, I want OpenAI-compatible and similar families to share strong protocol implementations where practical, so the Go codebase avoids duplicate connector logic.

#### Acceptance criteria

**7.1.** When multiple hosted backends are materially compatible with the same protocol family, the system shall allow a shared base protocol implementation to be reused with provider-specific configuration and thin overlays.

**7.2.** If a backend differs only by base URL, auth/header shaping, minor capability/profile adjustments, or limited plumbing code, the system shall not require a fully separate protocol implementation solely because it is a different provider target.

**7.3.** If a provider diverges materially in request shape, streaming framing, error semantics, capability negotiation, or other protocol behavior, then the system shall allow a provider-specific implementation or overlay to exist explicitly rather than forcing all behavior through an over-generalized shared layer.

**7.4.** The system shall prefer composition through protocol adapters, deployment profiles, and thin provider-specific shims over inheritance-heavy or switch-heavy connector structures.

**7.5.** When adding shared helpers for compatible protocol families, the system shall place those helpers behind a narrow package boundary that has an explicit responsibility, such as credential pooling, protocol mapping, capability profiles, or provider overlays, rather than a generic shared catch-all package.

---

### Requirement 8: Capability mismatches and semantic gaps shall fail explicitly

**Objective:** As an operator, I want backend differences surfaced clearly, so the proxy never pretends a provider supports semantics it cannot honor.

#### Acceptance criteria

**8.1.** When a canonical request requires semantics that the selected backend implementation or deployment profile cannot support, the system shall fail explicitly before upstream work starts or at the earliest protocol-safe point.

**8.2.** The system shall not silently drop required request semantics merely because the provider is “mostly compatible” with a shared base protocol implementation.

**8.3.** If a shared base implementation is extended by a provider-specific overlay, the system shall preserve explicit capability handling for that overlay rather than assuming the base protocol’s capabilities always apply unchanged.

---

### Requirement 9: Streaming and event mapping shall be first-class contracts

**Objective:** As a maintainer, I want backend connector parity proven primarily through streaming and canonical event behavior, so the most failure-prone integration path is validated directly.

#### Acceptance criteria

**9.1.** When an official backend protocol supports streaming, the system shall implement streaming as the primary execution path for that backend connector.

**9.2.** When a backend stream is consumed, the system shall map provider stream events into canonical events in a deterministic, protocol-correct order.

**9.3.** If a non-streaming response path is supported for a backend family, the system shall derive that behavior from the same canonical streaming/event path rather than maintaining an unrelated second execution path.

**9.4.** When porting backend behavior, the system shall verify usage propagation, stop/terminal behavior, and multimodal mapping where those semantics are part of the provider’s supported subset for this phase.

---

### Requirement 10: Emulator-first evidence shall anchor connector work

**Objective:** As an implementer, I want deterministic reference backends and conformance evidence available before or alongside live connectors, so protocol parity can be proven without relying on live provider behavior.

#### Acceptance criteria

**10.1.** For each official backend family in scope, the system shall provide emulator-first or deterministic reference evidence before considering the connector work complete.

**10.2.** When validating canonical request mapping or stream/event mapping for an official backend, the system shall prefer deterministic emulator, fixture, or reference-client evidence over live-network-only proof.

**10.3.** If a backend-specific behavior cannot yet be proven deterministically, the system shall document that gap explicitly rather than implying verified parity.

---

### Requirement 11: Tests shall lock the intended architectural behavior

**Objective:** As a maintainer, I want regression tests to prove both protocol parity and the simplified architecture, so Python-origin complexity does not creep back into the Go port.

#### Acceptance criteria

**11.1.** When implementing or fixing a backend connector in scope, the system shall begin with failing tests that exercise the relevant mapping, capability, streaming, or failure behavior before implementation is considered complete.

**11.2.** The system shall include regression coverage for pre-output credential rotation behavior, explicit failure when no credential is usable, and preservation of the no-retry-after-first-output invariant.

**11.3.** The system shall include regression coverage for backend instance identity remaining stable regardless of whether one or multiple credentials are attached to that instance.

**11.4.** Where OpenAI-compatible or otherwise reusable protocol families share a base implementation, the system shall include tests that prove provider-specific overlays do not silently change unrelated protocol behavior.
