# Brownfield Implementation Gap Analysis

## Baseline and Requirement Status
Current source is `10ed7ca12e2a7b30fc4d8f19d7b0dc27fca33e36`; lifecycle sources are unchanged from audit `8a041a2a`. Requirements contain four areas and 16 EARS criteria. Independent requirements review was applied before design: clarified surviving-terminal success, exactly-once logical finalization, and independent sanitized cleanup signaling; moved goroutine mechanics out of acceptance criteria.

## Requirements-to-Existing-Capability Map
| Criteria | Existing capability | Missing integration / smallest correction |
| --- | --- | --- |
| 1.1, 1.2, 1.3 | Immutable sources, independent readers, execution-lifetime cleanup | Remove competing frontend root close after successful transfer; preserve lexical failed-handoff cleanup and define partial/nil result handling |
| 1.4 | Shared pre-output recovery and content commitment | Preserve existing authority; prove replacement can reopen only before commitment |
| 2.1, 2.2 | Existing holdalive execution producer, cancellation grace | Typed abandonment disposer and rendezvous transfer; no buffered orphan or extra cleanup worker |
| 2.3, 2.4 | Canonical/wire execution wrappers, direct disabled paths, HTTP 102 | Dispose partial error-bearing stream in each wrapper; preserve error, timers, grace, and direct execution |
| 3.1, 3.2, 3.3, 3.4 | Shared receive/attempt outcome and secure-session audit seam | Remove outer EOF-versus-Close guess; use logical execution outcome without duplicating attempt or billing finalization |
| 4.1, 4.2 | Shared spill reader census and pending deletion across completion | Claim one removal attempt, retain failed identity/error, return deferred error; descriptor closes before census release |
| 4.3, 4.4 | Existing internal spill config, diagnostics logger, logical spool ledger | Narrow source-owned sanitized notice independent of ignored Close errors; logical accounting remains unchanged |

## Implementation Options
1. **Patch existing seams (selected).** Existing runtime source cleanup remains the successful-handoff owner. Existing holdalive producer disposes abandoned results. Shared receive state supplies logical outcome. Existing spill lifecycle records physical removal truth.
2. **Frontend wrapper owns successful replay.** Possible mechanically, but duplicates the runtime's existing successful source cleanup and risks evidence/cancellation method hiding. Reject unless source revalidation disproves runtime ownership.
3. **Recreate terminal classification in wire wrapper.** Easy to implement as an event switch, but lets an intermediate terminal event or repaired attempt become another audit authority. Reject; shared logical outcome must be consumed instead.
4. **Introduce a resource registry, completion service, or unlink worker.** No missing cross-request coordination warrants it; adds ownership and persistence/lifetime obligations. Reject.

## Compatibility and Scope Constraints
- No public `pkg/lipapi`, `pkg/lipsdk`, or `pkg/lipruntime` contract changes, new feature plane, config option, provider dependency, or schema.
- Keep `Source` unchanged; internal construction configurations can carry a typed pathless notification callback.
- Keep canonical/replay payloads distinct and shared attempt/receive ownership authoritative. No feature-specific execution/race engine is introduced.
- Existing second root Close remains nil/no retry. Physical cleanup may fail permanently; V1 makes that fact observable, not eventually successful.
- Source-owned diagnostics are necessary because existing runtime/frontend code intentionally ignores several Close errors. Returning an error alone does not satisfy 4.3.
- Filesystem and observer calls run outside the spill lifecycle mutex. A descriptor close must finish before its reader is released.
- Financial finality and logical spool reservation release are unrelated. Approved adjacent spec work requires shared-seam revalidation, not a billing-policy change here.

## Brownfield Work and Remaining Implementation Risks
- Existing passing tests omit successful-handoff receive replacement, abandoned successful/partial results, no-EOF terminal audit, and deferred unlink failure.
- Historical probes demonstrate missing contracts; the repaired production executor/frontend replacement sentinel must still be added during TDD. A fake retry performed inside Execute is insufficient.
- Failure schedules are local: cancellation/result rendezvous, completion before/after grace, root versus last reader, descriptor-close versus unlink, terminal success versus Close, and recovery replacement versus outcome selection.
- No unresolved external research dependency exists. Implementers must rebaseline shared source and active adjacent work before editing, and stop if it invalidates the selected ownership map.

## Delivery Estimate
One corrective PR, approximately 8–12 production Go files and under 400 added non-test lines; test code should stay near the repository's ~2x production-line guidance by extending existing harnesses and avoiding a new matrix. This is an estimate, not measured change-size evidence. Five bounded implementation tasks are expected, each carrying regression tests and its first serving consumer.

Use `make dev-delivery-plan DEV_BASE=<implementation-base> DEV_CONSUMER='existing streaming frontends'` before coding and after accepted tasks. Split if actual production/file/test growth exceeds the slice budget; do not remove required lifecycle coverage merely to fit the estimate.
