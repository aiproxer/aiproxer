# Requirements Document

## Introduction

Green unit, contract and in-process composition tests do not show that the shipped `lipstd` binary works when a client talks to it. Nothing routinely starts the real binary, sends real client traffic through it and checks what the client and the provider actually see.

This spec adds a small product smoke that does exactly that: build the stock distribution, start it as a separate process against fake loopback providers, and drive a handful of real client journeys through it. One command runs it in at most ten minutes.

The owner asked for verification tooling as the deliverable, so these requirements describe the smoke's behaviour. That exception to the "testing is never a requirement" rule in `.kiro/steering/testing.md` applies to this spec only. Other delivery budgets still apply, and so does the rule that a slice must merge on its own.

## V1 Slice

- **Delivers:** `make verify-product`, which builds `lipstd` once from the source under test, runs about a dozen sentinel journeys against it through every shipped frontend family, and reports one pass/fail line per journey.
- **First consumer:** the maintainer and agents verifying a change locally. While remote CI cannot run, it is also listed as `ci-stand-in` PR evidence for changes to frontends, backends, core or `cmd/lipstd`.
- **Safe degradation:** a journey that is skipped, missing, cached or unfinished at the deadline makes the run fail with its name. The smoke never reports a pass it did not observe. A journey that fails on `main` because of a product defect is quarantined with its issue and reported as quarantined, so it does not block unrelated work and is never shown as passing.
- **Cost:** the command finishes within 600 seconds wall time, build included. Target: under three minutes with a warm Go build cache.

## Deferred

- Journeys through installed executable connectors, including API-key rotation and quota cooling, and the descendant-process containment they need. Follow-up: TBD.
- The full Claude ACP journey with the pinned Node runtime and controlled gateway. It runs on demand or nightly, not on every change. Follow-up: TBD.
- Journeys for optional features (Secret Guard redaction, agent-loop guard, tool repair, steering, reasoning and cache features, transcripts, usage reports). Each feature adds its own journey using the recipe from this slice. Follow-up: TBD.
- SQLite restart/reopen durability journey and routing weights/racing/TTFT journeys. Follow-up: TBD.
- Responses WebSocket transport. Follow-up: TBD.
- A delivered-capability catalog that maps vision items to journeys, first as an advisory report. Follow-up: TBD.
- Selecting extra journeys from the changed paths of a branch. Follow-up: TBD.
- A remote CI job and any required-status change, once remote CI runs again (after 31 October 2026). Follow-up: TBD.
- Correcting existing contract-test traceability that links the post-output no-retry claim to a pre-output test (`internal/testkit/contract`). This is a separate small fix from `main`. Follow-up: TBD.
- Migrating the bash `feature-stub-smoke` scenarios into the Go harness. Follow-up: TBD.

## Boundary Context

- **In scope:** test-only code under a dedicated build tag, a `devcheck` task, a Make target, one bounded-wait fix in `tools/taskrunner`, and docs.
- **Out of scope:** production code changes. A product defect found by a journey is fixed in its own PR from `main`. The pre-commit hook, the default `go test ./...` run and existing CI lanes are unchanged.
- **Boundary ownership:** tests and tooling. Core, adapters, plugins and process owners are exercised, not modified.

## Requirements

### Requirement 1: The real distribution starts and serves

**Objective:** As the maintainer, I want the smoke to run the binary users run, so that a pass means the shipped product works rather than a test composition.

#### Acceptance Criteria

1. When the product smoke runs, it shall build the stock `lipstd` binary once from the source under test and start each proxy under test as a separate process with a configuration file in the documented format, using only loopback fake providers and no paid-provider credentials.
2. The product smoke shall treat a proxy as ready only after a real inference request through it succeeds, not when its port accepts connections.
3. If the configuration file is invalid, then `lipstd` shall exit non-zero with an error naming the invalid setting, and the product smoke shall assert that outcome.

### Requirement 2: Every shipped frontend serves a real client journey

**Objective:** As a proxy user, I want each client protocol checked end to end, so that a broken frontend or translation cannot pass unnoticed.

#### Acceptance Criteria

1. For each shipped frontend family (SystemOne, OpenAI Responses, OpenAI legacy chat, Anthropic, Gemini and OpenResponses), the product smoke shall send one streaming and one non-streaming request through the running proxy and assert the scripted content, the usage values and a protocol-legal terminal event or response on the client wire.
2. The product smoke shall route the frontend journeys to at least two different upstream wire families, so that at least one journey translates between different protocols.
3. When a journey completes, the fake provider shall assert the translated request it received (model, messages and tools), not only answer it.
4. When a journey carries a tool call and the client's tool result into a second turn, the provider shall observe second-turn history equal to the messages exchanged in the first turn, in order, with nothing added or reordered.
5. If a journey assertion fails, then the product smoke shall report the journey name, the client wire transcript, the provider request log and the tail of the proxy log, with configured credentials redacted.

### Requirement 3: Failures stay honest and recoverable

**Objective:** As a proxy user, I want failure paths checked through the real process, so that fallback and no-retry rules hold in the product rather than only in unit tests.

#### Acceptance Criteria

1. When the first route candidate fails before any output, the proxy shall serve the request from the configured fallback candidate, and the client shall receive a successful response.
2. When a provider fails after output has reached the client, the proxy shall end the client response with a protocol-legal error, and the fallback provider shall receive no request.
3. When the client cancels a streaming request, the fake provider shall observe its upstream request cancelled within a bounded time.
4. After each failure journey, a new request through the same proxy process shall succeed.

### Requirement 4: Lifecycle and credential safety

**Objective:** As an operator, I want reload, shutdown and credential handling checked on the running binary, so that operating the proxy does not break sessions or leak secrets.

#### Acceptance Criteria

1. When a valid configuration change is reloaded through the management API, later requests shall use the new configuration. When an invalid change is reloaded, the reload shall be rejected and the last good configuration shall keep serving.
2. Where the platform supports shutdown signals, when `lipstd` receives one it shall exit successfully within a bounded time and release its listening port.
3. The canary credential configured for a fake provider shall reach that provider and shall never appear in a client response body or header, or in the proxy log.

### Requirement 5: One bounded, truthful command

**Objective:** As the maintainer, I want one command with a hard time limit and an honest verdict, so that it fits my ten-minute iteration budget and its pass can be trusted.

#### Acceptance Criteria

1. When `make verify-product` is invoked on a development host, it shall build, run every required journey, stop its processes and print its verdict within 600 seconds of wall time. If the deadline expires, it shall fail and name every unfinished journey.
2. If a required journey is skipped, missing from the run or served from the Go test cache, or if no test runs at all, then the product smoke shall fail and name the cause.
3. Where a required journey is listed in the existing test quarantine with its tracking issue, the product smoke shall report it as quarantined, never as passed.
4. When a run finishes, times out or fails, the product smoke shall stop every process it started, and it shall report any process it could not stop as a failure.
5. The product smoke shall run on Linux and Windows development hosts. Only journeys that need POSIX signals shall be platform-limited, and the required-journey list shall declare which those are.
6. The product smoke shall run only when it is invoked explicitly. The pre-commit hook and the default unit test run shall not run it.
