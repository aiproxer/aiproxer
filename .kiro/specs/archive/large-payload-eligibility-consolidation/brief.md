# Brief: large-payload-eligibility-consolidation

## Problem
Maintainers must synchronize typed-plane identities and request-access classifications between the SDK manifest and handwritten large-body eligibility catalogs. Static compilation and defensive assessment also repeat hook and narrow-port blocker policy. These copies increase change amplification and can obscure the fail-closed decisions protecting proxy clients.

## Current State
The SDK already extracts canonical plane metadata through its generator and freezes generated policy. The large-body compiler separately maintains a closed count, ordered identifiers, and default access classes. Runtime composition copies occupancy into that separate census. Independent dynamic revalidation is necessary; separately authored copies of the same predicates are not.

## Desired Outcome
An existing plane declaration supplies the eligibility metadata consumed by production without additional handwritten identity/classification catalogs. Static and defensive evaluations reuse common hook/port decisions while retaining their distinct roles, current restrictions, diagnostic meaning, and fail-closed behavior.

## Approach
Conduct source-backed brownfield discovery on the current branch, compare derived-core generation with frozen SDK projection, choose the smallest existing-generator extension, and compile requirements, design, tasks, research, and independent review evidence. Do not implement Go changes during specification generation.

## Scope
- **V1 slice**: One eligibility-policy consolidation with its production compiler/composition consumer; no new wire capabilities.
- **First consumer**: Production large-body eligibility summary compilation and authority revalidation used by the standard HTTP host.
- **Deferred**: Broader generator rewrites, new extension planes, new counters/diagnostics, wider wire subsets, and unrelated runtime repairs.
- **Out**: Replay lifetimes, cancellation disposal, proof-memory fixes, generation-token repair, economic evidence, affinity, response clocks, and alternate-assessor retirement owned by sibling specs.

## Boundary Candidates
- Existing manifest extraction and deterministic generated metadata.
- Closed typed-plane eligibility projection and immutable composition input.
- Shared pure hook/port blockers with independent dynamic revalidation.

## Upstream / Downstream
- **Upstream**: Existing SDK manifest, generator, frozen metadata, and composition occupancy.
- **Downstream**: Existing static disposition and request-specific authority/backend proof; neither is replaced.

## Existing Spec Touchpoints
- **Baseline**: Archived `large-payload-streaming-fast-path` specification.
- **Adjacent**: `large-payload-lifecycle-correctness`, `large-payload-proof-and-parity`, `large-payload-assessment-simplification`; sibling worktrees are read-only alignment sources, not changes imported into this branch.

## Constraints
Preserve canonical/replay representations, immutable generation pinning, zero-allocation static rejection, unknown/unclassified rejection, stable blocker identity, conservative response-only restrictions, and provider-neutral dependency direction. No new public SDK contract, runtime registry, reflection, source imports into core, or changed optimization defaults unless brownfield evidence establishes a necessary minimal change.
