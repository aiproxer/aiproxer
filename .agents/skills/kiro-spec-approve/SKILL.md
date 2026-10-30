---
name: kiro-spec-approve
description: Approve a spec's requirements/design/tasks and mark it ready for implementation
---


# Spec Approval (Mark Ready for Implementation)

<instructions>
## Core Task
When the user asks to approve a Kiro spec or mark it ready for implementation, update `.kiro/specs/{feature-name}/spec.json` statuses. Do NOT auto-approve on your own — only act on an explicit user request.

## Execution Steps
1. **Locate spec**: Read `.kiro/specs/{feature-name}/spec.json` (feature-name is $1, or the spec the user named).
2. **Precondition check**:
   - All spec artifacts exist: `requirements.md`, `design.md`, `tasks.md` (plus `research.md`/`gap-analysis.md`/`design-review.md` when present).
   - Spec is not archived (`specs/archive/`), not already `phase: completed`, and not `ready_for_implementation: true`.
   - Stop and report if preconditions fail; do not force the status.
3. **Update statuses atomically with jq** (never hand-edit or sed the JSON):
   - `phase` → `"ready-for-implementation"`
   - `approvals.requirements.approved`, `approvals.design.approved`, `approvals.tasks.approved` → `true` (keep `generated: true`)
   - `ready_for_implementation` → `true`
   - `updated_at` → current local time, exact same format as `created_at` (e.g. `2026-08-15T13:58:10+02:00`); build the timestamp in one jq step — avoid sed/shell string surgery that mangles the `+02:00` offset.
4. **Validate**: Confirm the file is valid JSON (`jq empty file` succeeds) and the final statuses read correctly.
5. **Leave the rest untouched**: Do not edit `tasks.md` checkboxes (they stay `[ ]`; they are checked during implementation), and do not change `project_description`, `language`, or `created_at`.
6. **Commit only if asked**: Leave the change uncommitted by default — the implementation commit typically carries the spec.json update. If the user asked for a commit, commit just the spec.json change with a message like `spec: approve {feature-name} for implementation`.

## Completion Report
Respond with a short confirmation listing: new `phase`, all three `approved: true`, `ready_for_implementation: true`, and the path touched. Suggest `/kiro-impl {feature-name}` as the next step.
</instructions>

## Safety & Fallback

### Error Scenarios

- **Spec Not Found**: `.kiro/specs/{feature-name}/` missing → report and stop.
- **Artifacts Missing**: No `tasks.md` (or unapproved requirements/design) → point to `/kiro-spec-requirements`, `/kiro-spec-design`, `/kiro-spec-tasks` and stop.
- **Already Approved**: `ready_for_implementation` already `true` → report current state, make no changes.
- **Archived/Completed Spec**: Under `specs/archive/` or `phase: completed` → never re-approve; report and stop.

### Conventions (do not deviate)
- Phase value is exactly `"ready-for-implementation"` (historical repo convention).
- All three approvals flip together — partial approval is not a supported state.
- One spec at a time; no batching across specs unless explicitly requested.
