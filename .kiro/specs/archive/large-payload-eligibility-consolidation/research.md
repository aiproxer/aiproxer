# Brownfield Research and Design Decisions

## Summary
- **Feature**: `large-payload-eligibility-consolidation`; existing-system extension, not greenfield.
- **Implementation baseline**: `55e5d4412834a1948c418ac1923cca3c25576019` (`origin/main` at worktree creation).
- **Historical audit**: `8a041a2ad9c9c2c0d0bf8ee57005fe66f7479d6c`; the HTML audit is supporting history, not proof of current behavior.
- **Scope**: Declaration-derived typed-plane inventory and shared hook/narrow-port predicates with the existing serving consumers. No Go implementation performed during spec generation.
- **Key findings**: Manifest order already equals blocker-bit order. Generator extraction already validates classes and closed inventory. Defensive assessment must remain independent; a shared predicate is not a cached authorization result.

## Research Log: Source Ledger
All relative source references below were inspected at the implementation baseline through the initialized CodeGraph index. Line references describe this baseline, not future edits.

| Source | Current behavior / implication |
| --- | --- |
| `pkg/lipsdk/feature/plane_manifest.go:1096-1126` | `StandardPlanes` contains 28 ordered declarations; each declares `RequestAccess`. Its order exactly matches existing eligibility bit positions. |
| `internal/featureplanegen/plane_generator.go:16-42,60-81,239-266` | `planeInfo` already carries ID/access; closed annotation parsing, extraction, formatting and emission are build-time. Reuse this rather than another parser or SDK export. |
| `internal/featureplanegen/plane_extractor.go:11-154` | Missing/duplicate membership, duplicate IDs, undeclared references, missing standard list, and candidate integrity already fail generation. Preserve these checks for every output. |
| `internal/featureplanegen/plane_emitter.go:280-384` | Generated SDK canonical policies/access handles capture declaration metadata at initialization; mutable public descriptors are not request-time authorities. |
| `scripts/generate-feature-planes.go:15-67` | Currently one SDK output; `-check` is read-only, `-manifest` and `-out` support custom destinations, install uses existing per-file atomic writer. |
| `internal/core/largebody/eligibility.go:27-130,333-411` | Handwritten count/order and plane compiler; malformed complete census rejects; summary is sealed/copied and generation-bound. `uint32` plane mask permits 32 positions, currently 28. |
| `internal/core/largebody/authority_gate.go:117-192` | Separate handwritten access table seeds `NewStandardDependencyCensus`; this is the removable classification copy. Narrow/custom-port catalog is a different input and stays. |
| `internal/core/largebody/authority_gate.go:247-357` | Summary seal/pin/masks precede independent plane, hook, narrow-port and custom-port checks. Preserve reasons and protected-authority checks. |
| `internal/infra/runtimebundle/build_large_body_assessor.go:79-134` | Serving composition seeds the standard census, fills occupancy from frozen contributions and hook lengths, copies typed core/routing/billing/accounting ports, compiles summary and constructs the serving gate. A changed standard census is immediately consumed without replacing the builder. |
| `internal/core/runtime/core_ports.go` | Existing census augmentation gives unknown nonzero untagged core fields an unknown custom-port entry; future dependencies continue to fail closed. |
| `internal/core/largebody/disposition.go` | Static pre-capture decision is constant-time and allocation-free; it returns canonical or needs-assessment, never wire authorization. |
| `internal/archtest/plane_report.go:84-198` | Existing generated-path inventory and manifest currency check currently cover SDK output only. Extend them to the second artifact instead of inventing a feature-specific architecture gate. |
| `scripts/quality-checks.sh:115,194` | Existing change selector invokes generator `-check`; add the new core generated output to the selector. |

## Brownfield Authority and Effect Map

| State / resource | Constructor and owner | Mutators / observers | Cleanup, quiesce, identity |
| --- | --- | --- | --- |
| Declaration and ordered inventory | Hand-authored SDK manifest; existing build-time generator | Source author; extractor/emitter | Git source; no runtime lifecycle or quiesce |
| Generated core ID/access metadata (proposed) | Existing generator emits core-local literals | Regeneration only; census constructor/compiler | Static binary data; no callbacks, resources or mutable exported registry |
| Census occupancy | Standard composition seeds `NewStandardDependencyCensus`, adds frozen feature/hook/core-port facts | Candidate composition; authority gate reads census | Existing candidate/generation owner; not a new lifecycle mechanism |
| Sealed summary | `CompileWireEligibilitySummary` copies facts | Private fields; static disposition and gate observe | Existing generation pin; rejected candidate does not publish into admitted generation |
| Runtime wire authorization | Serving production assessor and existing execute validation | Request proof, route/backend gates | Existing request/attempt/terminal owners; untouched |

- Generation-owned effect: constructing/capturing immutable eligibility facts. Preserve publication isolation and candidate failure rollback; do not invent a cleanup owner for value data.
- Lexical reversible build effect: temporary file used by `WriteGeneratedFileAtomic`; existing writer owns close/remove/rename.
- External file replacement: each artifact install is atomic independently, not a multi-file transaction. Validate/render all selected outputs before any write; failed installation fails the command and `-check` detects inconsistent files. No cross-file rollback machinery is needed.
- Irreversible upstream/downstream emissions: none added. Existing backend opening, output commitment, billing usage, recovery and terminalization remain with runtime owners.
- Dependency graph: manifest -> generator -> generated SDK/core literals -> standard census -> occupancy -> sealed summary -> static disposition + independent defensive assessment -> existing request proof. No new runtime withdrawal edge.

## Current Policy Matrix

| Input | Current outcome to preserve |
| --- | --- |
| Occupied canonical-required plane (20 declarations) | Fixed plane blocker; authority rejection |
| Occupied response-only plane (response parts, completion gates, stream-observer factories, usage observers) | Fixed plane blocker; conservative rejection, not wire-pipeline reuse |
| Metadata-only plane (session openers, workspace resolvers, finalizer max-args, session classifier) | No plane blocker; remaining authority/proof still required |
| Wire-contract class | Recognized but no production declaration uses it; no new wire contract introduced |
| Five protected typed IDs | `local_turn_handlers`, `secret_guards`, `secret_guard_execution`, `terminal_decision_provider`, `control_tool_provider`: occupied always blocks; defensive classification weakening also rejects while unoccupied |
| Four occupied hook chains | Submit/request-part/response-part/tool all block; stale `HookBlockers` comment incorrectly excludes response-only chain |
| Caps resolver / conversation reader / preflight / compaction detector / required token counting | Only nonblocking with existing wire-proof subsumption / fresh-A-leg support / exact counter / wire support / exact counter, respectively |
| Other narrow-port blocker or missing two-phase capability | Existing port bit and authority rejection; 19 existing port bits, no width change |
| Custom port | Unknown class declines even if unoccupied; known blocker declines when occupied; occupied names containing `local_turn`, `secret_guard`, `terminal_decision`, `terminal.decision` decline even if marked wire-safe |
| Summary unsealed / generation mismatch | Authority blocker / generation mismatch, respectively; seal check precedes pin check |
| Static disposition reasons | Feature-disabled > invalid pin or static blocker > legacy resolver > gzip > below threshold > needs-assessment |

The custom-port name defense does **not** add a control-tool substring rule in this slice. Typed control-tool protection and existing unknown/blocker rules remain intact. Defensive plane checking is not identical to compile-time complete-census validation; do not silently add new defensive completeness or out-of-range semantics during predicate consolidation.

## Requirement-to-Asset Gap Analysis

| Requirements | Existing assets | Missing work |
| --- | --- | --- |
| 1.1, 1.5 | Ordered manifest extraction, matching legacy bit order | Emit core ID/access artifact; remove count/order/access handwritten copies |
| 1.2, 1.6 | Existing closed validation and formatting | Capacity bound for core output; validate every selected artifact before writes |
| 1.3, 1.4 | Complete-census compiler, fixed indices, bounded error IDs | Keep validation/order independence when count comes from emitted data |
| 2.1-2.5 | Existing conservative plane/hook rules and static disposition | Preserve behavior and correct misleading response-hook documentation |
| 3.1, 3.2 | Pure compile helpers plus duplicated gate conditions | Gate calls pure helpers on its own census; no acceptance from summary alone |
| 3.3, 3.4 | Custom-port gate, protected names, seal/pin checks | Preserve separately; not derivable from typed-plane manifest |
| 4.1-4.5 | Static ratchets, production builder, immutable summary and SDK policies | Extend currency checks; composed occupancy/immutability sentinel; retain contracts |

## Architecture Pattern Evaluation

| Option | Benefits | Costs / risks | Assessment |
| --- | --- | --- | --- |
| Extend existing generator with core-local ID/access artifact | No new SDK API/import; matching order; removes three handwritten copies | Second output needs currency/check/install handling | Preferred bounded brownfield option |
| Export a frozen SDK eligibility projection | Single generated runtime descriptor source and typed occupancy possible | New public API, SDK-to-core coupling or bridge; wider contract review | Viable but disproportionate to this slice |
| Parse mutable `StandardPlanes` at composition | Few generation changes | Descriptor/list mutation can reclassify admission; runtime parsing/registry temptation | Reject unless captured behind an existing frozen contract, which adds more machinery than option one |
| Retain handwritten tables with extra parity tests | Minimal source changes | Still multiple maintenance authorities; tests detect drift but do not remove it | Does not deliver 1.1 |

## Design Decisions and Synthesis
- **Generalization**: Inventory/access come from one validated ordered manifest. Hook chains and core narrow ports are independently owned facts; do not force them into a typed-plane manifest or unify their lifetimes.
- **Build vs adopt**: Reuse AST extraction, Go formatting, atomic writer, quality selector, existing currency/report checks and fixed masks. No new library, external research/API, runtime registry, generic schema or generator platform.
- **Simplification**: Emit existing `WireEligibilityPlaneCount`, `wireEligibilityPlaneOrder` and `standardPlaneV1Access` into one generated core file. They can remain derived copies in binaries; the problem is multiple handwritten authorities, not physical representation count.
- **Policy reuse**: Retain `compileHookChains` and `compilePortBlockers`; call them from the defensive gate on current census facts. Preserve early summary seal/pin checks and separate typed/custom-port defenses.
- **Capacity**: Current 28 planes fit 32 bits. A 33rd plane fails generation; a compile-time bound protects against manually stale output. Mask expansion is deferred.
- **Order**: Preserve all 28 current bit positions; subsequent plane introduction must preserve their order (append) or obtain a separate compatibility decision. Synthetic insertion tests describe emitter order, not permission to reorder production IDs.
- **CLI compatibility**: Default invocation regenerates/checks both artifacts. Existing explicit `-out` remains SDK-only unless a new explicit core-output flag is provided; fixture invocations must not touch repository defaults accidentally.

## Sibling Alignment and Integration Risks
Sibling sources were clean and committed in their own worktrees when inspected; each is approved for automatic spec generation, unimplemented, and `ready_for_implementation:false`.

| Spec | Sibling tip | Ownership / integration obligation |
| --- | --- | --- |
| `large-payload-lifecycle-correctness` | `a848849f` | Replay handoff, canceled-result disposal, unlink notices and terminal audit; preserve owners, no prerequisite here |
| `large-payload-proof-and-parity` | `be4bc6ce` | Bounded proof, composition nonce, views, affinity, economic evidence and clocks; leave `resolveLargeBodyAssessorGenID` untouched |
| `large-payload-assessment-simplification` | `14807850` | Removes unused DTO/adapters in `authority_gate.go` and elsewhere, keeps production assessor, concrete gate, census and serving policy; shared-file collision, not prerequisite |

The relative sibling spec directories already exist at this branch baseline; alignment additionally consulted `../spec-<sibling-name>/.kiro/specs/<sibling-name>/` in sibling worktrees. No sibling edits or commits were imported. Archived `large-payload-streaming-fast-path` is historical, not a writable implementation plan.

Billing-financial-safety and high-concurrency performance-hardening remain adjacent owners: paid exposure admission still blocks wire, and bounded replay/admission limits remain. They do not define this catalog's classification implementation or impose an unconditional dependency.

## Verification Evidence and Outstanding Work
Fresh commands in this worktree during generation, all exit 0. Each used an explicit ext4 scratch `TMPDIR`; machine-specific directory values are omitted here:
- `go test ./internal/featureplanegen ./pkg/lipsdk/feature ./internal/core/largebody -count=1`: complete three package suites PASS.
- `go run ./scripts/generate-feature-planes.go -check`: existing SDK artifact current.
- `go test ./internal/core/runtime ./internal/infra/runtimebundle ./internal/plugins/frontends/frontendpipe -run 'LargeBody|LargePayload|WireEligibility|StaticDisposition|StockHost|Blocker2' -count=1`: focused three package selections PASS, not complete package certification.
- `go test ./internal/archtest -run '^(TestExtensionPlanesManifestStatus|TestMeasureManifestStatus_)' -count=1`: existing manifest currency selections PASS.

These validate the brownfield baseline only. Proposed second-output emission, shared-policy implementation, new tests and integration gates remain unexecuted implementation tasks. No source-change test mutation, full QA, local/remote race, live-provider measurement, or performance-improvement claim is made.

## Remaining Research / Revalidation Triggers
- No unresolved external dependency or scope question blocks specification generation.
- At implementation start reidentify source HEAD, sibling implementation state and current plane order/mask widths. Reconcile shared `authority_gate.go` symbols with assessment retirement; reuse the concrete serving gate.
- Revalidate SDK/generator currency and existing generic architecture contracts if plane declarations or artifact paths change; do not create a new bespoke architecture scanner.
- Revalidate serving composition and relevant runtime/frontend sentinels after changes to catalog/occupancy/authority facts. Keep generation-token repair owned by proof/parity.
- Reconcile the existing architecture report's generated-path inventory and baseline using its explicit update tooling, never rewrite from read-only check mode.
