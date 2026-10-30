# HANDOFF — agent-loop-breach-prevention

## Mission

Implement Agent Loop Guard only as a concrete provider for the approved generic platform spec:

`.kiro/specs/terminal-decision-feature-extension/`

The platform owns the exclusive FeatureBundle contribution, terminal chokepoint, core continuation transaction, conversation-view steering lifecycle, immutable generation activation/withdrawal, process secure-session policy, and generic client/operator endpoints. ALG owns policy and evidence only.

## Dependency Gate

Before ALG provider integration, verify the platform spec is approved and its provider-contract, terminal-chokepoint, continuation-transaction, immutable-generation, process-policy, endpoint, and architecture-ratchet tasks are complete. Do not add a workaround in core if a platform task is incomplete; return to the platform spec instead.

## ALG Work Order

1. Establish platform dependency and characterize old ALG behavior/removal targets.
2. Add opt-in provider configuration and singular FeatureBundle registration.
3. Implement canonical cause/evidence projection and delegate pre-output transport to existing recovery.
4. Implement detached bounded semantic verifier and conservative multi-state verdict policy.
5. Implement stable progress tracking and bounded conditional internal recovery intent.
6. Integrate through the platform contract; migrate concrete ALG policy and verify the platform-owned removal of old core ALG policy/append/`guardHidden` authorities.
7. Add bounded telemetry, false-stop fixtures, and architecture/removal ratchets.
8. Run simplification and repository quality gates.

## Binding Decisions

- Only `CONTINUE` with a concrete existing user objective can produce an intent; timeout/error/malformed/uncertain always allow stop.
- Pre-output transport recovery remains the existing platform policy. Post-output continuation is a new B-leg intent, never retry/replacement.
- Completed tool calls/results are retained and never re-executed solely because a later stream failed.
- Recovery wording explicitly denies new user intent, approval, permission, or scope expansion and stops for user-dependent work.
- ALG consumes the platform's immutable next-request policy snapshot and never owns client/operator policy state or endpoints.
- ALG must not append to `Call.Messages`/`Call.Items`, use `turnTerminal.guardHidden`, claim terminals, open backends, mutate snapshots, or persist steering overlays.
- Fixed `alg-rec` is a bounded provider-scoped overlay identifier only when accepted by the platform intent contract; anchor resolution, snapshot freeze/reassertion, deactivation, and stale cleanup remain platform-owned.
- No Go native plugin loading, DI/container, service locator, reflection registry, generic effect runtime, provider-name switch, or concrete provider SDK import in core.

## Superseded Material

The former ALG Task 11 and its implementation notes are superseded by the platform spec's generic continuation/steering tasks. No pending ALG Task 11 remains. The old claims that the ALG spec owned PR435 remediation, direct append removal, or `turnTerminal.guardHidden` elimination are intentionally removed; ALG tests may verify the platform contract but must not implement that lifecycle.

## Verification Expectations

- Focused provider/config/evidence/verifier/progress/intent tests.
- Platform contract integration and no-provider removal tests.
- Deterministic semantic-stop, transport, cancellation, budget, and no-progress matrix.
- Architecture ratchets for no direct append, no `guardHidden`, no concrete ALG core references, and no second policy owner.
- `go test ./...`, `make quality-checks`, `make test`, and `make qa` when permitted; report Windows race limitations honestly.
