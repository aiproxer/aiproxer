# Research: usage-wire-accounting

## Status

Requirements approved by the user. Technical design generated for approval;
implementation remains gated. No implementation files were changed during
spec preparation.

## Confirmed Sources

- `pkg/lipapi/token_accounting.go`: PR #1325 widened the existing read-exclusion
  declaration to include writes. The archived cache-read-input-semantics
  contract explicitly excluded cache-write interpretation from its scope.
- `pkg/lipapi/events.go`, `Collected.AccumulateUsage` and `ClientVisibleUsage`:
  selection is event-local. Provider fallback quantities and subsequent
  client-visible scopes are both accumulated into the same counters.
- `internal/core/runtime/executor_token_accounting_stream_test.go`,
  `TestExecutorStreamAccountingRecordsProviderAndClientVisibleUsage`: a real
  executor test explicitly requires both provider usage and final scoped
  client-visible usage before the terminal event.
- `internal/infra/backendplugins/adapter/finalize_billing.go`,
  `finalizeBillingResponseToEvent`: finalizer conversion does not copy the
  declaration carried by `UsageEvidence`.
- `pkg/lipsdk/backendplugin/call_bridge.go`, `CanonicalEventFromLipapi`:
  missing totals become input plus output with explicit total presence.
- `pkg/lipapi/events.go`, `TotalTokensForPrompt`: retaining a larger provider
  total does not guarantee the equality requested for OpenAI wire totals.

## Delivery Boundaries for Design

1. **Prerequisite A, based on main**: repair client perspective selection with
   real-executor coverage. Preserve provider evidence. Decide the narrowest
   existing selection seam after tracing terminal synthesis; do not introduce
   a second accounting pipeline or suppress all provider events.
2. **Prerequisite B, based on main**: preserve omitted totals across the existing
   connector bridge, including explicit-zero and legacy nonzero compatibility.
3. **PR #1325 remediation, after prerequisites**: independent write-exclusion
   metadata, producer adoption, stream/finalizer transport, inclusive frontend
   projection, OpenAI sum totals, and stream/non-stream integration evidence.

The slices are independently reviewable; the final integration gate requires
all three. Prerequisite fixes must not depend on the new write-exclusion field.

## Design Questions to Resolve Before Implementation

- Avoid publishing fallback counters as irreversible streaming usage if a
  later client-visible perspective replaces them. Inspect each encoder's
  terminal usage emission and existing runtime synthesis before choosing the
  selection mechanism.
- Preserve old read-only flag semantics on both canonical metadata and SDK
  accounting evidence. Identify all in-scope producers and conversion seams
  before assigning protobuf field numbers or updating ABI baselines.
- OpenAI totals derive from the selected wire input/output breakdown; provider
  totals remain internal evidence, not fabricated reasoning or output tokens.
- Gemini may legitimately count separately reported reasoning in its total;
  do not impose the OpenAI equality rule on Gemini without a protocol basis.
- Anthropic preservation applies to the exclusive producer shape in this
  slice; generalized inclusive-to-Anthropic normalization is not implied.

## Requirements Review

Inline review: five requirement areas, nineteen acceptance criteria, numeric
IDs, EARS statements, first consumer and Deferred section present. Requirements
describe observable behavior rather than requiring a new pipeline or store.
No design approval or successful end-to-end verification is claimed.

## Design Discovery and Synthesis

- `finalizeTokenAccounting` reconstructs scoped client usage after provider usage
  has already reached the canonical stream. Event-local selection cannot prevent
  adding both perspectives; collector state must span the turn.
- `gemini.WriteStreamSSE` accumulates usage until response_finished, as do the
  final usage paths in Chat/Responses. `anthropic.WriteStreamSSE` uses the same
  accumulator for final message_delta, with provisional initial message usage.
  No general stream buffer or second execution engine is necessary.
- Selected explicit zero must count as a perspective decision. Resetting only
  usage state on first explicit selection avoids retaining provider cache and
  cost fields alongside reconstructed client token counts.
- Existing usage DTOs and the sideband fingerprint are the appropriate transport
  seams. Independent flags preserve compatibility without versioning the meaning
  of the read-only flag. No new dependency is needed.
- Preserve Gemini provider totals separately from the OpenAI sum invariant.
  Remove the unmerged TotalTokensForPrompt helper instead of generalizing a
  protocol-specific rule into public canonical API.
- Simplification: annotate Claude ACP in its existing classified stream instead
  of retaining a wrapper whose only responsibility is setting two booleans.

## Design Review Gate

Inline review passed after separating Gemini total behavior from OpenAI equality.
All nineteen criteria appear in the component table and have concrete behavior
and test coverage in design.md. The implementation spans three independently
reviewable deliveries already approved in the requirements, not one expanded PR.
No implementation correctness, external review or live-provider result is claimed.

## Task-Graph Review: Design Follow-up Required

The user approved the generated design. A fresh task-graph review then returned
NEEDS_FIXES. Task generation is paused rather than writing an executable plan
that hides an uncovered producer migration.

Source inspection confirms three additional existing read-exclusion producers:
`connectors/gitlabduo/internal/service/provider_evidence.go`,
`connectors/minimexoauth/internal/service/provider_evidence.go`, and
`connectors/commandcode-anthropic/internal/anthropic/provider_evidence.go`.
The approved file plan covers native Anthropic and Claude ACP only. Their
existing read declaration does not by itself establish whether writes are
included; the migration must audit that relation and explicitly adopt the write
flag where excluded, with unchanged native evidence and module-local tests.
This is distinct from the other ACP connector adoption deferred to #1324.

The owner approved including those three producers in the bounded annotation
migration, without dependency upgrades or a root release. The design now names
their production and existing test files and requires auditing the actual write
relation before annotation.

Task-local review repairs are also retained for the next draft: make semantic
rebase and both prerequisite regression checks explicit; split collector and
executor integration work, and split SDK carriers from host/finalizer work;
exercise mixed per-delta write declarations after the new contract exists;
assign Anthropic wire preservation to frontend integration rather than producer
annotation. Keep at most twelve leaves, each with its own behavioral proof.

## Task Plan Review Outcome

The amended plan has eleven leaves and covers all nineteen criteria. A fresh
review and one bounded repair review checked dependencies, sizing and coverage.
The repair review accepted the graph with one residual placement condition:
the real host converter is private, so connector-to-executor integration must
live in the existing adapter-package usage tests. The final plan adopts that
placement and uses the existing exported executor constructor; no new exported
converter or cross-package private fixture is required.

Other repairs are explicit in tasks.md: separate carrier/host work, split the
producer migration, gate semantic rebase on prerequisite delivery, keep OpenAI
total behavior out of prerequisite A, and narrow final integration to the new
connector/executor seam. The component traceability table now assigns Anthropic
wire preservation to frontend projection. Task approval and implementation
authorization remain pending; no code correctness claim is made.
