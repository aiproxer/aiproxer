# Approved-scope closeout

Completion covers the **whole approved scope**, not the last branch or a clean
current worktree. Keep one explicit session inventory beside the controller's
existing handoff execution index. Add objectives when scope expands; record
owned resources when creating them and required workflow runs when dispatching.
The inventory is data, not permission to merge, close an issue or delete anything.

## Inventory and evidence

Use version 1 with a session `id`, nonempty `objectives`, and exact `worktrees`
and local `branches` arrays. Each objective names its ID, requested `delivery`
(`submitted` or `merged`), PR numbers and **actual published head SHAs**, and any
issue, associated spec path, required runs and merged-main evidence. An objective
must declare at least one real deliverable or verification obligation.

Every objective requires explicit `spec_delivery`; use `none` only for work not
covered by a spec. Existing inventories must add this declaration rather than
letting omission mean "no spec". For spec-backed work, register `spec` before delivery:
`partial` names the active `spec.json` and requires a nonempty `spec_disposition`
explaining remaining scope; `complete` names the archive path and requires finished
metadata, retained approvals, checked tasks and no active copy. Do not omit the
spec to avoid closeout, or mark a partial slice as full spec completion. Reviewers
judge the disposition and task evidence; the checker enforces the declaration.

Required runs declare run ID, GitHub workflow `head` (`headSha`), required job/step
names and absolute downloaded artifact paths. Optional `tested_sha` identifies a
different checked-out revision (for example `workflow_dispatch` with `inputs.ref`);
it defaults to `head` for existing inventories. A distinct tested revision requires
artifact evidence. GitHub run identity is checked against `head`; every artifact's
`tested_sha=` marker is checked against `tested_sha`. A run-level success cannot
substitute for a skipped required step.
Missing, pending, failed, cancelled or stale evidence remains owned work until
resolved or explicitly removed from scope by the user—not silently forgotten.

Minimal shape (replace illustrative numbers/hashes/paths with actual evidence):

```json
{
  "version": 1,
  "id": "approved-work",
  "objectives": [{
    "id": "feature",
    "delivery": "merged",
    "prs": [{"number": 123, "head": "<published head SHA>"}],
    "issue": 120,
    "spec": ".kiro/specs/archive/example/spec.json",
    "spec_delivery": "complete",
    "main_evidence": "/absolute/scratch/main-result.json",
    "runs": [{
      "id": 456,
      "head": "<workflow headSha>",
      "tested_sha": "<tested checkout SHA; omit when equal to head>",
      "steps": [{"job": "race-fuzz (broad)", "name": "Tier-1 fuzz smoke"}],
      "evidence": ["/absolute/scratch/race-evidence.txt"]
    }]
  }],
  "worktrees": ["/absolute/container/worktrees/owned-task"],
  "branches": ["feat-owned-task"]
}
```

Merged-main evidence is an existing validated handoff result with real successful
commands/logs and its tested source identity. The tested main revision must
contain each declared PR merge. Evidence presence/identity is mechanical; the
reviewer still decides whether its scope proves the requested behavior.

Create/link the inventory when starting delivery, not after cleanup. Capture
merged-main commands through `handoff -task=run -index=/absolute/scratch/execution.json`, assemble the existing Result with
their `-command-record` receipts, and `-task=record` it with `-inventory` and the
execution `-index` (see [handoff commands](agent-handoffs.md)). Plain JSON flags
such as `"docs_checks": "passed"` are not a validated verification result.

Use the same `-index` for every acceptance run, with a fresh `-log` each time.
Indexed runs register `verification_attempts` before execution; missing/invalid
receipts and non-RED failures block closeout even after a passing retry or a new
task result. Retain each failed log/receipt and add a nonempty `disposition` to
that attempt in the index: cause, corrective evidence, or explicitly authorized
residual risk. A successful retry alone is not a fix. Disposition does not waive
receipt integrity and is a review claim, not automatic approval. Run closeout
with `-index` so it sees this ledger; file-only closeout sees only attempts
explicitly declared in the inventory. The tool cannot discover omitted runs.

## Reconcile in order

1. **Delivery:** use `make pr-status`/`make pr-watch` and the delivery skill within
   authorized scope. Record final heads after rebases and actual merge SHAs.
2. **Verification:** wait for registered runs using existing blocking watchers;
   inspect actual job/step results and downloaded artifacts. Attribute failures
   before repairing; shared/baseline fixes stay in separate slices.
3. **Issue/spec closure:** verify issue state explicitly, especially for stacked
   PRs. Archive completed specs through the archive skill, preserving approvals
   and Deferred scope; then verify the archive on merged main.
4. **Merged-main evidence:** run applicable scoped checks and real CLI/service
   smoke. Record source identity and successful command evidence.
5. **Owned cleanup:** verify clean/no-user/no-unique-work conditions before
   removing only the explicitly owned paths/refs under the delivery skill.

Check the complete inventory read-only:

```sh
go run ./tools/handoff -task=closeout -file=/absolute/scratch/session.json
```

Or link it while recording a normal task result with
`-inventory=/absolute/scratch/session.json`, then use
`-task=closeout -index=/absolute/scratch/execution.json` after restart. Other task
entries and the inventory link are preserved by atomic index updates.

The JSON report names every unresolved obligation; nonzero exit blocks a complete
claim. `-phase=submitted` checks publication/run obligations without requiring
issue/spec closure or resource deletion, and **always reports `complete: false`**.
Use it when only PR submission is authorized. It grants no further authorization.

The final delivery report includes the checker report path and its `complete`
value. If archival or cleanup is unauthorized or blocked, report that pending
obligation rather than substituting a narrower success for full closeout.

The checker runs only fixed read-only Git/GitHub queries and file reads. It cannot
infer omitted objectives or ownership, validate semantic test adequacy, confer
review approval, mutate remote state or perform cleanup. Those remain explicit
controller/reviewer/user responsibilities. Keep artifacts outside the repository.
