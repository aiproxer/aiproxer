---
name: aip-cordis-v4
description: "AIProxer ownership and lifecycle discipline for architecture/spec review, reload, shared resources, connectors, background work and approved refactors. Reuse existing authorities and the smallest demonstrated mechanism."
license: MIT
metadata:
  author: aiproxer
  version: "1.1.0"
  scope: "aiproxer/aiproxer-enterprise"
---

# AIProxer lifecycle discipline

Use Cordis as a reasoning discipline, not an implementation target. Preserve
existing owners and immutable generations. Choose the smallest mechanism that
addresses a measured problem; ordinary lexical cleanup stays lexical.

## Load by changed boundary

Read only the relevant numbered sections of [discipline](references/discipline.md):

| Change | Sections |
| --- | --- |
| New ownership/acquisition/rollback | 1–5: authorities, owned inverses, irreversible emissions, dependencies, withdrawal |
| Reload or generation publication | 5, 8, 11: withdrawal order, immutable generations, close linearization |
| Pooling/reuse/invalidation | 6–7, 9–12: semantic identity, ROI, owned builders, reserved claims, close and fail-closed eligibility |
| Background work/shutdown | 2, 5, 9, 11: owner, cancellation/join, withdrawal, terminal close |
| Connector growth | 13: profiles/contracts instead of Cartesian products |
| Spec/design validation | 14–16, 21: authority/effect map, measurable requirements, NO-GO criteria and decision rubric |
| Approved implementation | 17: characterization, private primitive, one integration seam, existing owners and final simplification |
| Review | 18–19: owner-first audit and concrete anti-patterns |
| Cordis terminology/source interpretation | 20, 23: mapping and adaptation limits |

Cross-boundary changes may need several rows. Findings must name a violated
invariant or unjustified complexity, not demand Cordis terminology.

Root `AGENTS.md` and steering own execution and verification; use
`docs/development-iteration.md` for local commands and remote-only race evidence.
This skill supplies no alternate runtime, DI graph or lifecycle authority.
