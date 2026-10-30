# Requirements Document

## Project Description (Input)
Proxy operators and clients currently encounter gaps in accepted large-request execution lifetimes: replay storage can close before pre-output recovery finishes, cancellation can abandon a produced stream, successful terminal delivery can be audited as failure, and failed spill deletion can be forgotten. Repair the end-to-end lifecycle contract using existing owners while preserving canonical fallback, cancellation, privacy, and output commitment semantics.

## Introduction
Repair one accepted-turn lifecycle from execution handoff through response disposal and replay-storage cleanup. Keep both canonical and bounded replay representations; neither requires a second orchestration or terminal owner.

## V1 Slice
- **Delivers**: Usable replay during permitted recovery, exactly-once disposal of abandoned streams, truthful terminal auditing, and observable failed spill cleanup.
- **First consumer**: Existing production streaming frontends and runtime executor; shared cancellation disposal also protects canonical execution.
- **Safe degradation**: Preserve existing fallback before wire commitment and recovery only before downstream content. Cleanup failure is reported separately and does not undo delivered content.

## Deferred
- `large-payload-proof-and-parity` — follow-up brief: bound nested metadata, composition proof identity, session-affinity eligibility, provider-economic evidence, and response-clock parity; no new proof policy in this slice.
- `large-payload-assessment-simplification` — follow-up brief: retire unused assessment concepts after moving meaningful tests to serving interfaces; remove production-resident test scaffolding and unrelated equivalent wrappers.
- `large-payload-eligibility-consolidation` — follow-up brief: derive access classification from the frozen SDK manifest, consolidate duplicate blockers, preserve hook census and conservative protocol support.
- Restart scavenging, persistent cleanup queues, automatic unlink retries, and spool-path/log hardening outside the cleanup signal — follow-up brief: require a separate owner and threat/lifetime analysis if later requested. No remote issue creation is part of this spec-generation request.

## Boundary Context
- **In scope**: Source ownership transfer, abandoned execution-result disposal for both representations, logical turn outcome at stream close, and failed immediate/deferred file removal.
- **Out of scope**: New billing rules, routing policy, backend semantics, default enablement, public API/ABI/config expansion, performance refactoring, and changes to archived or adjacent specifications.
- **Adjacent expectations**: Preserve the approved financial safety and high-concurrency contracts; changes to their shared terminal or stream seams require revalidation, not duplicate implementation.
- **Boundary ownership**: Frontend owns capture/handoff and cancellation delivery; core owns permitted recovery, logical terminal outcome, and replay-storage lifetime. Adapters keep wire semantics.

## Requirement 1: Replay remains usable for the accepted execution lifetime
**Objective:** As a client, I want an accepted replay request to recover before output without losing its request body.
### Acceptance Criteria
1. While an accepted execution can perform permitted pre-output recovery, the proxy shall keep its replay source available for opening replacement readers. **[1.1]**
2. When execution handoff fails before a usable stream is returned, the proxy shall release the captured source and any acquired readers without transferring ownership to a nonexistent consumer. **[1.2]**
3. When the returned execution lifetime is closed, the proxy shall close the execution stream before releasing its replay source and shall release that source at most once. **[1.3]**
4. When downstream content has been committed, the proxy shall preserve the prohibition on transparent retry or failover. **[1.4]**

## Requirement 2: Cancellation cannot abandon produced execution resources
**Objective:** As an operator, I want cancellation to leave every produced stream with a known disposal owner.
### Acceptance Criteria
1. When execution produces an owned stream, the proxy shall transfer it to the caller or dispose it exactly once, including when cancellation and result readiness coincide. **[2.1]**
2. When cancellation ends a wait before execution returns, the proxy shall dispose a subsequently produced stream without requiring the canceled caller to wait for execution completion. **[2.2]**
3. If execution returns both a stream and an error that prevents caller consumption, the proxy shall dispose the stream and preserve the established caller-facing error. **[2.3]**
4. When cancellation-result disposal is enabled, the proxy shall preserve existing holdalive informational-response, grace-period, and disabled-mode behavior. **[2.4]**

## Requirement 3: Cleanup preserves the logical terminal outcome
**Objective:** As an operator, I want completed responses to remain successful in the turn audit even when their consumer closes without an extra receive.
### Acceptance Criteria
1. When the surviving logical execution delivers its successful terminal event and its consumer closes without receiving EOF, the proxy shall finalize its turn audit once with the established success outcome. **[3.1]**
2. If the logical turn fails or is canceled before completion, the proxy shall retain the corresponding failure or cancellation outcome rather than reporting success from cleanup alone. **[3.2]**
3. When pre-output recovery replaces an attempt, the proxy shall determine the logical turn outcome from the surviving execution rather than an abandoned attempt's terminal event. **[3.3]**
4. When a logical turn resolves through success, failure, or cancellation, the proxy shall finalize one logical outcome exactly once through its existing authority and shall preserve attempt, continuity, terminal usage, and billing ownership; attempt events and subsequent cleanup shall not establish another logical outcome. **[3.4]**

## Requirement 4: Failed spill removal is attributable and observable
**Objective:** As an operator, I want to distinguish reclaimed replay storage from a plaintext spill that failed to unlink.
### Acceptance Criteria
1. While a spill has active readers, the proxy shall defer physical removal until the final reader releases it and shall reject new readers after root closure. **[4.1]**
2. When removal succeeds or the spill is already absent, the proxy shall mark physical cleanup complete; if removal fails, the proxy shall retain an attributable failure state instead of recording successful reclamation. **[4.2]**
3. When immediate or deferred spill removal fails, the proxy shall emit a bounded, sanitized operator-observable cleanup signal independently of cleanup-error delivery to its owner, without emitting the filesystem path, raw prompt bytes, or raw filesystem error; this signal shall not become a request or stream failure or alter the logical outcome. **[4.3]**
4. If spill removal fails after response completion, the proxy shall preserve the response outcome and existing logical reservation-release semantics, and shall not initiate request recovery or an unowned retry worker. **[4.4]**

## Requirements Review
Four areas and 16 criteria describe observable lifecycle effects, not a replacement execution design. Success, failure, cancellation, late completion, deferred removal, privacy, and existing ownership are explicit. Tests belong to implementation tasks, not acceptance criteria. The slice adds no persistence or public contract and keeps all remaining audit findings in the named deferred workstreams.
