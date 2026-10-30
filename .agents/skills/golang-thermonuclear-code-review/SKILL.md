---
name: golang-thermonuclear-code-review
description: "Strict Go review that hunts radical simplification: the same behavior with substantially less code and fewer concepts to maintain, including replacing wrong abstractions with better ones. Use for thermonuclear review, a merge-gate review of a branch or PR, or a deep maintainability audit of a package or subsystem."
---

# Go thermonuclear code review

## Mission

Treat code as **liability**: every type, interface, mode, flag, state, layer, wrapper, and package is something a future maintainer must hold in their head, keep correct, and migrate. Working code is not thereby justified. The goal is the same (or deliberately near-same) behavior with **substantially less code to maintain**.

Measure payoff in **concepts retired**, not lines shaved. A finding earns its place when it collapses accidental complexity: parallel implementations folded into one, a mode or flag family replaced by a model where the branches disappear, a layer that only forwards deleted, a wrong abstraction replaced by the right one. Statement-level tidying (inlining a variable, merging two `if`s, renaming) is out of scope here; leave it to `golang-simplify`.

Simplification may **add** code. A new abstraction is a simplification when it retires more, and worse, concepts than it introduces: one state type replacing a flag combination, one table-driven dispatch replacing N parallel switch chains, one canonical model replacing per-variant special cases. Judge by net concepts and net maintained code across the subsystem, never by the diff of one function.

Hunt the **wrong abstraction**: a generalization whose consumers each use a different subset, pass flags to bypass parts of it, or special-case around it. The remedy is often to inline it back into its consumers and re-extract along the real variation axis.

Preserve behavior and contracts, require evidence, and be direct: a demonstrated maintainability failure is a finding, not optional polish.

## 1. Scope

Pick the branch and record it:

- **Diff review** (branch/PR): base is `git merge-base origin/main HEAD`. Review the diff plus everything it touches. Also ask whether the diff needed to exist: could an existing concept have absorbed the change instead of a new one?
- **Area audit** (package/subsystem): the named area plus its consumers and dependencies. The main output is a ranked simplification plan.

Record branch, base, dirty files, and excluded generated code. Stay read-only unless fixes are requested.

Done when: scope, base, and exclusions are written down.

## 2. Map with ledgers

Build tables; each row cites `file:line`. A cell you cannot fill is either a finding or an explicit `unverified` entry. Use CodeGraph (load the `codegraph` skill): `explore` for the subsystem map, `callers`/`impact --depth N` for consumers and blast radius, `callees` for forwarding layers, `query NAME --kind interface|struct|function` to enumerate, `affected FILE...` for tests. Count production and test consumers separately; a concept with only test consumers is a candidate.

**Concept inventory** (always):

| Concept | Kind | Defined at | Prod / test consumers | Variation it serves (demonstrated?) | Essential or accidental | Collapses into |
|---|---|---|---|---|---|---|

**Variation matrix** (when several variants — providers, frontends, modes, configs — share behavior): rows are variants, columns are behavior points, cells cite where each is implemented. Duplicated cells and scattered per-variant branches expose the real variation axis and the abstraction that should own it. The co-change command in `golang-solid-principle-review` shows which axes actually move together in history.

**Transition matrix** (when the scope holds a state machine, explicit or implied by flags): states × events; cells are the target state and the handling site. Empty cells, impossible combinations the types still allow, and flags that encode a state are findings or simplification seeds.

**Ownership ledger** (when the scope touches goroutines, channels, locks, timers, streams, bodies, transactions, or cancel functions):

| Resource | Acquired at | Released on success | on error | on cancel | on shutdown | Owner |
|---|---|---|---|---|---|---|

Check the inventory against the Architecture Guardrails in `AGENTS.md` and the gates in `internal/archtest`.

Done when: every changed exported symbol (diff review) or every exported concept in the area (audit) has an inventory row, and each conditional ledger that applies is complete or carries its `unverified` cells.

## 3. Attack

Read the ledgers for collapse opportunities, in order of leverage:

1. **Accidental concepts**: rows whose variation is undemonstrated, whose only consumers are tests or one caller, or which only forward. Delete them.
2. **Duplicated variation**: matrix columns implemented N times with small differences. Find the one abstraction that owns the axis, even when it is new code.
3. **Encoded state**: flag combinations, partial-state protocols, and branches that a typed state model or explicit dispatch would make disappear. Turn exceptions into the default flow.
4. **Wrong abstractions**: generalizations bent by flags and special cases. Inline, then re-extract along the real axis.
5. **Misplaced policy**: feature checks scattered through shared paths; transport, storage, or provider details leaking into core policy.

Then check what the change must not break, loading the focused skill where depth is needed:

- **Contracts**: exported API, method sets, zero-value usability, aliasing, ordering, idempotency, nil-versus-empty, absent-versus-explicit JSON, error identity through `errors.Is`/`errors.As` (`golang-error-handling`), context propagation and cancellation (`golang-context`).
- **Lifecycle**: every ownership-ledger row closed on every path; goroutine exit, send/close ordering, lock scope, defer order (`golang-concurrency`).
- **Boundaries**: dependency direction and consumer-side interfaces (`golang-solid-principle-review`, `golang-hexagonal-architecture`).

Label each candidate: introduced, worsened, pre-existing, or uncertain.

## 4. Prove

- **Contract or lifecycle claim**: write a probe test that fails on the current code, or show the exact path in the ledger. Use `-race` for concurrency claims.
- **The change's own tests**: revert the fix locally, rerun its test, and expect it to fail. A test that stays green under revert proves nothing.
- **Simplification claim**: sketch the after-state: concepts retired and added, estimated net maintained LOC across the subsystem, and the contracts and tests that pin equivalent behavior. Note where `codegraph impact` shows the blast radius.
- Run the tests `codegraph affected` lists for the touched files. Record skipped checks and unrelated failures; adjacent green tests are not proof.

## 5. Refute

Try to break every candidate before reporting it: find the consumer that needs the concept, the test that pins the behavior, or the ADR or `AGENTS.md` rule that mandates it. Drop or downgrade whatever does not survive. When the harness can start a fresh context, hand each blocker or major candidate to an independent verifier with only the claim and its cited locations, not the reasoning that produced it.

## 6. Report

Severity:

- **blocker**: an introduced or worsened contract, lifecycle, or guardrail violation; or an introduced concept that duplicates or fights the existing design with demonstrated cost.
- **major**: a simplification that retires a concept family or a wrong abstraction, with a concrete after-state.
- **minor**: a single accidental concept with clear payoff.

Confidence is **high** (proved by test, revert, or complete ledger) or **medium** (traced but not executed). Anything weaker goes under `Unverified`, not under findings.

Per finding, header `severity | confidence | file:line/symbol | introduced/worsened/pre-existing`, then:

- `Evidence`: ledger rows, code, and proof.
- `Consequence`: the concrete failure or carrying cost.
- `Remediation`: the after-state, concepts retired versus added, estimated net maintained code, and the invariants to preserve. Split restructurings larger than the 100-modified-`*.go`-file PR gate into ordered slices.
- `Verification`: checks run and checks still required.

Order findings by severity, then payoff. Then include the ledgers, the inspected surface, and `Unverified`.

End a diff review with a verdict: `BLOCK` if any blocker survives, otherwise `APPROVE` plus the ranked simplification plan for follow-up. End an area audit with the ranked plan. If nothing survives, say so and attach the ledgers as the evidence of what was inspected.
