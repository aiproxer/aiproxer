# Compaction continuity preservation

`compaction-continuity` keeps a bounded, branch-scoped continuity capsule across
recognized compaction boundaries. It is an optional feature. The standard
distribution does not enable it unless a `plugins.features` row sets
`enabled: true`; the no-key dogfood configuration deliberately keeps the row
disabled in [`config/examples/dogfood-local-stub.yaml`](../config/examples/dogfood-local-stub.yaml).

This feature is separate from the #312 compaction detector. The detector is a
prerequisite: an enabled feature generation must have detector request preview
and committed-event support, the process branch coordinator, and the bounded
background auxiliary scheduler. Standard `aipstd` composition supplies these
services. A custom composition with one missing fails generation validation;
an absent or disabled feature does not require them.

## Enablement and route selection

The feature configuration is private to the `compaction-continuity` row. There
is no separate extractor `model` field: `extractor.route` is the normal
`backend:model` route selector (and may contain the route syntax accepted by
the configured router). The extractor route is independent from the primary
request route. Alternatively, set `extractor.inherit: true` to use the
primary request's route; `route` and `inherit` are mutually exclusive. When
semantic extraction is enabled, one of those two choices is required.

Start from the disabled row in [`config/config.yaml`](../config/config.yaml),
then opt in explicitly:

```yaml
plugins:
  features:
    - id: compaction-continuity
      enabled: true
      config:
        preserve:
          plan: true
          user_decisions: true
          constraints: true
          rationale: true
          rejected_alternatives: true
        extractor:
          route: "openai-responses:small-model"
          # Use this instead of route to inherit the primary selector:
          # inherit: true
          timeout: 8s
          max_input_tokens: 12000
          max_output_tokens: 2000
        worker:
          max_concurrency: 2
          queue_capacity: 16
        barrier:
          timeout: 2s
        capsule:
          max_tokens: 2500
          max_bytes: 1048576
        source:
          ttl: 2h
          max_bytes: 4194304
        result:
          ttl: 2h
          max_bytes: 4194304
          max_count: 16
        failure:
          mode: fail_open
        branch_ttl: 2h
        max_branch_entries: 1024
```

`extractor.max_concurrency` and `extractor.queue_capacity` are accepted
compatibility aliases for the corresponding `worker` fields. The flattened
aliases `barrier_timeout`, `pending_result_ttl`, `max_capsule_tokens`,
`source_ttl`, and `failure_mode` are also accepted; use the grouped form above
for new configuration. Unknown fields are rejected. All configured bounds
must be positive and finite; `branch_ttl` must be at least the source and
result retention windows.

The implementation defaults and hard maxima are:

| Setting | Default | Maximum / rule |
| --- | ---: | --- |
| `extractor.timeout` | `8s` | `24h` |
| `extractor.max_input_tokens` | `12000` | `1000000` |
| `extractor.max_output_tokens` | `2000` | `1000000` |
| `worker.max_concurrency` | `2` | `128` |
| `worker.queue_capacity` | `16` | `4096` |
| `barrier.timeout` | `2s` | `24h` |
| `capsule.max_tokens` | `2500` | `100000` |
| `capsule.max_bytes` | `1 MiB` | `64 MiB` |
| `source.ttl` | `2h` | `30d` |
| `source.max_bytes` | `4 MiB` | `64 MiB` |
| `result.ttl` | `2h` | `30d` |
| `result.max_bytes` | `4 MiB` | `64 MiB` |
| `result.max_count` | `16` | `4096` |
| `branch_ttl` | `2h` | `30d`; at least source/result TTL |
| `max_branch_entries` | `1024` | `65536` |

`failure.mode` defaults to `fail_open`. The request-time preservation path is
best effort: a preservation failure must not become a primary provider error
or a retry decision. Keep this mode explicit in production configuration.

## What an enabled turn does

The #312 detector first provides a pure preview. Before the primary request is
opened, continuity may prepare deterministic state, a non-billable preview
intent, and a bounded reinjection. It does **not** submit fresh extractor work
at this point. Only after the primary `Open` succeeds and the detector commits
the event can one coalesced semantic extractor job be admitted. A failed
primary `Open` therefore creates no new child `BillingCallID`, child B-leg, or
provider usage.

The semantic job is one off-session, private, no-tools child. The child uses
the configured independent route (or inherited route), suppresses the
continuity feature itself, and does not create a primary secure-session turn,
transcript entry, resume effect, client session header, or primary A-leg
mutation. Its private child A-leg is ordinary execution lineage only. The
continuity branch is captured from the authoritative parent session/primary
A-leg before the child exists; the child A-leg can never become branch or
route-override authority.

The child receives a bounded sanitized source window, not a raw transcript by
default. Preparation prioritizes explicit user decisions, relevant assistant
planning, and recognized structured plan carriers; it drops or bounds ordinary
tool output, logs, dumps, media, binaries, unrelated external content, and
unnecessary reasoning. Existing secret/redaction policy is applied before
egress when available. Source text is untrusted quoted data to the extractor,
and raw session/account/branch identifiers are not part of the model-facing
payload. A remote route is still explicit data egress: operators must treat
the selected backend as receiving this sanitized history and must review its
retention and privacy policy.

Opaque/native compaction items are preserved byte-for-byte. When continuity
state is available and valid, the feature adds one bounded, versioned
developer/system projection to the next eligible request; branch binding,
digest, call authority, and projection limits are checked first. A failed or
unsupported projection leaves the original canonical call unchanged.

## Usage and cost attribution

Semantic extraction is additional inference, latency, and potentially provider
cost attributable to the originating user/account. It is not included in the
primary frontend's protocol-visible usage. Where the host's metering and
usage-evidence composition is enabled, the child has its own `BillingCallID`
and B-leg and is classified with the bounded workload identity:

```text
class: auxiliary
role:  compaction_continuity_extractor
```

Not every boundary submits this child: deterministic carrier/capsule state or
the absence of a semantic candidate can complete the preservation path without
an extractor call.

Use that workload class/role (plus the child A-leg/B-leg lineage) to separate
continuity cost from primary inference in operator/account reports. Account
identity remains the originating principal; pricing and rating are the money
implementation's work and still use the existing selected route/model policy.
A pre-submit admission rejection creates no child
provider work. Once child provider work has been submitted, its actual usage
remains accountable even if the result is malformed, discarded, late, or never
reinjected.

## Durability and process boundaries

V1 continuity is process-local across compaction boundaries and immutable
configuration-generation reloads within one process. A queued job retains its
submit-time generation/route and the process-owned branch coordinator retains
the parent binding; a reload changes only future submissions. Worker, result,
preview-intent, capsule, and branch state remain bounded by the configured
limits and TTLs.

This feature does not add a durable capsule store, durable background queue, or
full-transcript store. Process restart loses process-local capsules, pending
jobs, preview intents, and reinjection watermarks. Existing continuity or
secure-session persistence does not automatically reconstruct this feature's
capsule. No optional authorized transcript-reconstruction adapter is currently
wired; after restart, missing state is an explicit fail-open condition and the
next eligible turn may rebuild only from the canonical history available to
that turn.

## Failure behavior and troubleshooting

| Symptom / condition | Expected behavior | Operator action |
| --- | --- | --- |
| Enabled generation reports missing detector preview/commit (#312), branch coordinator, or background auxiliary | Generation validation fails before serving; disabled rows are a no-op | Use standard `aipstd` composition, or wire all three process capabilities in the custom host; do not route around the detector |
| `extractor` enabled without `route` or `inherit: true`, or with both | Config validation fails | Select one explicit independent route or `inherit: true` |
| Callback error, panic, invalid mutation, or rollback | Fail-open; original primary call/event is retained and failure diagnostics are content-free | Check logs/metrics for stage and bounded status; do not treat it as a primary provider failure |
| Queue full, generation retain failure, or scheduler closed | No semantic job; deterministic/native continuity and primary traffic continue | Reduce compaction load or increase bounded worker/queue settings within maxima; restart only if the scheduler is intentionally closed |
| The admission check rejects the child | No child provider work; primary request continues | Check the admission authority's own configuration and the selected route/model; enabling the feature does not bypass the admission check |
| Child route/provider error or timeout | Child result is discarded fail-open; primary traffic is not retried because of preservation | Check the independent selector and upstream health; confirm the primary route is healthy |
| Barrier timeout or late result | The bounded barrier returns control to native flow. A valid late result remains pending for the configured result/branch TTL and may be consumed on the next eligible turn; expired/evicted output is forgotten | Inspect bounded job/result outcome and TTL settings; do not expect a late result to rewrite an already released response |
| Invalid schema, digest, revision, source reference, or branch binding | Result is rejected/forgotten without state regression or cross-branch adoption | Fix the extractor route/prompt contract only through the implementation; never relax branch or digest checks |
| Opaque compaction or projection capability is unavailable | Opaque bytes remain unchanged; continuity projection is skipped and the canonical request/event is returned unchanged | Verify the frontend/backend supports the ordinary canonical authority; continuity is best effort and does not rewrite opaque data |

For a deterministic configuration check before serving, first stage the
external local-stub connector as described in [`docs/dogfood-local.md`](dogfood-local.md)
(`make package-full PACKAGE_DEST=.aip-plugins`). Then run:

```bash
go run ./cmd/aipstd check-config --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/aipstd routes --config ./config/examples/dogfood-local-stub.yaml
go run ./cmd/aipstd inventory --config ./config/examples/dogfood-local-stub.yaml
```

The committed dogfood row remains `enabled: false`; these commands validate
the surrounding standard composition without enabling remote extraction.

## Related implementation and contracts

- [#312 compaction event detection](https://github.com/aiproxer/aiproxer-enterprise/issues/312)
- [`internal/infra/compactiondetect`](../internal/infra/compactiondetect/) — detector preview/commit capability
- [`internal/plugins/features/compactioncontinuity`](../internal/plugins/features/compactioncontinuity/) — feature-private configuration and semantics
- [`internal/plugins/features/compactioncontinuity/state`](../internal/plugins/features/compactioncontinuity/state/) — branch coordinator and capsule/job/injection state (feature-owned)
- [`internal/standardplugins/featurehost/compaction`](../internal/standardplugins/featurehost/compaction/) — authoritative parent binding adapter (standard-distribution composition)
- [`internal/core/auxreq`](../internal/core/auxreq/) — process-owned bounded background scheduler
- [`config/config.yaml`](../config/config.yaml) — commented reference row
- [`config/examples/dogfood-local-stub.yaml`](../config/examples/dogfood-local-stub.yaml) — disabled deterministic example
