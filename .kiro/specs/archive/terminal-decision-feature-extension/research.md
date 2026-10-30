# Research and Brownfield Gap Analysis

## Summary

- **Feature**: `terminal-decision-feature-extension`
- **Discovery Scope**: Brownfield extension platform and lifecycle integration
- **Key Findings**:
  - `FeatureBundle`/`MergedFeatureSurface` currently merge many ordered contributions but have no singular provisional-terminal capability; adding a slice would create ambiguous terminal authority.
  - `terminal.Owner`, `streamrecovery`, continuation, `ProcessServices`, and immutable generation management already own the relevant lifetimes and must remain authoritative.
  - The existing ALG planning wave placed conversation-view steering and terminal orchestration inside concrete ALG work. The generic seam must own those mechanics so ALG becomes removable policy.
  - A process-owned bounded secure-session policy is required because generation reload is not an appropriate owner for client/operator intent.

## Research Log

### Existing composition and generation owners

- **Sources consulted**: `internal/featurebundle/merge_surface.go`, `pkg/lipsdk/feature/bundle.go`, `internal/core/extensions/snapshot.go`, `internal/infra/runtimebundle/process_services.go`, `internal/infra/runtimehost`, `.kiro/steering/structure.md`, `.kiro/steering/tech.md`.
- **Findings**: Feature bundles merge at one explicit point; request snapshots are immutable; `ProcessServices` registers process cleanup in acquisition order; generation manager publishes, pins, quiesces, drains, and closes generations.
- **Implications**: Add one singular field and one merge validation path. Compose provider and policy explicitly at generation/process roots. Never live-mutate a published snapshot or create a parallel registry/cleanup owner.

### Existing terminal and continuation authorities

- **Sources consulted**: `internal/core/terminal`, runtime terminal settlement tests, `internal/core/streamrecovery`, continuation contracts, `.kiro/specs/agent-loop-breach-prevention/research.md` and `design.md`.
- **Findings**: Terminal ownership already linearizes request/attempt outcomes. Stream recovery owns transport classification and pre-output retry. Continuation records retain canonical trajectory and lineage. The ALG design correctly identified that post-output work must be preserve-and-continue, never replay.
- **Implications**: The platform chokepoint must sit before the logical terminal claim, while B-attempt settlement remains independent. Core transaction code must reuse existing continuation/authority/billing/B2BUA paths.

### Conversation-view review discoveries

- **Source**: ALG Task 11 review and merged PR #435 conversation-view steering infrastructure.
- **Finding 1**: Direct `Call.Messages`/`Call.Items` append plus `turnTerminal.guardHidden` creates two hidden-content authorities.
- **Finding 2**: `AfterIngressTail` must resolve against the accepted user ingress call, not a post-B1 call ending in assistant output; otherwise anchor validation fails.
- **Finding 3**: The steering store has no pattern query API. A fixed bounded provider overlay ID scoped by A-leg permits deterministic stale cleanup without adding an unapproved query surface.
- **Finding 4**: Raw A-leg IDs must not be prefixed into bounded overlay IDs; the store already scopes records by A-leg.
- **Implications**: These are generic core continuation invariants and now map to requirements 3.5, 3.6, 8.4, 10.3, and tasks 4.1/4.2/8.1. The concrete ALG spec supplies only its bounded intent/content and provider-scoped ID.

| Review discovery | Platform requirements | Platform tasks |
|---|---|---|
| Direct call append plus `guardHidden` creates dual hidden authorities | 3.5, 10.3, 10.4 | 4.1, 4.2, 8.1, 8.2 |
| Post-B1 assistant trajectory is invalid for ingress-tail anchor | 3.1, 3.5, 8.4 | 4.1, 4.2 |
| Raw A-leg prefix can exceed bounded overlay identity | 3.5, 9.4, 10.3 | 4.2, 8.1 |
| No pattern-query API for stale overlay discovery | 3.6, 8.4 | 4.2, 8.1 |
| Frozen snapshot must be reasserted exactly once after transforms | 3.3, 3.5, 4.2 | 4.1, 4.2, 5.1, 8.1 |

### Secure-session policy ownership

- **Sources consulted**: `internal/infra/runtimebundle/secure_session.go`, existing secure-session validation and diagnostics posture, `ProcessServices` lifecycle, project security steering.
- **Findings**: Secure-session authority and stores already authenticate/attribute sessions. Generation reload is intentionally immutable and process services are the established process owner. No existing durable policy store should be repurposed for ephemeral client/operator intent.
- **Implications**: Add a bounded process store keyed by authenticated secure-session/A-leg scope. Keep separate tri-state sources, use disable-first resolution, snapshot next-request, preserve through reload/disable-reenable, and reset naturally at process start.

### Exact policy endpoint contract

- **Selected routes**: Client `GET|PUT|DELETE /v1/lip/session/features/{feature_id}` resolves only the current authoritative secure session. Operator `GET|PUT|DELETE /admin/session-features/{session_id}/{feature_id}` runs under existing admin authentication and validates the authoritative target.
- **Selected writes**: Both PUT routes accept exactly `{"enabled": true|false}`; client DELETE clears client intent and operator DELETE clears operator intent. GET returns bounded feature/actor/effective state and revision, never `applies_from`; successful PUT/DELETE additionally return `applies_from: next_request`. No request-side expected revision is accepted; revision is response and internal linearization evidence only.
- **Error mapping**: Client and other existing mappings remain unchanged. For operators, unauthenticated access when distinguished upstream authentication fails is 401 `unauthorized`; a diagnostics shared-secret mismatch is 403 `forbidden`; an authenticated operator lacking target authorization is 403 `forbidden`; and an authorized operator with an invalid target is 404 `session_not_found`. Every error mutates nothing.

### Architecture and simplification review

- **Alternatives considered**:
  - Add a provider slice/chain to FeatureBundle — rejected because ordering and multiple terminal authorities become ambiguous.
  - Put ALG logic directly in `internal/core` — rejected because it creates provider-specific branches and prevents removal.
  - Add a service locator/DI or generic effect runtime — rejected by Go-LIP steering and Cordis adaptation; existing explicit owners suffice.
  - Let providers open backend legs or append hidden messages — rejected because it bypasses terminal, authority, and conversation-view ownership.
  - Keep mutable policy on each generation — rejected because reload would change in-flight request behavior and disable/re-enable would lose operator/client intent.
- **Selected approach**: one typed provider contribution, one core chokepoint/transaction, immutable generation projection, process-owned bounded policy, generic authenticated endpoints.
- **Evidence gate**: implementation must characterize terminal claim sites, contribution fields, concrete ALG core references, policy owners, and cleanup paths before retaining the seam. If deterministic counts do not show simpler ownership, narrow or reject it.

## Design Decisions

### Decision: Singular provider instead of a chain

- **Context**: Terminal decisions are not ordinary ordered transformations; two providers could each hold or publish a terminal.
- **Selected approach**: zero-or-one provider field with merge-time conflict rejection.
- **Rationale**: preserves one authority and makes provider removal/no-provider behavior explicit.
- **Trade-off**: future policies must compose inside one provider or obtain a separately approved contract; this is intentional.

### Decision: Core transaction instead of provider-owned continuation

- **Context**: Continuation touches terminal settlement, canonical trajectory, steering, route/authority/billing/B2BUA, and output commitment.
- **Selected approach**: provider returns bounded intent; core validates and executes one private transaction.
- **Rationale**: one owner for irreversible boundaries and cleanup; no retry fiction after output.
- **Trade-off**: provider authors need a sufficiently expressive intent contract, but cannot bypass core invariants.

### Decision: Process policy with two tri-state sources

- **Context**: Client and operator controls need independent intent and must behave consistently across reloads.
- **Selected approach**: process-owned bounded store, `unset/enabled/disabled` per source, disable-first effective resolution, request snapshot.
- **Rationale**: survives generation changes while keeping request behavior immutable; restart reset avoids accidental durable policy.
- **Trade-off**: policy state is ephemeral and operators must reapply it after restart.

### Decision: Generic HTTP surfaces

- **Context**: Concrete ALG routes would couple the platform to one feature and make replacement harder.
- **Selected approach**: provider-neutral client/operator resources with existing authentication and authorization, response-only revision evidence, and next-request semantics.
- **Rationale**: future providers can reuse controls; secure-session and operator ownership remains at established adapters.
- **Trade-off**: endpoint clients cannot assume ALG-specific response fields.

## Risks and Mitigations

- Provider timeout or panic could delay terminal publication — bounded contexts and allow-stop normalization.
- Continuation publication and B1 settlement ordering could duplicate settlement or lose the original candidate — explicit transaction state and schedule tests.
- Stale steering overlays could leak into a later request — fixed scoped ID, deterministic external-ingress deactivation, fail-closed persistence errors.
- Policy store could become an unbounded process registry — hard key/value bounds, reject-at-capacity semantics without mutation, and an architecture ratchet.
- A future feature could recreate core-specific branches — import/provider-name ratchets and removable fake-provider test.
- Abstraction cost could exceed benefit — baseline/target operation counts and mandatory simplification gate.

## References

- `.kiro/steering/structure.md` — package ownership and immutable generation map.
- `.kiro/steering/routing-and-orchestration.md` — terminal, B2BUA, and no-post-output-failover invariants.
- `.kiro/steering/tech.md` — explicit composition, process lifecycle, and no DI/native plugin rules.
- `.kiro/steering/testing.md` — TDD, race, architecture, and quality gates.
- `.kiro/specs/agent-loop-breach-prevention/` — concrete policy requirements now dependent on this platform.
