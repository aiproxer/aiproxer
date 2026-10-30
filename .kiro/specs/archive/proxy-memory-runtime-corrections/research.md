# Research Log: proxy-memory-runtime-corrections

## Summary

Source evidence for the two approved corrections. Every path and line below was read in the working tree at `a27ae0bab`; the reproductions were run as **scratch overlays** and are **not landed** and **not a certification**.

- The two auxiliary roles are missing from the closed allowlist, and the rejection aborts both phases **before** the backend is opened and **before** the billing call identity is minted, on every host including a usage-only one.
- The bank claim's conditional update omits the retry gate that its own candidate query and the session claim carry; the gap is reproducible without sleeps.

## The roles defect (chain, source-confirmed)

| # | Location | Behaviour |
|---|---|---|
| 1 | the feature's prompts module (the two role constants) | the role is stamped into the auxiliary request |
| 2 | the feature's worker (the collect path) | the owner scope is set, then the request is submitted to the background client |
| 3 | the core auxiliary client and the detached policy | the detached policy validates only the charset and the length; it performs **no membership check** |
| 4 | the core background scheduler (the worker) | the real scheduler calls the real runtime executor |
| 5 | the runtime executor, request preparation | the rejection point, **before** the billing call identity is stamped |
| 6 | the runtime's request-authority admission | the workload projection; the error is **unconditional**, before the branch that tolerates a missing request coordinator, so it fires on a usage-only host too |
| 7 | the core metering projection | delegates to the core allowlist |
| 8 | the core billing workload identity file (the closed lists) | exactly two other roles; everything else is rejected |

Composed effect: no billing call identity, no submit, no backend open, no terminal usage record, and only a bounded `phase1_failed` warning. The observed error names the role in the workload-identity rejection.

**Evidence status.** The roles discovery ran a focused seam probe and a composed production probe (real scheduler, real executor, recording backend) through external overlays on the current base: both proxies are red at baseline with the workload-identity rejection, and green with the allowlist addition, with the control role passing throughout. The evidence is scratch and external; it is not a certification, and the same kind of probe is what this slice's tests will land as committed cases.

**The minimal fix.** One production file: the two constants and their two closed-list entries. A workload role is a content-free label, so no money logic is introduced.

**Split coordination.** The open-core split is in flight on a feature branch and carves this allowlist to the usage-evidence package, keeping a forwarder alias behind. The same addition must be transplanted when the split lands. The slice is written against the **current base** file and does not wait for the split.

**Known precedent, deliberately out of scope.** A sibling feature's verifier role is rejected by the same allowlist and carries a characterization test asserting that rejection. It is a different feature and is not part of this slice.

## The bank claim defect

- **Root cause:** the bank claim's candidate query filters on the next attempt time, but its conditional update revalidates only the dirty flag and the lease. The session claim revalidates its **complete** candidate predicate, so the bank claim is the outlier.
- **Interleaving (no sleeps, no polling):** a worker claims a bank whose lease has expired; a late failure from the previous generation releases the lease and schedules a backoff; the stale claim statement then still matches and leases the bank early, increments the attempt counter and ignores the scheduled backoff.
- **Deterministic rendezvous:** a query hook that runs **before** the statement acquires a connection parks the claim statement, so the competing write lands in the gap. This works on a single-connection SQLite and on a fresh-schema PostgreSQL lane.
- **Evidence status:** the races discovery ran the case on both engines — red on the current source (the stale claim took the bank) and green with the predicate added, with the whole shared store contract and the neighbouring packages green afterwards. The evidence is scratch and external; this slice lands the case as a committed contract case.
- **The fix:** mirror the session claim — the same retry-gate predicate and its argument — leaving the lease-fencing predicate and the dialect-neutral statement untouched. The candidate query, the worker, the backoff and the attempt counter are unchanged: no scheduler redesign.
- **Residual, follow-up:** the lost-race skip in the bank claim is positional, so a candidate that left the window can make the offset skip an eligible row in the same call; the row stays dirty and is claimed by a later tick. The session claim uses an exact skip list. Pre-existing, and not part of this slice.

## The composed proof's home and fixture

- The featurehost composition is the only place that already composes the process, the generation, the real feature worker, the background client, the runtime executor and a backend. A core-level test cannot host the full proof, because core may not import the feature and the feature may not import core.
- The proof replaces **only** the mocked background client and the stub backend; the rest of the sentinel composition is reused. The recording backend must declare the capabilities the phase calls need — the structured-output capability is a hard requirement, streaming is required — and the phase selectors are the configured auxiliary backend identifiers, so the recording backend is registered under them.
- The executor's billing seams are exported and can be provided from another package, which is what makes the terminal call and leg records observable.

## Sources

- Working tree at `a27ae0bab` (worktree `fix-proxy-memory-review-corrective`).
- External scratch reports from the roles and races discovery agents, linked by the controller's handoff inventory; their reproduction claims are recorded as scratch evidence, not as certification.
- `.kiro/steering/{product,delivery,structure,testing}.md`, `VISION.md`, the root `AGENTS.md` and `.kiro/AGENTS.md`.
- *Drift warning: all of this is local evidence at the recorded base; it is not a statement about any remote ref, and the split status and the composition must be re-checked against the current `main` before the work lands.*
