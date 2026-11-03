# Product smoke

`make verify-product` builds the stock `aipstd` binary once from the source under
test, starts it as a separate process against fake providers on loopback, and
drives real client journeys through the shipped HTTP frontends. It prints one
verdict line per required journey, writes a JSON summary and per-journey
artifacts, and exits non-zero unless every non-quarantined required journey was
observed passing.

The smoke answers a different question from unit and contract tests: does the
binary users run still serve a client end to end? Tests in
`internal/testkit/productsmoke` carry the `productsmoke` build tag, so the
default suite, integration runs, hooks and CI never compile them. Nothing runs
the smoke implicitly; it is invoked only by name.

## What it proves

- `aipstd` builds from this tree and starts with a rendered configuration file.
  Readiness is one successful inference request through the proxy, not an open
  port.
- Each shipped frontend family serves one streaming and one non-streaming client
  request with the scripted content, the scripted usage, and a protocol-legal
  terminal event or collected response. SystemOne is collect-only: its shipped
  frontend delivers non-streaming decisions only.
- The fake provider asserts the translated request it actually received: native
  model id, path, messages in order, and tool declarations with their parameter
  schema. At least one journey crosses protocol families.
- An opt-in feature reaches the provider through the shipped binary: with
  `model-system-prompt` enabled, the configured instruction arrives as a
  standalone system message ahead of the client's own history, the client still
  receives the ordinary answer, and a non-matching model's request keeps only
  its own messages.
- A second turn's history at the provider equals the first turn's exchange, in
  order.
- Failure behavior holds in the product: fallback serves when the first
  candidate fails before any output; a post-output error ends the client
  response legally and the fallback provider receives nothing; a client
  cancellation reaches the provider; a later request through the same process
  succeeds.
- The management reload API accepts a valid candidate and keeps the last good
  configuration when a candidate is invalid.
- A canary credential reaches the provider and never appears in a client body or
  header, the configuration file, or the proxy log.
- On POSIX, the documented shutdown signal exits the process successfully within
  the bound and releases the listening port.

It does not prove live-provider compatibility, executable-connector journeys,
optional features beyond the `model-system-prompt` journey (Secret Guard,
agent-loop guard, tool repair, reasoning, transcripts, and so on),
SQLite/PostgreSQL parity, race safety, or another operating system. It
complements those gates; it does not replace them.

Run it for changes to `cmd/aipstd`, `internal/core`, frontends, backends, or the
smoke itself. Under the temporary remote-CI block it is listed as stand-in
PR evidence in [development iteration](../development-iteration.md#when-remote-ci-cannot-run).

## Command

```sh
make verify-product
make verify-product PRODUCT_SMOKE_EVIDENCE=/absolute/path/outside/the/repository
```

`PRODUCT_SMOKE_EVIDENCE` names the run's evidence directory. The default is a
fresh `product-smoke-*` directory under the dev temp root (`AIP_DEV_TMPDIR`,
default `~/.cache/aip-tmp`). An explicit path is created and used as given:
retention is then the caller's choice, and only the required journeys' own
subdirectories are cleared, so unrelated files in that directory survive. The
path must resolve outside the repository; an in-repository path is rejected
before it is created, because the source fingerprint includes untracked files
and the run's own artifacts would otherwise look like a source change.
`AIP_DEV_TMPDIR` gets the same validation.

The command always runs the whole required list; it accepts no package or
journey selection. It uses the repository's normal Go guard and build caches.

The exit code is the verdict. Zero means every non-quarantined required journey
passed and the run produced no problem. A non-zero exit prints the reason and
names each failing or unfinished journey, and still writes the summary. A run
killed before devcheck writes its summary has no verdict: V1 has no crash
recovery, and the previous summary is removed before a new run starts so an
older pass cannot be mistaken for this one. The outer supervisor must propagate
the controller's exit code unchanged: on Windows it reads the code from the
process handle it started, and if the code is unavailable after the child exits
it fails closed with exit 126 instead of reporting a pass.

## Time budget

The 600-second budget covers the whole `make verify-product` invocation,
including the `go run` compilation of `tools/devcheck`, as measured by wall
clock:

| Bound | Seconds | Covers |
| --- | ---: | --- |
| Command budget | 600 | Everything below, measured from the `make` recipe |
| Outer supervisor | 600 | POSIX `scripts/verify-product.sh` (`setsid` plus a session kill); Windows `scripts/verify-product.ps1` (`WaitForExit` plus `taskkill /T`) |
| Work deadline | 570 | The `aipstd` build plus the test run; derived from the absolute command deadline, so a slow `go run` compile consumes it |
| Reconciliation reserve | 30 | Reconciling observed events, writing the summary and printing the verdict before the supervisor's hard stop |
| Minimum test window | 30 | Refuse a test window too short to observe anything |

The Make target passes the absolute command deadline in
`AIP_PRODUCT_SMOKE_DEADLINE_UNIX` (recipe start plus 600 s) to devcheck. If the
`go run` compilation takes 40 seconds, devcheck has 530 seconds of work budget,
not a fresh 570. If the controller starts after its work deadline, it still
writes a failed summary naming every required journey `UNFINISHED`; the
supervisor is the last-resort stop. If the toolchain cannot start the controller
within 600 seconds, the supervisor kills the invocation at 600 seconds and the
command fails, but no per-journey summary can exist because the controller never
ran.

Devcheck builds `aipstd` under the remaining work deadline, then runs

```sh
go test -json -count=1 -tags=productsmoke ./internal/testkit/productsmoke
```

with `-timeout` set to the budget left after the build, minus the 30-second
reconciliation reserve. If that leaves less than the 30-second minimum test
window, the run reports every journey it could not start as `UNFINISHED` instead
of running a truncated test binary.

A run that exceeds 600 seconds is a cost defect. Reduce setup cost (the build,
the number of processes, the caches), never the required journeys. Both
supervisors include the compilation in the bound. The POSIX supervisor starts
the command in a new session and kills the whole session, so it also reaches the
separate process groups `taskrunner` creates; the Windows supervisor uses
`WaitForExit` and `taskkill /T`. Both invalidate an existing `summary.json`
before starting, so a timeout during compilation cannot leave a previous PASS in
a reused evidence directory. The POSIX supervisor requires `setsid`, `pkill`,
`pgrep`, `ps` and `realpath`.

The POSIX session identity handshake is mandatory. If the identity cannot be
acquired within the bounded bootstrap while the command is running, the
supervisor kills the discovered command tree and fails instead of supervising
without ownership; a command that exits before publishing its identity is
ordinary completion. A supervisor never deletes a summary in an invalid
in-repository destination; the controller reports the path error.

## Verdicts

Each required journey is printed as one line with its status, exact subtest ID,
elapsed seconds and detail, followed by the wall time, head commit, dirty-tree
flag and the overall verdict. The console prints statuses in upper case;
`summary.json` stores their lower-case form (`pass`, `fail`, `quarantined`,
`unfinished`).

| Status | Meaning |
| --- | --- |
| `PASS` | The required subtest reported its own `pass` terminal event in this run, and the Go test cache did not serve it. |
| `FAIL` | The journey failed, or its result cannot be trusted as an observation. This covers a reported `fail`, a reported `skip`, a journey that started but produced no terminal event without the run being cut, a required journey that was never observed, and an inconsistent event stream. The detail says which. |
| `QUARANTINED` | `.github/test-quarantine.txt` lists the journey's top-level test with an open tracking issue. It is reported as quarantined, never as passing, and does not block the run. A quarantined failure is still a real failure; the entry disappears with the fixing PR. |
| `UNFINISHED` | The devcheck deadline, the `go test -timeout`, or another cut ended the run before the journey recorded a terminal event. The journey is named, and the run fails. |

Specific non-passing observations and what they mean:

- **missing / not observed**: no `go test -json` event for the required subtest
  arrived. The run fails; this is not an empty pass. When a parent test failed
  without reaching the subtest, the detail names the nearest failed ancestor.
- **skipped**: the subtest reported `skip`. A required journey is never
  optional, so this fails. If a journey needs to be excused, quarantine it with
  an issue instead.
- **cached**: Go served a cached package result. `-count=1` should prevent this;
  if it happens, the journey is marked failed with "cached result; not observed
  execution" and the run records that only observed execution counts.
- **inconsistent event stream**: a test reported a terminal event without a
  preceding `run`, or reported contradictory terminals. Go does not produce
  that sequence for a fresh execution, so a `PASS` is not accepted from it and
  the run records "inconsistent go test event stream for <test>".
- **empty run**: no test events at all. The run fails with "the product-smoke
  package ran no tests".
- **build failure**: every journey is reported `FAIL` with "not run: aipstd did
  not build". If the deadline ended the build instead, they are `UNFINISHED`.
  Quarantined entries keep their `QUARANTINED` verdict in either case.
- **unexpected failure**: a failing test outside the required set is reported as
  a run-level problem unless it is the failed ancestor of a required journey.

Run-level problems are also printed into the summary `problems` list and the
failure reason: a deadline that ended the run, a fired `go test -timeout`, a
cached result, an empty run, an inconsistent event stream, a source change
during the run, or a non-required test failure. Any problem makes the overall
verdict `FAIL`.

A `PASS` is tied to one source snapshot. Devcheck fingerprints HEAD, the index
and the working bytes before the build and verifies the same fingerprint after
the test run. If the tree changed in between, the journeys may still show their
observed statuses, but the run-level verdict is `FAIL` with "source changed
during the product smoke; rerun against stable inputs". A HEAD plus a dirty flag
is not sufficient because it cannot identify which dirty bytes were tested.

## Evidence

The evidence directory holds:

```text
<evidence>/
  summary.json            machine-readable verdict, wall time, source and journeys
  build.log               go build output for aipstd
  go-test.json.log        every go test -json event
  go-test.stderr.log      test binary stderr
  aipstd                  the binary that was tested
  journeys/<slug>/        one directory per required journey
```

`journeys/<slug>` replaces `/`, `\`, space and `:` in the subtest ID with `_`.
Every started proxy journey writes `proxy.yaml` (the rendered configuration) and
`proxy.log` (the redacted proxy stdout and stderr). Journeys add their own
witnesses, for example `client-wire.txt`, `provider-requests.txt`,
`startup.txt`, `canary-check.txt`, `reload-valid.txt`,
`reload-invalid-client-wire.txt`, `continuity.txt` or `shutdown.txt`. A journey
that fails through the shared `failf` helper also writes `failure.txt`, which
collects the provider request log, the client wire transcript and the proxy log
tail with credential redaction. The console and `go-test.json.log` carry failure
messages, but the artifact directory is the durable record.

`summary.json` records:

- `schema_version`, `task`, `verdict` (`pass` or `fail`) and `reason`;
- `started_at`, `finished_at`, `wall_seconds`, `os`, `head`, `source_fingerprint`
  and `dirty`;
- `binary`, `binary_sha256`, `go_test_timeout`, `command` and `command_error`;
- `evidence_dir`, `problems`, and one `journeys` entry per required journey with
  `id`, `status`, `detail`, `elapsed_seconds`, `evidence` and, when applicable,
  `quarantine_issue`.

Credentials: a journey that configures a backend generates a fresh canary and
passes it to `aipstd` only through that backend's credential environment root; it
never appears in the configuration file. The harness replaces the canary with `[REDACTED:canary]` in
the proxy log, every evidence file and every failure message. Redaction happens
before an excerpt is bounded, so a credential straddling a cut cannot survive as
a fragment. The on-disk proxy log stops at 4 MiB with an explicit truncation
marker while the in-memory tail used for failure reports keeps later output. A
provider request body larger than the provider read cap is not retained at all,
because a partial body could preserve a credential fragment.

## Platforms

| Host | Required journeys | Notes |
| --- | ---: | --- |
| Linux | 22 | Includes `TestProduct_Shutdown/graceful` |
| Windows | 21 | Excludes the POSIX-only shutdown journey |

`internal/testkit/productsmoke/required.go` is the single source of truth. It
declares each required journey's exact subtest ID and marks the shutdown journey
`POSIXOnly: true`; `RequiredForOS` removes it on Windows, and the devcheck task
applies the same rule when it reconciles events. The journey's source file
(`shutdown_unix_test.go`) carries `//go:build productsmoke && unix`, so it does
not compile on Windows at all. Other journeys stop the proxy with a kill on
Windows and with the documented signal on POSIX; only the shutdown journey
asserts a graceful exit code and port release.

The Linux behavior above is measured on the maintainer's host. A Windows host
was not reachable for the task that added this smoke (no Wine or Windows
machine; remote CI is billing-blocked), so Windows runtime evidence remains an
open obligation rather than a claim. Do not infer Windows results from a Linux
run. The Windows Make branch uses PowerShell `Start-Process` and `taskkill /T`
to apply the same 600-second bound; that supervisor path has not been
runtime-measured on Windows.

## Adding a journey

Journeys are ordinary Go tests. Shared mechanics live in `sut_test.go` and
`provider_test.go`; assertions stay in the journey. Nothing here requires a
production change or a new framework.

1. **Write the test** in `internal/testkit/productsmoke/<area>_test.go` with
   `//go:build productsmoke`. It needs a `*testing.T`, calls `t.Parallel()`, and
   uses a fresh `newCanary(t)` so failures name a credential that cannot collide
   with another journey.
2. **Script the provider.** Pick the upstream wire family the journey needs:
   `newOpenAILegacyProvider`, `newAnthropicMessagesProvider` or
   `newSystemOneProvider`. The `fakeScript` sets the native model id, streamed
   deltas (whose join is the expected content), usage, an optional terminal
   `FinishReason` and an optional `ToolCall`. `SetFault` injects
   `faultHTTPErrorBeforeBody`, `faultAbortAfterEvents` (with the number of
   events after which to abort) or `faultStallUntilCancelled`.
3. **Start the proxy under test** with `startSUT(t, sutConfig{...})`. Give it the
   canary, the client frontends, one `backendWiring` per fake provider (kind,
   base URL, canonical/native model rows) and, when the journey needs the
   management API, `Management: true`. Omit `Canary` and the harness fails the
   journey rather than defaulting to a real credential.
4. **Send the client traffic** with `doJSON` (or `doRequest` for another
   method) and record the exchange in the returned `clientWire`. For a
   protocol journey choose a client frontend and a provider family that differ,
   so the journey proves translation rather than a pass-through.
5. **Assert both ends.** On the client: content, usage and a protocol-legal
   terminal, observed before it is compared. At the provider: the translated
   request (native model id, path, ordered messages, tools and their parameter
   schema), the expected request count when retries matter, and cancellation
   observation where the journey tests cancellation. If credentials are part of
   the journey, assert the canary reached the provider and is absent from the
   client and the proxy log. Report failures with
   `failf(t, failureContext{sut: h, provider: p, wire: w}, ...)` so the wire,
   provider log and proxy tail land in the evidence directory.
6. **Register the exact subtest ID** in `Required` in `required.go`, for example
   `TestProduct_Foo/bar`. Nested names use the same `/`-separated form Go
   reports in `go test -json`. Set `POSIXOnly: true` only when the journey needs
   POSIX signal or process semantics; the Windows required list then excludes
   it. Update the count and platform assertions in `required_test.go`.
7. **Prove the journey is not vacuous.** Run it once against a deliberate
   defect — omit the terminal event with `OmitTerminal: true`, drop the tool
   result from the second turn, let a second fallback attempt through, or echo
   the canary into a client body — and confirm it fails for the intended reason,
   not because the proxy did not start. Record that probe and its failure in the
   PR body; do not commit it. Committed journeys keep `OmitTerminal: false`.

Three journey shapes reuse the same helpers:

- A **protocol** journey pairs one client frontend with a different provider wire
  family and asserts both sides: the client wire and the provider's translated
  request.
- A **failure** journey gives the ordered route a faulty primary and a healthy
  fallback (`MaxAttempts` covers both), sets the fault with `SetFault`, and
  asserts the fallback's request count and a follow-up request through the same
  process. After a post-output error it asserts the fallback received nothing.
- A **lifecycle** journey sets `Management: true`, renders a candidate with
  `h.CandidateConfig`, writes it with `h.ReplaceConfig`, posts the management
  reload (`configReloadPath`) and reads `configStatusPath` to compare the active
  and last-good generations.
- A **feature** journey enables one opt-in feature row with
  `ModelSystemPrompt`, then asserts the client answer is unchanged while the
  provider received the feature's addition.

None of these changes production code: the harness drives the shipped binary
through its configuration, HTTP surfaces and signals.

Run one journey without devcheck while iterating:

```sh
go build -o /tmp/aipstd-product-smoke ./cmd/aipstd
AIP_PRODUCT_SMOKE_BIN=/tmp/aipstd-product-smoke \
AIP_PRODUCT_SMOKE_EVIDENCE=/absolute/path/outside/the/repository \
  go test -tags=productsmoke -count=1 ./internal/testkit/productsmoke \
  -run '^TestProduct_Foo$/^bar$'
```

Then run `make verify-product` for the whole list. `AIP_PRODUCT_SMOKE_BIN` is
required: a missing binary fails the journey rather than rebuilding or skipping.

The recipe stays inside the test harness. Journeys may not add provider SDKs to
core, branch core on a concrete provider, or change production composition to
make an assertion convenient. If a journey exposes a product defect that also
reproduces on `main`, fix it from `main` in its own PR. If that cannot happen
first, quarantine the top-level test in `.github/test-quarantine.txt` with its
open issue: devcheck passes the quarantined names to `go test -skip` and reports
their journeys as `QUARANTINED`, never as passing. The fixing PR removes the
entry.
