# Product Overview (Steering)

## Purpose

This file is subordinate to [`VISION.md`](../../VISION.md), the owner-ratified product vision. When they conflict, the vision wins.

AIProxer is a universal compatibility layer and protocol-neutral control plane between AI clients/agents and model backends. Its durable product promise is to let any popular client work with any popular backend, and to let clients and backends evolve independently, while the proxy owns cross-provider execution policy, continuity, safety, and observability.

Steering describes the architectural rules that keep that promise. It is intentionally **not** a catalog of currently implemented providers, connectors, feature plugins, or protocol versions.

## Enduring Product Contract

1. **Canonical translation, not pairwise translation**
   - Frontends decode wire protocols into canonical `pkg/aipapi` contracts.
   - Backends consume canonical calls and emit canonical events.
   - Adding a frontend or backend must not create frontend×backend translators.

2. **Streaming is the primary execution model**
   - Streaming semantics define the request/response lifecycle.
   - Non-streaming behavior is collection over the same canonical event path, not a second execution engine.

3. **Core owns shared execution semantics**
   - Routing, candidate planning, failover/races, output commitment, B2BUA attempt lifecycle, continuity, and other provider-neutral orchestration stay in the core.
   - Provider- or feature-specific policy reaches the core through explicit SDK contracts; it does not become concrete core branching.

4. **Capability gaps are handled explicitly, interoperability first**
   - Required semantics must be negotiated before upstream execution where possible.
   - When a backend cannot represent a request exactly, the default policy is an explicit, logged, operator-configurable degradation rather than refusal. Operators may select a strict policy that refuses instead.
   - Degradation is never silent, never fabricates conversation history, and never reorders model-authored content.

5. **Continuity and lineage are first-class**
   - A logical client turn and every backend attempt must remain attributable.
   - Recovery is bounded by downstream commitment: once client-visible output commits an attempt, transparent replay/failover is no longer allowed.

6. **Extensibility must preserve a small kernel**
   - Optional UX, safety, maintenance, reasoning, and workflow behaviors belong in feature plugins or infrastructure behind narrow SDK seams.
   - Compatible-provider growth is data-driven where a protocol family can be shared; dedicated adapters/connectors are used only when the wire/runtime contract genuinely differs.

7. **Security is fail-closed**
   - Client hints are not authority.
   - Secrets and provider diagnostics must not leak across the client boundary.
   - Unsafe exposure modes must be explicit and bounded.

 8. **Money is not stream orchestration**
   - Financial authorization, usage evidence, rating, settlement, and provider cost accounting remain separated from stream processing.
   - Money handling reaches the runtime only through attachment points: an optional monetary binding, a caller-owned usage-limit port, and optional protected admin mounts.
   - Terminal ownership appends `BillingCallID`-scoped, money-free usage evidence; rating and settlement belong to the money implementation and happen after usage.
   - Public runtime composition stays non-money. A host that needs those behaviours attaches them explicitly; an unattached port keeps its no-op default.
   - Ownership target: that money implementation moves to the Enterprise tree. The move is not finished — the legacy in-tree money composition is still here and still backs the stock composition, which declares the billing capability; the usage-authority implementation has been relocated to the Enterprise tree (task 6.1), so this tree keeps only the neutral usage-limit port and the unchanged provider quota window, and it backs no stock composition, because usage limits are enforced only through an injected port. The attachment ports are the contract that completes the move; they do not mean the rules or the monetary algorithms are gone.
   - No stream-time rating, journal I/O, or token-ledger money writes.

9. **Prompt-cache stability outranks byte savings**
   - Provider prompt-cache hits cost a small fraction of misses, so a miss the proxy causes is a real user cost, usually larger than any bytes it saved.
   - Model-bound content already sent in a session stays byte-stable on every replay. The proxy does not later rewrite, elide, truncate, summarize, reorder, or re-render earlier history, system/developer prefixes, or tool definitions.
   - A content transform is allowed only when it is a pure function of the item's own bytes, the history before it, and config, so it yields identical bytes on every replay without depending on proxy state or caches.
   - Proxy-owned dynamic state reaches the model append-only near the tail, not by mutating an earlier prefix.
   - The proxy does not inject proxy-answered tools or hidden model turns to save tokens; they cost extra round trips and create history the client transcript does not have.
   - Explicit, logged exceptions: recovery that would otherwise fail the session (for example context overflow), an operator config change, and routing or failover to another backend or model.

## Architectural Classes

The product is organized around durable classes rather than a fixed inventory:

- **Frontends** — driving adapters for client wire protocols.
- **Core** — provider-neutral orchestration, continuity, commitment, and shared policy mechanisms.
- **Backends** — driven adapters for essential in-process provider families and compatible protocol families.
- **Executable connectors** — optional out-of-process backend integrations discovered through trusted manifests.
- **Feature plugins** — optional behavior contributed through typed extension planes and host-feature bindings.
- **Infrastructure** — persistence, HTTP clients, observability, connector hosting, and other technology adapters.
- **Composition roots** — explicit assembly of the standard distribution and immutable runtime generations.

A new implementation that fits an existing class should normally **not** require a steering change.

## Where to Find Current Product State

Use executable sources for volatile inventories instead of copying them into steering:

- Bundled frontend/backend/feature contributions: `internal/standardplugins/`.
- Public standard-distribution requirements: `pkg/aipsdk/standard_bundle.go`.
- Compatible-provider profiles: `internal/providerprofiles/`.
- Optional executable connectors: connector manifests and release metadata under `connectors/`.
- Current operator-facing behavior and examples: `README.md` and `docs/`.

When these inventories change without changing an architectural rule, update the executable source/docs — not this file.

## Decision Rules for New Work

- If behavior exists only because of one wire protocol or provider, keep it at the adapter edge.
- If behavior is shared execution policy needed independently of optional features, it may belong in core.
- If behavior is optional product policy or UX, prefer a feature-owned implementation behind SDK contracts.
- If a provider is compatible with an existing protocol family, prefer a declarative profile before creating another in-process backend package.
- If an integration needs its own dependencies/process/runtime contract, prefer an executable connector over widening the root module.
- If correctness can be certified by family contracts and bounded real-stack tests, do not introduce Cartesian frontend×backend test matrices.

## Architectural Non-Goals

- No provider SDK or wire-format leakage into canonical/core packages.
- No pairwise protocol translators.
- No native Go `plugin` loading for backend extensibility.
- No DI/service-locator framework or reflection-based runtime registry.
- No feature-specific service map or request-time feature lookup in generic core composition.
- No stream-time financial rating/journal mutation.
- No context compression, elision, truncation, or retrieval tooling that rewrites already-sent history or adds hidden turns. Such proposals are closed, not deferred; the former `tool-result-elision` feature was removed for this reason.
- No steering maintenance whose only purpose is to mirror a changing provider, connector, package, feature, or version inventory.
