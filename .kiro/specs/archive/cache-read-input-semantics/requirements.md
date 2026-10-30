# Requirements Document

## Project Description (Input)
Clients consuming LIP's uncached-input usage extension receive incorrect counts when a backend reports input separately from cache reads. Anthropic reports 300 input and 600 cache-read tokens, but the current collector's inclusive-input assumption yields zero uncached tokens. Correct this interpretation without rewriting provider-native counters or billing evidence. The controller requested a separate correction after submission of the observability-only diagnostic PR #1079 and authorized preparation of this narrow spec, not implementation approval.

## V1 Slice
- **Delivers**: Correct uncached-input extensions for canonical usage explicitly identified as excluding cache reads.
- **First consumer**: Existing frontend usage encoders consuming the essential Anthropic Messages backend's canonical events.
- **Safe degradation**: Unannotated usage keeps the existing inclusive-input interpretation; absent usage is not invented.
- **Budget**: One additive interpretation flag, bounded per-collector arithmetic state, existing adapter annotations and regression fixtures; no new usage pipeline or provider-specific frontend branching.

## Deferred
- Canonical input normalization, total-token redefinition, native frontend counter translation, and cache-write inclusion semantics are excluded from this correction.
- Emitting Anthropic start-only usage into the canonical stream and reconciling cumulative stream snapshots is separate work: #1084.
- Optional connector adoption is separate work after the public metadata contract is proven: #1085. Historical persisted-usage backfill is not a deliverable of this correction.

## Boundary Context
- **In scope**: Correct the uncached extension for usage reaching the existing collection path; preserve original input/output/cache/total counters.
- **Out of scope**: Billing/rating policy, request mutation, routing, cache-collapse thresholds/ratio, new stream events, and new connector ABI capabilities.
- **Adjacent expectations**: Provider evidence retains its native quantities. Unannotated producers retain compatibility behavior. Explicit client-visible usage continues to take precedence over event-level usage.

## Requirements

### Requirement 1: Correct uncached-input interpretation
**Objective:** As a client, I want the uncached-input extension to reflect input semantics rather than subtracting cache reads twice.

#### Acceptance Criteria
1. When LIP receives input usage explicitly identified as excluding cache reads, the LIP collector shall count that input without subtracting cache-read tokens for its uncached-input result.
2. When LIP receives unannotated or inclusive input usage, the LIP collector shall retain the existing nonnegative input-minus-cache-read result.
3. When a response accumulates both inclusive and exclusive usage, the LIP collector shall apply the interpretation associated with each consumed usage entry, rather than the last entry's interpretation to the entire response.
4. When a usage event provides explicit client-visible usage, the LIP collector shall use that entry's input interpretation instead of the event-level interpretation.

### Requirement 2: Essential Anthropic adapter adoption
**Objective:** As an Anthropic backend user, I want supported usage events to communicate the provider's exclusive input semantics.

#### Acceptance Criteria
1. When the essential Anthropic Messages adapter constructs usage from a message-start or message-delta usage object, the LIP adapter shall identify its input quantity as excluding cache-read tokens.
2. When supported frontend encoders expose the uncached-input extension from those canonical usage events, LIP shall report 300 uncached tokens for 300 input and 600 cache-read tokens, for both streaming accumulation and non-streaming collection.
3. If a provider response omits an input or cache-read quantity, the LIP adapter shall retain that quantity's existing absence behavior.

### Requirement 3: Preserve existing quantities and execution
**Objective:** As an operator, I want this correction not to change accounting or ordinary protocol counters.

#### Acceptance Criteria
1. When LIP applies input interpretation metadata, LIP shall preserve the original input, output, cache-read, cache-write, reasoning and total quantities and their presence markers.
2. When LIP handles annotated usage, LIP shall preserve provider evidence, tariff inputs and monetary behavior without reclassifying any usage plane.
3. When LIP exposes usage on a frontend, LIP shall change only the uncached-input extension for the affected annotated usage, leaving ordinary protocol counters unchanged.
4. The LIP service shall retain its existing routing, stream-event ordering, terminal ownership and retry behavior.
