---
name: golang-testing
description: "Write, review, or prune Go tests: unit, integration, HTTP, concurrent, fuzz, benchmark. Use when adding tests (including to raise coverage), judging whether existing tests prove anything, choosing fixtures/fakes/clocks, or diagnosing flaky tests."
---

# Go testing

A test earns its place by defending a **claim**: one sentence naming behaviour that someone outside the code depends on, and that a plausible bug would break. Coverage is a by-product of tested claims, never a reason to write a test.

Each claim has three parts:

- **Claim**: what callers, users, or operators observe. "A request with an unknown proxy key gets 401 and never reaches a backend."
- **Source**: where that behaviour is required, named concretely (file, symbol, issue, steering line).
- **Mutant**: the concrete bug the test catches. "Delete the `!ok` check in `Gate.ServeHTTP`."

No source or no mutant means no test.

Reviewing existing tests instead of writing new ones: follow [review](references/review.md).

## Writing tests

1. **Find claims.** Read these, in order, and collect the behaviour each one requires of the code under change:
   - the task, issue, or bug report you were given;
   - `.kiro/specs/<feature>/requirements.md` acceptance criteria, when the work belongs to a spec;
   - the godoc of each exported symbol you are testing;
   - "High-Value Semantic Targets" in `.kiro/steering/testing.md` and "Architecture Guardrails" in `AGENTS.md`;
   - what the callers of the code do with its return values and errors.
2. **Write the claim table before any test code.** One row per claim: claim, source, mutant. A row whose source is "this line is uncovered" or "the function exists" is struck out.
3. **Choose where to observe the claim:**
   - pure logic or decisions: call the function directly, table test with one row per distinct outcome;
   - HTTP handler or middleware: `httptest`, asserting status, body, and what reached the next handler or backend;
   - frontend/backend/core protocol behaviour: add a case to the existing suite under `internal/testkit/contract/` before building a new harness;
   - external service or slow dependency: fake only that boundary; use real `pkg/aipapi` types everywhere else.
4. **Write the test.** Assert both what the caller receives (value, error class, status, events) and the side effect that must or must not happen (backend called or not, row persisted or not, retry attempted or not). Name it `Test<Unit>_<Claim>`: `TestGate_UnknownKeyNeverReachesBackend`, not `TestServeHTTP2`. Hard-code expected values worked out from the source, e.g. `want := 6`, never recomputed with the code's own formula.
5. **See it red.** For each row:
   - Under TDD, run the test before the implementation exists and confirm it fails.
   - For code that already exists, edit the production code to the mutant, run `go test -run '^TestName$' ./path/to/pkg`, confirm FAIL, undo exactly that edit, re-run to PASS, and confirm `git diff` on the production file shows no leftover mutant.
   - A test that stays green under its mutant asserts the wrong thing. Fix the assertion, not the mutant.
6. **Check every test** against these questions. Any "no" means rewrite or delete it:
   - Does its name state the claim?
   - Is the source a concrete requirement, contract, invariant, or bug?
   - Was it observed red under its mutant?
   - Would it still pass after a refactor that keeps behaviour the same?
7. **Hand off** the claim table with the red result, plus a list of code you left uncovered and why:

   | Test | Claim | Source | Mutant → result |
   | --- | --- | --- | --- |
   | `TestGate_UnknownKeyNeverReachesBackend` | unknown key → 401, next handler not called | `testing.md`: authorization boundaries | removed `!ok` check → FAIL |

**Coverage targets.** When a task asks for a coverage percentage, write the claim-backed tests, then report the remaining shortfall and the uncovered code by name. Uncovered code either serves a claim you missed (add the claim) or serves none (report it as dead or speculative). A coverage request never justifies a dummy test.

**Trivial code.** Test a getter, `String()`, or constructor only when its output is itself a contract: a wire value, a config key, a validation rule in the constructor. Then the claim is that contract, e.g. "`New` rejects an empty key map".

## Dummy tests

These pass against broken code or break on harmless refactors. Never write them; delete them when found.

| Pattern | Looks like | Do instead |
| --- | --- | --- |
| Existence check | `if New(...) == nil { t.Fatal() }` | Delete. Claim tests already construct the value. |
| Getter echo | `New("a").Name() == "a"` | Delete. |
| Discarded result | `_ = l.Allow(ctx, "t")`, then assert something else | Assert the returned value. |
| Smoke only | only `err == nil`, or "does not panic", on a function that produces output | Assert the output. |
| Mock echo | fake returns X; test asserts X came back | Assert what the unit does with X: transforms it, maps the error, decides. |
| Mirrored oracle | `want := l.Limit() - l.used["t"]` | Hard-code `want := 6`, derived from the source. |
| Testing Go or a library | JSON tags round-trip, `const == literal`, `errors.Is` works | Delete. If the wire shape is the contract, assert the exact bytes a client receives. |
| Change detector | reads private fields, asserts internal call order, exact log or metric text | Assert the public outcome. Assert call order only when order is the contract. |
| Padding rows | ten table rows that all take the same path | One row per distinct outcome or boundary: empty, at limit, limit+1, malformed. |
| Unrun gate | build tag or env var no `Makefile` target sets | Wire it into a documented target, or delete it. |
| Tested scaffolding | tests for a test helper or fake | Simplify the helper until it is obviously correct. |

## Go-specific gotchas

- `t.Fatal`/`FailNow` only from the test goroutine; workers send errors back for assertion after synchronization.
- `t.Context()` (Go 1.24+) is cancelled just before `t.Cleanup` functions run; use it for code that must stop with the test.
- `testing/synctest` (Go 1.25+) or an injected clock for timer/deadline ordering. `time.Sleep` is never synchronization; a timeout bounds a test, it does not fix a race.
- `-race` detects races only on exercised paths and cannot prove absence. In this repo never run `-race` locally: scheduled remote scans own race coverage and it is not a merge gate; see `AGENTS.md` Verification.
- Packages that own goroutines need a test-owned shutdown path for each; `goleak.VerifyNone`/`VerifyTestMain` proves `Stop` actually releases them (filter unavoidable runtime goroutines).
- Fuzz parsers and decoders that take untrusted input; the oracle is a contract property (round-trip, typed error instead of panic, output stays escaped), with a minimal seed corpus.
- `httptest.NewRecorder` skips the transport: use `httptest.NewServer` when headers, streaming flush, connection close, or client behaviour is the claim.
- `sql.Open` does not connect; prove readiness with `PingContext` under a deadline.
- Benchmarks: `b.Loop()` where the module's Go version supports it, `b.ReportAllocs()` when allocations are the claim, compare runs with `benchstat`.
- Flakes: reproduce with `-count=N` and fixed seeds; fix the synchronization, never weaken the assertion.

Repo test policy (layers, proportionality budget, gating, commands) lives in `.kiro/steering/testing.md` and overrides this skill.

References: [helpers](references/helpers.md), [HTTP tests](references/http-testing.md), [integration tests](references/integration-testing.md), [fakes and mocks](references/mocking.md).
