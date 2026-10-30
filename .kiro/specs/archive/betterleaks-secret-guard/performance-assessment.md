# BetterLeaks Secret Guard Performance Assessment

Historical task 8.6 assessment. Current adversarial JSON remediation and its measured limits are recorded in `review-remediation.md`, section "Full-path JSON allocation remediation (task 13.5)" (excluded from the open-source export); this historical acceptance does not override later full-head review findings.

Date: 2026-10-04
Commit assessed: `fb7032d7220d1598b9877048879c2023e11d942d`
Related task: 8.6 remediation and task 9.1 certification

## Evidence

The complete benchmark matrix and policy facts are in [`benchmark-report.md`](benchmark-report.md). The final allocation profiles are retained outside the repository. These are bundle-relative artifact labels, not repository links:

- `betterleaks-cert-20261003/task-8-6-final-exact-nohit-2m.mem.pprof`
- `betterleaks-cert-20261003/task-8-6-final2-hybrid-positive-2m.mem.pprof`

The comparable 2 MiB text measurements after task 8.6 were:

| Case | Before task 8.6 | After task 8.6 |
|---|---:|---:|
| Exact-only, no-hit | 4,195,509 B/op, 10 allocs/op | 1,221 B/op, 8 allocs/op |
| BetterLeaks-only, no-hit | 6,309,088 B/op, 30 allocs/op | 17,906 B/op, 28 allocs/op |
| Hybrid, no-hit | 6,309,232 B/op, 30 allocs/op | 18,210 B/op, 29 allocs/op |
| Exact-only, positive | 4,196,682 B/op, 10 allocs/op | 2,099,637 B/op, 21 allocs/op |
| BetterLeaks-only, positive | 6,513,221 B/op, 30 allocs/op | 2,307,245 B/op, 188 allocs/op |
| Hybrid, positive | 6,499,554 B/op, 30 allocs/op | 2,280,453 B/op, 172 allocs/op |

The independently repeated final no-hit run measured 1,221 B/op exact-only, 11,048 B/op BetterLeaks-only, and 17,816 B/op hybrid. The small difference from the tracked benchmark table is normal run-to-run benchmark variation; both measurements show that the multi-megabyte no-hit text copy was removed.

The positive 2 MiB profiles and repeated scans measured approximately 311–316 ms for BetterLeaks and hybrid. CPU profiles attribute the dominant matching cost to the upstream regular-expression matcher and Unicode folding. The remaining feature-owned request-sized allocation is one 2 MiB byte representation for occurrence mapping. Hybrid reuses that representation; it does not create a second request-sized copy.

JSON has a different ownership floor. The pre-BetterLeaks 2 MiB exact baseline was 10.48 MB of decode/mapping allocation. After task 8.6, the comparable rows are approximately 12.58 MB exact-only and 14.70 MB BetterLeaks-only. Those buffers support canonical JSON context, occurrence offsets, and mutation, so the measurements do not justify claiming a general JSON copy elimination.

The exact no-hit profile is setup-dominated after remediation. The hybrid-positive profile attributes the residual request-sized allocation to `betterLeaksLogicalFragmentSource.betterLeaksRaw`; scanner construction and `regexp/syntax` allocations are benchmark setup costs. These profile observations support the ownership conclusions above.

## Assessment and recommendation

Task 8.6 achieved its bounded objective: it removed avoidable no-hit text copies without changing detector defaults, scan limits, finding caps, confidence, decode depth, worker count, immutable ownership, canonical JSON context, cancellation, deterministic merge, or decoded fail-closed redaction.

The positive matching cost and required JSON decode/mapping buffers are a performance design-review trigger. The profile-backed recommendation for this feature review is to accept the bounded upstream matching cost and route further optimization to a separately owned performance task. That task can evaluate occurrence-map admission and JSON buffer reuse only if it preserves security ownership, clone-only mutation, canonical offsets, and fail-closed behavior for decoded findings.

Independent performance review adjudication: SATISFIED for the bounded opt-in security profile design review. The acceptance is limited to task 8.6's objective and the measured tradeoffs above: no-hit text copies are removed, while positive matching retains the upstream regular-expression and Unicode-folding cost and JSON retains the decode/mapping buffers required for canonical context and mutation. This adjudication does not create a numeric SLO or automatically approve the whole feature for production. No weaker detector default or hidden cap was introduced to make the measurements appear acceptable.
