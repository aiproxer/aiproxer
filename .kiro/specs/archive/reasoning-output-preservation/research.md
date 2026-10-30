# Current-State Review, Requirements Gap Analysis, and Design Validation: Reasoning Output Preservation

Generated: 2026-07-17T08:52:15+02:00

## Status

- Repository: `matdev83/go-llm-interactive-proxy`
- Reviewed ref: `main` at `cda1c3f02ef6bbb7eec7c314c9469275f309fcc5`
- Source issue: [#157 — Reasoning output preservation](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)
- Requirements source: `.kiro/specs/archive/reasoning-output-preservation/requirements.md`
- Design source: `.kiro/specs/archive/reasoning-output-preservation/design.md`
- Review mode: static source, contract, steering, archived-spec, issue-plan, and lifecycle review through the connected GitHub repository.
- Scope: brownfield requirements analysis followed by design validation. This PR changes specifications only.

## Executive Assessment

The repository already carries response-side reasoning through canonical events and has strong routing, immutable-attempt, secure-session, plugin, and adapter boundaries. It does not yet have the request-side representation or lifecycle seams needed to restore historical reasoning safely.

The feature cannot be implemented correctly as only an early request transform and response hook:

- early request transforms do not know the selected backend/model;
- candidate-aware request-part hooks execute after important capability, context, and accounting decisions;
- response hooks have no exactly-once attempt outcome lifecycle;
- completion gates buffer the response and are not an observation mechanism;
- canonical request messages cannot carry reasoning;
- current `CapabilityReasoning` is soft-downgradable and does not mean replay;
- adapters do not currently round-trip the required historical reasoning shapes.

The selected direction is to add two generic extension seams—a candidate-aware attempt transform and a final-canonical-stream observer—then implement issue #157 as an official feature plugin. Historical reasoning becomes a small shared canonical concept; all provider wire details remain adapter-owned.

## Reviewed Assets

### Steering and Kiro workflow

- `AGENTS.md`
- `.kiro/AGENTS.md`
- `.kiro/steering/api-standards.md`
- `.kiro/steering/routing-and-orchestration.md`
- `.kiro/steering/structure.md`
- `.kiro/steering/tech.md`
- `.kiro/steering/testing.md`
- `.kiro/settings/templates/specs/{requirements,design,tasks,spec.json}`

### Canonical and runtime contracts

- `pkg/lipapi/call.go`, `parts.go`, `events.go`, `capabilities.go`, `output_commit.go`
- canonical validation, clone, limits, sizing, token-accounting, and fuzz tests
- ADR 0002 immutable baseline/per-attempt derivation
- `internal/core/runtime/executor_open_attempt.go`
- retry/recv handlers, completion gates, parallel race, interleaved thinking, and B2BUA tests

### Extension platform and state

- `pkg/lipsdk/feature/bundle.go`
- request transforms, request/response part hooks, completion gates, session/scope/workspace views, state store
- core extension runners, snapshots, feature merge, registry wrappers, runtimebundle wiring, diagnostics inventory

### Protocol adapters

- OpenAI-compatible Chat frontend/backend adapters
- OpenAI Responses frontend/backend adapters
- Anthropic Messages frontend/backend adapters
- OpenRouter and compatible/custom routing helpers
- Gemini frontend/backend adapters

### Adjacent specifications

- archived Anthropic thinking-signature requirements/design/tasks
- interleaved-thinking configuration, memo, state, and runtime tests

## Existing Strengths to Preserve

1. **Response reasoning is canonical.** `EventReasoningDelta` exists, and Anthropic signatures already use a provider-neutral canonical carrier.
2. **Attempt isolation is explicit.** ADR 0002 requires a fresh clone from one immutable baseline for every candidate.
3. **Mutation belongs behind extensions.** Feature bundles already provide ordering, snapshot pinning, panic isolation, and explicit composition.
4. **Candidate metadata exists.** Runtime knows backend instance/family, model, candidate key, B-leg lineage, scope, and context/accounting facts.
5. **Authoritative session views exist.** Runtime can supply a safe partition without trusting client session hints.
6. **Parity and release practices exist.** Goldens, composed tests, race, fuzz, and architecture gates are normal project evidence.

## Brownfield Requirements Gap Analysis

| ID | Severity | Finding | Required disposition |
| --- | --- | --- | --- |
| **G-01** | P0 | Canonical requests have no historical reasoning part. | Add assistant-only ordered `PartReasoning` with bounded dialect/text/signature/opaque payload. |
| **G-02** | P0 | `CapabilityReasoning` requests new reasoning and is soft-downgradable. | Add hard `reasoning_replay`, derived from historical reasoning parts. |
| **G-03** | P0 | Request-wide transforms run before candidate selection. | Restore only on a candidate-specific clone after route selection/interleaved shaping. |
| **G-04** | P0 | Request-part hooks run after initial capability/context/preflight work. | Add an earlier candidate attempt-transform stage and recompute final eligibility/accounting. |
| **G-05** | P0 | Hook errors cannot cleanly exclude only one incompatible candidate. | Add `continue` and `exclude_candidate` decisions distinct from errors. |
| **G-06** | P0 | Response hooks lack an attempt-scoped exactly-once finish lifecycle. | Add an observer factory and runtime-owned finish outcome. |
| **G-07** | P0 | Completion gates buffer and would change TTFT. | Observe incrementally; integrate with gate results rather than using a gate. |
| **G-08** | P0 | Raw upstream events may differ from post-hook/post-gate client history. | Observe the final canonical stream after response mutation/gate resolution. |
| **G-09** | P1 | Core cannot prove frontend transport byte acknowledgement. | Define v1 success as runtime release of terminal canonical output. |
| **G-10** | P0 | Reasoning payload alone cannot reconstruct interleaved order. | Persist placement relative to non-reasoning part indexes. |
| **G-11** | P1 | Stable provider response/item identity is not a canonical v1 contract. | Use exact anchors only; duplicate associations are ambiguous. |
| **G-12** | P0 | SDK state `Get` + `Put` is not atomic bounded append/evict. | Use a feature-owned concurrent TurnStore. |
| **G-13** | P0 | Client session hints are not authoritative. | Runtime supplies an opaque authoritative session/A-leg partition. |
| **G-14** | P0 | Adapter request-side reasoning parity is incomplete. | Add adapter-owned Chat, Responses, and Anthropic dialect mappings and explicit unsupported paths. |
| **G-15** | P1 | Arbitrary backend instance IDs cannot seed a built-in catalog. | Explicit rules use instance IDs; built-ins use stable family prefixes plus model keywords. |
| **G-16** | P0 | Reasoning and anchors are sensitive and metrics can explode cardinality. | Fixed outcomes/counts/bytes only; forbid payloads, anchors, session partitions, and arbitrary labels. |
| **G-17** | P1 | V1 feature state is not durable across processes/replicas. | Document process-local/sticky-session posture and treat restart/rebalance as a state miss. |

## Requirements Remediation

The initial requirements draft was committed before this analysis. The final requirements were then revised as follows:

- made historical reasoning an assistant-only ordered canonical part;
- added hard replay capability, limits, deep cloning, sizing, and accounting participation;
- pinned the exact candidate transform position and exclusion decision;
- pinned final post-hook/post-gate observer placement and exactly-once outcomes;
- defined `success_released` rather than transport acknowledgement;
- added exact placement metadata and removed stable provider IDs from v1 matching;
- required a feature-owned process-local atomic bounded store;
- required opaque authoritative partition projection;
- split operator instance rules from family-prefix built-ins;
- made adapter dialect support explicit and incompatible paths non-lossy;
- added privacy/cardinality requirements and complete runtime/race/fuzz release evidence.

## Architecture Options Considered

### Option A — Early request transform plus response-part hook

**Rejected.** It lacks candidate identity, correct eligibility/accounting timing, exactly-once stream outcomes, and bounded state ownership.

### Option B — Completion gate plus request transform

**Rejected.** It changes TTFT by buffering and still restores before candidate identity is known.

### Option C — Core-owned preservation special case

**Rejected.** It would mix model/provider policy with core orchestration and bypass the extension platform.

### Option D — Generic attempt transform and final-stream observer plus official feature plugin

**Selected.** It preserves dependency direction, immutable-baseline isolation, streaming behavior, adapter ownership, and disabled non-interference.

## Design Validation Stage

The first `design.md` revision was reviewed against all final acceptance criteria and the actual runtime/adapter seams. The validation identified the following defects.

| ID | Validation finding | Correction applied to final design |
| --- | --- | --- |
| **V-01** | Observer was before response hooks/gates. | Moved observation to final canonical events after hooks and gate resolution. |
| **V-02** | Incompatibility was modeled as a transform error. | Added explicit `continue` / `exclude_candidate` decision. |
| **V-03** | Design assumed provider response IDs. | Removed IDs from v1; exact anchors are the sole association method. |
| **V-04** | Artifact lacked exact reasoning placement. | Added `PlacedReasoning.BeforeNonReasoningPart`. |
| **V-05** | Design used non-atomic SDK session state. | Added feature-owned atomic bounded TurnStore. |
| **V-06** | Observation could be opened for speculative/losing arms. | Open only for the active surfaced B-leg; losers never persist. |
| **V-07** | `success` overstated client delivery evidence. | Renamed and defined `success_released`. |
| **V-08** | Generic reasoning support was treated as dialect compatibility. | Added hard replay plus exact candidate dialect profile. |
| **V-09** | Restored content could bypass size/token/accounting calculations. | Required post-transform recomputation and backend-ingress measurement. |
| **V-10** | Conflicting client reasoning did not have a hard non-overwrite rule. | Classified as conflicting and left unchanged. |
| **V-11** | Telemetry fields/cardinality were under-specified. | Restricted records to fixed outcomes, counts, bytes, and safe correlation. |
| **V-12** | Built-in catalog matching relied on operator instance identity. | Built-ins use backend-family prefixes/effective flavor plus model keywords. |

### Design Validation Checklist

- **Requirement coverage:** all 79 acceptance criteria map to a design component and at least one task group.
- **Dependency direction:** no provider SDK/HTTP type crosses into canonical, SDK, or core contracts.
- **Mutation ordering:** candidate transform occurs before final capability/context/token/checkpoint/authorization work.
- **Streaming correctness:** final observation is incremental and does not enable completion buffering.
- **Failover/parallel correctness:** every candidate uses an independent baseline clone; losing arms do not persist artifacts.
- **No-retry invariant:** observer/storage failures cannot initiate retry after output commitment.
- **Session isolation:** plugin consumes only runtime-projected authoritative partitions.
- **Privacy:** no reasoning, signature, opaque data, excerpt, anchor, or partition is exposed.
- **Backward compatibility:** absent/disabled feature contributes no participants or state.
- **Delivery boundary:** v1 process-local limitation is explicit.

## Final Validation Verdict

**PASS after corrections.**

The final `design.md` is suitable for task generation. It satisfies the gap-remediated requirements, preserves repository architecture and streaming/failover invariants, and does not claim capabilities that the current runtime cannot prove.
