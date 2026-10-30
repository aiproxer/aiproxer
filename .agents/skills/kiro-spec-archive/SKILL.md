---
name: kiro-spec-archive
description: Close out a completed or superseded Kiro specification safely, including completion metadata, validation evidence, archive movement, dependency baselines, and Git delivery.
---

# Kiro Spec Archive

Use this skill for `/kiro-spec-archive {feature-name}` after implementation is complete, or when a user asks to close, archive, or certify a Kiro specification.

This is a **closeout workflow**, not an implementation workflow. It must not silently turn an incomplete spec into a completed one.

Repository `AGENTS.md`, `.kiro/AGENTS.md`, steering, and the target spec's own notes override this skill.

## Completion Contract

A spec may be archived only when all of the following are true:

1. Every implementation task in `tasks.md` is checked `[x]`.
2. The task completion gate is satisfied, including required regression tests and successor-baseline evidence.
3. The implementation is verified on `main` or its merge commit is directly confirmed through GitHub.
4. Required focused tests and repository documentation/spec checks pass.
5. Any explicitly deferred successor work is documented as deferred, not falsely marked complete.

If any condition is unresolved, stop and report the blocker. Do not set completion metadata merely because a PR exists or because the implementation branch is locally green.

## 1. Establish Ground Truth Before Editing

Work in a dedicated clean worktree based on current `origin/main`. Never edit `main` directly and never edit an unrelated active feature worktree.

Link this archive objective, related implementation PRs/specs and owned resources
into the existing session inventory before delivery (`docs/agent-closeout.md`).
Preserve that inventory across restarts; do not rebuild it from only the final PR.

```text
git status --short --branch
git worktree list --porcelain
git fetch origin --prune
git log -1 --oneline --decorate origin/main
```

Confirm:

- absolute worktree and branch paths;
- target feature name and active spec directory;
- current `origin/main`;
- implementation PR number, head SHA, and merge SHA;
- whether the target worktree contains user-authored changes;
- whether a successor spec exists and what it owns.

If the active spec worktree is dirty, preserve it. Create a separate closeout worktree rather than stashing, resetting, or overwriting its changes.

## 2. Read the Complete Spec Bundle

Read all files in `.kiro/specs/{feature}/` that exist, especially:

- `spec.json`;
- `requirements.md`;
- `design.md`;
- `tasks.md`;
- `validation.md`, `research.md`, `gap-analysis.md`, and review notes when present.

Load `.kiro/AGENTS.md` and applicable root/local `AGENTS.md` files first. Determine the language from `spec.json.language`; write spec Markdown in that language.

Do not assume a `status.json` file exists. In this repository, `spec.json` is the authoritative status document. Search for an established repository convention before creating any new status file; if none exists, do not invent one.

## 3. Check Task and Evidence Completion

Inspect `tasks.md` mechanically and semantically:

- every implementation checkbox is `[x]`;
- no task is marked complete while its validation is skipped or failing;
- the completion gate is satisfied;
- P0/review findings have dedicated regression coverage;
- successor-only removals are not described as completed in this spec.

Inspect `validation.md` for stale claims. It must identify the actual certified implementation and the merged `main` baseline. Replace stale predecessor/branch-only references with exact PR and merge SHA evidence, but preserve residual risks and explicit successor boundaries.

If a successor spec has a dependency record, update only fields that its existing schema and workflow define, such as the implemented-on-main SHA or verification state. Do not invent new dependency states. Do not modify the successor's requirements, design, or tasks as part of this closeout.

## 4. Set Completion Metadata

Update `spec.json` using the repository's established shape:

```json
{
  "phase": "completed",
  "ready_for_implementation": false,
  "completed": true
}
```

Keep all approved requirements/design/task approvals intact. Preserve the original feature description. Update `updated_at` only when the repository uses that field and use actual closeout or merge evidence.

Only add the optional `status: completed` property when an existing repository convention or schema requires it. Do not add `status.json` solely because the name sounds expected.

If `tasks.md` has no completion evidence section, add a short `## Completion Status` section with checked evidence items and the exact merged baseline. Do not rewrite completed task descriptions or alter their requirement mappings.

## 5. Move the Whole Spec Atomically

After metadata and evidence are correct, move the complete directory with Git:

```text
git mv .kiro/specs/{feature} .kiro/specs/archive/{feature}
```

The active source directory must disappear, and the archived directory must contain the whole bundle. Do not leave requirements, design, tasks, validation, or review notes behind in the active tree.

Archive completed specs and superseded specs alike, but use the correct metadata:

- completed: `phase=completed`, `completed=true`, `ready_for_implementation=false`;
- superseded: preserve the supersession relationship and set the repository's established superseded state.

## 6. Verify Before Commit

Run the narrowest applicable checks from the closeout worktree:

```text
git diff --check
git diff --cached --check
go test ./tools/kiro/speccheck
make docs-check
```

Also verify the artifact shape:

```text
python -m json.tool .kiro/specs/archive/{feature}/spec.json
# active directory absent; archive directory and spec.json present
git status --short
```

Run focused implementation tests again only when closeout changes touch behavior or when merge evidence is insufficient. A metadata-only archive does not justify broad unrelated test expansion.

Review the complete diff, including renames:

```text
git diff --stat origin/main...HEAD
git diff origin/main...HEAD
```

Reject unrelated edits, generated files, weakened tests, stale certification references, and accidental successor-scope changes.

## 7. Deliver Through GitHub

For an independent closeout change:

1. commit only the spec/archive paths with a conventional message;
2. fetch and fast-forward or rebase onto current `origin/main` without discarding user changes;
3. push a dedicated branch and open one PR;
4. use `make pr-watch PR=<n>` for current-head required checks; exit 5 means already merged, not checks passed;
5. merge only when the PR is open, mergeable, clean, reviewed as required, and all required checks have completed successfully;
6. fetch `origin/main` and verify the archived spec on the merged commit.

Do not bypass required checks, force-push over user work, or delete an implementation branch before the closeout PR is merged.

## 8. Cleanup and Report

Reconcile the entire approved scope using `docs/agent-closeout.md`, not only this
archive PR. Verify registered required runs and explicit issue closure; task
completion or merge alone does not remove remaining resource/evidence obligations.
Capture merged-main checks through `handoff -task=run`, assemble and record the
validated Result from its command receipts, and link it as `main_evidence` in the
inventory. A handwritten archive-verification JSON is not that Result.

After merged-main verification:

```text
git worktree remove <closeout-worktree>
git branch -D <closeout-branch>
git fetch --prune origin
git worktree prune --verbose
git worktree list --porcelain
git status --short --branch
go run ./tools/handoff -task=closeout -index=<execution-index>
```

Use `branch -D` only after direct confirmation that the PR was merged. On Windows, a directory lock may prevent physical deletion; remove the Git registration and branch, then report the remaining path for manual deletion after the lock releases. Never use a destructive reset to work around a lock.

The completion report must include:

- spec path before and after archiving;
- completion metadata and task evidence updated;
- explicit statement about `status.json` (created only if the repository convention requires it, otherwise not applicable);
- PR URL and merge SHA;
- checks and merged-main verification;
- successor/deferred scope;
- branch/worktree cleanup and any OS-lock residue.
- the retained closeout checker report and its `complete` value; unresolved obligations block full completion.

## Stop Conditions

Stop and ask for clarification or report a blocker when:

- the implementation PR is not merged;
- task checkboxes or completion-gate evidence are incomplete;
- `spec.json` is malformed or its language is unknown;
- a dirty worktree may contain user changes;
- the target archive already exists with different content;
- a successor dependency state cannot be determined from repository conventions;
- required checks are pending, stale, failed, or attached to an old SHA.
