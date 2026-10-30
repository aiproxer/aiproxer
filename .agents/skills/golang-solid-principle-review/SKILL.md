---
name: golang-solid-principle-review
description: "Review whether Go code absorbs change and substitution cheaply: cohesion, variation seams, behavioral substitutability, consumer-side interfaces, and dependency direction, judged by Go idioms and change evidence. Use for a SOLID review or an architectural review of a diff, package, or interface design."
---

# Go SOLID review

## Mission

The question is whether the structure absorbs change and substitution cheaply. SOLID is the vocabulary; **change cost** is the measure. A finding needs one of three kinds of evidence:

- **Change amplification**: one requirement forces edits across places that should not care about it.
- **Substitution failure**: an implementation or wrapper breaks what its callers rely on.
- **Inverted dependency**: stable policy imports volatile detail.

A principle that is merely unobserved is not a finding.

Judge every proposed seam by net concepts: an interface, port, or package split must name the branches, edit sites, or imports it retires. When the remedy retires a concept family or replaces a wrong abstraction, switch to `golang-thermonuclear-code-review`. Reach for Go-native seams first: a concrete type, a function value, a small consumer interface, generics, or data-driven dispatch; a registry only when runtime registration is a requirement. A `switch` over a closed protocol or state set is sound design.

Keep style, security, performance, and generic bug hunting out of scope unless a design issue causes them.

## 1. Scope

Pick the branch and record it:

- **Diff review** (branch/PR): base is `git merge-base origin/main HEAD`. Review the diff plus the interfaces, implementations, and imports it touches.
- **Area audit** (package/subsystem): the named area plus its consumers and dependencies.

Record branch, base, dirty files, and excluded generated code. Stay read-only unless fixes are requested.

Done when: scope, base, and exclusions are written down.

## 2. Map with evidence

Build tables; each row cites `file:line`. A cell you cannot fill is either a finding or an explicit `unverified` entry. Use CodeGraph (load the `codegraph` skill).

**Interface ledger** (always). Enumerate with `codegraph query NAME --kind interface`; `codegraph explore` lists `implements` edges; `codegraph callers` per method shows what each consumer calls. Count production and test implementations separately.

| Interface | Declared in (consumer / provider package) | Methods | Implementations (prod / test) | Consumers | Methods each consumer calls |
|---|---|---|---|---|---|

**Dependency table** (always). List edges with `go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./<scope>/...` and check each against the Package Zones and Architecture Guardrails in `AGENTS.md` and the gates in `internal/archtest`.

| Importer | Imported | Direction (policy → detail?) | Allowed by zones/guardrails? |
|---|---|---|---|

**Change evidence** (for each variation axis in scope: providers, frontends, features, storage, protocols):

- **Co-change**: which zones the axis's recent commits also touched. Commits over 40 files are excluded, since mass refactors and renames otherwise dominate the counts:

  ```sh
  git log -n 100 --no-merges --full-diff --format=@%h --name-only -- "$AXIS" | awk -F/ 'function flush(){ if (f && f <= 40) { k++; for (z in s) n[z]++ } delete s; f = 0 } /^@/ { flush(); next } NF { f++; s[$1"/"$2"/"$3] = 1 } END { flush(); for (z in n) print n[z] "/" k, z }' | sort -rn | head
  ```

  Read the commits behind any notable cross-zone count before calling it coupling.
- **Change walk-through**: take one real upcoming change (from `.kiro/specs`, an open issue, or the next provider, frontend, or feature) and list every edit site and its zone. Changes with no source in the repo's plans stay out.

**Substitution matrix** (when an interface has two or more production implementations or wrappers): rows are implementations and wrappers; columns are the contract points callers rely on: accepted inputs and nil/zero behavior, result shape, error identity and partial results, ownership and aliasing, ordering, idempotency and blocking, cancellation and deadlines, concurrency safety, resource closure, optional interfaces preserved.

Done when: every interface and cross-package edge in scope has a row, and every variation axis in scope has co-change data or a walk-through.

## 3. Principle lenses

Read the tables through each lens. Label each candidate: introduced, worsened, pre-existing, or uncertain.

**SRP** — start at the package, Go's unit of cohesion. Independent reasons to change show up as co-change clusters that do not overlap, or as distinct change triggers in the walk-through. Signals: grab-bag `common`/`util`/`types` packages, packages split only to break an import cycle, unrelated state under different synchronization, policy mixed with transport or persistence, configuration mixed with execution, goroutine startup and shutdown owned in different places. A cohesive orchestrator calling many collaborators is sound; length is a clue, never proof.

**OCP** — the axis must be demonstrated by two or more implementations, repeated branches, or the walk-through. A finding counts the edit sites one change on that axis costs, and names the seam that reduces them without hiding control flow. This repo has already decided several of these seams: compatible-provider growth is data-driven, request/response mutation sits behind hooks, and core never branches on concrete providers.

**LSP** — read the substitution matrix; stronger guarantees that keep callers' assumptions are sound. Check the Go traps:

- A wrapper drops optional interfaces (`http.Flusher`, `http.Hijacker`, `io.WriterTo`, `io.ReaderFrom`) that callers discover by type assertion. A lost `http.Flusher` silently turns streaming into buffering.
- A struct embeds an interface to implement it partially; the methods it leaves out panic on the nil embedded value.
- The method set shifts through pointer versus value receivers or promoted methods, so a type gains or loses an interface after a refactor.
- A typed nil pointer returned as an interface compares non-nil.

**ISP** — signals: consumers calling a small subset of an interface's methods, interfaces declared on the provider side, mocks stubbing unused methods, consumers that immediately type-assert, command and query methods mixed. Remedies: a narrow interface declared by the consumer, a function type, or the concrete type when substitution is not demonstrated.

**DIP** — stable policy importing volatile detail is the finding. The consumer (core) declares the port, adapters implement it, and the composition root wires them (`internal/infra/runtimebundle`, `internal/standardplugins`). Generated transport, database, and provider code lives at adapters. Constructors return concrete types unless a port is the point.

## 4. Prove

- **LSP**: add or run a case in `internal/testkit/contract` (`backend`, `frontend`, `core`, `semantic`, `metering`) that fails for the violating implementation, or name the gap in the kit's coverage.
- **SRP/OCP**: cite co-change counts with commit hashes, or the walk-through's edit sites.
- **DIP**: cite the import edge, and say whether an `internal/archtest` gate should catch it and why it does not.
- **Seam proposals**: list concepts retired versus added, and the edit sites per future change before and after.
- Record skipped checks and unrelated failures; adjacent green tests are not proof.

## 5. Refute

Try to break every candidate before reporting it: find the consumer that relies on the current coupling, the ADR in `docs/adr` or the `AGENTS.md` rule that mandates it, or the commits that explain a co-change spike (mass rename, release, migration). Drop or downgrade whatever does not survive. When the harness can start a fresh context, hand each blocker or major candidate to an independent verifier with only the claim and its cited locations, not the reasoning that produced it.

## 6. Report

Severity:

- **blocker**: an introduced or worsened substitution failure or dependency-direction violation that breaks a caller, a contract kit, or an `AGENTS.md` guardrail.
- **major**: demonstrated change amplification on an active axis, or an implementation that fails the contract it claims, with a concrete seam.
- **minor**: a single localized interface or seam improvement with clear payoff.

Confidence is **high** (proved by test, contract kit, or complete table) or **medium** (traced but not executed). Anything weaker goes under `Unverified`, not under findings.

Per finding, header `severity | confidence | file:line/symbol | introduced/worsened/pre-existing | principle`, then:

- `Evidence`: table rows, co-change or walk-through data, and proof.
- `Consequence`: the concrete failure or the change cost per future change.
- `Remediation`: the seam, concepts retired versus added, and the invariants to preserve.
- `Verification`: checks run and checks still required.

Order findings by severity, then payoff. Then include the tables, the inspected surface, and `Unverified`.

End a diff review with a verdict: `BLOCK` if any blocker survives, otherwise `APPROVE` plus ranked follow-ups. End an area audit with the ranked findings. If nothing survives, say so and attach the tables as the evidence of what was inspected.
