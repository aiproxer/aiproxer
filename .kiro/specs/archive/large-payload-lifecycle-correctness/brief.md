# Brief: large-payload-lifecycle-correctness

## Problem
Proxy operators and clients need accepted large requests to retain recoverability before output commitment and to release resources with a truthful terminal outcome. The architecture audit identified premature replay-root closure, abandoned successful execution results during cancellation, misleading success-versus-close audit classification, and forgotten failed spill-file removal.

## Current State
The completed original fast-path specification is archived at `.kiro/specs/archive/large-payload-streaming-fast-path/`. Canonical and replay representations already share core attempt, receive, recovery, and terminal owners. This corrective specification repairs their request-to-stream lifetime integration, not the execution architecture as a whole.

## Desired Outcome
One accepted turn keeps its replay usable until recovery is no longer possible; every produced execution stream is consumed or disposed; successful terminal delivery is not relabeled cancellation; failed spill removal remains attributable and observable without exposing prompt data or paths.

## Approach
Perform full brownfield discovery, requirements review, implementation-gap analysis, owner-first design, independent design/task review, remediation, and final mechanical compilation. Reuse the existing source, holdalive, core receive, and turn-audit seams. No runtime implementation is authorized by this document-generation workflow.

## Scope
- **V1 slice**: Repair the end-to-end accepted-turn ownership contract across execution handoff, cancellation, terminal delivery, and spill cleanup.
- **First consumer**: Existing streaming HTTP frontends using the production runtime executor; shared canonical callers benefit from abandonment cleanup as well.
- **Safe degradation**: Preserve existing pre-commit canonical fallback, body admission, cancellation errors, and pre-output-only recovery. A cleanup failure must not create a second execution or falsify completed response delivery.
- **Deferred**: Proof budgets, composition identities, affinity, economic evidence, response clocks, eligibility generation, alternate-assessor retirement, and unrelated equivalent cleanups belong to the already-agreed later remediation workstreams.
- **Out**: New request representations, public SDK changes, provider-specific semantics, persisted cleanup queues, restart scavenging, new background workers, and enabling the default-off optimization.

## Boundary Candidates
- Frontend transfer of an accepted replay source to the returned execution lifetime.
- Shared holdalive cancellation/result ownership handoff.
- Existing core turn outcome and terminal cleanup ownership.
- Existing spill lifecycle and tracked-reader removal/error handoff.

## Out of Boundary
This specification does not redesign billing settlement, route selection, capability proofs, protocol identity, or generation publication. It does not reopen the archived original specification or change unrelated approved specs.

## Upstream / Downstream
- **Upstream**: The archived fast-path contract, existing stream-close contract, and core routing/terminal invariants.
- **Downstream**: Subsequent proof/parity and architecture-deepening work may rely on corrected lifetimes; it is not a prerequisite for this V1.

## Existing Spec Touchpoints
- **Extends behavior from**: Archived `large-payload-streaming-fast-path`; history remains untouched.
- **Adjacent**: Active `high-concurrency-performance-hardening` and `billing-financial-safety-contracts`; discovery must identify overlapping files and invariants without expanding scope.

## Constraints
- Base: `10ed7ca12e2a7b30fc4d8f19d7b0dc27fca33e36`; audit baseline `8a041a2a`, differing only by unrelated ACP/CI changes.
- At most five requirement areas, 25 acceptance criteria, 300 design lines, and 12 executable leaf tasks.
- TDD during later implementation, tests through serving seams, focused non-race local checks, and remote-only race evidence.
- Keep ownership lexical where possible; no generic effect registry or alternate terminal authority.
- Artifact generation/review and explicit implementation approval are separate states.
