# Steering Principles

Steering files are **durable project memory**, not status reports, inventories, or compressed architecture documentation.

## Golden Rule

> If code changes while the governing pattern, ownership rule, or decision procedure stays the same, steering should usually not change.

A useful steering statement should remain true across many ordinary feature additions and refactors.

## What Belongs in Steering

Document facts that guide future decisions:

- architectural invariants and dependency direction;
- ownership boundaries and placement rules;
- compatibility/safety/security standards;
- lifecycle and concurrency rules;
- testing and verification procedures;
- naming/authoring conventions that are intentionally project-wide;
- decision criteria for choosing among allowed patterns;
- pointers to executable sources of truth for volatile state.

Prefer rules with rationale over summaries of what currently exists.

## What Does Not Belong in Steering

Avoid duplicating state that can be derived from the repository and can change without architectural meaning:

- provider, connector, feature, model, package, endpoint, or test inventories;
- complete directory/file listings;
- dependency/version snapshots that already live in module/config files;
- exact counts of registered components/planes/plugins;
- copied protocol matrices or registry tables;
- CI job names, workflow predicates, container versions, or full Make dependency graphs;
- implementation history, migration diary, PR narrative, or changelog prose;
- machine-local absolute paths or `file://` links;
- agent-specific tooling directories unless the tooling rule itself is the subject.

If an agent can answer "what exists right now?" by reading a registry, manifest, catalog, parser, `go.mod`, `Makefile`, or repository tree, steering should normally point there rather than copy the answer.

## Durable Source-of-Truth Pattern

For volatile state, write:

- the architectural class/invariant;
- which executable source owns the current inventory or grammar;
- the rule for adding/changing entries.

Example:

**Bad:**
> Optional backends are A, B, C, D, and E.

**Good:**
> Optional backends are executable connector modules discovered through trusted manifests. The manifests under `connectors/` are authoritative; adding another connector does not change steering unless the connector architecture changes.

## Update Policy

When synchronizing steering with the codebase, classify drift first:

1. **Rule drift** — ownership, invariant, standard, or procedure changed. Update steering.
2. **Derived-state drift** — another implementation follows the existing rule. Do not extend the inventory; remove/generalize stale snapshot prose if present.
3. **Implementation refactor** — files/packages moved but responsibility stayed the same. Update only a durable lookup pointer if necessary.
4. **Contradiction** — steering states something no longer true. Correct or replace it; preservation does not mean retaining misinformation.

Preserve user intent and valuable custom guidance, not stale wording. Additive edits are appropriate for new durable rules; deletion/rewrite is appropriate when reducing derived-state duplication or correcting drift.

Git is the history. Do not add `updated_at`, change-reason, history, or changelog footers to steering.

## Quality Standards

- **One domain per file**: product, structure, API, routing, technology, testing, etc.
- **First principles first**: start with invariants and decision rules.
- **Source pointers, not snapshots**: use repo-relative paths to executable truth.
- **Explain why** when a rule is non-obvious or prevents a recurring architectural failure.
- **Maintainable size**: concise enough to be loaded as working memory; split a genuinely distinct domain rather than growing an omnibus file.
- **No secrets**: never include credentials, database URLs, private infrastructure addresses, or sensitive data.
- **No machine-local links**: repository references must be portable across machines/worktrees.

## File-Specific Focus

- **product.md** — purpose, enduring product contract, product boundaries, non-goals, decision rules.
- **tech.md** — technology choices, runtime/lifecycle/concurrency standards, verification intents; not dependency/version inventory.
- **structure.md** — zones, ownership, dependency direction, placement rules; not a package tree.
- **api-standards.md** — canonical/wire boundaries, capability/error/dialect rules, protocol change procedure; not supported-surface inventory.
- **routing-and-orchestration.md** — planning, commitment, recovery, terminal/generation lifecycle ownership; parser remains grammar source of truth.
- **testing.md** — evidence strategy, cost policy, test selection/triage procedures; not current CI implementation details.

## Bootstrap and Sync Test

Before adding a steering statement, ask:

1. Would this still matter after several ordinary providers/features/packages are added?
2. Does it tell a future agent how to decide or what must never break?
3. Is the current value already mechanically discoverable elsewhere?

If (1) or (2) is no, or (3) is yes without a durable reason to duplicate it, the statement probably belongs in normal documentation/code rather than steering.
