# Brownfield Implementation Gap Analysis

This is the historical discovery-baseline analysis. The lifecycle prerequisite has since been completed, archived, and integrated in the implementation base; current implementation review is separate from this analysis.

## Baseline
Source: `c6598d5d770f8a9b1b981abdf03fe6ca5fbd2b77`. Original audit: `8a041a2ad9c9c2c0d0bf8ee57005fe66f7479d6c`. Requirements: five areas, 21 criteria; automatically approved for document generation, not implementation.

## Requirements-to-Capability Map
| Criteria | Existing authority / capability | Missing integration |
| --- | --- | --- |
| 1.1, 1.2 | `frontendpipe.BudgetBoundedWriter`, one profile counter, semantic-budget sentinel | Nested role/type buffers return raw buffers; OpenResponses also exposes status/store. Raw envelope capture checks occur after writes. Route all metadata retention through the existing before-write counter. |
| 1.3, 1.4 | `replayCandidate` same decode permit; streaming identity pass; final `ProofOutput.Validate` | Preserve fallback and identity; existing normalized aggregate checks cannot bound transient raw metadata. Prompt streaming must not be charged as retained metadata. |
| 2.1–2.4 | Immutable host generations; stamp/live-facts validation before `PrepareSecureSession` | `resolveLargeBodyAssessorGenID` reuses `snapshotgen.Publisher.Current().ID`, which identifies economic publication, not a complete assessor composition. Distinguish builds without retargeting old requests. |
| 3.1–3.4 | `PreparedSecureSession.BindSession`, `execctx.Views`, frozen `recvTurnFacts`, shared affinity resolver | `boundSession` exists, but wire views only populate Principal and use principal presence as their validity flag. Populate complete trusted views before freezing. |
| 4.1, 4.5 | `stampBillingStoreID`, frozen store value plus stamped marker, existing eligibility blockers | Canonical request assembly stamps StoreID; wire assembly does not. Freeze trusted result, including empty, without body-supplied defaults or removing blockers. |
| 4.2, 4.4 | Canonical provider binder, shared `attemptTx`/`attemptSession`, per-B-leg terminal evidence | Wire provider-open skips the binding step. Reuse its trusted identity projection for successful streams only. |
| 4.3 | Optional provider observation/checkpoint capabilities and existing consumers | `wireBodyClosingStream` and `wireBoundaryEventStream` embed only `ManagedEventStream`, hiding optional methods; preserve access without another drain or terminal owner. |
| 5.1–5.4 | Handler `BuildEncodeOpts`, existing shared response encoder cores, immutable identity seeds | Canonical OpenAI handlers honor `Exec.WallClock`; wire encoders choose deterministic time directly. Select response time at existing handler-to-encoder seam without changing proof seeds. |

## Source and Fresh Probe Evidence
Fresh Go overlay probes ran against this worktree without replacing production source. Overlay artifact: `proof-parity-current-overlay.json` in external audit scratch.
- Metadata: all three profiles accepted a 1 MiB whitespace-prefixed role under 4 KiB. Final aggregate fact bytes were only 130/127/141. Desired-contract assertions failed.
- Composition: two identity requests against the same populated publisher yielded `gen-1`/`gen-1`. Desired distinctness assertion failed. A full cross-composition execute rejection remains implementation TDD work.
- Clock: canonical fixed time `1700000000`; wire Responses `1715631327`, Chat `1715653203`. Desired parity assertions failed. This historical probe uses a permissive assessor/non-stream writer; production streaming parity still needs direct handler evidence.
- Economics: actual Responses provider parser yielded canonical store resolution/binding/native observation counts `1/1/1`; wire `0/0/0`. Characterization probe passed by asserting the defect, not the desired contract. Exposure admission was unoccupied; no paid charge-impact claim follows.
- Affinity: production assessor accepted a fresh-session proof. Ignore policy omitted binding; fail-closed returned `affinity: identity required` despite a created authoritative session. Characterization probe passed by asserting the defect.

Reproduction commands and portable fixture descriptions are in `research.md`; scratch paths are supporting evidence, never implementation prerequisites.

## Options and Trade-offs
1. **Extend existing seams.** Budget existing buffers before writes; isolate existing stamp identity; complete admitted views; share provider binding and preserve optional methods; use existing clock/encoder options. Smallest footprint, but shared runtime files require rebaseline coordination.
2. **Conservatively decline affected wire use.** Disable session-affine/economic/oversized-metadata cases before commitment. Viable containment, but does not deliver the explicitly requested accepted-wire parity or fix clocks/composition identities.
3. **Narrow private fact projection helpers.** Can reduce divergence in view assembly and successful-open binding when existing callers need the same contract. Use only demonstrated common data; do not make a universal mutable request bag.
4. **New registry/admission engine or full canonical decoding.** Adds ownership, dynamic lookup, or payload-sized allocations without a missing authority; reject as disproportionate.

## Adjacent Specifications and Delivery Dependencies
- Lifecycle correctness is present as a spec, not a completed code dependency. Tasks may implement local fixes independently; any full HTTP receive-replacement proof is gated on its source-lifetime repair or must observe the shared acquisition seam directly and report the HTTP sentinel pending.
- Financial-safety requirements 4, 8, 12, and 22 own funding, finality, capability, and transport-independent liability. This spec repairs evidence plumbing only; paid exposure remains a wire blocker. Revalidate if financial work moves these seams.
- High-concurrency requirements 4, 7, 12, and 17 own materialization/retention/allocation hardening. Preserve fixed-buffer proof and existing allocation ratchets; do not add a new load framework or promise 10k streams.
- Assessment-simplification and eligibility-consolidation remain separate deletion/classification workstreams. No adjacent or archived artifact is edited.

## Risk and Research Disposition
Local missing handoffs are proven; external-provider behavior, money loss, broad reload exploitation, and live latency benefit are not. Construction identity and optional stream capability details require source-level design reconciliation, not a new external dependency. No new library, remote service, schema, or public ABI is needed.

## Delivery Estimate
One intended corrective PR: roughly 12–18 production Go files, under 500 added non-test Go lines, fewer than ~40 changed files including reused tests. This is a planning estimate, not measured change evidence; keep test additions near ~2x production additions and split rather than weakening proof if actual growth exceeds budgets.

Before coding and after accepted tasks: `make dev-delivery-plan DEV_BASE=<implementation-base> DEV_CONSUMER='existing certified streaming frontends and executor'`. Full source changes, their tests, and immediate consumers belong in each task; no standalone certification task.
