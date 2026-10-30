# Requirements Document

## Project Description (Input)

Create the first **Go** re-implementation of the LLM Interactive Proxy as a greenfield core/runtime that is fundamentally simpler than the current Python system while preserving the product’s most distinctive strengths:

- multi-frontend API compatibility,
- multi-backend API compatibility,
- cross-API translation,
- streaming-first execution,
- **core-owned** routing/failover/load-balancing,
- **core-owned** B2BUA-like orchestration that can swallow recoverable pre-output failures and continue on another backend within the same client request,
- and **plugin-owned** advanced behaviors connected through strict hook surfaces rather than direct dependencies.

The new system must be deliberately **small-core, boundary-first, idiomatic Go**, with a standard distribution that bundles official frontend and backend plugins while keeping the runtime itself free of protocol-specific and provider-specific coupling.

## Scope and boundaries

**Primary target**

Design and implement the first production-capable Go core that can serve the shared prompt/tool/streaming subset across the required protocol matrix, while establishing durable boundaries for future feature growth.

**In scope for this spec**

- Greenfield Go repository and package structure
- Canonical request / event model
- Core runtime, plugin registry, hook bus, configuration, diagnostics
- Client-facing protocol plugins:
  - OpenAI Responses API
  - Legacy OpenAI-compatible API
  - Anthropic Messages API
  - Gemini generateContent-compatible API
- Backend protocol plugins:
  - OpenAI Responses API
  - Legacy OpenAI-compatible API
  - Anthropic Messages API
  - Gemini generateContent API
  - Amazon Bedrock Converse / ConverseStream
  - ACP prompt-turn subset
- Cross-protocol translation via canonical model
- **Multimodal** request and response handling on the proxy (frontends and backends) for a **v1 declared shared subset** (including at least images and document files such as PDFs where each protocol supports them), with matching multimodal coverage in reference emulators and conformance tests
- Streaming-first execution, including collector-based non-streaming support
- Core routing, weighted load-balancing, ordered failover, session-aware first-request routing
- Core B2BUA-like request orchestration and attempt lineage
- Reserved hook surfaces for:
  - user request submit hooks,
  - request part altering,
  - response part altering,
  - tool call reactors with rewrite/swallow/replace decisions
- TDD-first implementation plan and conformance suite

**Explicitly out of scope for v1**

- Full feature parity with the Python repository
- OAuth/personal-auth connector families
- Out-of-process plugin sandboxing
- Dynamic tool compression / context compaction
- Dangerous-command protection
- Advanced loop detection
- Persistent multi-node B2BUA state replication
- Full ACP terminal/filesystem/slash-command surface
- Exhaustive vendor-specific multimodal and attachment options beyond the **v1 declared shared multimodal subset** (each protocol’s exotic or preview-only media features), while **not** relaxing the requirement to correctly implement that declared subset on proxy frontends, backends, and emulators
- Production feature plugins beyond no-op/reference hook implementations

## Requirements

### Requirement ID convention

Each acceptance criterion is labeled **`N.M`** (requirement **N**, criterion **M**). These IDs are the stable handles used in `tasks.md`, `design.md`, and traceability tables. To find a criterion, search this file for `**N.M**` (for example `**2.6**`).

### Requirement 1: Small core ownership and boundary isolation

**Objective:** As a maintainer, I want the new Go implementation to have a tiny, explicit core with strong package boundaries, so that future features do not recreate the current coupling and dependency sprawl.

#### Acceptance Criteria

**1.1.** When the repository is initialized, the system shall separate canonical contracts, stable plugin SDK, provider-agnostic core runtime, and bundled protocol plugins into distinct Go packages with explicit ownership boundaries.

**1.2.** The core runtime shall not import provider SDK packages, bundled protocol plugin packages, or feature-plugin implementation packages.

**1.3.** Where behavior can be supplied externally, the system shall expose only narrow typed interfaces and registration contracts rather than direct references to concrete implementations.

**1.4.** If a behavior defines request-execution semantics for every call, such as route planning, failover, or B2BUA lineage, then the system shall keep orchestration in the core and isolate policy details behind narrow interfaces.

**1.5.** The v1 system shall not depend on Go’s native `plugin` package for runtime extensibility.

### Requirement 2: Canonical request, event, and capability model

**Objective:** As an architect, I want one small canonical model for requests and one small canonical event stream for outputs, so that translation stays linear rather than pairwise.

#### Acceptance Criteria

**2.1.** When any supported frontend request is accepted, the system shall decode it into one canonical call model before route planning or backend execution begins.

**2.2.** When any supported backend produces output, the system shall normalize that output into one canonical event stream before frontend encoding begins.

**2.3.** Where protocol-specific data must be preserved, the system shall isolate that data in explicit vendor-extension fields rather than expanding the core semantic surface without review.

**2.4.** If a required request capability cannot be represented or preserved for the selected backend, then the system shall reject the request with a deterministic capability error before the upstream call is started.

**2.5.** The system shall implement cross-API translation only through protocol-to-canonical and canonical-to-protocol adapters, not through pairwise protocol-to-protocol translators.

**2.6.** For **multimodal** user and assistant content in the **v1 shared subset**—including at least **images** and **documents** such as **PDFs** where the relevant APIs define a representation—the canonical call and event models shall represent those inputs and outputs with explicit part kinds and metadata so that adapters can translate without ambiguous or lossy encoding inside the supported capability set.

**2.7.** If a multimodal request requires semantics that cannot be represented in the canonical model or forwarded to the selected backend without violating **2.4**’s “no silent loss” rule, then the system shall reject the attempt with a deterministic capability error before the upstream call is started.

### Requirement 3: Client-facing frontend compatibility

**Objective:** As an operator, I want the Go distribution to expose the required client-facing API surfaces, so that existing tools can point at the new proxy without being rewritten.

#### Acceptance Criteria

**3.1.** When the standard distribution starts successfully, the system shall expose an OpenAI Responses-compatible frontend surface.

**3.2.** When the standard distribution starts successfully, the system shall expose a legacy OpenAI-compatible frontend surface for chat-style clients.

**3.3.** When the standard distribution starts successfully, the system shall expose an Anthropic Messages-compatible frontend surface.

**3.4.** When the standard distribution starts successfully, the system shall expose a Gemini generateContent-compatible frontend surface.

**3.5.** Where a frontend supports streaming in its native protocol, the system shall expose streaming using that protocol’s legal framing and event semantics.

**3.6.** If a request fails before protocol output begins, the frontend shall return an error shape that remains valid for that frontend protocol.

**3.7.** Each bundled frontend shall **decode** client multimodal payloads (within the v1 shared subset) into the canonical model and **encode** multimodal canonical parts into protocol-valid requests and responses, including **streaming** paths where the protocol defines streaming for multimodal outputs.

**3.8.** The frontend shall not silently drop, flatten, or mis-label multimodal parts that are within the declared supported subset; unsupported combinations shall fail with a protocol-valid error consistent with **2.7**.

#### Implementation notes — v1 bundled frontend subset (2026-04-21)

Normative API URLs and the authoritative wire-vs-canonical matrix live in **`research.md`** → *Official API specification references (normative docs)* and the per-protocol **Frontend subset vs spec** bullets there. The following aligns **Requirement 3** acceptance criteria with the **current** Go adapters under `internal/plugins/frontends/*` (contract tests in each package’s `*_test.go` and `integration_test.go`).

- **OpenAI Responses (`frontends/openairesponses`):** Decode supports `POST /v1/responses` with `input` as a string or an array of **`message`-typed items** only; other input item types (e.g. `function_call_output`) are rejected. `instructions` is a **JSON string** only. **`tool_choice`** is decoded (`auto` / `none` / `required`, and `type: function` with a name); wire **`required`** maps to canonical **`ToolChoice` mode `any`** so `lipapi` validation matches OpenAI “must use a tool” semantics. Encode maps canonical **tool-call** events to Responses output / SSE; canonical **`EventReasoningDelta`** is not re-emitted on the Responses wire in v1.

- **Legacy OpenAI Chat Completions (`frontends/openailegacy`):** Decode covers `messages`, `tools`, **`tool_choice`** (same **`required` → `any`** rule as Responses), `stream_options`, sampling, multimodal `image_url` / `file`, **`developer` → system**, and **`role: tool`**; assistant **`tool_calls` / `function_call`** on inbound messages are rejected. Encode maps canonical tools to **`delta.tool_calls` / `message.tool_calls`** and **`finish_reason: tool_calls`** when applicable.

- **Anthropic Messages (`frontends/anthropic`):** Decode covers `system` (string or text block array), `messages`, tools, **`tool_choice`**, positive **`max_tokens`**, sampling, base64 **`image` / `document`** blocks, **`tool_use` / `tool_result`** in history; **`metadata` / `top_k`** are ignored for canonical options. The HTTP **`anthropic-version`** header is not validated or stored on the canonical call. Encode streaming emits **text** and **`tool_use`** SSE (`input_json_delta`); canonical reasoning deltas are not mapped to Messages SSE in v1.

- **Gemini generateContent (`frontends/gemini`):** Decode covers `contents`, `systemInstruction`, `generationConfig`, `tools` / `toolConfig`, parts **`text`**, **`inlineData`**, **`functionCall`**, **`functionResponse`** (camelCase and snake_case keys where implemented). **`allowedFunctionNames`** does not narrow canonical `ToolChoice.Name` in v1. Encode streams **text** and **`functionCall`** chunks plus trailing **`usageMetadata`** when usage deltas exist; **non-stream** responses omit **`usageMetadata`** by subset contract even if usage appeared on the canonical stream.

### Requirement 4: Backend protocol compatibility

**Objective:** As an operator, I want the Go distribution to speak the required backend API flavors, so that one frontend can target any supported backend family on the shared subset.

#### Acceptance Criteria

**4.1.** When configured, the system shall support an OpenAI Responses backend adapter.

**4.2.** When configured, the system shall support a legacy OpenAI-compatible backend adapter.

**4.3.** When configured, the system shall support an Anthropic Messages backend adapter.

**4.4.** When configured, the system shall support a Gemini generateContent backend adapter.

**4.5.** When configured, the system shall support an Amazon Bedrock backend adapter based on Converse / ConverseStream semantics.

**4.6.** When configured, the system shall support an ACP backend adapter for the prompt-turn subset defined by this specification.

**4.7.** Where an official SDK or reference implementation exists and is suitable, the backend adapter shall use it behind the plugin boundary rather than reimplementing wire behavior ad hoc.

**4.8.** Each bundled backend connector shall **map multimodal** canonical request parts to provider-valid API payloads and map provider multimodal response content into the canonical event stream for the v1 shared subset, preserving content types, ordering, and binary integrity within the limits of each API (no silent truncation or type confusion for supported modalities).

### Requirement 5: Streaming-first execution

**Objective:** As a developer, I want streaming to be the primary execution mechanism, so that protocol behavior, failover rules, and non-streaming support all follow one semantic path.

#### Acceptance Criteria

**5.1.** When the core invokes any backend adapter, the backend adapter shall return or emit a canonical event stream as the primary execution result.

**5.2.** Where a client requests non-streaming behavior, the system shall produce the non-streaming response by collecting the canonical event stream rather than by using a separate execution pipeline.

**5.3.** When a client disconnects or a request context is cancelled, the system shall propagate cancellation through the core and the active backend adapter.

**5.4.** If client-visible content has already begun, the system shall not silently retry or fail over to a different backend for the same client response.

**5.5.** While the system is waiting during a recoverable pre-output failure path for a streaming request, the system shall emit protocol-legal keepalive output where needed to avoid idle timeouts.

### Requirement 6: Core routing, load balancing, and failover

**Objective:** As an operator, I want dynamic routing and failover to remain a first-class core feature, so that the proxy can continue to improve UX and reliability without moving orchestration into plugins.

#### Acceptance Criteria

**6.1.** When parsing route selectors, the system shall support explicit backend-plus-model routing, backend-instance routing, model-only routing, ordered failover chains, weighted routing, and URI-style selector parameters.

**6.2.** When an ordered failover selector is used, the system shall attempt candidates left-to-right until the request succeeds or the attempt policy is exhausted.

**6.3.** When a weighted selector is used, the system shall choose from eligible weighted candidates using deterministic, testable selection logic.

**6.4.** If a weighted candidate fails before client-visible output begins, the system shall be able to exclude the failed candidate and re-select among the remaining eligible weighted candidates within the same client request.

**6.5.** Where backend health or temporary exclusion state exists, the route planner shall consider that state before producing the final attempt order.

**6.6.** The system shall record the selected route plan and the final surfaced branch for diagnostics.

### Requirement 7: Session-aware first-request routing

**Objective:** As an operator, I want the first request of a session to be steerable independently from later requests, so that onboarding, warmup, or expensive-model gating can be controlled without breaking later weighted behavior.

#### Acceptance Criteria

**7.1.** When a weighted selector contains exactly one first-request annotation, the system shall force that branch for the first request of the logical session.

**7.2.** When the first request of the logical session has already been consumed, the system shall ignore the first-request annotation and use normal weighted routing.

**7.3.** If more than one branch in the same weighted selector is marked as first-request, then the system shall reject the configuration or request deterministically.

**7.4.** When a retry or failover path is entered after the initial branch has been selected, the system shall ignore first-request annotations and only consider the remaining eligible candidates.

**7.5.** The system shall persist or retain the first-request-consumed state inside the logical session state owned by the core.

### Requirement 8: B2BUA-like multi-attempt orchestration

**Objective:** As a power user, I want one client request to be able to create multiple related backend attempts under one logical exchange, so that recoverable backend failures can be masked and UX can remain smooth.

#### Acceptance Criteria

**8.1.** When the core accepts a logical client request, the system shall create or resolve one core-owned A-leg identity for that logical exchange.

**8.2.** When the core starts a backend attempt, the system shall allocate a distinct B-leg identity for that attempt and link it to the A-leg.

**8.3.** If a backend fails before client-visible output begins and the failure is recoverable under policy, then the system shall be allowed to swallow the failure and continue with another backend attempt within the same client request.

**8.4.** Where recovery is attempted, the system shall record attempt order, backend identity, effective model, timing, and recovery reason in the A-leg lineage.

**8.5.** When a follow-up client request is recognized as belonging to the same logical session, the system shall be able to reuse the same A-leg identity according to the configured continuity policy.

**8.6.** If continuity cannot be resolved safely, then the system shall create a new A-leg rather than guessing across isolation boundaries.

### Requirement 9: User request submit hook surface

**Objective:** As a future feature author, I want a typed submit-hook API before route planning, so that request admission, annotation, and lightweight rewrites can be added later without modifying the core.

#### Acceptance Criteria

**9.1.** Before route planning begins, the system shall invoke zero or more registered submit hooks against the canonical call and request metadata.

**9.2.** Submit hooks shall receive only canonical and core-owned types, not provider SDK types or frontend-specific request models.

**9.3.** A submit hook shall be able to annotate request metadata, rewrite selected canonical fields, reject the request, or pass the request through unchanged.

**9.4.** The core shall support deterministic hook ordering and per-hook failure mode selection.

**9.5.** The standard distribution shall remain functional when no submit hooks are registered.

### Requirement 10: Request and response part altering hook surface

**Objective:** As a future feature author, I want request-part and response-part hook APIs, so that future integrations can alter canonical content without re-entering the protocol adapters.

#### Acceptance Criteria

**10.1.** Before backend translation begins, the system shall allow registered request-part hooks to inspect, insert, replace, or remove canonical request parts.

**10.2.** Before frontend encoding begins, the system shall allow registered response-part hooks to inspect, insert, replace, or remove canonical response parts or events.

**10.3.** If a hook returns an invalid mutation that violates canonical invariants, then the system shall reject that mutation deterministically and record a typed hook error.

**10.4.** Hook interfaces shall operate on canonical parts and events rather than frontend-specific or backend-specific payload types.

**10.5.** The v1 standard distribution may ship with no-op part hooks, but the hook contracts and execution points shall exist and be covered by tests.

### Requirement 11: Tool call reactor hook surface

**Objective:** As a future feature author, I want a tool-call reactor API with rewrite/swallow/replace decisions, so that later steering features can be added without reworking the stream engine.

#### Acceptance Criteria

**11.1.** When a backend emits canonical tool-call lifecycle events, the system shall expose those events to registered tool reactors through a typed interface.

**11.2.** A tool reactor decision shall be able to pass through, rewrite, swallow, or replace tool-call-related output using canonical structures.

**11.3.** Tool reactor interfaces shall include enough stream and session context to support future reactor behaviors without depending on global mutable state.

**11.4.** The v1 system shall reserve the orchestration path for tool reactors even if the standard distribution ships without policy-heavy reactor implementations.

**11.5.** Unless explicitly configured otherwise, tool reactor failures shall fail open and preserve the underlying request flow.

### Requirement 12: Plugin registration, configuration, and capabilities

**Objective:** As a maintainer, I want plugins to register themselves through stable contracts with opaque private configuration, so that the core stays independent from plugin implementation details.

#### Acceptance Criteria

**12.1.** When the standard distribution is composed, the system shall register bundled frontends, backends, and hook plugins explicitly in the composition root.

**12.2.** The core shall validate plugin identities, mandatory plugin presence, and declared capability sets without importing plugin-private packages or reading plugin-private state.

**12.3.** Where a plugin owns private configuration, the core shall pass the plugin its opaque configuration payload without introducing core-owned schemas for plugin-private behavior.

**12.4.** The core configuration surface shall define only core-owned settings such as routing, B2BUA, diagnostics, and server wiring.

**12.5.** If duplicate plugin IDs or incompatible mandatory contracts are detected at startup, the system shall fail fast with a deterministic startup error.

**12.6.** The v1 implementation shall use constructor-based registration and shall not require a reflection-heavy dependency injection container.

### Requirement 13: Observability and diagnostics

**Objective:** As an operator, I want minimal but meaningful diagnostics for routing, B2BUA, and translation decisions, so that the new core is debuggable from day one.

#### Acceptance Criteria

**13.1.** When a request enters the system, the core shall assign a trace identifier that is carried through route planning, hook execution, backend attempts, and frontend encoding.

**13.2.** The system shall record A-leg and B-leg attempt lineage in structured diagnostics suitable for debugging recoveries and route decisions.

**13.3.** Where routing, capability negotiation, or hook execution materially changes request behavior, the system shall emit structured logs or counters describing that decision.

**13.4.** The standard distribution shall expose a basic health surface and a minimal attempt-diagnostics surface.

**13.5.** Diagnostics shall be obtainable through core-owned records and logs without requiring inspection of plugin-private internals.

### Requirement 14: Idiomatic Go engineering standards

**Objective:** As a Go maintainer, I want the new implementation to follow idiomatic Go patterns, so that the rewrite gains simplicity rather than only changing languages.

#### Acceptance Criteria

**14.1.** When implementing request-scoped behavior, the system shall use `context.Context` for cancellation, timeout, and trace propagation.

**14.2.** The system shall prefer the Go standard library for HTTP serving, JSON handling, streaming, and structured logging unless a dependency materially reduces protocol risk.

**14.3.** The implementation shall avoid package-level mutable globals for runtime request state.

**14.4.** Public interfaces shall remain narrow, explicit, and typed; generic catch-all maps shall be isolated to configuration extension points or vendor extensions.

**14.5.** The codebase shall be structured as small packages and files with explicit constructors rather than a reflection-heavy dependency injection framework.

**14.6.** The production test suite shall run successfully under the Go race detector.

### Requirement 15: TDD, conformance, and migration safety

**Objective:** As a lead developer, I want the rewrite to be test-first and behavior-driven, so that the Go version can replace the Python version without reintroducing semantic regressions.

#### Acceptance Criteria

**15.1.** Before implementing a new core package or protocol adapter, the system shall define failing contract tests or golden tests for the intended behavior.

**15.2.** The repository shall include a test kit with provider stubs, stream fixtures, and helper assertions for canonical events and protocol output.

**15.3.** The v1 implementation shall provide a shared-subset conformance matrix covering each bundled frontend against each bundled backend.

**15.4.** Decoder and selector parsers shall be covered by fuzz tests for malformed or adversarial inputs.

**15.5.** The migration suite shall include fixtures or goldens derived from the current Python repository’s captures, stream shapes, or documented behaviors where practical.

**15.6.** The implementation shall not be marked ready for production migration until the conformance matrix, race tests, and critical fuzz targets are green.

**15.7.** Reference **client** emulators used for end-to-end tests shall support **multimodal** request and response scenarios for the v1 shared subset—including at least one **image** and one **document (e.g. PDF)** path per protocol **where the official API supports that modality**—so that multimodal behavior of the proxy can be exercised without relying on production provider accounts alone.

**15.8.** Reference **backend** emulators shall accept multimodal inputs and emit multimodal outputs (within the same v1 shared subset and protocol limits) so that integration tests can validate frontend adapters, backend connectors, and canonical translation under multimodal load.

**15.9.** The conformance matrix shall include explicit **multimodal** cases in addition to text-only cases: at least one multimodal-capable frontend paired with at least one multimodal-capable backend, covering **image** input and **document** input separately. Each multimodal row shall verify canonical part preservation through decode → canonical → encode → emulator decode without content-type confusion or silent truncation. Combinations where the shared subset carries no viable multimodal overlap shall be explicitly listed and justified rather than silently skipped. _(Task 12.3)_

**15.10.** The conformance matrix shall be expressed as a parameterized test harness that enumerates each bundled frontend × bundled backend combination, where each cell runs at minimum: (a) a text prompt round-trip, (b) a streaming plus collected non-streaming verification, and (c) a protocol-valid error-shape test when the backend emulator returns a recoverable failure. Combinations where the shared subset is empty or degenerate shall be explicitly listed and justified rather than silently skipped. _(Task 12.0, Task 12.1)_

**15.11.** Each conformance matrix cell that has a non-empty shared subset shall include at least one tool-definition and basic tool-call round-trip, and at least one usage-propagation assertion, unless the specific frontend/backend combination cannot express that semantic in the shared subset—such exclusions shall be documented. _(Task 12.2)_

**15.12.** The conformance test harness shall live in `internal/testkit/conformance/` (or equivalent) with a machine-readable matrix definition (for example a Go test suite or table) so that adding a new frontend or backend plugin automatically generates failing test stubs for the new combinations. _(Task 12.0)_

**15.13.** The migration fixture set shall include at minimum one streaming capture and one non-streaming capture from the Python repository for the OpenAI Responses protocol pair, and at least one additional protocol pair where practical. Each fixture shall be accompanied by a golden file in `testdata/` with documented provenance. _(Task 12.4)_
