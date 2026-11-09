# Kiro Workspace

Specs are opt-in: they apply when work is triggered by `/kiro:*`, a path under `.kiro/specs/`, or an explicit spec request. Otherwise follow root [`AGENTS.md`](../AGENTS.md).

```text
.kiro/
├── specs/
│   ├── {feature-name}/  one active spec: spec.json, requirements.md, design.md, tasks.md, research.md
│   └── archive/         completed and superseded specs; read-only history, pre-rebrand names kept
├── settings/templates/  spec and steering templates read by the kiro-* skills
└── steering/            durable project memory; edited through the kiro-steering skill
```

- Spec workflow, one phase per skill: `.agents/skills/kiro-*/` (rules under each skill's `rules/`).
- Slice and spec budgets, deferral: [`steering/delivery.md`](steering/delivery.md).
- When a design must be revalidated: the `kiro-validate-design` skill.
- Closing a spec: the `kiro-spec-archive` skill.
