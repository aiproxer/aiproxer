# Technical Design Review

This is the historical pre-implementation design review. Its generation-only authorization hold and unfinished lifecycle prerequisite describe that baseline, not current status. The user subsequently authorized implementation and the archived lifecycle repair is integrated; independent implementation and whole-spec reviews now approve all 21 criteria. Remote CI/race certification and delivery remain separate obligations.

## Assessment
**GO for specification task generation after one wording clarification.** This is a design verdict, not feature GO, implementation approval, or proof that runtime defects are repaired.

An independent read-only reviewer read the generated requirements, design, research and gap analysis, then checked current owning source. Five requirement areas contain 21 criteria, all mapped to existing component boundaries. No significant blocking design concern remained.

## Critical Review Lenses
| Lens | Evidence and assessment | Disposition |
| --- | --- | --- |
| Bounds, identity, and trust | Raw nested sinks/post-write metadata charging, economic/composition ID aliasing, incomplete frozen session views; design repairs these using before-write accounting, one construction nonce and complete authoritative views | GO; no new policy authority |
| Capability/effect ownership | Rich-source precedence and accounting enabled state are explicit; successful-open binding precedes publication; existing provider/attempt owners still retain/drain/terminalize | GO; no new queue, checkpoint API, or financial owner |
| Executability and adjacent work | Concrete file plan, claim/source/mutant observations, unchanged eligibility blockers, and lifecycle source-lifetime prerequisite | GO for task generation; final HTTP replacement acceptance waits for the lifecycle repair |

## Corrections Applied
1. Source research before final design identified host publication IDs are assigned too late to serve assessor construction. The final design uses a fresh bounded composition nonce at the existing builder and removes economic/global sequence aliasing, without moving manager lifecycle.
2. Source research before final design identified rich drain methods can shadow native observations. The final design delegates real rich records or lifts native-only observations, preserves coverage and explicit disabled state, and introduces no separate drain owner.
3. Independent design validation noted `execctx.Views` has no HasPrincipal field. Clarified copying the prepared Principal and keeping its presence flag separate from view availability; `recvViewsOK` cannot remain tied only to principal presence. No requirement change was necessary.

## Strengths
- Both request representations remain justified; shared attempt, receive, commitment, and terminal ownership is preserved.
- Only existing seams change; no public contract/config, provider SDK in core, new registry, persisted state, or speculative lifecycle framework.
- Failure and absence remain truthful: preaccept proof fallback, terminal foreign-stamp rejection, genuinely missing identity policy, absent StoreID/evidence, and unchanged paid-exposure blockers.
- Testing is inside implementation tasks, extending serving/family contracts with independent literal oracles rather than a Cartesian matrix.

## Acceptance Holds and Revalidation
- Lifecycle specification artifacts are not a completed code dependency. The full HTTP receive-replacement sentinel requires its replay/source handoff repair integrated into the implementation base; local acquisition replacement is not substitute evidence.
- Financial-safety/performance and active ownership changes touching these files require a fresh source/boundary reconciliation before coding.
- Proposed implementation growth remains an estimate. Use the existing delivery report; split if actual file/production/test growth exceeds slice guidance.
- Live charge impact, latency benefit, broad cross-generation exploitability, whole-repository QA and remote race/OS results are not established by this review.

## Result
No open design blocker. Independent task-graph review passed after one repair/recheck; fresh compiled-document sanity also passed. Keep `ready_for_implementation: false` because the request authorizes generation/review only, matching the adjacent lifecycle spec's status discipline. Final compilation and evidence are recorded in `validation.md`.
