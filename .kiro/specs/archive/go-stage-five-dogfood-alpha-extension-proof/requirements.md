# Requirements Document

## Introduction

The Go LLM Interactive Proxy standard distribution needs to become dogfoodable for maintainers and early operators while proving that advanced Python-era behaviors can be delivered through explicit extension seams. The feature focuses on alpha-ready operator workflows, local stub-backed validation, cross-frontend smoke coverage, diagnostic visibility, and proof plugins that demonstrate extension boundaries without expanding core responsibilities.

## Boundary Context

- **In scope**: alpha server operator workflows, no-key local dogfood configurations or documented local smoke harnesses, reference-client smoke paths, extension inventory visibility, proof-plugin behavior, diagnostic startup safety, and regression gates that protect plugin/core boundaries.
- **Out of scope**: full production hardening, live-provider parity claims beyond explicitly gated smoke workflows, SSO authentication, sandboxing, CBOR wire capture, dynamic tool-output compression, ProxyMem, and broad Python feature ports.
- **Adjacent expectations**: existing canonical request/event contracts, routing continuity, secure-session authority, standard bundle composition, and stage-four extension seams remain the foundation for this feature.
- **Test/production boundary**: Existing reference clients and reference backends are test support surfaces; this feature must not make test-only emulators part of production standard-server wiring unless a deliberately scoped dogfood-only backend surface is specified later.
- **Revalidation triggers**: routing, streaming, capability negotiation, secure session, diagnostics, startup security, and extension-stage visibility.

## Requirements

### Requirement 1: Alpha Server Operator Workflow
**Objective:** As a maintainer, I want a clear standard-server workflow, so that I can validate and run the proxy without reading internal package details.

#### Acceptance Criteria
1. When an operator requests configuration validation, the Go LIP standard distribution shall report whether the selected configuration is valid without starting request serving.
2. If configuration validation fails, the Go LIP standard distribution shall report actionable validation errors and exit with a failure outcome.
3. When an operator requests route information, the Go LIP standard distribution shall show the effective default route and enabled backend route targets.
4. When an operator requests inventory information, the Go LIP standard distribution shall show the configured frontend, backend, feature, and extension surfaces without requiring live provider credentials.
5. When an operator starts serving with a valid configuration, the Go LIP standard distribution shall expose the configured client-facing protocol surfaces and shut down gracefully on normal termination.

### Requirement 2: No-Key Local Dogfood Configurations
**Objective:** As a new maintainer, I want local stub configurations, so that I can exercise the proxy without provider accounts or secrets.

#### Acceptance Criteria
1. The Go LIP repository shall provide at least one no-key local dogfood path that requires no provider API keys.
2. When a maintainer validates a no-key local dogfood path, the Go LIP standard distribution or documented smoke harness shall accept it without requiring external network credentials.
3. Where protocol-specific local stub configurations are included, each configuration shall identify the intended client-facing protocol surface and default route behavior.
4. If a local stub configuration enables diagnostics, the configuration shall either bind diagnostics safely or require an explicit diagnostic secret.
5. The Go LIP repository shall distinguish local stub configurations from live-provider examples so operators do not mistake deterministic local testing for provider parity.
6. If test-only reference backends or reference clients are used for local dogfood validation, the Go LIP repository shall keep them out of production standard-server wiring and label them as test support.

### Requirement 3: Stub-Backed Reference Client Smoke Paths
**Objective:** As a maintainer, I want deterministic smoke paths for supported frontend protocols, so that I can verify the standard server behaves like a usable alpha target.

#### Acceptance Criteria
1. When an OpenAI Responses-compatible client sends a minimal request through the standard server to a stub backend, the Go LIP standard distribution shall return a valid OpenAI Responses-compatible response.
2. When an OpenAI legacy chat-compatible client sends a minimal request through the standard server to a stub backend, the Go LIP standard distribution shall return a valid legacy OpenAI-compatible response.
3. When an Anthropic-compatible client sends a minimal request through the standard server to a stub backend, the Go LIP standard distribution shall return a valid Anthropic-compatible response.
4. When a Gemini-compatible client sends a minimal request through the standard server to a stub backend, the Go LIP standard distribution shall return a valid Gemini-compatible response.
5. If a smoke path encounters a routing, capability, or stream-completion failure, the Go LIP standard distribution shall surface a deterministic failure that identifies the failing protocol path.
6. The Go LIP repository shall include a default-suite smoke set that is smaller than the full conformance matrix and does not require live provider credentials.
7. The default-suite smoke set shall exercise the same standard-server request handling path used by the serving workflow, rather than validating frontend protocol mounts in isolation.

### Requirement 4: Extension Boundary Proof Plugins
**Objective:** As a platform maintainer, I want representative proof plugins, so that advanced behavior can be validated without adding feature-specific logic to the core.

#### Acceptance Criteria
1. Where a first-prompt auto-append proof plugin is enabled, the Go LIP standard distribution shall apply the configured append behavior only to the first logical session request.
2. Where a tool-policy proof plugin is enabled, the Go LIP standard distribution shall filter disallowed tool definitions and block disallowed emitted tool calls in a deterministic way.
3. Where a traffic-accounting proof plugin is enabled, the Go LIP standard distribution shall record client-proxy and proxy-backend traffic observations separately enough to distinguish one logical request from its backend attempts.
4. Where a redacted-capture proof plugin is enabled, the Go LIP standard distribution shall persist or expose captured transcript data only after configured redaction rules are applied.
5. If a proof plugin is disabled, the Go LIP standard distribution shall not apply that plugin's behavior to requests or expose it as active in runtime inventory.

### Requirement 5: Extension Inventory and Diagnostics Visibility
**Objective:** As an operator, I want active extension behavior to be visible, so that I can understand what the proxy will do before and during dogfooding.

#### Acceptance Criteria
1. When inventory is requested, the Go LIP standard distribution shall show active extension stages and handler occupancy for configured feature plugins.
2. When tool catalog filters, tool-call policies, request transforms, route hints, completion gates, traffic observers, usage observers, capture sinks, or redactors are configured, the Go LIP standard distribution shall expose their active status in inventory.
3. Where an extension uses privileged capabilities, the Go LIP standard distribution shall identify the privileged capability category without exposing secrets.
4. If a feature plugin fails to build its extension contribution, the Go LIP standard distribution shall expose the plugin's inactive or error state in inventory.
5. The Go LIP standard distribution shall not expose provider credentials, local API keys, resume tokens, diagnostic shared secrets, or raw unredacted captures through inventory output.
6. When inventory is requested through a command-line workflow or an HTTP diagnostics workflow, the Go LIP standard distribution shall present consistent active-plugin and active-extension information.

### Requirement 6: Startup and Diagnostic Safety for Alpha Use
**Objective:** As an operator, I want safe alpha defaults, so that dogfooding does not accidentally expose sensitive runtime surfaces.

#### Acceptance Criteria
1. While the server is configured for local no-auth operation, the Go LIP standard distribution shall require a local-only trust posture.
2. If diagnostics, metrics, profiling, or session inspection are exposed on a non-local trust boundary, the Go LIP standard distribution shall require explicit protective configuration.
3. When a configuration uses live-provider credentials, the Go LIP standard distribution shall distinguish that mode from no-key local stub operation.
4. If startup detects an unsafe administrative, credential, or diagnostics posture, the Go LIP standard distribution shall fail before serving client traffic.
5. The Go LIP standard distribution shall preserve proxy-owned session authority and shall not treat client-provided session hints as authoritative session identity.
6. If diagnostics protection is omitted, the Go LIP standard distribution shall permit diagnostics exposure only when the configured listener and access posture are local-only.
7. The Go LIP standard distribution shall treat attempts, inventory, route trace, metrics, profiling, model diagnostics, and secure-session summaries as protected diagnostics surfaces distinct from the health endpoint.

### Requirement 7: Regression and Boundary Guardrails
**Objective:** As a maintainer, I want automated guardrails, so that dogfood work does not reintroduce Python-era coupling or weaken core invariants.

#### Acceptance Criteria
1. The Go LIP repository shall verify that core packages do not depend on concrete protocol or feature plugins.
2. The Go LIP repository shall verify that stable public SDK contracts do not depend on core implementation packages or provider SDKs.
3. The Go LIP repository shall verify that provider SDK usage remains outside core and stable public contracts.
4. When extension stages mutate canonical calls or events, the Go LIP repository shall verify that canonical validation still occurs after mutation.
5. When routing or streaming behavior is exercised by dogfood smoke paths, the Go LIP repository shall verify that no transparent retry or failover occurs after client-visible output has started.

### Requirement 8: Operator Documentation and Feature Migration Clarity
**Objective:** As a maintainer or early operator, I want concise dogfood documentation, so that I can run the alpha server and understand what is intentionally not covered.

#### Acceptance Criteria
1. The Go LIP repository shall document the local stub dogfood workflow from configuration validation through serving and a minimal request.
2. The Go LIP repository shall document how to inspect routes and inventory before serving traffic.
3. The Go LIP repository shall document which proof plugins demonstrate extension boundaries and which advanced Python-era features remain deferred.
4. If live-provider smoke workflows are documented, the Go LIP repository shall mark them as optional and environment-gated.
5. The Go LIP repository shall keep dogfood documentation consistent with README, architecture, extension-point, plugin-authoring, and feature-migration guidance.
