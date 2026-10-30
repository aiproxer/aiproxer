# Delivery (Steering)

AIProxer competes in a fast-moving market. Time-to-merge is a product requirement, weighed against every other requirement. A ~1,000-line feature takes about one working day of agent time, from issue to merge. When work runs longer, it is out of proportion: cut scope rather than add proof.

The leading word is **slice**: the smallest change that delivers observable value, degrades safely, and can merge on its own. Every issue, spec, task plan, and PR is shaped as a slice.

## Slice First

- Start from the simplest safe behaviour. For advisory or fail-open features, that means the V1 may return `unknown`, skip, or fall back to existing behaviour wherever the full answer is costly.
- Write the V1 slice and a **Deferred** list. Every deferred item becomes a one-paragraph follow-up issue, not a requirement.
- These are deferred by default; they enter V1 only when the user explicitly asks for them in the issue:
  - optional remote/external service integrations;
  - durability across restart, cross-node coordination, leases, or new persisted state for an advisory feature;
  - parity for secondary execution paths (for example wire/large-body) when the primary path delivers the value;
  - new extension stages, planes, or SDK contract surface when an existing seam can carry the feature;
  - metrics or diagnostics beyond a few counters on the decision itself;
  - operator docs beyond a config example and a short README section.
- A substrate feature (one whose value comes from other features consuming it) ships with its first real consumer, in the same PR or the immediately following one.

## Budgets

| Artifact | Budget |
| --- | --- |
| GitHub issue body | about one page (~60 lines): goal, V1 slice, deferred list, done criterion |
| Spec | at most 5 requirement areas and 25 acceptance criteria; `design.md` at most 300 lines; at most 12 leaf tasks |
| PR | at most ~1,500 lines of non-test Go and ~40 changed files |
| Tests in a feature PR | at most ~1.5x the production lines they cover (alpha budget) |

`go run ./tools/kiro/kirocheck` enforces the Spec row (and a Deferred section in `requirements.md`) for every active spec; the pre-commit hook and CI run it. Specs that predate the check have shrink-only ceilings in `internal/qa/kirospec/budget.go`.

When a budget is exceeded, split the work into slices and report the split. A budget is a design signal, not a gate to argue past: the slice is wrong, not the budget.

Before submission, run `make dev-delivery-plan DEV_BASE=<intended-base>
DEV_CONSUMER='<immediate consumer>'` and account for every budget row. Passing the
file-count gate does not waive test proportionality. Reduce duplicated proof or
split the slice; an exception requires explicit maintainer authorization rather
than an agent declaring the guidance soft.

## Alpha budget

- Test budget: new test lines ≤ ~1.5× new product lines per slice; exceeding it needs a one-line reason in the PR body.
- Hardening (bounded/fail-closed/crash-safe/parity/race-proof) is deferred by default unless it protects a VISION "line never crossed".
- Spec size: aim for at most ~10 tasks (12 is the enforced ceiling); split otherwise. Direct code stays the default.
- Handoffs/issues fit one screen: goal, files, done-when; no pre-scripted transcripts.
- Freeze until 2026-11-21: no new gates/ratchets/censuses/hooks/dev tooling, except a gate that is broken or blocks delivery.

## Tests Serve The Slice

Test policy lives in `testing.md` (Test Proportionality). In short: tests prove the shipped behaviour; testing is never a requirement or a standalone task; new architecture checks extend existing generic rules.

## Completion And Evidence Ownership

Completion accounts for every objective in the approved scope, including older
workstreams, required verification, issue/spec closure and owned-resource cleanup.
Publication, merge, verification and full closeout are distinct states. Dispatching
a required run does not discharge it: an owner tracks its exact revision and
required executed steps until resolved. Use `docs/agent-closeout.md` and the
existing handoff inventory/checker; a valid artifact or green PR alone is not
completion, semantic approval or authorization for further operations.

Checks are read-only and apply to stable identified inputs. Source/index mutation
during a gate invalidates readiness; restage and reverify. Build outputs belong in
scratch, and metadata rewriting uses explicit update commands, not check mode.

## Red Main And Unrelated Failures

- Required checks (branch protection) gate merge. Other checks inform: a red non-required check is fixed in the branch only when the branch caused it.
- A failure that also reproduces on `origin/main` is not owned by the feature branch. Record the failing command in the PR body and continue the slice. When it blocks a required check, add the test to `.github/test-quarantine.txt` with its tracking issue (format and validation: `go run ./tools/devcheck -task=quarantine`); the fixing PR removes the entry.
- Fixes to shared gates, flaky tests, or other features go in their own small PR from `main`, then the feature branch rebases. A feature PR contains only its slice.
- Update a feature branch from `main` only when needed to merge.

## Large-Change Overrides Are Maintainer-Only

The change-size overrides described in the root `AGENTS.md` are applied by the maintainer, never by an agent. When the size gate fires, stop, propose a split into slices, and report.
