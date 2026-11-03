# Task handoffs and restart recovery

Use JSON artifacts for implementation/review status. Chat carries a short summary
and the artifact path; Markdown headings are not a machine protocol. The shared
shape is `tools/handoff/result.schema.json`; `go run ./tools/handoff` checks the
shape, evidence files, status contradictions and current source identity.
Artifacts validate integrity, not correctness or independent review.
Unknown/duplicate fields, trailing documents and artifacts larger than 2 MiB fail
validation. Logs stay in separate files; keep result payloads small.

## Agent result

The controller assigns an absolute scratch directory **outside the repository**,
task ID and result path to each agent. Save command output there. After final
verification, capture the source identity:

```sh
go run ./tools/handoff -task=snapshot -repo=.
```

Copy that object into `source`. The fingerprint covers HEAD, staged/unstaged diffs
and untracked file bytes, honoring Git ignore rules. The repository must remain
stable across verification and capture; capture before and after commands and
require equality. Artifacts belong outside the repository so writing them cannot
invalidate their own identity. Do not store credentials or private payloads in logs.

Where practical, generate command evidence instead of transcribing exit codes:

```sh
go run ./tools/handoff -task=run -index=/absolute/scratch/execution.json -log=/absolute/scratch/check.log -purpose=verification -- go test ./path/to/package
```

This runs explicit argv, streams output to stderr and the log, and emits a
source/command JSON record on stdout. It preserves a nonzero command failure and
refuses attribution if source changes during execution. Use `-purpose=red` for
expected failing characterization; the command still exits nonzero. Combine
records into the result; the recorder does not choose a task status or verdict.

The recorder also atomically saves `<log>.record.json`, including exact argv,
exit status, source identity and the log digest. Result validation checks these
receipts when present: changed commands/logs and verification from another source
are rejected. Generated commands carry `command_record`, so deleting the receipt
also fails validation. Legacy logs without receipts remain readable, but are not
recorder-bound evidence. Receipts protect integrity, not authenticity.

For long/background gates use `-output=summary`: the complete log is retained,
while stdout reports the source, command and real exit status. Execute the gate
directly through the recorder rather than piping it through `tail`/`grep`, which
can hide failures. Wait for the background completion notification.

For search-based checks, distinguish matches (`grep` exit 0), no matches (1),
and execution errors (>1). Decide which match outcome satisfies the check;
preserve the raw status in its evidence and treat execution errors as failures.

### Assemble recorded evidence

Write only the result metadata, findings and status to a JSON template at the
result path. Populate `source` and `commands` from receipts, without copying argv
or exit codes by hand:

```sh
go run ./tools/handoff -task=assemble -repo=. -file=/absolute/scratch/result.json \
  -command-record=/absolute/scratch/red.log.record.json \
  -command-record=/absolute/scratch/verify.log.record.json
```

Assembly validates before atomically replacing the template; failure leaves it
unchanged. Verification receipts must match the current source. Historical RED
may precede implementation; historical verification belongs in a separate
artifact, not a current readiness claim. For direct `go test` RED, a build-only or
infrastructure failure does not prove the behavior: the log must include a
failing test (`go test -json` is supported). Shell wrappers still require review
of the failure cause. Resolved blocking findings become FYI with the fix and
verification described; open blockers keep their severity.

Every result has `version: 1`, `role`, `task`, `status`, `source`, `behavioral`,
`commands` and `findings` (use `[]` when empty). Commands carry `purpose` (`red`,
`verification`, or `baseline`), `argv`, actual `exit_code`, and an evidence file
path. Relative evidence paths resolve against the result file's directory.
Implementers use `READY_FOR_REVIEW`, `BLOCKED`, or `NEEDS_CONTEXT`; reviewers use
`APPROVED` or `REJECTED`. Blocked/context-needed results need actionable `blocker`;
rejections need findings and actionable `remediation`. Findings use the existing
Critical/Important/Suggestion/FYI severity scale and concrete locations/details.

Ready/approved artifacts require successful verification and no blocking finding
or blocker. Behavioral implementation additionally needs failing, nonempty RED
evidence. Nonbehavioral work uses `behavioral: false` and its applicable checks;
it needs no invented RED test. Record unrelated baseline failures as baseline
evidence and nonblocking findings, not as passing verification.

## Review verdicts

Bind each review report to its included scope, explicit exclusions and exact inputs.
For repository reviews, include the existing `source.head` and `source.fingerprint`
identity; for external/ignored file inputs, record paths and content digests since
the repository snapshot does not cover them. Verify input identity remains stable
across the checks. A subset re-review approves only that subset unless unchanged
input identities and earlier accepted evidence establish the rest of the scope.

Give findings stable IDs, retained through remediation and re-review. Prefix the
existing JSON `detail` with the ID rather than adding schema fields. Panel
reconciliation qualifies IDs by reviewer, so `r1/F1` and `r2/F1` remain distinct.
The Markdown report carries scope/exclusions; schema validity still is not approval.

## Controller acceptance and recovery

Validate an artifact before reading its status or dispatching the next stage:

```sh
go run ./tools/handoff -file=/absolute/scratch/task-result.json -repo=.
go run ./tools/handoff -task=record -file=/absolute/scratch/task-result.json -index=/absolute/scratch/execution.json -repo=.
```

The controller alone writes the index. Atomic replacement preserves task/role
links, status and tested source identity across restart; agents never share an
index-writing responsibility. A corrupt index fails rather than being overwritten.
An invalid artifact is repaired locally; request only missing facts from its owner
if they cannot be recovered from existing logs. Formatting never needs a new agent.

On restart, read the index, approved task checklist and actual Git state. Completed
tasks stay completed only with their accepted commit and matching verification
evidence; record the acceptance commit in the controller's scratch checkpoint.
Resume the first incomplete stage. Changed source invalidates current readiness:
reverify affected work instead of replaying every earlier task. Use
`-current=false` only to inspect historical evidence; it cannot advance the index.
Never interpret an index entry alone as approval, spec completion or permission to
commit/publish. Reviewers independently inspect the actual diff and evidence.

For whole-scope completion, required-run ownership and resource reconciliation,
use `docs/agent-closeout.md`. Link its explicit session inventory through
`record -inventory=<path>`; ordinary index writes preserve that link. Task-level
readiness does not complete earlier objectives or asynchronous verification.
