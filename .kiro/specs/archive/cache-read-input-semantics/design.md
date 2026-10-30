# Technical Design

## Overview
Correct the existing uncached-input extension without normalizing provider token counters. The essential Anthropic adapter identifies its exclusive input semantics; the canonical collector consumes that interpretation. Existing frontend encoders already delegate uncached-input calculation to the collector, so no frontend-specific provider branches are needed. Unannotated events keep legacy behavior.

### Goals
- A 300-input/600-cache-read Anthropic event produces an uncached extension of 300, not zero.
- Preserve native counters, presence, usage planes, provider evidence and billing behavior.
- Keep one additive public scalar and bounded per-collector arithmetic state; no dependencies.

### Non-Goals
- Normalize native input/total/cache-write counters or repair native frontend dialect translation.
- Publish start-only usage or reconcile cumulative provider snapshots (#1084).
- Adopt metadata in optional connectors (#1085) or backfill historical evidence.
- Change PR #1079's diagnostic, including its prescribed ratio and sampling behavior.

## Boundary Commitments

### This Spec Owns
- A provider-neutral input/cache-read interpretation annotation on existing canonical usage metadata.
- Correct uncached-input interpretation in `Collected` for event-level and selected client-visible scoped usage.
- Essential Anthropic message-start/message-delta annotation and observable frontend extension regressions.

### Out of Boundary
- No core runtime, routing, metering normalization, billing rating/journal, connector protobuf, or diagnostic implementation edits.
- No new usage events, usage scopes, planes, observer chains, registries or factories.

### Allowed Dependencies
- `pkg/lipapi` uses only its current dependencies; no provider names or SDKs.
- `internal/plugins/backends/protocols/anthropicmessages` retains its current dependency on `pkg/lipapi` and the Anthropic SDK.
- Existing frontend and parity tests use canonical streams and the existing encoders.

### Revalidation Triggers
- Additive public metadata requires the existing public-ABI compatibility/snapshot gates and consumer compilation.
- A proposal to rewrite quantities, add scopes, publish start usage, or modify billing is outside the approved boundary and requires renewed design review.

## Architecture

### Seams Reused
- `UsageAccountingMetadata` already annotates legacy counters without replacing them. Extend it instead of introducing another usage plane.
- `ClientVisibleUsage` already chooses scoped frontend usage; interpret the metadata on the selected entry, not on an unrelated event shell.
- `Collected.AccumulateUsage` is shared by streaming and non-streaming frontend encoders. Retain original counters there and track only the correction needed for the uncached helper.
- The new scalar is necessary for 1.1: no current provider-neutral field distinguishes inclusive and exclusive input.

### Project Boundary Questions (Go LIP)
- Canonical contract/collector own interpretation; the backend plugin owns provider annotation.
- Cache-read inclusion is provider-neutral; no provider identity checks enter public/core code.
- Streaming remains primary; non-streaming uses the same accumulator.
- Provider SDKs stay in the existing backend adapter.
- No execution, retry, failover, lifecycle, secure-session, diagnostics exposure or startup-security changes.
- No new library, I/O, goroutine or configuration.

## File Structure Plan

### Modified Files
- `pkg/lipapi/token_accounting.go` — document/add the interpretation flag to `UsageAccountingMetadata`.
- `pkg/lipapi/events.go` — bounded private correction state on `Collected`; use selected entry metadata in accumulation and the uncached helper.
- `pkg/lipapi/collected_clone.go` — copy the private arithmetic state through the existing collector clone seam.
- `pkg/lipapi/event_stream_test.go` — extend existing collection tests for exclusive, inclusive, mixed and scoped-priority cases.
- `internal/plugins/backends/protocols/anthropicmessages/map_events.go` — annotate both existing usage constructors, without changing quantities or emission behavior.
- `internal/plugins/backends/protocols/anthropicmessages/map_events_internal_test.go` — extend existing mapping fixtures to assert annotation and unchanged native evidence.
- `internal/plugins/frontends/openailegacy/encode_test.go` — extend existing usage-extension fixtures with annotated canonical usage, covering stream and non-stream ordinary counters and extensions.
- `internal/plugins/frontends/anthropic/encode_test.go` — extend existing usage-extension fixture to prove native counters remain unchanged while its extension is corrected.
- Existing tracked public ABI baseline only if the existing gate requires regeneration; do not relax the gate or change compatibility policy.

## Components and Interfaces

| Component | Package | Intent | Requirements |
|---|---|---|---|
| Usage interpretation metadata | `pkg/lipapi` | Declare input/cache-read relation without replacing counters | 1.1, 1.2, 2.1, 3.1, 3.2 |
| Collector | `pkg/lipapi` | Compute uncached result from selected usage semantics; retain counters | 1.1, 1.2, 1.3, 1.4, 2.2, 3.1, 3.3 |
| Anthropic usage constructors | `internal/plugins/backends/protocols/anthropicmessages` | Annotate exclusive input at the protocol boundary | 2.1, 2.3, 3.1, 3.2, 3.4 |
| Existing frontend encoders | `internal/plugins/frontends` | Consume corrected helper without new provider branches | 2.2, 3.3, 3.4 |

### Public Contract
Add `InputTokensExcludeCacheRead bool` to `UsageAccountingMetadata`.
- `false` retains the legacy inclusive-input assumption; explicit input semantics from existing callers remain source-compatible with keyed/zero-value construction.
- `true` declares that cache-read quantities are not contained in the associated input quantity. This is interpretation only, not tariff selection, authority or normalization.
- Both `Event.Accounting` and `ScopedUsageDelta.Accounting` can carry the flag.
- Document that producers reporting exclusive input must opt in; do not infer semantics from `DedupeKey`, provider IDs or raw JSON.

### Collector Contract
- `AccumulateUsage` continues choosing `ClientVisibleUsage`, accumulating exactly the same original quantities and money fields.
- Keep a private scalar for cache-read quantities associated with exclusive input. `UncachedInputTokens` subtracts only cache reads associated with inclusive input and retains its nonnegative clamp.
- This supports mixed inclusive/exclusive entries and pre-populated `Collected` values; direct construction without accumulated annotation retains legacy behavior.
- Arrange the arithmetic to avoid an unnecessary `InputTokens + correction` intermediate. Do not introduce new overflow policy for the existing counter accumulator.
- Cloning/copying a collector must retain this arithmetic state; use the existing copy/clone seam and test it if that seam copies fields individually.

## Error Handling
No new errors or stream failures. Unannotated usage remains backward-compatible. Invalid/negative provider counts keep their current adapter rejection/absence behavior. Zero and absent cache reads do not change original presence markers.

## Testing Strategy
- Existing collector table: exclusive 300/600 -> 300; inclusive 900/600 -> 300; inclusive zero/underflow remains nonnegative; mixed entries; explicit client-visible scope overrides an opposite event-level annotation. Assert original counters remain unchanged.
- Existing mapper fixtures: both usage constructors opt in; raw/provider evidence retains 300 input and 600 cache reads. Existing V2 component-evidence tests remain unchanged and pass.
- Existing frontend parity/wire fixtures: stream and non-stream extension reflect the corrected canonical result, with ordinary counters unchanged. Avoid a frontend-by-backend product.
- RED each changed behavior before implementation. Reuse generic ABI/architecture checks; no feature-specific source scanner.
- Scoped tests/lint for API, mapper and consumers, followed by `make dev-contract-check` and applicable public compatibility gates from `docs/development-iteration.md`. No local race runs; no concurrency mechanism is added.
- Implementation target: at most 10 changed source/test files, roughly 50–100 production lines and tests at most twice production additions. If transport/schema edits or wider normalization become necessary, stop and review the boundary.
