# Brownfield Gap Analysis

## Existing Capabilities
The serving composition already constructs `ProductionLargeBodyAssessor` from concrete authority, route and backend-proof gates. Real `Executor.ExecuteLargeBody` validates accepted proof and uses shared route-plan, attempt, receive, recovery and terminal machinery. Canonical calls and bounded replay represent different payload costs, not competing lifecycle authorities.

## Requirement-to-Gap Map
| Requirements | Existing behavior | Gap / required change |
| --- | --- | --- |
| 1.1, 1.2, 1.3 | Serving assessor and internal optional capability interfaces exist | Remove seven unused families, dependent DTOs, simulated override methods and isolated race-transfer chain; keep interfaces and concrete gates |
| 2.1, 2.2 | Proof-bound replay, conservative census and frontend same-admission fallback | No serving policy change; preserve gate and stock assessment evidence while pruning alternative-model tests |
| 2.3 | Direct validators and real executor reject stamp/domain/source drift | Retain validators and migrate unique tamper claims without error masking |
| 3.1, 3.2, 3.3 | Shared attempt/recovery/terminal owners and real wire race tests | Delete specialized test entrance; retain actual weighted-first/race/recovery/ownership tests and refresh misleading current documentation |
| 3.4 | Public canonical/SDK/runtime/backend-plugin contracts unchanged | No public edits or new dependencies; verify no alias/ABI/connector consumer enters deletion set |

## Approaches and Recommendation
1. Retain wrappers: low immediate risk, no simplification delivered.
2. Move all machinery into tests: removes production placement but preserves the alternative model; insufficient default.
3. Delete verified unused families, preserve unique evidence through existing production surfaces: recommended, no new mechanism.
4. Build another facade: rejected; existing serving composition already supplies the seam.

Detailed source anchors, consumer counts and keep/delete/port evidence are in [research](research.md). A lack of serving constructors matters more than an interface implementation edge. Tests are classified by current behavior, not historical task numbering.

## Adjacent Boundaries and Dependencies
- Lifecycle correctness owns replay handoff, cancellation result disposal, logical audit and unlink failure. Proof/parity owns metadata budgets, composition identity, admitted session/economic facts and response clocks. No fixes from either sibling enter this deletion slice.
- Financial safety owns funding/finality/durable economic obligations. Keep current paid-exposure blockers, native evidence access and admitted-exposure abort behavior.
- High-concurrency owns capacity and measured allocation/retention changes. This spec claims reduced maintained concepts, not measured speed or heap improvement.
- Eligibility consolidation will derive canonical manifest classification; this spec keeps the serving census/catalog and conservative support unchanged.
- Prefer lifecycle/proof repairs first when delivering the audit as a coordinated program. No unconditional dependency on their complete implementation is introduced: deletion of unused internals can merge independently if actual base behavior/coverage remains intact. If a preserved regression needs a sibling repair to execute, integrate that repair first and rerun rather than skip or fabricate acceptance.

## Proposed Delivery Shape
Four sequential, 1–3 hour deletion work units: authority/initial wrappers; route/late/backend alternatives; stamp/DTO alternatives; runtime race-transfer harness plus current documentation. Each unit includes its own claim migration/pruning and focused tests. These are one independently buildable PR, not a new test project.

Estimate: nine production Go files, 12–14 test files, one current documentation file, roughly 1,288 removed production lines and substantial net test shrink. Use actual delivery reports, not estimated deletion counts, at implementation checkpoints. A newly discovered serving consumer changes the deletion boundary and requires review rather than automatic removal.

## Research Needed and Verification
No new external dependency, configuration, migration or runtime prerequisite is missing. Implementation must refresh caller/embedding/alias/non-Go searches and reconcile sibling changes against the chosen commit. Unique capability-tamper and combined-domain assertions need focused non-vacuity probes; remaining historical projection tests should be removed rather than ported.

Fresh baseline verification: complete `largebody`, `runtime`, `runtimebundle`, `archtest` and `qa/kirospec` suites pass. Those results establish source health only. Final deletion acceptance still requires affected package/stock-host evidence, applicable contract/lint/hook/CI checks and a source-bound before/after inventory. Remote race belongs to GitHub CI if serving concurrent behavior is touched; no local race run is authorized.
