---
name: kiro-impl
description: Implement approved tasks using TDD with subagent dispatch. Runs all pending tasks autonomously or selected tasks manually.
---

# Approved-task implementation

Validate spec approvals and implementation readiness before editing. With no
task numbers, run one actionable leaf task per implementer → independent review
→ verification → selective commit cycle. With task numbers, execute those tasks
directly. Preserve existing work and the approved task boundaries.

## Reach the relevant procedure

- **Setup or restart:** read `references/execution.md` Steps 1–2 and
  `docs/agent-handoffs.md`; establish approvals, task dependencies, source state
  and the first unfinished stage from the scratch execution index.
- **Autonomous execution:** read Step 3, Autonomous Mode. Load
  `templates/implementer-prompt.md` only when briefing the implementer; request
  review through `kiro-review-panel`.
- **Manual execution:** read Step 3, Manual Mode.
- **Blocked or repeated remediation:** read Step 3's debug procedure and
  `templates/debugger-prompt.md`; apply `kiro-debug` to root-cause investigation.
- **Task acceptance:** apply `kiro-verify-completion` before Step 3's task-state
  update/commit procedure; Step 4 covers final validation. Only current evidence and independent review permit
  completion; valid JSON alone grants neither.

Command policy lives in `docs/development-iteration.md`; task artifacts and
restart recovery live in `docs/agent-handoffs.md`. Read task-relevant steering
and focused Go skills, not the entire catalog. Behavioral work follows RED/GREEN;
nonbehavioral work runs applicable checks without invented RED tests.
