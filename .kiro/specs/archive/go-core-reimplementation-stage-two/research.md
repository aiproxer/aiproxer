# Research notes — Go core reimplementation stage two

Spec name: `go-core-reimplementation-stage-two`

## Inputs considered

- v1 repository implementation and composition shape
- [`v1_code_review.md`](v1_code_review.md)
- existing stage-one Kiro artifacts in the repo ([`go-core-reimplementation-v1`](../go-core-reimplementation-v1/))
- original Python LIP product intent and the preserved distinctive behaviors

---

## Requirement anchors (normative IDs in [`requirements.md`](requirements.md))

| Research topic | Primary criteria |
|----------------|------------------|
| Static registration vs Go `plugin` | **1.1**, **12.3** |
| SQLite-first durable store | **6.1**, **6.3** |
| Candidate-aware capabilities | **7.1**, **7.2** |
| Routing policy layer | **5.1**, **5.2**, **13.1** |
| Tool-use history focus | **8.1**, **8.2**, **8.4** |
| Phase-specific feature plugins | **9.1**, **9.6** |
| Immutable baseline / attempts | **3.1**, **3.2** |
| Architectural theater risk | **1.4**, **2.1**, **6.2**, **11.3** |

---

## Main observations from v1

### 1. The direction is correct

The Go repo already has the right high-level instincts:

- canonical contracts
- streaming-first execution
- B2BUA retry semantics
- explicit routing syntax
- early hook seams
- deterministic tests

So stage two should **not** replace the v1 foundation. It should make it honest and extensible.

### 2. The main remaining risk is architectural theater

Several abstractions exist but are not yet the true runtime mechanism:

- plugin registrations exist but composition is switch-based
- frontend config exists but does not control actual mounting
- lifecycle exists but is not exercised
- continuity/routing config exists but parts are dead or placeholder

Stage two therefore prioritizes truthfulness of composition over headline feature count.

### 3. Mutable-attempt semantics are the highest technical risk

The most serious correctness issue in v1 is request contamination across retries.

That issue matters more than any single missing protocol edge because it can silently distort behavior.

This is why stage two centers the design on immutable baseline + attempt-local derivation.

---

## Decision: keep static linking, avoid Go `plugin`

Reasoning:

- standard Go plugin loading remains operationally narrow compared to normal static builds
- cross-platform support and tooling ergonomics are worse than explicit registration
- the project does not need runtime binary plugin loading to achieve real decoupling

Conclusion:

- use **static bundle registration** for stage two
- make construction flow through registries/factories
- revisit out-of-process plugins only if isolation becomes necessary later

---

## Decision: SQLite is the right first durable continuity store

Reasoning:

- stage two needs durability, not distributed systems complexity
- SQLite is excellent for a single-binary Go service
- it lowers operational friction compared to introducing Postgres first
- it is good enough for local, edge, and many single-node proxy deployments

Conclusion:

- memory + SQLite should be required implementations in stage two
- networked stores can come later if justified

---

## Decision: capability negotiation must become candidate-aware

Reasoning:

- provider capability varies by model and sometimes route flavor
- backend-instance-wide caps are too coarse
- stage two expands protocol fidelity and routing sophistication, making false accepts/rejects more harmful

Conclusion:

- capability resolution should be per route candidate and, where needed, per model
- keep the API deterministic and cheap; do not require live discovery in stage two

---

## Decision: routing policy needs its own layer

Reasoning:

- the executor already has enough responsibility
- health, cooldown, circuit-breaking, and max-attempt logic are policy concerns
- keeping policy separate prevents the executor from becoming the new central god object

Conclusion:

- split parsing, policy, health, and stream execution responsibilities
- preserve selector syntax, but route final behavior through policy objects

---

## Decision: stage-two protocol work should focus on tool-use history

Reasoning:

- that is where real-world continuity and cross-API behavior starts breaking down today
- it is higher-value than chasing minor vendor-only knobs first
- it aligns directly with preserved LIP strengths

Conclusion:

Stage-two protocol fidelity work should prioritize:

- OpenAI Chat assistant tool-call history
- OpenAI Responses non-message input items needed for tool-use continuation
- equivalent supported subsets across Anthropic/Gemini/Bedrock where practical

---

## Decision: feature plugins should stay phase-specific

Reasoning:

- submit hooks, request/response mutations, and tool reactors do different jobs
- a generic “feature plugin does anything” surface encourages coupling and opaque ordering
- phase-specific middleware preserves clarity

Conclusion:

Keep distinct feature families, but make them fully real via factory-driven composition and proper metadata/lifecycle handling.

---

## Risks to watch during stage two

1. Replacing switchboards with a giant registry singleton
2. Replacing executor problems with a bigger orchestration object
3. Over-centralizing shared translation helpers into a new god package
4. Making persistent store support too abstract too early
5. Trying to solve every protocol edge case before composition is honest

---

## Working stage-two thesis

Stage two should be judged less by “how many more features were added” and more by this question:

> After stage two, can new routing, continuity, and feature behavior be added without editing core runtime packages or bundle switchboards?

If the answer is yes, the rewrite is succeeding.
If the answer is no, the project is still carrying Python-era architectural gravity.
