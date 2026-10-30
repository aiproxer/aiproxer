# Brief: large-payload-proof-and-parity

## Problem
Proxy clients and operators need certified large requests to obey bounded metadata admission and preserve the same trusted execution facts as canonical requests. The architecture audit found nested metadata buffers escaping the budget, reused composition identities, missing bound-session and economic evidence handoffs, and divergent response clocks.

## Current State
Canonical calls and bounded replay intentionally coexist and already share attempt, recovery, commitment, and terminal owners. The completed original specification is archived at `../archive/large-payload-streaming-fast-path/`. The adjacent `../large-payload-lifecycle-correctness/` specification owns replay lifetime, canceled-result disposal, terminal-outcome classification, and unlink failures; it is not implemented by this work.

## Desired Outcome / V1 Slice
Make the existing accepted-wire path a bounded, composition-bound alternative representation, not an alternative source of session, billing, or response semantics. Cover the five explicitly selected audit findings using existing frontend proof, runtime admission/acquisition, provider parser, and host construction seams.

## Boundary Candidates
- Frontend proof parsers: charge retained metadata before buffering; preserve fail-safe canonical fallback.
- Runtime composition: distinguish assessor compositions from economic snapshot versions.
- Runtime admission: transfer the authoritative bound session and trusted request economic facts.
- Attempt acquisition and backend stream adapters: preserve existing optional provider evidence capabilities.
- Frontend response writers: reuse the configured clock without a second response timeline.

## Constraints
- No new orchestration owner, lifecycle framework, registry, extension plane, database, or public contract.
- No request-body trust for StoreID, principal, authoritative session, or admission identity.
- Do not unblock wire eligibility for occupied billing exposure admission or other blockers.
- No billing policy, rating, journal, affinity policy, routing policy, or post-output retry changes.
- Keep bounded replay/allocation behavior; do not decode the complete prompt to recover missing facts.
- User requested automatic complete brownfield generation, review, remediation, and final compilation without questions. This authorizes specification artifacts only, not implementation or publication.

## Alignment and Deferred Work
- Lifecycle integration remains owned by `large-payload-lifecycle-correctness`; this spec uses its existing owners rather than editing that spec.
- Assessment deletion and manifest-derived eligibility consolidation remain separately scoped follow-ups.
- Live-provider benefit, charge impact, new certified profiles, and a Cartesian frontend/backend matrix are not required to establish these corrective contracts.

## Evidence Baselines
- Current worktree baseline: `c6598d5d770f8a9b1b981abdf03fe6ca5fbd2b77`.
- Original read-only audit baseline: `8a041a2ad9c9c2c0d0bf8ee57005fe66f7479d6c`; revalidate owning source before relying on its results.
- Audit report: `architecture-review-20261013-194245.html` in external audit scratch (supporting evidence, not a portable dependency).
