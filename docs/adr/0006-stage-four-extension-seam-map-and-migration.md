# ADR 0006: Stage four extension seam map, brownfield migration, and reload assumptions

## Status

Proposed (stage four — extension platform). Authoritative product detail lives in [`.kiro/specs/archive/go-core-stage-four-feature-extension-platform/`](../../.kiro/specs/archive/go-core-stage-four-feature-extension-platform/).

## Context

The Go proxy already has registry-driven composition, a small orchestration core, and a **feature hook bus** (submit, request-part, response-part, tool reactors). That is enough for simple feature plugins. Advanced behaviors (session openers, tool catalog filtering, completion gates, auxiliary requests, traffic capture, transport auth) need **named extension stages**, **typed context views**, **narrow service facades**, and **operator-visible inventory** so features stay in plugins instead of leaking back into the core.

This ADR records the **seam map**, **privileged boundaries**, **brownfield rules** for absorbing the hook bus into a richer model, and **reload-related non-goals** for this stage.

## Decision

### 1. Stage-owned extension seams (legal pipeline)

The core owns the **ordered list of legal extension stages**. Plugins attach only to these stages; they must not invent ad hoc runtime stages. The minimum ordered pipeline (requirement **R2**) is:

1. Transport authentication / identity attachment (standard HTTP layer — `stdhttp`, not core business logic).
2. Session open / session context resolution.
3. Submit-time request enrichment / rejection (today’s submit hooks live here in spirit).
4. Tool catalog filtering.
5. Request-wide shaping and history/context shaping.
6. Route hinting.
7. Attempt lifecycle hooks / observers.
8. Stream event mutation (response-part style hooks).
9. Tool event reaction.
10. Completion gating / buffering / replacement.
11. Traffic observation and capture.
12. Egress encoding.

Note: `attempt_lifecycle` and `egress_encoding` are legal pipeline labels for inventory and policy. They do not imply a dedicated plugin-handler slice in the typed `FeatureBundle`; `attempt_lifecycle` is owned by the core attempt loop, and `egress_encoding` remains inside frontend/transport encoding.

**Intra-stage ordering** (design section 17): for handlers within one stage, ordering is:

1. Explicit stage `order` / priority (ascending),
2. Plugin instance or bundle id (ascending, stable),
3. Registration sequence tie-break (ascending).

The same order must eventually appear in **diagnostics/inventory** so operators can review runtime behavior.

### 2. Privileged-contract boundaries

Some capabilities are **privileged** and must remain visible in inventory/diagnostics (design section 14, requirement **R14**), including at least:

- Raw capture access,
- Auxiliary-request access,
- Auth-provider role (transport auth),
- Completion-gate role.

Inventory must surface extension truth: which stages a plugin participates in, ordering, failure mode, and privileged flags — not only opaque plugin rows.

### 3. Brownfield migration guardrails (hook bus preservation)

Per design section 15:

1. **Keep** the current hook bus and hook interfaces as the first compatibility layer.
2. Introduce **`FeatureBundle`** (typed, versionable) assembly **in parallel** with existing hook-chain construction.
3. Adapt hook-only plugins into bundle form **mechanically** at registration/composition time.
4. Move new seams into stage runners **one concern at a time**.
5. Retire compatibility shims only after the richer surface is proven by tests and reference plugins.

**Guardrails:**

- Unchanged frontends, backends, routing, and existing feature plugins must preserve behavior while new seams land.
- Wrapper/adapter layers are acceptable.
- Unrelated provider, routing, and transport packages must not require broad rewrites solely to adopt new extension seams.
- The hook bus remains a **supported registration path** during migration so feature logic is not duplicated across competing extension systems.

### 4. Reload-friendly assumptions and explicit non-goals (Q7 / design section 15B)

**Assumptions for this stage (compatibility only):**

- Composition should be able to produce an **immutable execution snapshot** (active bundles, stage runners, service bindings).
- Each request should execute against **one stable snapshot** for its full lifetime (even if operators later swap configuration).
- Stage assembly and facades should avoid **hidden process-wide mutable globals** where a snapshot model can replace them.
- Plugin lifecycle should allow future explicit activation/deactivation rather than **ambient singleton** state.

**Explicit non-goals for this stage:**

- No operator-triggered config reload APIs.
- No rollback policy for invalid config snapshots.
- No in-flight swap semantics across listeners, routes, or continuity stores.
- No guarantee that every backend/transport/store integration is reload-safe yet.

Those reload capabilities are owned later by [ADR 0008](0008-versioned-runtime-config-reload.md) / [runtime-config-reload.md](../runtime-config-reload.md), not by stage four.

### 5. Anti-backsliding and proof exit (design sections 18–19, R16)

Design section 18 rules apply once a seam exists (e.g. no new advanced feature in core when a seam fits; auxiliary traffic only through published services).

Stage four is **exit-complete** only when **reference proof plugins** demonstrate seams without feature-specific core branches (R16 / design section 19): session-start shaping, tool policy, workspace safety, traffic observer/capture, completion-gate + auxiliary — each with tests that assert invariants, not only happy paths, and inventory showing stage occupancy and privileges where applicable.

## Consequences

- New code and tests may land under `internal/core/extensions/`, richer inventory under `internal/core/diag` / `internal/stdhttp`, and stable contracts under `pkg/aipsdk/...` per the feature spec’s tasks.
- Architecture import tests and complexity budgets in [`internal/archtest/guardrails_test.go`](../../internal/archtest/guardrails_test.go) may need intentional bumps when extension scaffolding grows; document rationale in PR or ADR 0005.

## Related documents

- [Architecture guardrails](../architecture-guardrails.md) — automated budgets and composition-root rules.
- [ADR 0001](0001-registry-driven-composition.md), [ADR 0005](0005-architecture-guardrails-and-complexity-budgets.md) — composition and budgets.
- Spec: [requirements](../../.kiro/specs/archive/go-core-stage-four-feature-extension-platform/requirements.md), [design](../../.kiro/specs/archive/go-core-stage-four-feature-extension-platform/design.md).
