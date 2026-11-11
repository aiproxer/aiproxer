# Capacity Evidence Ledger

## Current status

No capacity measurements were added by the 2026-11-10 spec review. The previous file was an extensive template, not measured evidence. Its prior version remains in Git history.

Public load/soak work is tracked in [aiproxer/aiproxer#736](https://github.com/aiproxer/aiproxer/issues/736), with release acceptance under [#737](https://github.com/aiproxer/aiproxer/issues/737). Monetary spool/journal/admission profiles belong to [Enterprise #394](https://github.com/aiproxer/aiproxer-enterprise/issues/394). This file is a concise index; public results must be usable without private documents.

## One run brief, written before measurement

Record:

- Candidate: repository, source SHA, public export provenance if applicable, binary checksum/build flags.
- Profile: secret-free configuration reference/digest, enabled features, persistence, built-in/connector execution, both transport legs.
- Environment: CPU/RAM/OS/Go/runtime settings, socket/handle limits, and placement of generator, fake upstream, proxy, database, and connector children.
- Workload: scenario, target logical streams, ramp, prompt/output size, event rate, warm-up, steady duration, repetitions, injected faults, and cleanup/expiry window.
- Objectives: correctness expectations, expected rejections/cancellations, accepted capacity/latency/resource budgets with sources, and environment stop conditions. Use “not defined” for an unaccepted SLO; do not manufacture a certificate.
- Artifacts: exact command/config, raw measurements, selected profiles, and a durable accessible result location. Keep raw dumps outside the repository and never include real credentials or customer traffic.

A run-plan file used by the opt-in experiment is an implementation convenience. No versioned schema registry or automated gate platform is required.

## Result entry

Use one short entry per baseline or candidate experiment:

| Field | Record |
| --- | --- |
| Identity | Run ID/date; exact candidate and workload/config references |
| Achieved load | Attempted, sustained and peak streams; connections; duration; completions |
| Integrity/outcomes | Content/order/terminal checks; expected versus unexpected errors, rejections and cancellations |
| Timing | TTFT, event/completion distributions; repeat variability; attribution method for any “proxy-added” claim |
| Resources | Proxy CPU/RSS/heap/goroutines/handles; connector children separately; generator/upstream/database limits |
| Cleanup | Warm baseline, end-of-load and post-drain samples; retention/TTL context; remaining owned work |
| Hypothesis evidence | Only the relevant allocation, CPU, lock, queue, query or durability observations |
| Verdict | Measured/pass, failed/NO-GO, environment-limited, not measured, or inapplicable; exact reason |
| Decision | Keep/revise/revert/no production change; associated fix/tracker and affected scenarios to rerun |
| Raw evidence | Accessible artifact references and checksums where useful |

Mark unsupported metrics unavailable, never zero. Preserve failed and inconclusive experiments. A component benchmark or static audit is supporting evidence, not a system-level certificate.

## Scenario disposition

The initial state below is intentional. Update rows only from executed evidence; do not check a row because a harness supports it.

| Scenario | State | Evidence / remaining obligation |
| --- | --- | --- |
| HOLD | Not measured | Public candidate; healthy long stream and held-load tiers |
| DELTA | Not measured | Active streaming; declared built-in/connector profile |
| START | Not measured | Open/prompt bursts |
| BODY | Not measured | Canonical/capture, chunked and supported compressed ingress |
| OUTPUT | Not measured | Retention slope and slow readers |
| COMPLETE | Not measured | Nonfinancial completion burst and drain |
| RETRY | Not measured | Pre-output retries and no retry after commitment |
| OBSERVE | Not measured | Actual observation/redaction modes and slow sinks |
| AUTHZ | Not measured | Auth sinks and enabled technical admission |
| B2BUA | Not measured | Selected continuity/recording and persistence profile |
| DISCONNECT | Not measured | Open/stream cancellation, siblings and forced close |
| SOAK | Not measured | Multi-hour representative load, reload and reclamation |

Enterprise financial scenarios have **no selected profile in this public slice**. If separately scheduled under #394, record actual attached money behavior, durable acceptance/replay evidence, completion/admission latency, and database topology privately. Never infer financial capacity from an unattached/no-op port.

## Final capacity statement

State the highest demonstrated profile/tier and duration, unresolved correctness defects, environment limits, unmeasured scenarios, and decisions still required for release acceptance. Distinguish a successful investigation from a successful capacity claim.

There is no measured 1k/5k/10k claim in this ledger yet.
