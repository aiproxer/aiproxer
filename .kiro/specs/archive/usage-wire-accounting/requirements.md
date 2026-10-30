# Requirements Document

## Introduction

Issue #1319 requests protocol-correct usage for Claude ACP on OpenAI frontends.
The review of PR #1325 identifies incompatible cache-write interpretation,
lost finalizer metadata, overlapping accounting perspectives, an insufficient
wire-total invariant, and fabricated total presence. The owner approved the
remediation direction, not implementation or the documents generated from it.

## V1 Slice

- **Delivers**: One coherent usage representation per client response, with
  independent cache-read/cache-write interpretation and truthful provider evidence.
- **First consumer**: Claude ACP requests through Chat Completions and Responses;
  shared projections also serve the Anthropic and Gemini frontend encoders.
- **Safe degradation**: Undeclared cache relations retain the legacy inclusive
  interpretation. If no explicit client-visible usage is supplied, existing
  unscoped usage remains the fallback; missing quantities are not invented as
  provider reports.
- **Delivery**: Pre-existing perspective-selection and total-presence defects
  ship as separate prerequisite PRs from main. PR #1325 then rebases onto those
  fixes and delivers the relation/translation correction and combined proof.

## Deferred

- Adoption by the other ACP connectors and migration into shared ACP support:
  #1324; this slice does not change its published-module dependency strategy.
- Historical usage backfill, monetary rating changes, new persistence,
  additional telemetry, and wire/large-body execution redesign are out of scope.

## Boundary Context

- **In scope**: Canonical input interpretation, connector event/finalizer
  transport, frontend usage selection, protocol projections, and ordinary
  canonical-stream execution in streaming and non-streaming modes.
- **Out of scope**: Changing customer billing policy, provider charge quantities,
  routing, retries, transcript content, or session authority.
- **Adjacent expectations**: Provider usage remains available to existing
  accounting/evidence consumers even when it is not selected for the client.
- **Ownership**: Canonical contracts own interpretation and selection rules;
  SDK/host adapters preserve declarations and presence; producers declare their
  own semantics; frontend adapters own wire legality.

## Requirements

### Requirement 1: Compatible cache inclusion declarations

**Objective:** As a producer author, I want input declarations to retain their
meaning so that existing callers are not silently double-counted.

#### Acceptance Criteria

1. When a producer declares only cache-read exclusion, AIProxer shall add only
   the excluded reads when projecting inclusive input, without adding writes
   already included in input.
2. When a producer separately declares cache-write exclusion, AIProxer shall
   add the excluded writes when projecting inclusive input independently of
   the cache-read declaration.
3. When neither exclusion is declared, AIProxer shall retain the existing
   inclusive-input interpretation.
4. When Claude ACP reports input excluding both cache categories, AIProxer
   shall declare both exclusions without changing its original quantities.

### Requirement 2: Truthful connector transport and final usage

**Objective:** As an operator, I want transported evidence to retain its
quantities and presence rather than acquire inferred provider claims.

#### Acceptance Criteria

1. When usage crosses a connector boundary as a stream event or finalized
   response, AIProxer shall preserve both exclusion declarations independently.
2. When a provider omits total usage, AIProxer shall preserve that absence
   through connector conversion instead of asserting a reported total.
3. When a provider reports an explicit zero total, AIProxer shall preserve
   that zero and its explicit presence through connector conversion.
4. When usage is translated for client output, AIProxer shall preserve the
   original provider quantities and presence for existing evidence consumers.

### Requirement 3: One client usage perspective

**Objective:** As a client user, I want usage for one turn rather than a sum of
different accounting perspectives of that turn.

#### Acceptance Criteria

1. When an executed turn supplies explicit client-visible usage as well as
   provider usage, AIProxer shall report the client-visible quantities without
   adding the provider quantities to them.
2. When an executed turn supplies no explicit client-visible usage, AIProxer
   shall retain unscoped usage as the client fallback.
3. When several usage deltas belong to the selected perspective, AIProxer
   shall accumulate those deltas using their individual cache declarations.
4. When client usage selection excludes a provider perspective, AIProxer
   shall retain that provider evidence for existing accounting consumers.

### Requirement 4: Protocol-correct projection and totals

**Objective:** As a client author, I want usage fields to obey the target
protocol instead of retaining an incompatible provider breakdown.

#### Acceptance Criteria

1. When encoding selected usage on Chat Completions or Responses, AIProxer
   shall report inclusive input and a total equal to that input plus the
   reported output, even when a provider total differs.
2. When encoding selected usage on Gemini, AIProxer shall report prompt input
   including the declared excluded cache categories without adding categories
   already included by the producer.
3. When encoding read-and-write-exclusive provider usage on Anthropic,
   AIProxer shall preserve its exclusive input, output, cache-read and
   cache-creation quantities.
4. Where usage extensions are disabled, AIProxer shall provide the same
   corrected ordinary counters without exposing opt-in extensions.

### Requirement 5: Equivalent execution modes

**Objective:** As a user switching response modes, I want the same selected
usage regardless of streaming transport.

#### Acceptance Criteria

1. When equivalent canonical usage reaches streaming and non-streaming Chat,
   Responses, Anthropic or Gemini encoders, AIProxer shall report equivalent
   final usage quantities for that frontend.
2. When real executor output contains both provider and client-visible usage,
   AIProxer shall apply the same perspective selection in both response modes.
3. When unscoped connector usage crosses the real connector conversions,
   AIProxer shall retain its declarations and presence before applying the
   frontend projection.
