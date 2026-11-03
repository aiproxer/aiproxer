---
name: kiro-review-panel
description: Review a Kiro task implementation with a best-of-n panel of headless OpenCode reviewers and a judge. Use after an implementer finishes a task, after remediation, or before accepting a task as complete.
---

# kiro-review-panel

You are the controller: the panel performs the review, you launch it and act on
its verdict. `scripts/kiro-review.sh` runs reviewers from different model
families concurrently, then a stronger judge reviews independently with their
reports as unverified suggestions. Each panel agent follows the `kiro-review`
protocol. The roster — which model runs as r1, as r2 and as judge — comes from
the roster file the script reads, and from nowhere else: the container's `.env`
by default. The run logs where its roster came from.

## Steps

1. **Brief.** Name the spec (directory under `.kiro/specs/`) and the scope
   (task IDs, a range, or a short description). When you hold task context the
   panel cannot rediscover, write it to a file in scratch outside the repository:
   exact task text, requirement and design section numbers, `_Boundary:_`,
   validation commands, the implementer artifact path and relevant
   `## Implementation Notes`. Reuse earlier evidence to avoid repeating unrelated
   checks; a prior approval or deferral does not shield a reachable counterexample
   from review within the current scope.
2. **Launch** from the checkout under review, as a background command with no
   tool timeout, then end the turn and wait for its completion notification:

   ```sh
   scripts/kiro-review.sh <spec> "<scope>" [--context /abs/scratch/context.md]
   ```

   A run takes 20–60 minutes. Progress lives in `<RUN_DIR>/run.log`; read it
   only when asked for status.
3. **Read the outcome.** Stdout ends with `RUN_DIR=`, `VERDICT=`, `REPORT=` and,
   when written, `RESULT=` lines. Exit 0 is `APPROVED`, 3 is `REJECTED`, 1 is a
   failed run, 2 a usage error. Read the judge report at `REPORT` in full.
   `RESULT` is the reviewer JSON artifact: validate it per
   `docs/agent-handoffs.md` before it authorizes anything.

Done when you hold the judge's verdict, have read its report, and can state its
findings with severities and required remediation, or have recorded why the
panel produced no verdict.

## Blocking Scope

One panel round by default; a second runs only for a blocking finding (correctness bugs including task-caused test failures, security/leakage, VISION violations, or missing/false verification evidence — see `kiro-review`).
The judge ranks "removable" findings (tests, abstractions, hardening the slice
does not need) alongside defects in its report; like all non-blocking findings,
they go to the Deferred list, never into a repair PR.

## When the panel fails

Exit 1, a missing `opencode` CLI or an unavailable model leaves the task
unreviewed. Report the cause from `run.log` and `<RUN_DIR>/<agent>/opencode.log`,
then obtain review from a fresh reviewer subagent briefed with
`.agents/skills/kiro-impl/templates/reviewer-prompt.md`, or leave acceptance
pending for a human. Review performed in your own context is self-review, never
independent approval.

When the cause is an unavailable model, it is a roster problem, not a code
problem: swap the model in the roster file the run names in its
`roster source:` line — the container's `.env` by default — and relaunch. That
needs no change to the repository.
