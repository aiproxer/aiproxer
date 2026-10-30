# Current-State Review, Protocol Research, Compatibility Evidence, and Design Validation

Generated: 2026-07-27T14:23:07+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Brownfield baseline: `main@eb843ba4f2d60a2b85c9be7e94f542311384b73b`
- Feature: `openresponses-api-support`
- External profile: OpenResponses `2026-04-24`
- External source baseline: `openresponses/openresponses@92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c`
- Scope: specification artifacts only
- Implementation readiness: designable/taskable but intentionally unapproved and not ready for implementation

## Steering, Rules, and Adjacent Specifications Reviewed

- `AGENTS.md` and `.kiro/AGENTS.md`
- `.kiro/steering/product.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/api-standards.md`
- `.kiro/steering/routing-and-orchestration.md`
- `.kiro/steering/testing.md`
- `.kiro/rules/ears-format.md`
- `.kiro/rules/gap-analysis.md`
- `.kiro/rules/design-discovery-full.md`
- `.kiro/rules/design-principles.md`
- `.kiro/rules/design-review.md`
- `.kiro/rules/tasks-generation.md`
- `.kiro/specs/archive/backend-connector-plugin-architecture/*`
- `.kiro/specs/archive/generic-compatible-backend-modes/*`
- `.kiro/specs/archive/openai-responses-reasoning-preservation/*`
- `.kiro/specs/archive/llm-api-parity/*`
- archived reference-client/reference-backend matrices and API-porting specifications

## Sources Reviewed

### Official OpenResponses

- specification landing page and dated specification
- official API reference
- official `openresponses/openresponses` repository
- dated `2026-04-24` OpenAPI snapshot
- dated normative MDX source
- compliance-test implementation
- changelog and Apache-2.0 license

### Go implementation candidates

- official OpenAI Go SDK
- `github.com/joeychilson/openresponses` package record
- `github.com/webforspeed/openresponses-go`

No candidate was selected as the production standard contract. The default remains project-owned Go wire code aligned to immutable official sources.

### Repository implementation

Frontend families:

- OpenAI Chat Completions (`openailegacy`)
- OpenAI Responses (`openairesponses`)
- Anthropic Messages (`anthropic`)
- Gemini/Vertex-compatible (`gemini`)

Backend families:

- OpenAI Chat Completions
- OpenAI Responses
- Anthropic Messages
- Gemini/Vertex
- Amazon Bedrock Converse
- ACP prompt-turn subset
- OpenRouter
- NVIDIA

Cross-cutting systems reviewed include canonical API, capability negotiation, routing/failover, output commitment, secure state, audit/redaction, token accounting, runtime composition/reload, plugin ABI, and standard endpoint/credential/inventory infrastructure.

## Executive Conclusion

OpenResponses is **not the same API contract** as the existing OpenAI Responses implementation. It is a separately governed, dated, Apache-2.0 interoperability specification with item-first semantics, assistant phases, complete response-resource presence, standalone compaction, sequential WebSocket turns, connection-local `store:false` continuation, and implementor-aware extension rules.

The correct production design is a distinct OpenResponses frontend and backend codec over an additive protocol-neutral ordered item model. The correct evidence design is equally important: two independent test-only emulators and an exhaustive canonical frontend×backend matrix. A production codec tested only against itself can prove internal consistency but not interoperability.

The repository already contains the right brownfield testing architecture:

- `internal/refclient/*` for black-box protocol clients;
- `internal/refbackend/*` for remote provider emulators; and
- `internal/testkit/conformance/*` for Cartesian frontend-to-backend translation.

OpenResponses must extend all three rather than introduce an isolated one-off test server.

## OpenResponses Protocol Findings

### 1. Governance and versioning

OpenResponses publishes dated snapshots. The reviewed `2026-04-24` profile added WebSocket transport, compaction, assistant phase, and related compliance cases. Go-LIP must pin immutable sources/digests and treat future versions as explicit compatibility additions.

### 2. HTTP and SSE

The profile uses JSON request/non-streaming response bodies. Streaming uses semantic SSE events where:

- `event:` equals the event body's `type`;
- SSE `id` is not response authority; and
- terminal response semantics are followed by literal `data: [DONE]`.

Basic framing resembles OpenAI Responses, but schema presence and event coverage are broader.

### 3. Item-first context

Items—not messages alone—are the ordered unit of context. The dated schema includes messages, item references, reasoning, compaction, function calls/results, custom tool forms, and provider-derived hosted forms. Item identity, status, phase, call identity, content ordering, and replay data cannot be preserved by flattening everything into current messages/parts.

### 4. Roles and assistant phase

OpenResponses distinguishes system and developer roles and includes assistant `commentary` and `final_answer` phases. These distinctions are hard semantics: a backend unable to represent them must be rejected unless a documented lossless equivalence is proven.

### 5. Response resource

The response resource requires many explicit fields, including nullable/defaulted status, model, previous response, instructions, output, errors, tools, tool choice, truncation, sampling, reasoning, usage, storage, service tier, metadata, and safety/cache controls. The existing sparse OpenAI response encoder is not sufficient.

### 6. Continuation

`previous_response_id` logically expands to:

`previous input` → `previous output` → `new input`

The proxy must own response IDs and materialized canonical state. Raw remote IDs cannot be client authority because they leak provider semantics, pin routing implicitly, weaken tenant isolation, and break backend substitution/failover.

### 7. Compaction

`POST /responses/compact` is a separate model-required operation returning a reusable compacted item window rather than a continuation ID. It must be a core-routed protocol-neutral operation, not a frontend shortcut to one backend.

### 8. WebSocket

The profile defines an alternate transport for the same response resource:

- client sends `response.create`;
- HTTP-only fields are forbidden;
- one response is in flight per connection;
- accepted turns execute sequentially;
- output uses the same event objects without SSE framing;
- `store:false` continuation may use connection-local state;
- classified 4xx/5xx-equivalent continuation failure evicts the referenced local parent;
- disconnect, cancellation, and unrelated transport failure do not evict it;
- maximum connection age is 60 minutes.

Client WebSocket termination can use ordinary canonical execution and upstream HTTP/SSE. Upstream persistent WebSocket pooling is not required initially.

### 9. Extensions and dated-schema tension

Normative prose requires implementor-prefixed non-standard types, while the dated generated schema includes unprefixed OpenAI-derived hosted types. Selected precedence:

1. normative prose governs portability and extension policy;
2. dated OpenAPI governs recognized shape and required presence;
3. official compliance defines executable minimum behavior;
4. portable common forms are typed;
5. recognized dated provider-derived forms are exact capability/dialect gated;
6. newly unknown non-standard forms must be implementor-prefixed and bounded;
7. no opaque extension may cross to an incompatible implementor.

### 10. Unsupported background/conversation modes

The dated schema contains optional controls without a complete proxy job-resource contract. The initial profile must reject unsupported modes rather than accept and ignore them.

## Brownfield Test Architecture Findings

### Independent reference clients

`internal/refclient` is explicitly test-only. Existing subpackages exercise vendor-shaped requests and parse responses as real applications would. Production core/protocol packages must not import them.

For OpenResponses, the lack of an official suitable Go SDK changes implementation technique but not the independence requirement. `internal/refclient/openresponses` should use separately maintained wire structs/parsers or fixture-driven request builders aligned to the pinned schema. It may share immutable bytes, digests, and neutral test fixture data, but not the production OpenResponses encoder/decoder/state machine.

### Independent remote backend emulators

`internal/refbackend` provides test-only spec-shaped server emulators for OpenAI Responses, OpenAI Chat, Anthropic Messages, Gemini, Bedrock, and ACP, with provider-specific helpers for additional connectors. These servers validate backend connectors without live provider networks.

`internal/refbackend/openresponses` must be independently implemented and scriptable. It must capture requests, assert exact fields, emit valid and invalid JSON/SSE/compact/WS sequences, simulate errors and timing, and expose a request counter so negative capability/projector tests prove that upstream work never began.

### Authoritative FE×BE matrix

Current frontend IDs:

1. `openai-responses`
2. `openai-legacy`
3. `anthropic`
4. `gemini`

Current backend IDs:

1. `openai-responses`
2. `openai-legacy`
3. `anthropic`
4. `gemini`
5. `bedrock`
6. `acp`
7. `openrouter`
8. `nvidia`

Current product: 4 × 8 = 32 cells.

After this feature:

- frontend IDs add `openresponses`;
- backend IDs add `openresponses` (the generic compatible implementation used against the reference backend);
- product becomes 5 × 9 = 45 cells.

The matrix must remain generated from authoritative lists. A new frontend or backend must fail completeness checks until every new cell and feature status is classified.

### Existing matrix metadata is insufficient

Current viability flags are useful but too coarse for OpenResponses. Per-cell evidence must classify at least text JSON, streaming, roles/instructions, history, tools, multimodal input/output, usage/finish/errors, reasoning/replay, assistant phase, item references, continuation, compaction, extensions, cancellation/backpressure, and commitment.

Allowed outcomes:

- `lossless`;
- `documented_deterministic_projection`;
- `rejected_before_network`; or
- `out_of_scope` only where no product surface exists.

No silent drop or ambiguous “supported” boolean is acceptable.

## Emulator Independence Decision

### Selected

Build two independent test implementations:

- client-side request/response/event behavior in `internal/refclient/openresponses`;
- server-side request validation and scripted output in `internal/refbackend/openresponses`.

Share only immutable pinned fixtures, source digests, and protocol-neutral test data. Add architecture tests preventing production imports and preventing the emulators from importing production OpenResponses adapter/profile codec packages.

### Rejected

Using the production codec to generate reference-client requests or parse reference-backend responses.

### Rationale

Such tests would verify that one implementation agrees with itself. They would miss shared mistakes in required-field presence, discriminators, event ordering, extension validation, null/default handling, and continuation semantics. Independent wire implementations plus official fixtures create triangulated evidence:

1. production frontend versus independent client;
2. production backend versus independent provider;
3. independent client versus independent provider; and
4. official compliance against the complete proxy path.

## Cross-API Compatibility Expectations

Compatibility is defined through the canonical middle. No pairwise translator package is permitted. Each path requires a capability audit, positive portable-subset tests, and negative zero-upstream tests for non-representable semantics.

### Forward: OpenResponses frontend to existing backends

| Path | Expected portable subset | Mandatory fail-closed audit |
|---|---|---|
| OpenResponses → OpenAI Chat Completions | ordered text messages, compatible roles after explicit projection, standard tools/results, common sampling/usage, supported image/file forms | assistant phase, item references, compaction, provider-bound replay/extensions, structured outputs not representable by target, video or unsupported annotations |
| OpenResponses → OpenAI Responses | broad item/message/tool/reasoning intersection and streaming lifecycle through the existing OAI adapter | OpenResponses-only required presence is frontend-owned; reject nonportable extension dialects, unsupported phase/compaction/native continuation semantics rather than relying on OAI coincidence |
| OpenResponses → ACP | text prompt-turn subset and permitted resource/reference content | tools, tool results, unsupported multimodal forms, phase, reasoning replay, compaction, extensions, and full-agent features; request counter must stay zero |
| OpenResponses → Anthropic Messages | text/system/developer normalization where documented, tools/results, image/document input, common sampling/usage | phase, compaction, item references without equivalence, provider-bound encrypted replay, video/extension forms not supported by Messages |
| OpenResponses → Gemini/Vertex | contents/system instruction, text, compatible tools/function responses, supported inline/file inputs, common generation controls | phase, compaction, unsupported item references/replay/extensions, target-incompatible annotations or tool-output forms |
| OpenResponses → Amazon Bedrock | Converse messages/system blocks, tools/results, image/document input, usage/stop mapping | phase, compaction, provider-bound replay/extensions, unsupported item references/video/tool-output forms |
| OpenResponses → OpenResponses | full pinned portable profile plus explicitly configured compatible dialects | unsupported configured profile/dialect/extension; malformed lifecycle |
| OpenResponses → OpenRouter | portable intersection through provider connector plus explicitly typed OpenRouter behavior | unconfigured proprietary fields/dialects, incompatible provider failover |
| OpenResponses → NVIDIA | portable OpenAI-compatible intersection supported by the connector | phase/compaction/extensions/replay or fields the connector cannot express |

The exact portable subset is proven by tests, not assumed from protocol similarity.

### Reverse: existing frontends to OpenResponses backend

| Path | Expected projection into ordered OpenResponses input | Mandatory fail-closed audit |
|---|---|---|
| OpenAI Chat Completions → OpenResponses | legacy message-authority call projected into ordered message items; compatible tools/results, multimodal parts, sampling, streaming | conflicting authority, unsupported extensions, semantics not representable by pinned OR input |
| OpenAI Responses → OpenResponses | existing canonical messages/parts/reasoning projected into ordered items, preserving exact replay dialect only when compatible | OpenAI-specific provider replay/extensions without exact OR backend capability; dropped known fields forbidden |
| Anthropic Messages → OpenResponses | system/messages/content blocks/tools/results converted to ordered OR items | Anthropic thinking signatures or provider-bound blocks without compatible dialect; unsupported cache/vendor controls |
| Gemini/Vertex → OpenResponses | contents/systemInstruction/function calls/responses and supported multimodal parts projected to ordered items | Gemini-specific cached-content/provider controls or content forms without a portable OR representation |
| OpenResponses → OpenResponses | item-authority pass through canonical validation and profile mapping | conflicting authorities, malformed lifecycle, unsupported dialect |

The OpenResponses backend must explicitly support both canonical item-authority and legacy message-authority input. Legacy messages are not an accidental fallback path; they require a tested constructor/projector.

## Conformance Strategy

### Pinned official scenarios

The immutable official suite includes basic response, assistant input/output phase, SSE, system prompt, tools, image input, multi-turn, compaction/missing model, WebSocket response/sequential/continuation/reconnect/missing-parent/eviction/compact-new-chain scenarios.

### Evidence layers

1. **Source/profile evidence**: pinned schema, normative source, compliance digest, license.
2. **Independent wire evidence**: `refclient/openresponses` directly against `refbackend/openresponses` for JSON, SSE, compact, WS, errors, tools, multimodal, phase, continuation, and required presence.
3. **Canonical contracts**: items, authority, projectors, requirements, continuation materialization, compaction.
4. **Production codec/state-machine unit tests**: unions, presence, lifecycle, error mapping.
5. **Frontend adapter evidence**: independent refclient → OpenResponses frontend → canonical stub.
6. **Backend adapter evidence**: canonical fixtures → OpenResponses backend → independent refbackend.
7. **Complete FE×BE matrix**: all 45 baseline text cells, supported streaming cells, positive feature subsets, and negative pre-network rejection cells.
8. **Protocol-specific parity suites**: reasoning/replay, assistant phase, item references, continuation, compaction, extensions, multimodal output, and ACP exclusions that cannot be proven by one generic text matrix.
9. **Full black-box evidence**: independent client → real frontend → core → real backend → independent provider emulator.
10. **Official suite**: pinned runner against the full black-box deployment.
11. **Robustness**: race, leak, fuzz, malformed events, backpressure, cancellation, failover/commitment, redaction, reload, shutdown.
12. **Coverage quality**: named scenario/branch registry, coverprofiles, no unexplained regression, and ≥90% target for new deterministic codec/state-machine/emulator packages unless reviewed exceptions document generated/unreachable branches.

The official suite is necessary but insufficient because it does not cover all Go-LIP translation cells, failover, security, adapter boundaries, and negative capability behavior.

## Quality and Coverage Interpretation

The project correctly treats tests as executable contracts rather than maximizing test count. Therefore:

- coverage percentage is a guardrail, not the compatibility definition;
- every supported semantic needs a named positive scenario;
- every unsupported semantic needs a named pre-network negative scenario;
- every matrix cell needs linked evidence;
- branch/error/lifecycle paths deserve explicit tests even when statement coverage is already high;
- new deterministic packages target at least 90% statement coverage, but a lower number is acceptable only with a reviewed explanation of generated or unreachable paths;
- no unexplained coverage regression is acceptable in touched adapter/canonical packages.

## Security Findings

Primary threats include cross-tenant ID probing, lineage confusion, raw native-ID leakage, extension smuggling, oversized opaque payloads, continuation amplification/cycles, WebSocket origin abuse, unbounded queues/state, event injection, sensitive logging, and reload/shutdown ownership leaks.

Compatibility testing adds further threats:

- silent semantic downgrade during projection;
- sending unsupported data before capability rejection;
- cross-provider opaque replay leakage on failover;
- emulator self-consistency masking production defects;
- matrix cells marked viable without exercising real wire handlers;
- reference backend request capture leaking sensitive fixture data.

Controls include exact capability requirements, bounded projectors, zero-upstream negative assertions, independent emulator implementations, redacted request capture, deterministic limits, and exactly-once lifecycle ownership.

## Performance and Scale Considerations

- SSE/WS remain incremental; no full collection on streaming paths.
- Non-streaming/compact aggregation is explicitly bounded.
- Continuation materialization avoids recursive round trips and quadratic copies.
- Matrix tests remain deterministic and in-process using `httptest`; slow/external soak variants are separately gated.
- Emulator scripts use bounded queues/buffers and virtual clocks instead of sleeps.
- The 45-cell baseline should be table-driven and parallelized only where ownership/global configuration permits.
- Feature-specific suites should avoid duplicating the entire matrix when a smaller canonical/projector contract test localizes failures better.

## Adjacent Architecture Revalidation

### Backend plugin ABI

Implementation must determine how external connectors advertise/execute OpenResponses create, compaction, item/extension dialects, assistant phase, continuation materialization, and opaque output. ABI DTOs and fake connector conformance must advance before support is claimed.

### Generic compatible backend modes

The new backend is a dependency-free built-in protocol-family mode with independent instances, shared endpoint/credential/inventory/admission/diagnostics infrastructure, and no provider-specific policy leakage.

### LLM API parity framework

The authoritative FE×BE lists, matrix completeness, feature metadata, frontend mounting, backend constructors, and parity evidence index must be updated. OpenResponses becomes both a protocol row and backend column, not a special test outside the parity framework.

## Design Validation Findings and Corrections

| ID | Severity | Initial risk | Correction |
|---|---:|---|---|
| DV-01 | P0 | OpenResponses treated as OAI alias. | Separate identities/routes/packages/profiles. |
| DV-02 | P0 | Raw JSON bypasses canonical policy. | Typed neutral items plus bounded opaque forms. |
| DV-03 | P0 | Client continuation IDs expose provider state. | Proxy IDs and canonical materialization. |
| DV-04 | P0 | Compaction bypasses routing. | Core-routed operation. |
| DV-05 | P0 | Route collision resolved by order. | Pre-serve route claims. |
| DV-06 | P0 | Plugin ABI omits new operations/caps. | Explicit ABI revalidation. |
| DV-07 | P1 | Upstream WS required prematurely. | Client WS over upstream HTTP/SSE. |
| DV-08 | P1 | Extension prose/schema conflict. | Pinned precedence and exact capability gating. |
| DV-09 | P1 | Legacy/item authorities diverge. | One-authority validation and explicit projectors. |
| DV-10 | P1 | Shared helpers regress OAI. | Characterization/differential tests first. |
| DV-11 | P1 | `store:false` persists. | Connection-local typed store. |
| DV-12 | P1 | ID lookup becomes oracle. | Scoped indistinguishable not-found. |
| DV-13 | P1 | Unknown events dropped. | Bounded opaque output. |
| DV-14 | P1 | Sparse resources fail schema. | Required-presence builder. |
| DV-15 | P1 | Unfit dependency adopted. | Project-owned codec and dependency gate. |
| DV-16 | P0 | No independent client emulator. | Add black-box `refclient/openresponses`. |
| DV-17 | P0 | No independent provider emulator. | Add scriptable `refbackend/openresponses`. |
| DV-18 | P0 | Requested cross-API paths omitted. | Add OpenResponses row/column to 45-cell matrix. |
| DV-19 | P0 | Pairwise translators proliferate. | Canonical projectors only. |
| DV-20 | P0 | Tests reuse production codec and agree with themselves. | Enforce emulator independence with architecture tests. |
| DV-21 | P0 | Unsupported semantics silently downgrade. | Feature-level outcomes and zero-upstream rejection. |
| DV-22 | P1 | High coverage claim lacks scenario proof. | Scenario registry plus coverprofile/no-regression/90% deterministic-package target. |
| DV-23 | P1 | Official suite exercises only frontend or only backend. | Run full independent client→proxy→provider path. |

## Final Validation Verdict

**GO only with the emulator and compatibility-matrix expansion.**

The core architecture remains sound, but excellent compatibility cannot be inferred from protocol similarity or a single end-to-end path. Completion requires independently implemented client/server emulators, explicit canonical projectors in both authority directions, all 45 matrix cells, feature-level positive/negative evidence, and official conformance on the full path.

## Open Implementation Decisions

May be finalized during contract-first tasks:

1. Exact internal names for emulator request/response helper types.
2. Whether immutable official fixtures are copied into each emulator package or shared under a neutral testdata directory.
3. Exact matrix feature-enum/type names and evidence registry format.
4. Which supported feature suites run in default versus tagged/precommit jobs, provided deterministic baseline cells remain mandatory.
5. Exact reviewed coverage exceptions for generated union boilerplate.
6. Exact plugin ABI version increment.
7. Exact recognized provider-derived item/tool catalog.
8. Whether native continuation optimization ships initially.

Require design revalidation:

- merging OpenResponses and OpenAI protocol identities;
- forwarding client response IDs upstream;
- bypassing core for compaction;
- raw full-request tunneling;
- arbitrary header forwarding;
- failover across incompatible opaque dialects;
- pairwise translator packages;
- production reuse of test emulator code;
- emulator reuse of production OpenResponses wire codecs;
- removing the complete matrix or accepting silent/skipped required cells;
- requiring upstream persistent WebSocket initially.
