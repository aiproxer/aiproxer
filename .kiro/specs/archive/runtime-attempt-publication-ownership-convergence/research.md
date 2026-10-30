# Research and Brownfield Gap Analysis

## Executive Conclusion

The remaining runtime complexity is no longer primarily a decomposition problem. The merged request-attempt and receive-terminal refactors established useful phase products and five post-open owners, but the transition between them is still not lifecycle-complete. The current code can transfer an attempt into a long-lived owner before all fallible readiness work succeeds, then perform cleanup through narrower raw-resource paths. Replacement and parallel paths repeat the same weakness in different forms.

The highest-ROI architectural correction is therefore **publication ownership convergence**: make every attempt move through a single acquisition owner, a single unpublished-ready state, one linearizable publication boundary, and one terminalization protocol. Context projection and parallel mutation should be corrected in the same tranche because both violate that ownership model and otherwise keep alternative state authorities alive.

Brownfield baseline: `main` at `8b6f5d4f5b8c8628c0a513cdf1a1408998dea68a`.

## Current Assets That Should Be Preserved

The previous refactors created several strong building blocks that should be completed rather than replaced:

- `retryRecvStream` is already a small EventStream facade with five direct owners: immutable `recvTurnFacts`, `attemptSlot`, `recoveryController`, `responsePipeline`, and `turnTerminal`.
- `attemptSession` already groups one B-leg, candidate, authority lifecycle, attempt terminal, accounting, tool-finalization, prompt-cache, and final-stream observation state.
- `attemptTx` already owns much of pre-handoff B-leg/authority/backend-open rollback and has an explicit `Handoff` transition.
- `recoveryController` already centralizes route-progress state used across initial and replacement paths.
- `recvTurnFacts` already captures request-bound model views and exposes a context projector.
- architecture ratchets already exist for both the turn/recv ownership topology and the request-attempt state simplification.

This means the target is an incremental ownership convergence, not a new framework.

## Gap 1 — Handoff Occurs Before Attempt Readiness Is Complete

### Current evidence

`attemptTx.Handoff` marks the transaction completed and constructs `attemptSession` immediately from the opened backend stream, B-leg and authority. This is an ownership transfer: rollback no longer owns those resources.

Initial stream assembly then installs the session into `attemptSlot`, consumes sideband evidence, derives views, and only afterward invokes `openFinalStreamObservation`. If final observer startup fails, assembly closes only `out.session.inner` and returns an error.

That path is structurally unsafe because the transferred attempt may also own:

- B-leg lifecycle registration;
- active authority/reservation state;
- metering/accounting state;
- billing leg responsibility;
- tool/prompt-cache state;
- attempt terminal state;
- final-observer session state.

Closing the raw stream cannot prove those obligations are settled.

The replacement path has the same ordering problem in a more dangerous form: it can register and publish the replacement through the slot and then call `openFinalStreamObservation`; a pre-output observer startup error may return after the replacement became current.

### Requirement impact

This gap drives 2.1–2.6, 3.1–3.8, 4.1–4.6, 7.2–7.4 and 9.1–9.3.

### Needed change

Introduce an explicit **unpublished ready-attempt capability**. All fallible attempt-local initialization that is required before receive must complete while the attempt is still unpublished and still has one cleanup owner. The slot must accept only that capability, not an arbitrary `*attemptSession`.

## Gap 2 — `attemptSlot` Protects Pointer Swaps, Not Publication Ownership

`attemptSlot` currently serializes `current` and `publicationClosed`, with `install`, `swapIfOpen`, and `closePublicationAndSnapshot`. This is a useful synchronization primitive but it does not encode whether an attempt is ready, whether winner-only effects are committed, or whether request ownership has transferred.

A replacement can therefore be structurally visible before all of its readiness/commit obligations are complete. `Close` races are pointer-linearized, but lifecycle publication is not yet a single transaction.

### Needed change

Strengthen the slot boundary so a single publication lease/commit linearizes:

1. readiness acceptance;
2. current-attempt replacement;
3. closure rejection;
4. winner-only effect ownership;
5. exactly-once handoff state.

No backend/observer/store work may run while the slot lock is held.

## Gap 3 — Attempt Terminalization Still Has Multiple Production Paths

Current attempt cleanup can occur through several mechanisms:

- `attemptTx.Rollback` using `terminalizeAttemptEphemeral`;
- `attemptSession.cancelAndClose` / `takeInner`;
- turn-terminal methods that finalize attempt authority and billing;
- replacement-specific cleanup;
- parallel-loser cleanup;
- direct stream close in assembly and parallel bridging.

The previous refactor centralized important terminal mechanics, but the production surface still allows callers to bypass the complete attempt lifecycle by operating directly on `inner` or authority-adjacent fields.

### Needed change

One attempt-owned terminal operation should accept a typed terminal intent/evidence and own all attempt-level effects. Request terminalization remains separate. Prepublication rollback, replacement, parallel loser, cancellation, timeout and normal completion should all converge on that operation rather than duplicate subsets of cleanup.

## Gap 4 — Frozen Facts Are Not Yet the Sole Business Authority

`recvTurnFacts` is designed as immutable request-lifetime authority and its `projectContext` method projects values into context. However `viewsFor(ctx)` currently prefers `execctx.FromContext(ctx)` before falling back to frozen `recvViews`.

That means a stale or conflicting caller `Recv` context can still override frozen principal/scope/session/workspace facts for downstream metadata/policy paths. Similar brownfield fallback patterns exist where code reads metering/scope from context when typed facts are absent.

### Needed change

After the freeze boundary:

- typed facts are authoritative;
- context is used for cancellation/deadlines/tracing/diagnostics and compatibility projection;
- projector code overwrites authoritative keys, including authoritative absence;
- business code does not resolve the same fact from caller context first.

This is a correctness and security property, not only a cleanup optimization.

## Gap 5 — Parallel Workers Still Participate in Shared Recovery Mutation

Parallel candidate code already gives each opened leg its own `attemptTx`, which is a strong foundation. But worker goroutines still update shared recovery structures such as failure history and exclusions, coordinated with a local mutex. They also publish raw streams and per-leg state into a shared `parallelLeg` structure before winner reduction.

This makes concurrency correctness depend on worker scheduling and a broad shared coordination block. It also allows winner selection, route-progress mutation, and resource cleanup to remain interleaved.

### Needed change

Workers should return immutable **arm outcomes**:

- ready unpublished attempt capability, or failure;
- failure/rejection delta;
- observed usage/evidence;
- pending interleaved/winner-only effects.

One reducer/coordinator should mutate recovery progress, select the winner, publish exactly one ready attempt, and terminalize all losers. Stable reduction order must preserve the current final-error precedence while first-success behavior may still follow controlled arrival order.

## Gap 6 — Raw Attempt Resource Access Remains Too Available

`attemptSession` exposes package-private fields and helpers such as `loadInner`, `storeInner`, and `takeInner`. Because many runtime files share the package, package privacy does not prevent lifecycle bypass. Examples include assembly reading `out.session.inner`, replacement code consuming the raw stream, and parallel code replacing `winnerSession.inner` with a bridge stream.

### Needed change

The architecture needs enforceable lifecycle-complete methods plus AST ratchets. Production code outside the attempt owner should not directly read/write `inner`, authority internals, terminal internals, or other lifecycle-sensitive fields except through narrow allowlisted construction/testing seams.

This is more reliable than relying on naming conventions or reviewer discipline.

## Requirement-to-Asset Map

| Requirement group | Existing asset | Gap | Disposition |
|---|---|---|---|
| 1 preserve semantics | characterization and architecture tests, existing runtime owners | broad behavior must remain pinned during ownership moves | extend tests first |
| 2 acquisition ownership | `attemptTx` | handoff happens before all readiness work | extend into complete prepublication owner |
| 3 publication | `attemptSlot` | pointer swap is not full lifecycle commit | strengthen slot/publication capability |
| 4 terminalization | `streamTerminal`, `turnTerminal`, `attemptTx.Rollback` | several partial cleanup entry points | converge attempt terminal operation |
| 5 frozen facts | `recvTurnFacts.projectContext` | context-first resolution remains | make typed facts authoritative |
| 6 parallel reduction | `tryOpenParallelGroup`, per-arm `attemptTx` | workers mutate shared recovery state | typed arm outcomes + serial reducer |
| 7 lifecycle boundaries | five-owner facade, archtests | raw attempt internals still reachable | encapsulation + AST ratchets |
| 8 domain/concurrency | existing core/domain split | lock/effect boundaries need explicit protection | preserve domain owners; no lock across effects |
| 9 certification | fault/race/arch tests | current gates do not cover readiness publication seam | add adversarial lifecycle matrix |

## Implementation Options

### Option A — Patch Known Failure Sites

Add full cleanup to initial and replacement observer-start failure branches and keep current publication order.

**Advantages**
- smallest immediate diff;
- quickly fixes known leak paths.

**Disadvantages**
- every new fallible post-open step can recreate the same defect;
- cleanup remains caller-specific;
- does not solve context authority or parallel mutation;
- leaves raw publication semantics unchanged.

**Assessment:** rejected as insufficient. It treats symptoms rather than ownership topology.

### Option B — Add a Generic Resource Transaction/Workflow Framework

Represent every acquired runtime resource in a generic registry and drive attempt stages through a generic state machine.

**Advantages**
- could centralize cleanup mechanically.

**Disadvantages**
- conflicts with established project preference for explicit concrete orchestration;
- risks hiding ordering that is security/accounting critical;
- adds abstraction surface larger than the problem;
- duplicates domain-owner semantics.

**Assessment:** rejected. Disproportionate complexity and wrong dependency direction.

### Option C — Complete Existing Concrete Owners

Extend `attemptTx`/`attemptSession`/`attemptSlot` with one explicit unpublished-ready publication boundary, converge terminalization, make context projection one-way, and make parallel workers return typed outcomes.

**Advantages**
- leverages merged architecture instead of replacing it;
- removes defect classes rather than branches;
- keeps control flow visible;
- produces enforceable ownership rules and smaller change-propagation surface.

**Disadvantages**
- requires careful migration across initial, replacement and parallel paths;
- concurrency tests must be stronger than ordinary unit coverage.

**Assessment:** preferred hybrid/refactor approach.

## Brownfield Requirements Repair

Gap analysis exposed one specification-quality defect: the original acceptance criteria were locally numbered `1`, `2`, etc. under each requirement rather than carrying canonical globally traceable IDs. `requirements.md` was repaired to explicit `N.M` IDs before design/task generation.

No product-scope expansion was required. The existing nine requirement groups already covered the discovered code gaps. The design must preserve the distinction between attempt terminal lifetime and request terminal lifetime and must not introduce a generic framework.

## Risk Assessment

- **Effort:** XL. The code is already partially converged, but publication, replacement, parallel and terminal paths are concurrency-sensitive and touch critical billing/authority/B2BUA invariants.
- **Risk:** High during migration, Low-to-Medium after completion. The main risk is transient double ownership or missing cleanup while authority moves between existing components.
- **Primary mitigations:** characterize before movement; introduce one boundary at a time; fault-inject every acquisition/readiness step; retain current public/domain contracts; run race/checkptr/leak and platform suites; make structural ratchets fail on lifecycle bypass.

## Research Needed During Implementation

No external dependency or protocol research is required for this tranche. Remaining investigation is repository-local:

1. enumerate all fallible attempt-local operations after backend open and classify whether they must move before publication;
2. inventory every production direct access to `attemptSession` lifecycle-sensitive fields;
3. inventory terminalization entry points and their side effects before convergence;
4. confirm whether any winner-only durable interleaved state requires a narrow atomic store command across existing memory/SQLite/PostgreSQL implementations.

These are implementation-discovery tasks, not unresolved product decisions.
