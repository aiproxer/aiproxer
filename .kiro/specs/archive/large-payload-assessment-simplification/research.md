# Research and Design Decisions

## Summary
- **Feature**: `large-payload-assessment-simplification`.
- **Discovery**: Brownfield refinement; integration-focused discovery, complete candidate consumer classification, and historical refutation.
- **Baseline**: `ba1df9f9fc46c079acdcb013fd9fa4a38e5ca0fa`; original architecture audit at `8a041a2ad9c9c2c0d0bf8ee57005fe66f7479d6c`.
- Seven alternative wrapper/gate families and exclusively dependent projections have no serving constructions. The original six-family audit understated the deletion boundary: `BackendWireProofAssessor` and its simulated override methods are also unused.
- The runtime race helper delegates to the real race engine but remains an isolated test entry point, not a second serving engine. Delete its transfer chain, not the shared attempt owner.
- Preserve meaningful claims at concrete gates, direct validators, production assessment and real execution. Obsolete policy and vacuous sentinels are not equivalence obligations.

## Research Log
### Serving path and interface ledger
CodeGraph source and independent repository-wide bare-symbol/literal searches cover root Go, tests, public packages, connectors, connector-support, anonymous embeddings, and non-Go references. Call counts below are construction/direct-call sites, not ambiguous synthesized interface edges. No public aliases, registration, generated ABI, or connector consumers were found.

| Concept | Definition | Test construction/call sites | Serving construction/call sites | Replacement surface |
| --- | --- | --- | --- | --- |
| `AuthorityAssessor` | `internal/core/largebody/authority_gate.go:368` | 6 in 3 files | 0 | `AuthorityAssessmentGate.Evaluate`; production assessor |
| `InitialRouteAssessor` | `internal/core/largebody/initial_route_gate.go:220` | 1 in 1 file | 0 | `InitialRouteAssessmentGate.Evaluate` |
| `RouteOverrideAssessor` | `internal/core/largebody/route_override_gate.go:172` | 2 in 2 files | 0 | Concrete override gate and real executor |
| `LateSelectorAssessor` | `internal/core/largebody/late_selector_gate.go:497` | 3 in 2 files | 0 | Concrete late gate and backend-proof composition |
| `BackendWireProofAssessor` | `internal/core/largebody/backend_wire_proof.go:286` | 5 in 2 files | 0 | `BackendWireProofGate.Evaluate`; production assessor |
| `StampValidatingExecutor` | `internal/core/largebody/stamp_validation.go:246` | 3 in 1 file | 0 | `ValidateExecuteLargeBody`; real executor |
| `ConservativeDependencyAssessmentGate` | `internal/core/largebody/conservative_dependencies.go` | 19 in 3 files | 0 | Actual dependency census and authority gate |
| `AssessAuthorityGate` | `internal/core/largebody/authority_gate.go:359` | 10 in 1 file | 0 | Concrete gate evaluation |
| `executeWireParallelRace` | `internal/core/runtime/executor_execute_large_body.go:1236` | 2 in 1 file | 0 | Actual `Executor.ExecuteLargeBody` route plan |
| `WireTakeStream` | `internal/core/runtime/attempt_session.go:1201` | 1 test plus retired harness | 0 | `readyAttempt.Prepare` and `Consume` |

The internal optional capability interfaces in `internal/core/largebody/capability.go:13–46` remain. Their exclusion from public `lipsdk.ExecutorView` ABI is explicit at lines 31–40. `ProductionLargeBodyAssessor` implements the actual assessor/static-disposition contracts (`executor_execute_large_body.go:201–237,353`); `runtimebundle.buildLargeBodyAssessor` constructs it from concrete gates (`build_large_body_assessor.go:65–79`). The real executor directly invokes `ValidateExecuteLargeBody` before session preparation/BeginTurn (`executor_execute_large_body.go:372–421`).

### Deletion ledger
Baseline ranges are navigation anchors, not permission to delete unchanged line numbers after other work lands.

| Production file | Delete | Keep |
| --- | --- | --- |
| `internal/core/largebody/authority_gate.go:359–405` | Helper and `AuthorityAssessor` family | Concrete gate, census, port catalog |
| `internal/core/largebody/initial_route_gate.go:218–254` | `InitialRouteAssessor` family | Concrete gate and backend resolution |
| `internal/core/largebody/route_override_gate.go:170–251` | `RouteOverrideAssessor` family | Concrete gate and domain construction |
| `internal/core/largebody/late_selector_gate.go:495–584` | `LateSelectorAssessor` family | Concrete gate, bounded contracts, `unionWireDomainFacts` |
| `internal/core/largebody/backend_wire_proof.go:136–429` | `LegalDomainBackends`, `VerifyOverrideCandidate`, `ComposeOverrideCandidate`, `BackendWireProofAssessor`, local `containsString` | Gate constructor, `resolveBackend`, `Evaluate` at lines 1–134 |
| `internal/core/largebody/stamp_validation.go:243–283` | `StampValidatingExecutor` family | Errors, live facts, validators, stamp revalidation |
| `internal/core/largebody/conservative_dependencies.go:1–478` | Entire file | Serving `WireTurnFacts` lives elsewhere and stays |
| `internal/core/runtime/executor_execute_large_body.go:1195–1339` | `wireAttemptInput`, `wireParallelRaceResult`, `executeWireParallelRace` | Real assessor/executor, `wireAttemptPayload`, acquisition and lifecycle |
| `internal/core/runtime/attempt_session.go:1156–1229` | `wireAttemptStream`, constructor, Recv/Close/Cancel, `WireTakeStream` | Actual attempt/session, ready publication/consumption and terminalization |

Whole conservative-file deletion retires `WireReceiveView`, `WireTerminalFacts`, `WireConversationObserverFacts`, `WireContinuationLineage`, `WireCompactionMeta`, `WireInterleavedStateFacts`, `WireDependencyFacts`, their validation/projection methods, and the conservative gate. These are not the similarly named serving request/receive/terminal owners.

### Behavioral claim migration and pruning
| Existing evidence | Disposition and surviving oracle |
| --- | --- |
| Authority/initial wrapper tests | Keep concrete gate decisions; remove duplicate wrapper assertions. Real `TestProductionLargeBodyAssessor_Audit_DeclinePaths_RealGates` and stock census tests defend composition. |
| `TestRouteOverrideAssessor_WithLateSelectorGate` in `late_selector_gate_test.go` | Port unique combined finite union, mixed-envelope decline, universal union to `BackendWireProofGate.Evaluate`; independent literal expected outcomes, not wrapper-generated expected facts. |
| Override helper simulation | Delete helper plumbing; keep `TestTask13_1_LiveRouteOverride_ConstrainedToDomain` in `runtime/task13_1_execute_large_body_test.go` for finite/universal, alias, outside-domain behavior. |
| `TestBackendWireProofAssessor_StampTamperSwapCloneIsolation` in `stamp_validation_test.go` | Preserve independent request and domain required-capability mutations at `ValidateExecuteLargeBody`, starting with valid matching generations. Assert the relevant disagreement field so a generation error cannot mask the capability oracle. |
| Stamp wrapper disagreement cases | Preserve unique profile/body-mode/rewrite cases directly at `ValidateAssessmentStamp`/`ValidateExecuteLargeBody`; real `TestTask13_1_StampAndSourceValidation` already covers generation/domain/source/nil/declined/zero stamp. |
| Task12.3 DTO validation/projection booleans | Delete non-serving DTO assertions. Move `TestTask12_3_StandardNarrowPortCensus_ContinuationAndTerminalPorts` unchanged to `authority_gate_test.go`. Keep current fresh-session and compaction certification. |
| Task12.3 response-only blocker | Delete obsolete wire-bypass policy assertion. Do not reinstate its historical blanket restriction; current response-authority eligibility and real-pipeline evidence remain. |
| Authority sentinel at `authority_gate_test.go:458` | Delete this vacuous case: sentinels are not injected into the assessor. Keep independent meaningful serving-boundary sentinel tests. |
| H3 parallel case at `large_payload_race_differential_red_test.go:364–452` | Delete unsupported synthetic `MarkedFirst` parallel case; selector grammar rejects parallel first/weight annotations. Do not widen grammar to port it. |
| H3 sequential case at lines 454–505 and store-write failure at 515 | Keep actual `ExecuteLargeBody` two-turn weighted-first and store-failure behavior. |
| Recovery harness private-field equality at lines 556–625 | Delete wiring assertion; keep real pre-output replacement and close-during-failover tests in `large_payload_wire_recovery_blocker3_red_test.go`. |
| `WireTakeStream_ResetsTerminalDefaults` at `large_payload_wire_ownership_red_test.go:269–296` | Delete retired method assertion; preserve same file's real cancellation/EOF/early-close terminal evidence. |
| Race B2 tests at `large_payload_race_differential_red_test.go:140,203,250,310` | Keep actual winner-content, handicap, noncooperative-loser and panic-isolation tests. |

### Historical refutation and active change walk-through
- #628 `9419ca4f` introduced production assessment/composition; alternative task-11 wrappers persisted after the serving owner existed.
- #629 `e305a3ee` collapsed wire races onto canonical attempts; #630 `d1847d5e` repaired cancellation/default ownership; #631 `ae8f980d` retired recovery mirroring. Delete residual test entrances without undoing these fixes.
- #632 `66aa658f`, #633 `83c28f8f`, #634 `0010ee43`, #636 `aa8cd2d7` moved authority/evidence, selector preparation, billing identity and stock reachability to real boundaries. Historical DTO assertions do not justify retaining projections now unused.
- #635 concerns another feature and is excluded. #973 `6e500869` intentionally preserves distinct canonical/wire cancellation classifications; #976 `9bef0924` retains one admitted-exposure abort exit; #1005 `1bae799a` privatized wire steps. Preserve these contracts, not their old scaffolding.
- Upcoming sibling changes touch proof identity, bound views, provider evidence and stream/source outcomes at real owners. After deletion, maintainers update those existing owners/tests rather than carrying the dead alternative accepted-assessment models too. No new abstraction or cross-zone dependency is required.
- Current `docs/large-payload-fast-path.md:226` misleadingly names the isolated harness as delegation surface; update only that explanation. Archived specs and historical evidence remain unchanged.

## Architecture Pattern Evaluation
| Option | Benefit | Cost / disposition |
| --- | --- | --- |
| Leave alternatives intact | No deletion risk | Retains false authority and drifting policy; does not deliver selected outcome |
| Move every alternative to test-only files | Smaller production surface | Preserves duplicate model and obsolete assertions; fallback only for a genuinely unique small helper, not selected framework |
| Delete proven-unused families; use existing gates and real executor | Retires concepts and tests exercise serving behavior | Requires explicit claim triage; selected |
| Replace all assessment with a new unified facade | Could reduce apparent entry points | Serving assessor already unifies gates; unnecessary contract and lifetime risk; reject |

## Design Synthesis
- **Generalization**: The existing production assessor already provides the needed composition. No new facade, registry, plane, store, state machine, or canonical type.
- **Build versus adopt**: Reuse current concrete gates, validators, production executor and test fakes. No new library or external API; no web research needed.
- **Simplification**: Retire seven alternative families, seven projection DTOs and the race/transfer chain; count shared serving models as retained, not duplicated liabilities.
- **Equivalence**: Establish characterization before deleting code. For this no-behavior-change slice, existing passing invariants are baseline proof; unique migrated assertions need deliberate fault probes to show they reject the actual defect. A deletion is not expected to make a behavior test initially red.

### Portable Claim and Fault-Probe Recipes
These are implementation recipes, not executed mutation evidence from this specification session.

| Claim | Source / retained observation surface | Deliberate fault that must be rejected |
| --- | --- | --- |
| Combined domains include every finite member and retain universal semantics | Requirement 2.1; `BackendWireProofGate.Evaluate`, retained `unionWireDomainFacts` | Omit a late-domain member or falsely mark the union universal; assert literal resulting members/support |
| Incompatible mixed domain envelopes decline | Requirement 2.2; backend-proof gate | Suppress mixed-envelope decline; assert decline and no accepted stamp |
| Request capability mutation cannot survive accepted proof | Requirement 2.3; `ValidateExecuteLargeBody` | Bypass request capability digest check; valid control and independent request-only mutation must distinguish accept/reject and exact field |
| Domain capability mutation cannot survive accepted proof | Requirement 2.3; same validator | Bypass domain capability digest check; independent domain-only mutation must distinguish accept/reject, with all other facts matching |
| Stamp disagreement preserves typed invariant rejection | Requirement 2.3; direct validation and real executor | Bypass one retained profile/mode/rewrite check; assert the exact typed field and no backend/session effect where the real executor observes it |

Run deliberate failing probes in a separate scratch-backed process/overlay and retain its nonzero result. `t.Fatalf` is `runtime.Goexit`, not a recoverable panic; do not wrap it with `recover` or put expected-failing subtests inside a normal committed parent. Restore the original source, rerun the desired test successfully, and bind evidence to the chosen implementation revision.

## Risks, Bounds and Evidence Limits
- Approximately 1,288 existing non-test lines can retire across nine source files; 12 directly affected test files, possibly one existing executor migration destination. Estimate 21–23 Go files plus one current documentation file, substantial net shrink; test removals/rewrites roughly 1,500–2,500 lines are estimates, not targets.
- Conservative DTOs exported from `internal` are not public ABI, but internal visibility alone is not consumer proof. Re-run caller classification at implementation time and retain any newly serving candidate pending design revalidation.
- Sibling specs are present but not implemented prerequisites. Prefer correctness-first coordinated delivery; this isolated deletion may proceed without waiting only if all preserved claims execute on its actual base and no correctness repair is silently bundled.
- Current baseline suites pass, not a deletion experiment. No source changed; no deletion-build probe, whole-repository QA, remote race, OS certification, live-provider, latency/allocation or charge-impact measurement was performed.
- Portable implementation evidence is defined in design/tasks. External historical audit probes are supplemental, not required runtime infrastructure or unavailable prerequisites.
