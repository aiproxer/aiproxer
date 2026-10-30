# Research and Brownfield Gap Analysis

## Summary

- **Feature**: `agent-loop-breach-prevention`
- **Discovery Scope**: Concrete feature-provider policy depending on `terminal-decision-feature-extension`
- **Key Findings**:
  - Existing stream recovery, continuation, auxiliary requests, terminal ownership, B2BUA, billing, and canonical event infrastructure are reusable; ALG must not duplicate them.
  - A clean-stop verifier is necessarily conservative: generic future-looking text, user questions, optional improvements, and user-owned next steps are not sufficient continuation evidence.
  - Post-output interruption must preserve committed canonical trajectory and completed tool results; replaying the committed attempt is unsafe.
  - Former ALG Task 11 findings are generic platform requirements now owned by the terminal-decision platform, not unfinished ALG work.

## Research Log

### Brownfield authorities

- **Sources consulted**: `.kiro/steering/product.md`, `structure.md`, `routing-and-orchestration.md`, `testing.md`; current `internal/core/streamrecovery`, `continuation`, `auxreq`, `terminal`; `.kiro/specs/terminal-decision-feature-extension/`.
- **Findings**: stream recovery owns transport and pre-output replay; continuation owns canonical retained trajectory/lineage; auxiliary requests provide detached internal execution and recursion controls; terminal owners linearize B-attempt and A-request outcomes; billing/authority/B2BUA remain normal admission paths.
- **Implication**: ALG returns a provider-neutral decision intent only. Platform core executes the intent and preserves all existing authorities.

### Prior-art policy findings

- **Stop hooks and independent goal checks**: provisional terminal plus a separate bounded evaluator is a useful pattern, but evaluator uncertainty must not create work.
- **Unexpected-stop classifiers**: wording-only heuristics false-positive on complete answers containing “Next steps” or optional offers.
- **Next-speaker checkers**: distinguish model-owned immediate work from user-owned questions, but a bare “Please continue” can invent scope.
- **Stuck detectors and explicit completion**: progress fingerprints and trusted normalized completion facts are useful evidence, not universal requirements.
- **Implication**: ALG uses a multi-state verdict, concrete objective requirement, conditional internal recovery wording, explicit completion policy, and a no-progress/maximum budget.

### Platform migration discoveries

The old ALG design included a pending Task 11 for conversation-view steering integration. The approved platform redesign resolves those discoveries as generic contract/transaction requirements:

1. **Duplicate hidden authorities**: direct `Call.Messages`/`Call.Items` append and `turnTerminal.guardHidden` conflict with the canonical conversation-view steering owner. ALG now returns internal intent content only; platform tests enforce no direct append and no secondary hidden field.
2. **Ingress anchor boundary**: `AfterIngressTail` must resolve against the accepted user ingress call, not a post-B1 call ending in assistant output. The platform transaction owns this resolution and fails closed when the terminal user message is absent/excluded.
3. **Bounded overlay identity**: ALG may request fixed provider-scoped `alg-rec`, while the platform scopes it by authoritative A-leg. Raw A-leg IDs are not prefixed into the bounded overlay ID.
4. **Stale cleanup**: the steering store has no pattern query API. The platform deterministically deactivates the fixed provider-scoped ID on external ingress; not-found/inactive is a no-op and persistence failure fails closed.
5. **Snapshot/reassertion**: the platform freezes a new next-turn snapshot after intent registration and reasserts it once after late transforms. ALG never mutates snapshots or owns persistence.

These findings map to platform requirements 3.5, 3.6, 8.4, 10.3, and platform tasks 4.1/4.2/8.1. ALG tasks only verify the provider contract and do not recreate Task 11.

## Architecture Pattern Evaluation

| Option | Strengths | Risks / Limitations | Verdict |
|---|---|---|---|
| ALG policy in core | Short initial call path | Provider-specific branches, impossible removal, duplicate terminal authority | Rejected |
| Wording-only continuation | Cheap | False positives, scope invention, no canonical evidence | Rejected |
| Bare `Please continue` | Simple | Treated as new intent; can repeat/invent work | Rejected |
| Require explicit completion tool | Strong when available | Not portable across frontends | Optional strong evidence only |
| Feature provider plus bounded verifier and intent | Reusable, testable, conservative | Adds bounded verifier cost and platform dependency | Selected |

## Design Decisions

### Decision: ALG is a feature provider, not a core gate

- **Context**: A generic platform now owns the terminal chokepoint and continuation transaction.
- **Selected approach**: Build ALG under `internal/plugins/features/agentloopguard`, register one provider through FeatureBundle, and consume immutable policy/continuation evidence.
- **Rationale**: Concrete policy becomes removable without changing core and cannot bypass terminal, authority, steering, or generation owners.
- **Trade-off**: Provider contract validation and platform dependency add an integration gate; this is preferable to hidden ownership.

### Decision: Conservative multi-state verdict

- **Context**: False-positive continuation can create unauthorized work or side effects.
- **Selected approach**: `ALLOW_STOP`, `CONTINUE`, `NEEDS_USER`, `BLOCKED`, `UNCERTAIN`; only `CONTINUE` with a concrete existing objective is actionable.
- **Rationale**: User-dependent and uncertain cases stop safely.
- **Trade-off**: Some premature stops remain uncorrected; telemetry and future evaluation can improve policy without weakening safety.

### Decision: Conditional internal recovery content

- **Context**: A bare continuation message can be mistaken for new user intent.
- **Selected approach**: bounded internal wording that denies new request/approval/scope, resumes only existing work, and stops for user-dependent steps.
- **Rationale**: Second safety barrier after verifier decision.
- **Trade-off**: More content and contract validation than a one-line nudge.

### Decision: Existing transport and canonical continuation boundaries

- **Context**: Pre-output retry is replay-safe; post-output replay is not.
- **Selected approach**: Delegate pre-output transport to existing recovery; return a new-leg intent only for safe post-output trajectory.
- **Rationale**: Preserves no-post-output-failover and completed side-effect invariants.
- **Trade-off**: Unsafe interruptions stop instead of attempting aggressive recovery.

## Risks and Mitigations

- Verifier false positive — require concrete objective, conditional wording, explicit completion evidence, no-progress cap, and user-authority stop branches.
- Verifier false negative — use bounded objective/trajectory evidence and observability; do not respond by weakening uncertainty behavior.
- Added latency/cost — opt-in, bounded timeout, small internal role, trusted explicit completion option.
- Duplicate tool side effects — retain completed results and reject unsafe partial arguments/opaque state.
- Recursive verifier recovery — detached internal request with ALG suppression and depth guard.
- Platform contract drift — dependency gate and provider contract tests before integration.
- Concrete feature reintroduces core authority — import/name/append/`guardHidden` ratchets and removable-provider tests.

## References

- `.kiro/specs/terminal-decision-feature-extension/` — generic provider, transaction, policy, and endpoint platform.
- `.kiro/steering/routing-and-orchestration.md` — pre-output recovery, B2BUA, and no-post-output-failover rules.
- `.kiro/steering/testing.md` — TDD, canonical fixtures, race, and architecture tests.
- Existing project prior-art links and issue context are retained in repository history; implementation decisions are summarized above for a self-contained spec.
