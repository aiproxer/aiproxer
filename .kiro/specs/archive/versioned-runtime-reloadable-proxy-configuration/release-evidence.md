# Versioned Runtime Reload — Release Evidence

**Recorded:** 2026-07-22T16:43:18Z

**Reviewed head:** `9b5e879799d73223dcec46481c0d695b37566f37`

**Platform:** Linux 6.17.0-1018-oracle x86_64

**Go:** go1.26.5 linux/amd64

**Reviewer:** Hermes (independent final validation after iterative Cursor/Bugbot review and remediation)

## Acceptance result

All mandatory local release gates pass. The implementation satisfies the approved requirements and design boundaries for explicit transactional runtime reload, immutable request-plane generations, stable-listener publication, generation pinning, rollback, bounded retirement, management/signal administration, and public facade compatibility.

## Exact gate evidence

| Area | Command | Result |
|---|---|---|
| Default quality + unit + tagged conformance | `make test` | PASS |
| Full race detector | `make test-race` | PASS — `Race detector scan passed.` |
| Fuzz smoke | `make test-fuzz` (`FUZZTIME=500ms`) | PASS, including reload source, effective canonicalization, diff, management decode, and generation lifecycle targets |
| Benchmarks | `make bench` | PASS |
| Bounded reload soak | `go test -tags=precommit -run '^TestRuntimeConfigReloadSoak$' -count=1 -v ./internal/stdhttp/...` | PASS |
| Module integrity | `go mod verify` | PASS — all modules verified |
| Full repository lint | `golangci-lint run --timeout=10m` | PASS — 0 issues |
| Feature-delta lint | `golangci-lint run --timeout=10m --new-from-rev=95bf54a4` | PASS — 0 issues |
| Vulnerabilities | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` | PASS — 0 reachable vulnerabilities; 0 imported-package vulnerabilities |
| No-drop/failover/parallel focus | `go test -race ./internal/stdhttp/... ./internal/core/runtime/... -run 'RuntimeConfigReload.*NoDrop|HTTP2|SSE|Failover|Parallel' -timeout=10m` | PASS |
| Last-good/restart-required focus | `go test -race ./internal/core/configreload/... ./internal/infra/runtimehost/... ./internal/infra/runtimebundle/... ./internal/stdhttp/... -run 'LastGood|Rollback|RestartRequired|AtomicRename|FaultMatrix' -timeout=10m` | PASS |
| Dynamic component focus | `go test -race ./internal/infra/runtimebundle/... ./internal/plugins/... ./internal/pluginreg/... ./internal/stdhttp/... -run 'Reload.*Dynamic|Generic|Discovered|NoInstall|NoWatcher' -timeout=10m` | PASS |
| Docs/config focus | `go test ./cmd/lipstd/... ./internal/qa/... -run 'ConfigExample|Docs|Reload' -timeout=10m` | PASS |
| Architecture boundaries | `go test ./internal/archtest/... -run 'Reload|Ownership|Watcher|ProcessService|ExternalModule' -timeout=10m` | PASS |
| Management shutdown timeout/retry | `go test -race ./internal/stdhttp/admin/configreload -run '^TestManagement_ServerShutdown(AppliesConfiguredTimeout|TimeoutCanBeRetried)$' -count=100 -timeout=10m` | PASS |
| Bound-model failover fixture | `go test -race -tags=precommit,integration ./internal/core/runtime -run '^TestBoundModel_RecvFailoverKeepsBoundCatalogAfterRefresh$' -count=5000 -timeout=10m` | PASS |
| Effective no-op source baseline | `go test -race ./internal/infra/runtimehost -run '^TestCoordinator_EffectiveNoopAdvancesActiveSource_RejectsInPlaceEdit$' -count=500 -timeout=10m` | PASS |
| Cursor/Bugbot review loop | two isolated rounds against `origin/main...HEAD`; Hermes independently reviewed and validated the accepted patch | PASS — round 1 repaired one IMPORTANT source-integrity defect; round 2 returned `TERMINATION: NITPICKS_ONLY` with no edits |
| Added-marker scan | added lines from `git diff --unified=0 95bf54a4 -- '*.go' '*.md' '*.yaml' '*.yml' '*.json'` scanned for TODO/FIXME/XXX/HACK/PLACEHOLDER/NOT IMPLEMENTED | PASS — no hits |

After PR review, management shutdown was bounded by the configured timeout. Independent cross-checking found and repaired the corresponding retry-state defect, then the full PR lint gate was cleared without suppressions. A load-sensitive failover fixture double-consume was exposed by the canonical race gate, repaired, stressed 5,000 times, and followed by a clean complete race rerun. A later Cursor/Bugbot round found that an accepted atomic replacement with an unchanged effective digest did not advance the raw source identity baseline, allowing a subsequent same-inode rewrite to evade the non-atomic-update rejection. The baseline now advances on an accepted effective no-op without publishing a generation; the deterministic real-file regression passed 500 times under race. A fresh follow-up Bugbot round found no further production defect and stopped at one safe lock-style nitpick.

## Soak parameters and assertions

`TestRuntimeConfigReloadSoak` is bounded and deterministic:

- 48 reload rounds;
- 4 concurrent data-plane workers;
- retained-generation budget of 2;
- one boot-generation SSE pin held throughout;
- 2-second coordinator timeout;
- mixed valid, invalid, no-op, restart-required, and retention-pressure triggers;
- asserts accepted traffic is not dropped, no unexpected status is returned, retained generations stay bounded, and goleak reports no owned leak.

## Reload-specific benchmark baseline

Fresh single-host results from `make bench` on AMD EPYC 9J14 (`GOMAXPROCS` selected 2 benchmark workers):

| Benchmark | Result | Memory |
|---|---:|---:|
| `BenchmarkManager_AcquireRelease-2` | 59.68 ns/op | 16 B/op, 1 alloc/op |
| `BenchmarkGenerationDispatcher_AcquireLease-2` | 1,074 ns/op | 784 B/op, 9 allocs/op |
| `BenchmarkManager_Publish-2` | 1,014 ns/op | 476 B/op, 4 allocs/op |
| `BenchmarkRetainedGenerationOverhead/n=1-2` | 38.92 ns/op | 0 B/op, 0 allocs/op |
| `BenchmarkRetainedGenerationOverhead/n=4-2` | 41.66 ns/op | 0 B/op, 0 allocs/op |
| `BenchmarkRetainedGenerationOverhead/n=16-2` | 53.12 ns/op | 0 B/op, 0 allocs/op |
| `BenchmarkRetainedGenerationOverhead/n=64-2` | 92.96 ns/op | 0 B/op, 0 allocs/op |
| `BenchmarkCandidateCompilation-2` | 9,085,490 ns/op | 4,591,539 B/op, 19,600 allocs/op |

These feature-specific benchmarks establish the initial comparison baseline. There is no equivalent pre-feature series, so this evidence does not claim statistical improvement from one run. Future changes must compare repeated samples on equivalent hardware and treat meaningful hot-path regressions as release blockers or document explicit acceptance.

## Platform coverage

- Linux/amd64 is the authoritative local execution platform for this record.
- Unix SIGHUP behavior and shutdown separation are covered by tagged command tests.
- Non-Unix API-only/build behavior is covered by platform-specific compile/contract tests in the full suites.
- HTTP/1.1 keep-alive, HTTP/2 multiplexing, SSE, cancellation, failover, parallel race, pre-output recovery, post-output no-retry, and management paths are covered by deterministic composed tests.

## Security and dependency review

- No watcher, polling, runtime plugin installation, or arbitrary source/path ingestion was added.
- `internal/core` driving-adapter import guards pass.
- Management source path remains startup-fixed; request bodies cannot supply YAML, paths, URLs, commands, or plugin instructions.
- Errors/status/telemetry remain bounded and secret-safe under corpus tests.
- The Go dependency delta is an indirect `golang.org/x/*` security-compatible update set centered on `x/text`; no new direct dependency was added.
- `govulncheck` reports no reachable vulnerability.

## Optional external gates not executed

The following are intentionally outside mandatory offline certification and were not used as release evidence:

- live Cursor SDK/API tests requiring `CURSOR_API_KEY`;
- live hosted-provider inference tests requiring external credentials and billable calls;
- external PostgreSQL/PgBouncer integration environments unrelated to this feature’s ownership/configuration changes;
- macOS and Windows native runtime execution (non-Unix behavior is compile/contract tested).

## Known limitations / explicit non-goals

- Editing or replacing the configuration file alone has no effect; an explicit SIGHUP or authenticated management API trigger is required.
- There is no file watcher, polling, debounce loop, automatic retry, plugin discovery rescan, download, or runtime installation.
- Runtime source reload depends on supported atomic-replacement identity semantics; unsupported filesystems/platforms report reload unavailable while startup serving remains possible.
- Listener/server topology, process services, plugin discovery/trust, global observability, store topology, and other startup-only fields return restart-required.
- Management is startup-fixed and opt-in by address; multi-user or non-loopback use requires dedicated strong authentication.
- Long-lived pinned work is not killed to reclaim a generation; retained-budget pressure rejects later publication safely.
- Fuzz evidence here is a 500 ms per-target smoke. Extended fuzzing remains a nightly/manual responsibility.
- Benchmark values are a first baseline, not a cross-host or statistically repeated regression comparison.
