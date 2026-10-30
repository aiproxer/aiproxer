# Requirements Document

**Source context:** Follow-up validation for completed [`.kiro/specs/archive/reasoning-output-preservation/`](../reasoning-output-preservation/) (issue [#157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157)). Parent feature implements observe/restore; this specification proves it end-to-end on the standard HTTP distribution.

## Introduction

Maintainers and release reviewers need executable proof that reasoning-output-preservation works through the **actual** standard HTTP stack—not only in-process Phase 5 runtime stubs. Today, operator examples are config-validation dogfood and behavioral proofs are `TestPhase5_*` composed runtime tests. Those leave a gap: real frontend/backend adapters, `runtimebundle.BuildBootstrap`, `stdhttp` handlers, and protocol-shaped backend emulators over `httptest` are not jointly exercised for multi-turn transcript fidelity.

This feature adds opt-in full HTTP E2E validation that drives a stateful client transcript (observed vs submitted), an independent backend-request oracle, deterministic control scenarios, a seeded random matrix, and an env-gated soak. Fixes are TDD-narrow and only for concrete defects exposed by those proofs. OpenAI Responses replay harness work is explicitly deferred.

## Boundary Context

- **In scope**: full HTTP E2E harness on the standard distribution; OpenAI Chat deep coverage; Anthropic signed `thinking` / `redacted_thinking` cross-check; deterministic controls; seeded 64×20 matrix; env-gated 1000×100 soak; content-safe failure traces; suite/gate placement; docs/checklist/EchoesVault updates; independent review; focused/race/quality/parity/QA evidence; narrow production fixes only when an E2E defect is proven.
- **Out of scope**: redesigning reasoning-output-preservation semantics; fuzzy/semantic matching or reasoning synthesis; OpenAI Responses full E2E until a stable replay harness exists; live provider network calls; durable multi-replica artifact stores; relaxing exact-match/privacy rules; broad refactors unrelated to a failing E2E case.
- **Adjacent expectations**: preserve parent-spec exact matching, restore-only-when-missing, no overwrite on conflict/ambiguity, authoritative session isolation, hard `reasoning_replay`, streaming-first, no retry after first client-visible content, and content-safe observability.
- **Boundary ownership**: E2E harness, oracles, and emulators are test-only (`internal/testkit`, `internal/refbackend`, `internal/stdhttp` tests, Make/CI wiring, docs). Production changes are limited to concrete adapter/runtime defects proven by failing E2E evidence, primarily OpenAI Chat assistant reasoning+tool-call replay.
- **Revalidation triggers**: parent feature contracts, adapter wire shapes for Chat/Anthropic reasoning replay, `stdhttp`/runtimebundle composition, secure-session partitioning, suite topology (`precommit` / soak gates), and release-checklist evidence paths.

## Requirements

### Requirement 1: Standard HTTP Stack Composition
**Objective:** As a release reviewer, I want E2E proofs to run through the real standard distribution composition, so stub-only green tests cannot hide adapter or wiring regressions.

#### Acceptance Criteria
1.1. When a full HTTP E2E case runs, the harness shall assemble the proxy through `runtimebundle.BuildBootstrap`, standard plugin registration, and the `stdhttp` handler surface.
1.2. The harness shall exercise real frontend and backend adapters for the protocols under test rather than substituting core-only stub executors for those adapters.
1.3. The harness shall place the proxy and protocol backend emulators behind `httptest` servers with no live provider network dependency.
1.4. While the feature under test is configured disabled, observe, or restore, the harness shall use operator-valid configuration shapes consistent with shipped examples and parent-spec semantics.
1.5. The harness shall not claim success from config-validation dogfood alone; each accepted scenario shall perform at least one multi-turn HTTP round trip through the composed stack.

### Requirement 2: Stateful Client Transcript and Backend Oracle
**Objective:** As a maintainer, I want independent observed/submitted client state and a backend-request oracle, so restoration claims are proven at the wire boundary the backend actually receives.

#### Acceptance Criteria
2.1. The harness shall maintain a stateful client transcript that distinguishes what the client observed from what the client later submits.
2.2. The harness shall capture backend-bound request observations independently from the client transcript.
2.3. When comparing a backend-bound request to the precomputed plan, the oracle shall assert visible text, reasoning metadata and order, tool IDs/names/arguments/results, and signatures/opaque presence as applicable.
2.4. The oracle shall reject unexpected reasoning insertion, incomplete restoration, duplication, reordering, and cross-turn identity mismatches.
2.5. Plans and oracles shall be deterministic from explicit seeds and shall not use package-global RNG or wall-clock time for expectations.
2.6. Existing reusable `internal/testkit/reasoninge2e` and OpenAI Chat `Responder` emulator work on branch `test/reasoning-preservation-e2e-validation` shall be preserved and extended rather than replaced.

### Requirement 3: Deterministic Control Matrix
**Objective:** As a maintainer, I want a fixed set of named controls, so classification and restore behavior remain reviewable without depending on random exploration.

#### Acceptance Criteria
3.1. The deterministic suite shall cover feature disabled with no preservation side effects.
3.2. The deterministic suite shall cover action `observe` (capture/classify without mutating backend-bound reasoning).
3.3. The deterministic suite shall cover action `restore` with client drop-all reasoning for both non-streaming and streaming turns.
3.4. The deterministic suite shall cover client preserve-all reasoning (no restoration mutation required).
3.5. The deterministic suite shall cover reasoning combined with tool calls and tool results.
3.6. The deterministic suite shall cover mixed turns with and without reasoning in one session.
3.7. The deterministic suite shall cover conflicting client-supplied reasoning left untouched.
3.8. The deterministic suite shall cover ambiguity and changed-anchor cases without rewriting history.
3.9. The deterministic suite shall cover authoritative session isolation with no cross-session leakage of reasoning artifacts.
3.10. Deterministic controls shall run in the ordinary default test suite (no `integration`/`soak` gate required for these cases).

### Requirement 4: Protocol Coverage and Explicit Deferrals
**Objective:** As a protocol maintainer, I want deep OpenAI Chat proof plus Anthropic thinking cross-check, so the highest-risk replay paths are covered without blocking on deferred Responses harness work.

#### Acceptance Criteria
4.1. The E2E suite shall provide deep OpenAI Chat Completions coverage for streaming and non-streaming multi-turn reasoning replay under restore and observe.
4.2. The E2E suite shall include deterministic Anthropic Messages cross-checks for signed `thinking` and `redacted_thinking` replay legality.
4.3. OpenAI Responses full HTTP E2E shall remain explicitly deferred until a stable Responses replay harness exists; absence of Responses E2E shall not block this specification’s definition of done.
4.4. Where a protocol path is deferred, documentation and tasks shall name the deferral and shall not silently treat Phase 5 or goldens as a substitute for the deferred HTTP E2E.
4.5. Tests shall preserve tool call/result correlation across turns for covered protocols when tools are part of the scenario.

### Requirement 5: Seeded Random Precomputed Matrix
**Objective:** As a maintainer, I want a bounded seeded matrix, so combinatorial retention/stream behavior is explored without flaky live randomness.

#### Acceptance Criteria
5.1. The random matrix shall use 64 seeds × 20 turns with expectations precomputed before execution.
5.2. Of the 64 seeds, 16 shall use random backend content with client drop-all retention.
5.3. Of the 64 seeds, 16 shall always emit reasoning with random client retention decisions.
5.4. Of the 64 seeds, 32 shall combine backend variability with client retention variability.
5.5. The matrix shall alternate streaming and non-streaming turns according to the precomputed plan.
5.6. The matrix shall force coverage categories required by Requirement 3 where a pure random draw would under-sample them.
5.7. When a matrix case fails, the failure shall emit seed, mode/policy, turn identity, and a compact structural trace.
5.8. Failure output shall not include reasoning text, signatures, or opaque payloads.
5.9. Where suite topology is concerned, the randomized matrix shall use the repository `precommit` tag pattern for large regression matrices: omitted from default `go test ./...` / ordinary `make test-unit` and from the lightweight `make test-precommit-extra` hygiene/runtime package set; executed by `make qa` / CI tagged unit jobs (`go test -tags=precommit,integration ./...`) and by the targeted command `go test -tags=precommit -run TestReasoningPreservationHTTP_RandomMatrix ./internal/stdhttp/` per `.kiro/steering/testing.md`.

### Requirement 6: Env-Gated Soak
**Objective:** As a release operator, I want an optional deep soak, so rare transcript defects can be hunted without making every PR pay the cost.

#### Acceptance Criteria
6.1. The soak shall support 1000 seeds × 100 turns under an explicit environment gate.
6.2. The repository shall expose a dedicated Make target that runs the soak only when the gate is set.
6.3. The repository shall provide a dedicated GitHub Actions workflow using `workflow_dispatch` and a nightly schedule for the soak.
6.4. The soak shall not be a mandatory PR gate.
6.5. The soak shall bound worker concurrency, use fresh bounded sessions, enforce a hard timeout, and support replaying a single failing seed.
6.6. Soak failures shall obey the same content-safe structural failure contract as Requirement 5.7–5.8.

### Requirement 7: Defect Repair Policy
**Objective:** As an implementer, I want a strict TDD repair policy, so E2E work cannot become speculative product redesign.

#### Acceptance Criteria
7.1. Implementation shall follow red → green → refactor for every behavioral change.
7.2. Production or adapter code shall change only to fix a concrete defect demonstrated by a failing E2E (or focused regression derived from it).
7.3. The most likely first defect class—OpenAI Chat assistant reasoning combined with tool-call replay—shall be covered by a dedicated red case before any production patch for that class.
7.4. The harness and production code shall not introduce relaxed matching, fuzzy anchors, or reasoning synthesis to make tests pass.
7.5. Unrelated refactoring outside the failing boundary shall be out of scope for this specification’s implementation tasks.

### Requirement 8: Privacy, Isolation, and Content Safety
**Objective:** As an operator, I want E2E evidence to strengthen privacy and isolation claims, so hidden reasoning cannot leak through test output or sessions.

#### Acceptance Criteria
8.1. Tests shall prove no cross-session leakage of reasoning artifacts between authoritative sessions.
8.2. Ordinary logs, metrics assertions, and failure messages in the E2E harness shall remain free of reasoning text, signatures, opaque payloads, and anchor digests.
8.3. While validating restore success, the suite shall still assert that conflicting/ambiguous/unmatched classifications do not overwrite client-supplied reasoning.
8.4. Disabled configuration cases shall allocate no preservation side effects observable through the E2E oracle or feature telemetry surface used by the harness.

### Requirement 9: Documentation, Review, and Release Evidence
**Objective:** As a release reviewer, I want docs and gates updated with independent review, so the validation story is discoverable and evidenced.

#### Acceptance Criteria
9.1. Operator/docs/release-checklist material for reasoning-output-preservation shall document the full HTTP E2E suite, matrix placement, soak opt-in, and the OpenAI Responses deferral.
9.2. EchoesVault compiled knowledge for reasoning-output-preservation shall be updated to reference this follow-up validation specification and its suite topology.
9.3. Before claiming this specification complete, an independent review shall be performed against these requirements and the design boundary commitments.
9.4. Completion evidence shall include focused E2E package tests, race where practical (`make test-race` / Linux CI strict race), `make quality-checks`, `make parity-checks`, and `make qa`.
9.5. Windows local runs shall not claim race-green when `make test-race` is a no-op; Linux CI race evidence remains required for completion claims that include race.
9.6. Parent issue [#157](https://github.com/matdev83/go-llm-interactive-proxy/issues/157) and the completed parent spec path shall be linked from this specification’s docs updates.
