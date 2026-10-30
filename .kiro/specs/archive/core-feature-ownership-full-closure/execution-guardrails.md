# Execution Guardrails for `core-feature-ownership-full-closure`

## Status and precedence

This file is a **normative execution clarification** for `.kiro/specs/core-feature-ownership-full-closure/` and must be read before `tasks.md` is executed.

It does **not** change the target architecture in `requirements.md` or `design.md`. It clarifies two execution concerns that otherwise create avoidable risk for an instruction-following implementation agent:

1. how to classify repository-wide failures that already exist on the exact starting `main` SHA; and
2. how to keep the first implementation wave from mixing baseline discovery with featurehost production rewiring.

Where generic closeout wording in `tasks.md`/`design.md` says to run a repository-wide gate, this file governs **failure attribution and acceptance**. It does not permit waiving a failure introduced or worsened by this SDD.

The objective is architectural closure of kernel-vs-feature ownership, **not global repository perfection**. Unrelated pre-existing defects must not expand this SDD into a general cleanup program.

---

## 1. Hard predecessor gate

Do not change production code for this SDD until the predecessor `pre-oss-core-slimming` SDD is canonically completed and archived.

Expected predecessor location after closeout:

```text
.kiro/specs/archive/pre-oss-core-slimming/
```

Before Task 1 starts, record all of the following in the implementation PR/tracker and in the Task 1 baseline artifact:

- exact starting `main` SHA;
- archived predecessor path;
- predecessor final certified SHA;
- predecessor final closeout/race evidence reference;
- predecessor residual-ownership inventory path and its inventory/baseline SHA;
- proof that generated-only standard planes, retired-package absence, the runtimebundle concrete-feature import ban, the external SDK fixture and predecessor architecture budgets are present on the starting tree.

If the predecessor is still active/uncompleted, if its final evidence is missing, or if any required predecessor invariant is false, **STOP**. Do not create a compatibility shim inside #572.

---

## 2. Capture a repository verification baseline before production edits

Task 1 must create a durable implementation evidence artifact:

```text
.kiro/specs/core-feature-ownership-full-closure/implementation-baseline.md
```

This artifact is produced during implementation, not by the spec-only PR.

Use the exact starting `main` SHA. Record OS, Go version, checkout depth/history state and the exact command for each gate. At minimum establish the current result of:

```text
go run ./scripts/generate-feature-planes.go -check
go test -count=1 ./...
make quality-checks
make arch-report
make docs-check
go test -count=1 ./tools/kiro/speccheck
go vet ./...
go mod verify
```

Also capture the current broad Linux race state using the repository's canonical strict race command/workflow. If a local Windows host cannot certify it, use exact Linux CI evidence for the starting SHA.

Do **not** assume the broad repository gate is green merely because merge-required CI is green.

### Required failure table

For every non-green baseline result, record a row with:

| Field | Required value |
| --- | --- |
| Gate/command | Exact command or workflow |
| Package/test/job | Exact failing package/test/job |
| Failure signature | Short stable error/race signature |
| Classification | `baseline product defect`, `baseline test/CI harness defect`, `environment/history limitation`, or `suspected flaky` |
| Reproduced on starting SHA | yes/no + evidence |
| Intersects #572 production scope | yes/no + rationale |
| Existing tracker | issue/PR if one exists; create one only when repository workflow requires it |
| #572 treatment | `must fix before touching area`, `must fix before final certification`, or `out-of-scope baseline` |

Do not classify a failure as pre-existing from memory or a previous chat. It must be observed/reproduced against the recorded starting SHA.

### Checkout/history caveat

Some repository architecture/spec evidence tests inspect historical commit objects. A failure caused only because CI checked out depth `1` and the pinned commit object is unavailable is an **environment/history limitation**, not evidence that the production tree is broken.

For #572 evidence that depends on historical SHAs, use a checkout/fetch depth sufficient to resolve those pinned commits. Do not rewrite valid evidence SHAs merely to accommodate a shallow checkout.

---

## 3. Regression attribution policy

All #572-touched behavior and architecture must be green. Baseline accounting is not a waiver mechanism.

### Always blocking

A failure is a blocker for the current wave when any of the following is true:

- it is new relative to the recorded starting baseline;
- an existing baseline failure becomes more frequent, broader, more severe or changes signature because of this SDD;
- it occurs in a package/file/resource/lifecycle path modified by the current wave and prevents reliable equivalence assessment;
- it affects a core invariant this SDD is explicitly required to preserve, including routing/failover, B2BUA authority, output commitment, immutable generation publication, process/generation ownership, secure-session authority, request hot-path behavior or feature-state isolation;
- it prevents the required focused race/lifecycle certification for state moved by this SDD;
- it invalidates the final ownership census, core-admission manifest, import ratchets, change-surface probes or external public contract introduced by this SDD.

### Baseline failures that may remain outside this SDD

A repository-wide failure may remain at closeout only when **all** of these are true:

1. the same stable failure was recorded and reproduced on the exact starting SHA;
2. the failing area is outside #572's modified production/code-generation/test-infrastructure surface;
3. this SDD did not worsen the failure;
4. the failure does not prevent direct verification of any #572 acceptance criterion;
5. it is explicitly listed in `implementation-baseline.md` and final closeout evidence;
6. any normal repository tracker requirement for the defect is satisfied.

Do not fix such a failure merely to make a broad dashboard green. That is scope expansion.

### Suspected flakes

Do not immediately label a failure flaky. Re-run the smallest deterministic reproducer enough to classify it. If the same failure cannot be reproduced on the baseline SHA, treat it as unclassified and investigate before using it as baseline evidence.

---

## 4. Revised Wave A sequencing

The existing Task order remains authoritative, but the first PR boundary is tightened for a weaker executor.

### Wave A0 — evidence only: Tasks 0–1

No production ownership movement.

Deliver:

- predecessor prerequisite proof;
- refreshed ownership census;
- per-process-resource transition table;
- behavior/lifetime characterization inventory;
- `implementation-baseline.md` including repository-wide failure accounting;
- structural/LOC/change-surface/performance baselines.

Task 1 characterization tests may be added where genuinely missing, but do not introduce `featurehost` or move production resources in A0.

### Wave A1 — featurehost structural seam: Tasks 2.1–2.3

Introduce only the featurehost process/generation facade and the single `StandardFeatures` handle.

At the end of A1:

- no legacy process feature resource has been transferred merely because featurehost exists;
- no resource has dual construction or dual cleanup;
- overlapping generation compilation constructs zero duplicate process resources;
- the transition table still identifies the sole current owner of every untransferred resource.

### Wave A2 — predecessor adapter routing: Task 2.4

Only after A1 is green, route predecessor reasoning/secret-guard generation composition through featurehost.

This remains generation composition, not permission to move unrelated process ownership.

After A2, continue Tasks 3–12 in the original numbered order.

---

## 5. Per-wave execution ledger

Every implementation PR/wave must report a compact ledger:

```text
Starting SHA:
Ending/head SHA:
Production packages moved/rewired:
Process resources whose physical owner changed:
Legacy constructor/closer removed in same change:
New consumer-owned core ports/interfaces:
Request-hot-path changes: none / exact list
Baseline failures observed again: exact list
New failures: none / exact list
Focused race/lifecycle evidence:
Architecture ratchets added/updated:
Residual ownership rows discharged:
```

If a process resource changes physical owner, the PR must name both the removed old constructor/cleanup site and the new single owner. "Close is idempotent" is not evidence of correct ownership.

Do not carry temporary dual execution or dual semantic authority across a merged checkpoint.

---

## 6. Architecture simplicity guardrails

Use the smallest structure that makes ownership obvious.

### Featurehost is composition, not a framework

`internal/standardplugins/featurehost` may know the concrete standard distribution. It must not become a generic dependency runtime.

Do not add an abstraction merely because two adapters share method names. Introduce/shared abstractions only when they remove real duplicated generic wiring or are required by an already-proven common host capability.

Feature algorithms, policies, prompts and state machines belong under their owning feature packages. A small explicit adapter is preferable to a universal binder/registry.

### Do not replace one god package with another

If a featurehost source file begins accumulating feature algorithms or large policy/config schemas, move those details into the feature or a featurehost child adapter rather than increasing the facade budget.

### No speculative cleanup

Do not rename/restructure unrelated packages while touching a dependency edge. Mechanical moves should remain mechanical until characterization passes at the new location.

Do not perform drive-by cleanup in billing, provider/back-end code, database abstractions, test infrastructure or other out-of-scope areas solely because a broad gate is already red there.

---

## 7. Final certification interpretation

Task 12.3 must still run the repository's broad correctness/architecture/docs/security/module/race signals. The difference is that the result is evaluated against the recorded starting baseline rather than against an unstated assumption that every unrelated gate was green before #572.

Final certification requires all of the following:

1. **All #572-focused correctness and architecture gates are green.**
2. **Every concurrency-sensitive process/generation resource moved by #572 has green exact Linux race evidence in focused scopes that directly exercise its new owner/lifetime.** No baseline waiver is allowed for those scopes.
3. `go test -count=1 ./...` and the broad strict race/QA workflows are rerun and compared with `implementation-baseline.md`.
4. There are no new repository-wide failures and no worsened baseline failures attributable to #572.
5. Any remaining broad failure is an explicitly documented out-of-scope baseline failure satisfying Section 3 above.
6. Generator checks, external public SDK/host-binding contracts, ownership manifest/census, core/runtimebundle/featurehost architecture ratchets and disposable change-surface probes are all green.
7. Final ownership census contains zero `mixed`, `unknown`, `temporary`, `compat-to-remove`, `future simplification` or deferred ownership row.
8. Independent review finds no material kernel/feature ownership defect caused or left unresolved by this SDD.

A broad pre-existing failure in an unrelated subsystem is not, by itself, a reason to reopen that subsystem inside #572. Conversely, a failure in an area moved by #572 cannot be dismissed merely because some unrelated broad gate was already red.

---

## 8. Final closeout evidence

The completed SDD must retain enough evidence to make a later reviewer understand why it closed without reconstructing CI history from scratch.

Final closeout evidence must contain:

- starting and final merged-main SHA;
- predecessor archived path/final certified SHA;
- initial and final ownership census summaries;
- initial and final core/runtimebundle/featurehost structural budgets;
- process-resource transition table with every feature resource discharged to one final owner;
- focused Linux race evidence for every moved concurrent/process-shared resource class;
- broad repository gate comparison against the Task 1 baseline;
- list of any remaining unrelated baseline failures and proof they meet Section 3;
- independent review verdict;
- explicit statement that the core simplification program is closed with zero residual ownership debt.

Do not create a third core-slimming tracker as a substitute for completing an ownership item that belongs to this SDD.
