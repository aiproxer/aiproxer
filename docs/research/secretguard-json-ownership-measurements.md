# SecretGuard JSON ownership measurements (#818)

## Change and ownership boundary

`walkLogicalFragments` borrows admitted `json.RawMessage` bytes for the duration
of a synchronous scan instead of cloning the buffer at admission. The built-in
exact matcher, positional mapper and BetterLeaks source read those bytes;
request-local `rawCache` updates a fragment descriptor, not the canonical buffer.
Private occurrence values retain their existing owned copies and cleanup.

Redaction still starts from `aipapi.CloneCall`. Replacement callbacks install
new owned bytes; they do not overwrite the admitted buffer. Item JSON text needs
one owned string-to-byte conversion; its redundant outer clone is removed. No
unsafe conversion, pool, SDK contract, response hook or scanner change is added.

## Complete-guard workload

`BenchmarkGuardEvaluateJSON` measures complete `Guard.Evaluate`, including the
working clone for redact, with exact-only, BetterLeaks-only and hybrid detection;
block/log/redact; no-hit/positive JSON; and 1 KiB, 100 KiB, 1 MiB and 2 MiB inputs.
Two concurrent near-2 MiB hybrid log cases supplement the serial matrix.

Both versions exercised 74 cases with six samples each, using `-cpu=2`,
`-benchtime=100ms` and `-benchmem`. Every sample checks the decision and preserves
an independent snapshot of the original input. The measured authority is legacy
`json.RawMessage`; native item text allocation is not separately certified.

Representative 2 MiB medians (bytes allocated per complete Evaluate):

| Detector / input / action | Before B/op | After B/op |
| --- | ---: | ---: |
| exact / no-hit / block | 12,582,060 | 10,484,904 |
| exact / no-hit / log | 12,582,122 | 10,484,970 |
| exact / no-hit / redact | 20,971,038 | 18,873,788 |
| hybrid / no-hit / block | 14,695,304 | 12,601,436 |
| hybrid / no-hit / log | 14,695,304 | 12,601,372 |
| hybrid / no-hit / redact | 25,188,146 | 23,097,688 |
| hybrid / positive / redact | 35,891,296 | 33,793,744 |
| concurrent hybrid / no-hit / log | 14,695,201 | 12,593,032 |

All 74 median byte-allocation comparisons improved in the initial matrix, by
approximately one admitted buffer per request. Runtime/scanner initialization
noise affects small differences in allocation counts; this is not a new ratchet.

## Timing qualification and interleaved control

Initial sequential samples showed apparent no-hit slowdowns. To investigate,
an isolated checkout of the same baseline received the identical benchmark
harness (verified byte-for-byte). Three alternating baseline/candidate rounds,
two samples per round, `-benchtime=3x -cpu=2`, covered 16 cases: exact/hybrid,
no-hit/positive, 1 KiB/2 MiB, block/redact.

For the eight 2 MiB cases, candidate/baseline median time ratios ranged from
0.937 to 1.118. Every case's paired ratios crossed 1; the earlier broad sequential
slowdown did not reproduce consistently. The 1 KiB paired ratios were especially
noisy (roughly 0.48 to 1.82), so they do not support a precise latency claim.

The decision to retain the change is based on allocation reduction and unchanged
behavior, not proven latency improvement. Neither latency equivalence nor a
request-latency SLO is certified on this shared host.

## Correctness evidence and limits

- Fresh SecretGuard/engine, SDK and extension-runner suites passed.
- A shared-buffer regression checks block/log immutability, successful redaction,
  matcher failure and a scan limit after admitted JSON. Parallel subtests share
  one frozen BetterLeaks generation and the same positive JSON backing bytes, so
  remote race tests exercise concurrent hybrid scans of the borrowed buffer.
- Replacing the working clone with a shallow call copy makes the scan-limit
  regression fail; the clone was restored without a production diff.
- Existing cancellation, cap, unsupported-token, overlap, multipart, JSON-mirror
  and atomic-publication controls remain in force.
- Concurrent benchmark requests share immutable fixture bytes and generation
  services; remote CI must supply race certification before merge.

Raw samples are retained in the task scratch files `secretguard-borrow-before.txt`,
`secretguard-borrow-after.txt`, and `secretguard-borrow-paired-{before,after}-{1,2,3}.txt`.
Tested bound: 2 MiB, not the configurable 64 MiB maximum. Whole-system capacity
and soak certification remain #394; no capacity or production readiness claim is made.
