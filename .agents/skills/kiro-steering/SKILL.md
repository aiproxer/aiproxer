---
name: kiro-steering
description: Manage .kiro/steering/ as persistent project knowledge
metadata:
  shared-rules: "steering-principles.md"
---

# Kiro Steering Management

<background_information>
**Role**: Maintain `.kiro/steering/` as durable project memory.

**Mission**:
- Bootstrap: derive enduring architecture/product/testing guidance from the codebase.
- Sync: keep steering aligned when governing rules change.
- De-volatilize: remove copied inventories/version snapshots that are better represented by executable sources of truth.
- Preserve: protect user intent and valuable custom rules, not stale derived-state prose.

**Success Criteria**:
- Steering captures invariants, ownership boundaries, standards, and decision procedures.
- Volatile state is referenced through authoritative registries/manifests/parsers/module files rather than duplicated.
- Ordinary additions that follow an existing pattern do not create steering churn.
- Contradictory or stale steering is corrected rather than preserved for history.
- All `.kiro/steering/*.md` are treated as working project memory, including custom files.
</background_information>

<instructions>
## Scenario Detection

Check `.kiro/steering/` status:

**Bootstrap Mode**: Empty or missing the core product/technology/structure guidance.

**Sync Mode**: Core steering exists.

---

## Bootstrap Flow

1. Load templates and this skill's `rules/steering-principles.md`.
2. Analyze the codebase just-in-time:
   - product/architecture documentation for enduring promises;
   - composition/registry/manifest code for ownership patterns;
   - module/config/build files for technology and verification policy;
   - architecture/QA tests for enforced boundaries.
3. Separate **rules** from **derived state** before writing:
   - Rule: "optional backends are manifest-discovered executable connectors" → steering.
   - Derived state: "the optional connectors are A/B/C/..." → point to manifests, do not copy list.
4. Generate concise domain-focused steering.
5. Verify references are repository-relative and portable.
6. Present a summary of durable decisions captured and executable sources referenced.

**Focus**: future decision quality, not a snapshot of current implementation inventory.

---

## Sync Flow

1. Read all existing `.kiro/steering/*.md`.
2. Inspect the code/docs/tests relevant to each steering domain.
3. Classify observed drift:
   - **Rule drift**: ownership/invariant/standard/procedure changed → update steering.
   - **Derived-state drift**: another provider/feature/package follows the same pattern → normally no steering update; generalize/remove stale inventory prose if present.
   - **Refactor drift**: implementation moved but responsibility stayed the same → update only durable lookup pointers if necessary.
   - **Contradiction**: steering no longer describes enforced behavior → correct/replace it.
4. Prefer executable sources of truth for volatile facts: registries, manifests, catalogs, parsers, `go.mod`, `Makefile`, architecture tests.
5. Remove machine-local absolute links, copied version numbers/counts, CI implementation details, and exhaustive inventories unless they are themselves a deliberate contract.
6. Preserve user-authored intent while rewriting stale representations when needed.
7. Report what changed at the rule level and what volatile content was deliberately *not* copied.

**Update Philosophy**: additive for new durable rules; corrective replacement/deletion for stale or derived-state duplication. Git is the history.

---

## Granularity Test

Before adding a statement, ask:

1. Will it still matter after several ordinary features/providers/packages are added?
2. Does it tell a future agent how to decide or what must never break?
3. Is the current value mechanically discoverable elsewhere?

If the answer is "no" to 1/2 or "yes" to 3 without a strong reason to duplicate it, keep it out of steering and point to the authoritative source instead.

## Common Anti-Patterns

**Bad**: list every provider, feature, package, connector, test job, or exact version.

**Good**: state the architectural class, ownership rule, and source of current inventory.

**Bad**: preserve stale prose because updates are supposed to be additive.

**Good**: preserve user intent, delete snapshot noise, and correct contradictions.

**Bad**: machine-local `file://` links.

**Good**: repository-relative paths or plain package/file references.
</instructions>

## Tool Guidance

- Use repository search/tree inspection to find authoritative sources.
- Read architecture/QA tests when a boundary is supposed to be enforced.
- Prefer targeted inspection over dumping the whole repository into steering.
- After updates, search steering for machine-local links and obvious inventories/version snapshots.

## Output Description

Update files directly, then summarize:

- durable rules added/changed;
- stale or volatile material removed/generalized;
- executable sources of truth referenced;
- unresolved contradictions or gaps, if any.

## Safety & Fallback

- Never include credentials, secrets, private database URLs, or sensitive infrastructure details.
- When evidence conflicts, report the conflict and prefer executable behavior/tests over stale prose.
- Do not invent a current inventory from partial repository reads.

## Notes

- All `.kiro/steering/*.md` are working project memory.
- Templates are starting points, not required shapes.
- Ordinary code following existing patterns should not require steering updates.
- Steering is not the place for `.kiro/` metadata documentation or agent-tooling inventories.
