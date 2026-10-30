# Discovery Brief

## Problem and Outcome
The large-payload architecture audit identified alternative assessor/executor/gate abstractions and replay-specific test execution scaffolding that may have no serving consumers. Maintainers should have one discoverable serving acceptance/execution path rather than a second apparent policy authority sustained only by tests.

## Selected V1 Approach
Confirm callers against the current branch, delete only proven-unused internal machinery, and preserve valuable invariants through the existing production assessor, executor, and shared attempt owners. Do not manufacture a replacement assessment framework or port every obsolete test mechanically.

## Boundary Candidates
- Internal assessment wrappers, their private dependency projections, and isolated test execution scaffolding.
- Existing production acceptance and execution surfaces remain the authorities and first consumers of retained coverage.
- Public canonical/SDK contracts, replay lifetime, frontend proof, billing/session evidence, and eligibility catalog behavior remain outside this slice.

## Brownfield Context
- Baseline: `ba1df9f9`; original audit: `8a041a2ad9c9c2c0d0bf8ee57005fe66f7479d6c`.
- Source-bound inventory and consumer classification must precede design deletion commitments.
- Both sibling corrective specs are present in this baseline; their specification approval is not implementation completion.
- Prefer lifecycle and proof/parity repairs before deletion when coordinated delivery would otherwise obscure their regression evidence. Revalidate any overlapping files before implementation.

## Non-Goals and Authorization
No runtime implementation, commits, pushes, PR changes, or approval for implementation is part of this request. Automatic approval applies to specification generation only. No latency, allocation, race-safety, or charge-impact improvement is claimed.
