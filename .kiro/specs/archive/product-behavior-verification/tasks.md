# Implementation Plan

Each task carries its own tests. A journey that exposes a defect on `main` is not fixed in this spec: the fix ships from `main` in its own PR, or the journey is quarantined with its issue until that fix lands.

## Tasks

- [x] 1. Bound the taskrunner wait after kill
  - Set `exec.Cmd.WaitDelay` in `taskrunner.Run` so a descendant holding the output pipes cannot block `Wait` past the deadline; classify it as `CleanupFailure`.
  - Done when a grandchild in its own process group holding the pipe no longer hangs `Run`, and existing taskrunner tests pass on Linux and Windows.
  - _Requirements: 5.1, 5.4_
  - _Boundary: tools/taskrunner_
  - _Validation: `go test ./tools/taskrunner/...`_

- [x] 2. Build the harness and the startup journeys
  - Add `required.go`, the proxy-under-test handle (`sut_test.go`) and scripted fake providers (`provider_test.go`) with request recording, faults, cancellation observation and a canary key.
  - Implement `TestProduct_Startup/{ready,invalid_config}` and `TestProduct_Lifecycle/canary_credential`, plus the OpenAI legacy chat stream and collect journeys as the first protocol witnesses.
  - Done when these pass against a locally built `lipstd` and a missing `LIP_PRODUCT_SMOKE_BIN` fails rather than skips.
  - _Requirements: 1.1, 1.2, 1.3, 2.1, 2.3, 2.5, 4.3, 5.4, 5.5, 5.6_
  - _Boundary: tests (internal/testkit/productsmoke)_
  - _Validation: `go test -count=1 -tags=productsmoke ./internal/testkit/productsmoke` with the binary built manually_

- [x] 3. Add the bounded command with truthful reconciliation
  - Add `devcheck -task=product-smoke`: one 600-second deadline, build once, run with `-json -count=1`, reconcile against `Required` and the quarantine, print per-journey verdicts and write the JSON summary. Add `make verify-product`.
  - Done when recorded event streams for a missing, skipped, empty, quarantined, failed and deadline-cut run produce the expected non-passing verdicts, and `make verify-product` passes with the task 2 journeys.
  - _Depends: 1, 2_
  - _Requirements: 2.5, 5.1, 5.2, 5.3, 5.4, 5.6_
  - _Boundary: tools/devcheck, Makefile_
  - _Validation: `go test ./tools/devcheck/...`; `make verify-product`_

- [x] 4. Cover the remaining frontend families (P)
  - Add stream and collect journeys for SystemOne, OpenAI Responses, Anthropic, Gemini and OpenResponses, routed to at least two upstream wire families with one cross-protocol pairing.
  - Done when each journey asserts content, usage and a legal terminal on the client and the translated request at the provider, and a recorded non-vacuity probe shows a dropped terminal fails its journey.
  - _Depends: 2_
  - _Requirements: 2.1, 2.2, 2.3_
  - _Boundary: tests (internal/testkit/productsmoke)_
  - _Validation: `make verify-product`_

- [x] 5. Cover continuity and failure journeys (P)
  - Implement `TestProduct_Continuity/tool_round_trip` and `TestProduct_Failure/{pre_output_fallback,post_output_no_retry,client_cancel}`, each followed by a successful new request.
  - Done when the journeys pass and recorded probes show that a reordered history and a second post-output attempt each fail their journey.
  - _Depends: 2_
  - _Requirements: 2.4, 3.1, 3.2, 3.3, 3.4_
  - _Boundary: tests (internal/testkit/productsmoke)_
  - _Validation: `make verify-product`_

- [x] 6. Cover reload and shutdown journeys (P)
  - Implement `TestProduct_Lifecycle/{reload_valid,reload_invalid}` through the management API and `TestProduct_Shutdown/graceful` on POSIX, marked POSIX-only in `Required`.
  - Done when the journeys pass on Linux, the shutdown journey is excluded from the Windows required list, and the reload journeys pass on Windows.
  - _Depends: 2_
  - _Requirements: 4.1, 4.2, 5.5_
  - _Boundary: tests (internal/testkit/productsmoke)_
  - _Validation: `make verify-product` on Linux and Windows_

- [x] 7. Document, adopt and measure
  - Add `docs/testing/product-smoke.md` with the command, the verdicts and the journey recipe. List `make verify-product` as `ci-stand-in` evidence in `docs/development-iteration.md`.
  - Measure one cold and three warm runs on the maintainer's Linux host and on one Windows host, and record the times in the PR. Every run must finish within 600 seconds.
  - Done when the docs are merged and the measurements are recorded. If any run exceeds the budget, reduce setup cost, never journeys.
  - _Depends: 3, 4, 5, 6_
  - _Requirements: 5.1, 5.5, 5.6_
  - _Boundary: docs_
  - _Validation: `make verify-product` timings; `go run ./tools/kiro/kirocheck`_
  - _Done: docs merged (`docs/testing/product-smoke.md`; `make verify-product` listed as `ci-stand-in` evidence in `docs/development-iteration.md`). Linux timings on main `5b5c873c`: 13 s, 11 s, 11 s, 11 s, all within 600 s (the first run had a warm Go build cache). The Windows measurement is deferred to #1635 by owner decision (2026-10-29)._

## Deferred / Out-of-Plan

- Windows-host `make verify-product` timings (part of task 7) are deferred to #1635: no Windows host was available; the Linux half is recorded above.

## Completion Status

- [x] All tasks checked; the bounded product smoke shipped in #1397 (spec #1372, later coverage #1510).
- [x] Verified on main `5b5c873c` (2026-10-29): `make verify-product` passes in 11–13 s; the full root suite `go test -tags=precommit,integration ./...` passes.

## Implementation Notes

- Task 1: `taskrunner.Run` sets a one-second `cmd.WaitDelay`. A fired deadline keeps `DeadlineExceeded` and records the cleanup error; a clean child exit with leaked stream pipes becomes `CleanupFailure`. The owner authorized accepting Linux runtime plus Windows build/vet for task 1, with Windows runtime execution deferred to task 7's Windows measurement; do not relabel static checks as runtime proof.
- Task 2: the harness retains the last readiness exchange for failure evidence, redacts before any lossy truncation, treats incomplete client captures and provider request bodies as explicit failures (never as a pass or an absence proof), and only a blank-line-dispatched SSE frame satisfies the terminal assertion. The translated top-level tool-schema type is asserted. Task 2 verified Linux runtime and Windows vet; Windows runtime remains a task-7/platform gate.
- Task 3: the controller authorized one small `taskrunner` prerequisite outside the task-3 directory list: opt-in `Request.KillGroupOnExit` (default unchanged) kills the child's process group after `cmd.Wait`, so an inner `go test -timeout` cannot leave a real proxy alive; the product-smoke run opts in. A real attributed timeout event makes every required journey without a terminal `UNFINISHED` and names the timeout. Reused evidence directories clear the previous summary at startup. Review remediation: capture mode now lets os/exec own its pipes, so the opt-in group kill observes a leader that exits normally while a descendant holds both pipes; Linux group confirmation ignores killed-but-unreaped zombies instead of reporting them as survivors.
- Task 4: the owner approved a collect-only SystemOne witness because the shipped frontend hardcodes non-streaming decisions; `Required` intentionally has no `systemone/stream`. The journeys cross three upstream wire families (OpenAI legacy chat, Anthropic Messages, System One decisions), with Gemini→Anthropic and Responses/OpenResponses→OpenAI legacy as cross-protocol pairings.
- Task 4 exposed a pre-existing product defect outside this test-only spec: on a clean upstream EOF without a finish marker, the OpenAI legacy and Anthropic adapters synthesize a successful client terminal with absent/zero usage and no degradation log (`internal/plugins/backends/openailegacy/map_events.go`, `internal/plugins/backends/protocols/anthropicmessages/map_events.go`). This violates the no-hidden-degradation vision item and requirement 3.2's honesty; it is unchanged on `main` and needs its own fix PR from `main` with an explicit completion-vs-incomplete policy, observability and a regression witness. It is not quarantined because no required successful journey fails solely because of it.
- Task 4 inherited six tagged-lint advisories in the smoke package (three modernize, three thelper) from task 2. The mandatory lint selection (which disables those categories) is clean; resolve or disposition them before claiming whole-branch tagged lint is clean.
- Task 5: the cancellation helper retains a real partial `clientWire` (status, headers, bytes read before the intentional cancel, HTTP/read error) and passes it to every assertion context; it never drains the stalled stream and marks the capture partial so it cannot prove credential absence. The pre-output fallback journey asserts the fallback's translated request, not only request counts. The second-post-output-attempt probe is a synthetic direct fallback request that proves the zero-count assertion is live, not a proxy-origin retry mutation. Windows runtime remains task 7's gate. Review remediation: the Make supervisor enforces the 600-second wall clock including `go run`; devcheck derives its work deadline from that absolute value, fingerprints HEAD/index/working bytes before and after the run, centralizes infrastructure failures into a failed summary, and rejects a terminal PASS without a run event or contradictory terminals. Second review remediation: the POSIX supervisor is a session supervisor that reaches taskrunner's separate process groups; both supervisors invalidate a reused summary before compilation; in-repository evidence and `LIP_DEV_TMPDIR` are rejected before creation; and a malformed quarantine file retains the platform-filtered baseline journey list. Third review remediation: session identity is mandatory and the delayed-bootstrap path fails closed after killing the discovered tree; evidence paths are canonicalized through the deepest existing ancestor; supervisors do not delete a summary in an invalid in-repository destination; and Windows summary invalidation fails closed on errors other than not-found.
- Task 6: the valid reload atomically replaces the fixed config (sibling temp + rename), changes only the backend `base_url`, publishes generation 2 and routes new requests to provider B; the invalid reload (`logging.level: verbose`) is rejected as 422/`invalid` and generation 1/provider A keep serving. The POSIX-only shutdown journey sends SIGTERM, asserts exit 0 and proves port release by both a failed dial and a successful rebind; ordinary cleanup still accepts a successful forced kill. `Required` has 21 IDs (20 on Windows; shutdown is `POSIXOnly`). The owner accepted Linux runtime plus Windows compile/vet for this task; Windows runtime remains unmeasured and task 7 must measure it or report it unavailable.
- Task 7: `docs/testing/product-smoke.md` and the `ci-stand-in` adoption in `docs/development-iteration.md` are implemented. Linux measurements: 82.173 s cold (fresh `GOCACHE`) and 10.683/10.611/10.460 s warm, all well under 600 s. The owner explicitly waived task 7's Windows cold/warm measurements; the docs and PR-ready notes record them as unavailable rather than fabricating timings. The delivery PR body records the timings and the waiver; task 7 stays unchecked in the PR because its done-when is the merge, and the post-merge archive step flips it and archives the spec.
