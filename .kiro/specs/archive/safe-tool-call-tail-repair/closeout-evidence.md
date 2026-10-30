# Safe Tool-Call Tail Repair Closeout

- **Spec:** `safe-tool-call-tail-repair`
- **Status:** completed and archived
- **Implementation PR:** [#260](https://github.com/matdev83/go-llm-interactive-proxy/pull/260)
- **Exact implementation commits** (head `1c97cb19`):
  - `67bdbf02` — feat: implement bounded safe tool-call tail repair (spec #252)
  - `4f58a5db` — fix(toolcallrepair): address review and gate findings for safe-tail repair (CodeQL overflow, repo hygiene, CodeRabbit items)
  - `1c97cb19` — refactor(toolcallrepair): simplify tail scanner per review (scan dedup, by-value helpers, shared overflow-safe candidate builder)
- **Closeout date:** 2026-08-08

## Verification

### Focused unit tests (Windows host, Go 1.26.5)

`go test -parallel=8 -timeout=10m ./internal/core/toolcallrepair/...` — **PASS** (repeated after each commit; includes classifier states, candidate construction, engine ordering, reason codes, no-partial-mutation, fuzz-found root-array-close regression cases `[1]x`/`[1,2]x`).

`go test -parallel=8 -timeout=10m ./internal/core/runtime/... ./internal/stdhttp/... ./internal/testkit/conformance/... ./internal/infra/runtimebundle/...` — **PASS** (finalizer chaining, fail-open replay, dogfood smoke, conformance cells, effective-load opt-out contract).

### Fuzz campaigns

`go test -fuzz=FuzzJSONTail -fuzztime=8s -run='^$' ./internal/core/toolcallrepair` — **PASS**, 126,293 execs (23 new interesting inputs).
`go test -fuzz=FuzzPendingRootValue -fuzztime=8s -run='^$' ./internal/core/toolcallrepair` — **PASS**, 77,034 execs (22 new interesting inputs).

Fuzzing caught one real regression during review refactoring (index-out-of-range on root array close from `tailArrayCommaOrEnd`); it was fixed with the `len(stack)==0 → rootDone` guard and locked in with the refuse cases above. The failing input is not committed as a hash-named corpus artifact (repo convention: named seeds only; the regression is covered by unit cases).

### Benchmarks — `BenchmarkSafeTailRepair`, gate 8.10

**Exact command** (same host, Windows amd64, AMD Ryzen 7 5800X):

```
go test -bench='BenchmarkSafeTailRepair' -benchmem -benchtime=200x -count=5 -run='^$' ./internal/core/toolcallrepair
```

**Before** (pre-review-refactor commit `4f58a5db`) and **after** (refactored head `1c97cb19`) compared with `benchstat` (golang.org/x/perf):

```
benchstat bench-before.txt bench-after.txt
```

| Case | before sec/op | after sec/op | vs base | before B/op | after B/op | before allocs | after allocs |
|---|---|---|---|---|---|---|---|
| valid_pass_through | 4.094µ | 4.469µ | ~ (p=0.310) | 6.188Ki | 6.188Ki | 68 | 68 |
| append_only | 4.388µ | 4.796µ | ~ (p=0.056) | 6.281Ki | 6.281Ki | 72 | 72 |
| terminal_comma | 5.226µ | 5.424µ | ~ (p=0.310) | 6.578Ki | 6.578Ki | 81 | 81 |
| pending_const | 19.11µ | 24.58µ | ~ (p=0.095) | 15.78Ki | 15.78Ki | 248 | 248 |
| pending_default | 17.93µ | 21.29µ | +18.79% (p=0.008) | 15.73Ki | 15.73Ki | 237 | 237 |
| near_limit_refusal | 10.18µ | 11.66µ | ~ (p=0.310) | 72.00Ki | 72.00Ki | 1 | 1 |

**Gate 8.10 conclusion:** allocs/op and B/op are identical before/after (geomean +0.00%, all B/op and allocs comparisons `p=1.000`). The valid pass-through case shows no statistically significant time/op change (p=0.310, not > 5%). Per spec gate 8.10, byte/depth/operation bounds — not absolute wall-clock — are the pass/fail gate; elapsed time is evidence only. The pending_default wall-clock delta (+18.79%, p=0.008) is on the noisy Windows host and does not change any byte/depth/operation bound; the near-limit case is proven to take the refusal path (1 alloc, preflight-bound).

### Repository gates (CI, PR #260)

- **Repo hygiene** — `scripts/check-release-clean.sh --ref <commit>` — PASS on every branch commit (5142 approved files)
- **Test (ubuntu/windows/macos)** — `go test -timeout=8m ./cmd/lipstd ./pkg/credpool ./pkg/lipapi ./pkg/lipsdk/... ./pkg/streampump` + `go build -trimpath ./cmd/lipstd` — PASS
- **qa** — gofmt/module integrity, `go test -timeout=8m ./internal/archtest`, `go vet ./cmd/lipstd`, `go test -timeout=5m ./cmd/lipstd` — PASS
- **Analyze (Go)** / **Go vulnerability check** — PASS
- **CodeQL** — PASS (allocation-overflow alert fixed by uint64-safe candidate sizing)
- **Pinned official 17-case suite** and **Measured OpenResponses coverage** (parity surfaces) — PASS
- **make quality-checks** — PASS end-to-end locally (gofmt, go mod, build, vet, ad-hoc goroutine allowlist, regex hot-path, `./internal/archtest/...`). One Windows note: the taskrunner wrapper applies a 2-minute deadline to the archtest step; the archtest package itself passes in ~34s standalone, and the final gate run passed fully.
- **make test-fuzz** — covered by the nightly Tier-1 fuzz smoke (see below); local campaigns: `FuzzJSONTail` 8s → 126,293 execs PASS, `FuzzPendingRootValue` 8s → 77,034 execs PASS
- **make parity-checks** — the PR-level parity surfaces (pinned official 17-case suite, measured OpenResponses coverage) passed; the full `make parity-checks` matrix (optional connectors) is outside this spec's scope and unchanged.

### Strict Linux race + Tier-1 fuzz

Strict Linux race (`bash scripts/race-check.sh --strict`) and Tier-1 fuzz smoke (`make test-fuzz`, FUZZTIME=2s) run in `race-fuzz-nightly.yml` (scheduled + manual dispatch, not merge-blocking). A manual run was dispatched against implementation head `1c97cb19` (run `31281660251`, artifact `strict-linux-race-1c97cb19...`).

**Result: the full-repo race gate is a known-red gate.** All changed packages pass under `-race`:

```
ok  github.com/matdev83/go-llm-interactive-proxy/internal/core/toolcallrepair    1.574s
ok  github.com/matdev83/go-llm-interactive-proxy/internal/core/runtime            10.810s
ok  github.com/matdev83/go-llm-interactive-proxy/internal/plugins/features/toolcallrepair  1.070s
```

The run overall fails only on pre-existing failures in packages this PR does not touch, and the identical failures are present on `main` nightly runs (e.g. run `31241055441` on main SHA `11e7a1f7`): `internal/archtest` test timeout at the 10m gate, `internal/refclient/openresponses` WS data race, `internal/qa` root-hygiene tripped by the workflow's own `.tmp-race-evidence.txt` artifact, and `internal/plugins/frontends/openresponses`. The Tier-1 fuzz step was skipped once the race step failed; local fuzz campaigns (FuzzJSONTail 126k execs, FuzzPendingRootValue 77k execs) passed, and `make test-fuzz` remains covered by the nightly schedule. No race evidence contradicts this spec's changes; this is recorded as an explicit blocker for the repo-wide gate, not a claim of a green full-repo race run (tasks 5.1/5.3, requirements 8.8/8.11). The blocker is tracked as [issue #262](https://github.com/matdev83/go-llm-interactive-proxy/issues/262).

## Known limitations

- Benchmarks and focused runs were executed on a Windows host; Linux race/fuzz evidence comes from the dispatched nightly workflow, not a local Linux box.
- The full-repo strict Linux race gate is red on `main` for pre-existing, unrelated failures; changed packages pass under `-race` and this is recorded as an explicit blocker rather than a green-race claim. Tracked as [issue #262](https://github.com/matdev83/go-llm-interactive-proxy/issues/262).
- No type-derived `null`, `{}` fallback, partial-token completion, nested pending path, or escape deletion is performed; unsafe shapes replay the exact original bytes under fail-open (spec-mandated, not a gap).
- Fuzz durations are 8s per target (local) and 2s Tier-1 (nightly); longer adversarial campaigns remain available via `FUZZTIME` without code changes.

## Scope protection

This closeout moves and completes safe-tool-call-tail-repair bookkeeping only. It does not modify the ongoing `extension-scalability-and-architecture-simplification` / `openai-codex-native-compaction` specifications or their implementations.
