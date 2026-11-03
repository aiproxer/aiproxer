---
name: kiro-review
description: Reviewer protocol, the checks one reviewer performs against approved specs, task boundaries, and verification evidence. Load it when your prompt assigns you kiro-review by name; task reviews are requested through kiro-review-panel.
---

# kiro-review

<background_information>
This skill performs task-local adversarial review. It verifies that the implementation is real, complete, bounded, aligned with approved requirements and design, and supported by mechanical verification evidence.

Boundary terminology continuity:
- discovery identifies `Boundary Candidates`
- design fixes `Boundary Commitments`
- tasks constrain execution with `_Boundary:_`
- review rejects concrete `Boundary Violations`
</background_information>

<instructions>
## When to Use

- After an implementer reports `READY_FOR_REVIEW`
- After remediation for a review rejected on a blocking finding
- Before marking a task `[x]`
- Before accepting a task into feature-level validation

Do not use this skill to invent missing requirements or silently reinterpret the spec.

## Inputs

Provide:
- Task ID and exact task text from `tasks.md`
- Relevant requirement section numbers
- Relevant design section numbers
- Spec file paths (`requirements.md`, `design.md`, optionally `tasks.md`)
- The implementation artifact path (see `docs/agent-handoffs.md`)
- The task `_Boundary:_` scope constraints
- Validation commands discovered by the controller
- Relevant steering excerpts when applicable
- Relevant `## Implementation Notes` entries when applicable

## Outputs

Return one of:
- `APPROVED`
- `REJECTED`

Also return:
- Mechanical results
- Findings with severity
- Required remediation
- One-sentence summary

Use the language specified in `spec.json`.

## First Action

Run `git diff` to inspect the actual code changes. If the diff is large or ambiguous, read the changed files directly. Do not trust the implementer report as source of truth.

## Core Principle

Read the spec yourself. Read the diff yourself. Verify mechanically where possible. Reject on concrete failures rather than interpretive optimism.
The main review question is not just "does it work?" but "does it stay inside the approved responsibility boundary without hiding new coupling?"

## Mechanical Checks

Run these checks and use the result as primary signal.

### 1. Regression Safety
- Run the project's canonical test suite using the validation commands discovered by the controller.
- Reject attributable failures. Failures that also reproduce on `origin/main` are recorded under delivery steering, not treated as this task's regressions.

### 2. No Residual Placeholder Markers
- Check changed files for `TBD`, `TODO`, `FIXME`, `HACK`, `XXX`.
- Reject if new placeholder markers were introduced without explicit task justification.

### 3. No Hardcoded Secrets
- Check changed files for hardcoded secrets or credentials.
- Reject if concrete secret patterns are introduced.

### 4. Boundary Respect
- Compare changed files against the task `_Boundary:_` scope.
- Reject if the change spills outside the approved boundary without explicit justification.
- Reject if the implementation introduces hidden cross-boundary coordination inside what should be a local task.

### 5. RED Phase Evidence
- For behavioral tasks, inspect the implementation artifact's RED command/log under `docs/agent-handoffs.md`.
- Reject if RED evidence is missing, empty, or unrelated to the task's acceptance criteria.

### 6. Runtime-Sensitive Static Checks
- If the project already has lint or equivalent static analysis for the touched stack, run the relevant command for the task boundary.
- Pay attention to patterns that can survive typecheck/build yet fail at runtime: type-only imports used as values, missing namespace value imports for qualified-name access, unresolved globals, and newly introduced runtime-sensitive dependencies without matching boot/runtime handling.
- If no project lint command exists, perform a targeted diff-based spot check in the changed files for those patterns.
- Reject on concrete findings that create a realistic boot-time or module-load failure.

## Judgment Checks

### 7. Reality Check
- Confirm the implementation is real production code, not a placeholder, stub, fake path, or deferred-work shell.

### 8. Acceptance Criteria Coverage
- Read the task description and confirm all aspects are implemented, not only the primary happy path.

### 9. Requirements Alignment
- Read the referenced sections in `requirements.md`.
- Confirm each requirement is satisfied by concrete observable behavior.
- Use original section numbers only.

### 10. Design Alignment
- Read the referenced sections in `design.md`.
- Confirm the implementation uses the prescribed structures, interfaces, and dependency direction.
- Reject silent substitutions for design-mandated choices.

### 10.4 Vision Alignment
- Check the change against the decision principles in `VISION.md`.
- Reject a change that narrows which clients, backends, or combinations work unless it names the priority that justifies it.
- Reject refusal-by-default where an honest, explicit, operator-configurable degradation exists, and any fabricated or reordered model-authored history.

### 10.5 Boundary Audit
- Compare the implementation against the design's boundary commitments and out-of-boundary statements.
- Reject if downstream-specific behavior is pushed into an upstream boundary for convenience.
- Reject if the implementation creates new hidden dependencies, shared ownership, or undeclared coupling across adjacent boundaries.
- Reject if a task that is not an explicit integration task now behaves like one.
- When a stage moves across an authority/context/lifecycle seam, compare its old/new inputs: what becomes visible, disappears or changes provenance? Require conflict-seeded behavioral evidence for that changed boundary, not an exhaustive matrix. `internal/testkit/execviewfixture.PoisonedParent` seeds distinguishable request views; existing execctx and detached-runtime tests show how to prove masking while preserving trusted attribution. This is a judgment audit, not a syntactic lint rule.
- When a shared input boundary admits a new shape, trace it through materially different affected consumer classes. Establish whether each preserves it, applies an explicit degradation, or rejects it; use focused family contracts rather than a Cartesian matrix.
- When a request reuses retained state, check that explicit current-request controls cannot be overridden by that state. Distinguish omitted from explicit values where meaningful; for a refusal, verify the pending operation remains usable when the contract requires it.

### 11. Test Quality
- Confirm tests prove the required behavior rather than only scaffolding.
- Confirm tests would fail if the implementation were removed or broken.

### 12. Error Handling
- Confirm relevant failure paths are handled and not silently swallowed.

## Blocking Scope

Only these findings block acceptance (`REJECTED`): correctness bugs (including failing tests caused by the change), security/leakage, VISION violations, and missing or false verification evidence. Everything else is advisory (`APPROVED` with `Suggestion`/`FYI` findings). Where checks above say "Reject", apply it only to blocking-scope findings.

- Report **removable code** as a first-class finding category: tests,
  abstractions, or hardening the slice does not need. Rank it alongside defects
  in the report, at `Suggestion` severity unless blocking under the rule above.
- Hardening suggestions go to the spec's Deferred list, never into a same-PR or
  follow-up repair PR, unless blocking under the rule above.
- One review round by default; a second round runs only for a blocking finding.

## Severity Model

Use:
- `Critical` for correctness bugs, security/leakage risks, VISION violations, missing or false verification evidence, or data loss
- `Important` for must-fix items inside the blocking scope above
- `Suggestion` for non-blocking improvements, including removable code and hardening notes
- `FYI` for informational notes

## Stop / Escalate

Escalate instead of papering over the issue when:
- The approved spec is ambiguous in a correctness-critical way
- The design conflicts with what is technically possible
- Required evidence cannot be gathered
- The implementation only works by silently deviating from approved scope
- Boundary ownership cannot be determined cleanly from requirements, design, and task scope

## Common Rationalizations

| Rationalization | Reality |
|---|---|
| “Tests pass, so approve” | Passing tests do not prove spec compliance or boundary respect. |
| “The extra behavior is useful” | Extra behavior outside approved scope is still drift. |
| “The implementer said RED was done” | RED must be evidenced, not asserted. |
| “This gap is small enough to let through” | Blocking gaps must be rejected or escalated; non-blocking gaps are advisory findings, not rejections. |

## Output Format

Write and validate the assigned reviewer JSON artifact using
`docs/agent-handoffs.md`. Include independently executed checks, actual exit
codes/logs, source identity, severity/location/spec references and actionable
remediation for rejection. Return its path and a short summary; formatting is not
a protocol and schema validity is not an approval decision.
</instructions>
