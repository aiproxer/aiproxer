# Research and Design Decisions

## Summary
- **Feature**: cache-read-input-semantics.
- **Discovery Scope**: Focused extension of canonical usage metadata and existing collection.
- The reproduced bug is in the uncached-input helper; the reproduction does not establish incorrect billing.
- Existing counters are explicitly compatibility totals; changing them would widen this slice.
- Adding a client-visible scope to provider events is not a safe shortcut: it changes core provider snapshot classification/filtering.

## Research Log

### Reproduction and consumers
- Sources: `pkg/lipapi/events.go` (`Collected.UncachedInputTokens`, `AccumulateUsage`, `ClientVisibleUsage`); frontend `wireLegacyUsage`, `wireResponsesUsage`, `wireAnthropicUsage`, `gemUsageMetaFromCollect`.
- Current helper subtracts all cache reads. Anthropic-shaped 300 input/600 cache reads yields zero.
- Existing frontend consumers call the helper; stream encoders accumulate into the same collector as non-streaming.
- These helper call sites are frontend projections, not billing rating call sites.

### Provider preservation
- Sources: `internal/plugins/backends/protocols/anthropicmessages/map_events.go` (`usageFromMessageDelta`, `usageFromMessageStart`, `anthropicEvidenceDraftWithRaw`); existing mapper evidence test and core normalization inclusive/separate-input tests.
- Both constructors preserve separate provider input/cache quantities. V2 evidence preserves raw provider quantities and does not treat a convenience total as native total evidence.
- Message-start usage is sideband-only today; this correction does not change that emission policy.

### Rejected scoped-usage shortcut
- Source: `internal/core/runtime/executor_settlement.go` (`authoritativeProviderSnapshotKey`, `authoritativeProviderUsageEvents`).
- Coalescing excludes events carrying scopes; authoritative filtering examines explicit scope classifications. Adding scopes just to influence a frontend risks changing provider evidence processing.
- Keep the existing event/scopes shape and add scalar interpretation metadata instead.

## Architecture Pattern Evaluation
| Option | Benefit | Reason selected/rejected |
|---|---|---|
| Normalize all input to inclusive totals | Uniform totals | Rejected: rewrites quantities, provider evidence and native frontend behavior outside the approved narrow slice |
| Add frontend scope to Anthropic usage | Uses current collector priority | Rejected: affects core coalescing/filtering and adds an accounting-plane entry |
| Branch on provider IDs/raw JSON in frontend | No public field | Rejected: pairwise provider semantics leak into frontend/public logic |
| Add explicit neutral metadata | Preserves quantities and existing seams | Selected: one additive flag plus bounded collector state; default preserves existing callers |

## Design Decisions
- Generalize the contract to cache-read inclusion semantics, not an Anthropic switch. Implement only the first essential adapter consumer.
- Reuse existing metadata and accumulator rather than adding a classifier, interface, registry or dependency.
- Leave native protocol translation and cache-write semantics unchanged; this slice corrects the extension only.

## Risks and Mitigations
- Adding a field to an exported Go struct can affect unkeyed external literals: run current compatibility gates and document the additive surface; do not silently bypass ABI checks.
- Optional connectors may not transport/adopt new metadata: explicitly deferred, not advertised as fixed.
- Collector copies could lose private correction state if copying individual fields: inspect the existing clone seam and verify retained state.
- Mixed entries require per-entry accounting of the correction: avoid a last-event boolean on the aggregate.
- Billing must not interpret the new flag as pricing: unchanged quantities/evidence and existing normalization fixtures guard the boundary.
