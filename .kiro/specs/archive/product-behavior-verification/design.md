# Design Document

## Overview

Add a small product smoke: ordinary Go tests behind a `productsmoke` build tag. Each test starts the real `lipstd` binary as a child process, points it at fake providers running inside the test process, and drives client traffic over loopback HTTP. A new `devcheck -task=product-smoke` builds the binary once, runs the tests under one 600-second deadline, and checks that every required journey actually passed. `make verify-product` is the entry point.

This extends the existing pattern of `scripts/feature-stub-smoke.sh` (spawned `lipstd`, scripted backend, wire assertions). It replaces substring line counts with typed Go assertions and adds a deadline and honest accounting.

### Goals

- Show that the shipped binary serves real client journeys through all six frontend families, fails honestly and recovers, reloads, shuts down and does not leak credentials.
- Give one bounded command whose pass is observed, not inferred.
- Make adding a journey cheap: one test function using shared helpers.

### Non-Goals

- No production code changes, no new test framework or scenario language, and no capability catalog in V1.
- No pre-commit or default-suite cost, no CI job or required-status changes in V1, and no executable-connector or descendant-containment work.

## Boundary Commitments

- **Owns:** the `productsmoke` test package, the `product-smoke` devcheck task, the `verify-product` Make target, one bounded-wait fix in `tools/taskrunner`, and docs.
- **Preserves:** all production composition, adapters, plugins, process owners, the pre-commit hook and existing test lanes.
- **Dependency direction:** `tools/devcheck` imports only the portable required-journey list from the test package's non-test file. Test code talks to the product only through the built binary, its config file, its HTTP surfaces and signals. Nothing in `pkg/` or `internal/core` changes.
- **Project checks:** canonical model neutrality, streaming-first behaviour and post-output no-retry are exercised rather than changed. No provider SDK enters core.

## Components

| Change | Path | Responsibility |
| --- | --- | --- |
| New | `internal/testkit/productsmoke/required.go` | Portable `Required` list of journey test IDs, with a POSIX-only flag where needed. No build tag. |
| New | `internal/testkit/productsmoke/sut_test.go` | Proxy-under-test handle: render config, start, readiness, log capture, stop, leak check. |
| New | `internal/testkit/productsmoke/provider_test.go` | Scripted fake providers per upstream wire family: request recording, scripted streams, fault injection, cancellation observation. |
| New | `internal/testkit/productsmoke/{startup,protocols,continuity,failure,lifecycle}_test.go`, `shutdown_unix_test.go` | Journeys. |
| New | `tools/devcheck/productsmoke.go` (+ test) | Build, run, deadline, required-journey reconciliation, summary and evidence. |
| Modify | `tools/devcheck/main.go` | Dispatch `-task=product-smoke`. |
| Modify | `tools/taskrunner/runner.go` (+ test) | Bounded wait after kill (`exec.Cmd.WaitDelay`). |
| Modify | `Makefile` | `verify-product` target. |
| New / modify | `docs/testing/product-smoke.md`, `docs/development-iteration.md` | Command, verdicts and the recipe for adding a journey. List the smoke as `ci-stand-in` evidence. |

All `_test.go` files in the package carry `//go:build productsmoke`. Default, `integration` and `precommit` runs never compile them.

## Proxy Under Test

`devcheck` builds `./cmd/lipstd` once into the run's scratch directory and passes its path in `LIP_PRODUCT_SMOKE_BIN`. If the variable is missing, the tests fail; they never skip and never rebuild. Each journey that needs isolated state starts its own proxy, which is cheap: startup is a single process with linked backends.

`start(t, cfg)` does the following:

- Allocates a loopback port.
- Writes the config into `t.TempDir()`, using the same YAML shape as `testdata/feature-stub-smoke` with the `custom-*-compatible` backend kinds pointed at the fake providers.
- Starts the binary with `os/exec`, with `WaitDelay` set and stdout/stderr drained into a bounded buffer that is also written to the journey's evidence file.

Readiness means one real inference request succeeds, polled until the test context's deadline. An open port is not enough.

`t.Cleanup` stops the proxy:

- **POSIX:** send the documented shutdown signal, wait up to a bound, then kill.
- **Windows:** kill.

Only the shutdown journey asserts a graceful exit. If the process is still alive after the kill bound, the journey fails. V1 configurations use linked backends only, so the proxy starts no children. Descendant containment is deferred along with the executable connector journeys.

## Fake Providers

The fake providers are `httptest.Server` handlers in the test process. They are independent of the proxy's encoders: they parse requests with plain `encoding/json`, check the expected fields, and stream scripted wire bytes (SSE or JSON). Each provider records every request, observes cancellation through `r.Context()`, and supports three faults: fail with an HTTP error before any body, close the connection after N stream events, and stall until cancelled.

Providers cover the upstream wire families needed for R2.2: OpenAI legacy chat plus at least one other, e.g. Anthropic or OpenAI Responses. Every provider expects a canary API key and records whether it arrived.

Client-side checks reuse existing testkit decoders and event assertions (`internal/testkit` HTTP/SSE helpers, `events_assert.go`) where they fit. Otherwise they parse the wire directly. They assert a terminal was observed before comparing its value. No new client library is added.

## Journeys

The required list starts with these journeys (subtest names are fixed in `required.go`):

| Journey | Witness |
| --- | --- |
| `TestProduct_Startup/ready` and `/invalid_config` | Real request succeeds; invalid config exits non-zero with the named setting. |
| `TestProduct_Protocols/<frontend>/{stream,collect}` × 6 | Content, usage, legal terminal on the client; provider saw the translated model, messages and tools. At least one pairing crosses protocols. |
| `TestProduct_Continuity/tool_round_trip` | Turn 2 history at the provider equals turn 1's exchange in order. |
| `TestProduct_Failure/{pre_output_fallback,post_output_no_retry,client_cancel}` | Fallback serves; post-output error is legal and the fallback provider stays untouched; cancel reaches the provider; a new request then succeeds. |
| `TestProduct_Lifecycle/{reload_valid,reload_invalid}` | Management API reload (`LIP_RELOAD_MANAGEMENT_ADDRESS`) changes routing; an invalid reload is rejected and the last good config keeps serving. |
| `TestProduct_Lifecycle/canary_credential` | Canary reaches the provider; it is absent from client bodies and headers and from the proxy log. |
| `TestProduct_Shutdown/graceful` (POSIX) | Signal leads to exit 0 within the bound, and the port is free. |

Journeys use `t.Parallel()` with isolated proxies, ports and temp dirs. Journey assertions stay in the journey; shared mechanics stay in `sut_test.go` and `provider_test.go`.

## Command, Deadline and Verdict

`make verify-product` runs `go run ./tools/devcheck -task=product-smoke [-evidence=<dir>]`. The default evidence directory is under the dev temp root, outside the repository. The Make recipe starts an OS-level supervisor: on POSIX `scripts/verify-product.sh` starts the command in a new session and kills that whole session on timeout, which reaches the separate process groups `taskrunner` creates; on Windows `scripts/verify-product.ps1` uses `WaitForExit(600000)` plus `taskkill /T`. The POSIX session-identity handshake is mandatory: if the supervisor cannot acquire the identity within its bounded bootstrap while the command is running, it kills the discovered command tree and fails rather than supervising without ownership. Both supervisors invalidate a reused `summary.json` before starting, so a compile-time timeout cannot leave a previous PASS; they do not delete a summary in an invalid in-repository destination, and the Windows deletion fails closed on errors other than not-found. The supervisor passes the absolute command deadline in `LIP_PRODUCT_SMOKE_DEADLINE_UNIX`. Devcheck derives a 570-second work deadline from that value, so a slow `go run` compilation consumes the work budget instead of escaping the 600-second command bound. The 30-second difference is the reporting reserve before the supervisor's hard stop. If the controller starts after its work deadline, it writes a failed summary naming every required journey unfinished. A direct invocation without the environment variable falls back to a 570-second work budget and is not externally bounded. Devcheck then:

1. **Builds** `lipstd` through `taskrunner` with the remaining budget and records the binary hash.
2. **Runs** `go test -json -count=1 -tags=productsmoke ./internal/testkit/productsmoke` through `taskrunner`, passing `-timeout` derived from the remaining budget minus a 30-second reserve for reconciliation and reporting.
3. **Reconciles** the observed test events against `Required`, filtered to the host OS:
   - a required test that was not seen, or that is skipped → **FAIL**;
   - a cached result (not possible with `-count=1`, checked anyway) → **FAIL**;
   - an empty run → **FAIL**;
   - a required journey that the existing test quarantine (`.github/test-quarantine.txt`, existing parser) lists with an issue → **QUARANTINED**, shown as not passing;
   - any failure → **FAIL**, naming the journey and its evidence file.

   On deadline expiry, every required journey without a terminal event is named as unfinished.
4. **Reports** one line per journey with its status and elapsed time, the total wall time, the head commit, the source fingerprint and a dirty-tree flag. It writes the same data as a JSON summary to the evidence directory. The exit code is non-zero unless every non-quarantined required journey passed.
5. **Binds the verdict to one source snapshot:** it fingerprints HEAD, the index and the working bytes before the build and verifies the same fingerprint after the test run. A change between snapshots fails the run with "source changed during the product smoke; rerun against stable inputs"; a HEAD plus dirty flag cannot identify which dirty bytes were tested. An evidence directory inside the repository is rejected before creation, and `LIP_DEV_TMPDIR` gets the same validation, because the run's own artifacts would otherwise enter the fingerprint. The platform-filtered required list is built before quarantine is parsed, so a malformed quarantine file still produces one journey entry per requirement.

V1 has no run IDs or crash-recovery records. The verdict is printed by the process that observed it. If the controller starts late it can still write a failed per-journey summary; if it never starts, the outer session supervisor kills the command at 600 seconds and no per-journey summary exists.

`taskrunner` previously drained caller-owned capture pipes before `cmd.Wait`, so a leader that exited successfully while a descendant held the pipes never reached Wait and consumed the whole timeout. `os/exec` now owns the capture pipes too, so Wait observes the leader immediately and `cmd.WaitDelay` bounds a leaked pipe. The result is `CleanupFailure` when the leak required the delay; existing callers keep their classification and no longer hang. Process-group confirmation checks for live members on Linux, so a killed but unreaped zombie is not reported as a survivor.

## Adoption

- `docs/testing/product-smoke.md` covers the command, the meaning of each verdict, and a short recipe for adding a journey: add a test, add its ID to `Required`, script a provider.
- `docs/development-iteration.md` lists `make verify-product` among the `ci-stand-in` PR evidence for changes to `cmd/lipstd`, `internal/core`, frontends, backends or the smoke itself.
- No hook, CI workflow or branch-protection change is made in V1.
- A journey that exposes a defect on `main` is fixed from `main`. If that cannot happen first, it is quarantined with its issue, and the quarantine entry is removed by the fixing PR.

## Requirements Traceability

| IDs | Design elements |
| --- | --- |
| 1.1, 1.2, 1.3 | Proxy under test, build-once, readiness, Startup journeys. |
| 2.1, 2.2, 2.3 | Protocol journeys, fake providers, cross-protocol pairing. |
| 2.4 | Continuity journey. |
| 2.5 | Per-journey evidence files, devcheck summary, canary redaction. |
| 3.1–3.4 | Failure journeys and the follow-up request. |
| 4.1, 4.2, 4.3 | Lifecycle and Shutdown journeys, canary credential. |
| 5.1, 5.4 | Devcheck deadline, taskrunner `WaitDelay`, SUT stop and leak check. |
| 5.2, 5.3 | Reconciliation against `Required` and quarantine. |
| 5.5 | Portable harness; POSIX flag in `Required`. |
| 5.6 | Build tag; no hook or default-suite change. |

## Testing Strategy

- **Devcheck reconciliation:** table tests on recorded `go test -json` streams for a missing required test, a skipped test, an empty run, a quarantined test, a failure, a deadline with unfinished journeys, a terminal event without a run event and contradictory terminals. The controller also has tests for a late start (every required journey unfinished), a malformed absolute deadline (summary still written), a malformed quarantine file (baseline journey entries retained), a source change between snapshots, and an in-repository evidence directory or `LIP_DEV_TMPDIR` (rejected before creation).
- **taskrunner and the POSIX supervisor:** a test with a grandchild in a new process group that holds the pipe open. `Run` must return within the deadline plus the bound and report `CleanupFailure`. Capture mode has the same leaked-pipe test (leader exits zero while a descendant holds both pipes) and a Linux test that a killed-but-unreaped zombie is not reported as a surviving process. A shortened-deadline test runs the POSIX session supervisor against a hanging command that spawns an isolated-group descendant, and asserts exit 124, descendant cleanup and removal of a reused evidence summary. A delayed-bootstrap test injects a slow inner Bash with an isolated-group descendant and asserts the supervisor fails closed and kills it instead of proceeding without a session identity. Another test asserts a summary in an in-repository destination is not deleted by the supervisor.
- **Journeys:** each critical journey shows its assertion is live once, when it is written. For example, script the provider to drop the tool result, or to add a second fallback attempt, run the journey as its own process, and record in the PR body that it fails (see `testing.md`, Non-Vacuity). These probes are not committed to the suite.
- **Cost:** the final task measures `make verify-product` once with a cold build cache and three times warm, on the maintainer's Linux host and one Windows host, and records the times in the PR.
