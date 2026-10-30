# Kiro Spec-Driven Development - Agent Guidelines

## Opt-In Scope

Applies ONLY when explicitly triggered via `/kiro:*`, explicit paths under `.kiro/specs/`, or spec requests. Otherwise follow root [`AGENTS.md`](../AGENTS.md).

---

## Core Invariants

- **Slice First**: A spec describes one V1 slice plus a Deferred list, within the budgets in [`steering/delivery.md`](steering/delivery.md). When the slice does not fit, split the spec; do not grow it.
- **Spec First, Code Second**: No code edits before approved `requirements.md` and `design.md`.
- **TDD Mandatory**: Write failing test first, implement second. Tests live inside implementation tasks and follow Test Proportionality in `steering/testing.md`.
- **Core Policy vs Edge Adapters**: Core owns orchestration, routing (including A-leg overrides), failover, B2BUA, and the two billing seams (cheap credit screen, then atomic operational exposure admission plus terminal usage). Provider SDKs stay strictly in edge plugins.
- **Streaming First**: Non-streaming is collection over canonical streams (`pkg/aipapi`).
- **No Retry Post-Output**: Never retry or failover after the first downstream content event.

---

## Spec Directory Layout

```text
.kiro/
├── specs/
│   ├── {feature-name}/  (active: spec.json, requirements.md, design.md, tasks.md, research.md)
│   └── archive/         (completed and superseded specs)
├── settings/templates/  (spec and steering templates read by the kiro-* skills)
├── steering/            (product.md, delivery.md, api-standards.md, routing-and-orchestration.md, structure.md, tech.md, testing.md)
└── AGENTS.md
```

---

Spec rules live with the skills that apply them: `.agents/skills/kiro-*/rules/`.

## Workflow Phase Checklist

- **Phase 0 (Steering)**: `/kiro:steering` — Update `.kiro/steering/` durable project memory.
- **Phase 1 (Init)**: `/kiro:spec-init` — Initialize feature workspace and `spec.json`.
- **Phase 2 (Requirements)**: `/kiro:spec-requirements` — Write testable EARS requirements for the V1 slice and list deferred scope. Specify core vs frontend/backend/feature plugin boundaries.
- **Phase 2.5 (Gap Analysis)**: Compare requirements against current code paths.
- **Phase 3 (Design)**: `/kiro:spec-design` — Define architecture & interfaces. Must explicitly verify:
  1. Core vs plugin ownership.
  2. Canonical model neutrality (`pkg/aipapi`).
  3. Streaming-first compatibility.
  4. Zero provider SDK leaks into core.
  5. Post-output retry prevention.
  6. Proportionality: every component serves a V1 requirement, and existing seams are reused before new stages, planes, stores, or contracts are added.
- **Phase 3.5 (Validation)**: `/kiro:validate-design` — Validate core orchestration or API contract changes.
- **Phase 4 (Tasks)**: `/kiro:spec-tasks` — Create testable TDD implementation tasks with `_Boundary:_` and `_Depends:_`.
- **Phase 5 (Impl)**: `/kiro:spec-impl` — TDD execution (red -> green -> refactor).

---

## Revalidation Triggers

Re-run design validation and integration tests when changing:
- Canonical request/event contracts (`pkg/aipapi`), including tool classification.
- Selector syntax, routing semantics, or A-leg routing overrides (`internal/core/routing`, `internal/core/routeoverride`).
- B2BUA continuity or authority coordination rules (`internal/core/authoritycoord`).
- Plugin registration contracts (`pkg/aipsdk`, `internal/standardplugins`).
- Billing exposure/usage, journal, admission, or host injection (`internal/core/billing`, `runtimebundle.ComposeBilling`).
