# Tool-Call Buffering Limits

Operator-facing description of the attempt-local resource bounds the completed-tool-call assembler applies, and of what an operator sees when one of them is reached.

---

## 1. Scope and why these limits exist

The tool-call assembler collects the argument deltas of every in-flight tool call on an attempt so that finalizers (for example path virtualization, which expands reserved path aliases, or tool-call repair) can see a call's **complete** arguments before it is released downstream.

Per-call bounds cannot express that. A single call has its own declared completeness requirement and its own shared assembly ceiling, but an **interleaved** stream can present many partially-collected calls at once. Without an attempt-local bound, one stream carrying many concurrent tool calls would grow the attempt's buffering without limit.

The limits below therefore bound **concurrent argument payloads, retained fragments, and identity bookkeeping** for a single attempt. They are deliberately independent of any one tool or feature.

These are **logical byte budgets, not an RSS cap**. The same argument bytes also exist in the original events and in growable assembly slices, so resident memory is higher than the logical budget. The budgets bound the assembler's own accounting so that the accounting stays bounded and the exhaustion reason stays attributable.

## 2. The limits

| Limit | Value | Applies to | Exhaustion result |
|---|---|---|---|
| Concurrent active tool-call buffers | 16 | Distinct in-flight tool calls on one attempt | `ErrToolCallBufferBudget` on the 17th `tool_call_started` |
| Total buffered argument bytes per attempt | 2 × `aipapi.MaxEventDeltaBytes` (16 MiB) | Sum of all active calls' assembled argument bytes | `ErrToolCallBufferBudget` on the exceeding `tool_call_args_delta` |
| Fragments retained per tool call | `aipapi.MaxItems` (4096) | Original events retained for one call (empty or duplicate fragments included) | `ErrToolCallBufferBudget` on the exceeding delta |
| Tracked tool-call identities per attempt | `aipapi.MaxItems` (4096) | Distinct tool-call IDs seen by the assembler (active, pass-through, completed, or refused) | `ErrToolCallBufferBudget` on the exceeding `tool_call_started` |

`ErrToolCallBufferBudget` is content-free: it carries no tool identity, tool name, path, or argument content, and it names no specific tool call.

## 3. These bounds are stricter than the canonical envelope

This is intentional, and it is the main operational surprise:

- **`aipapi.MaxItems`** is the canonical per-part item bound for a single logical payload. The assembler reuses it for two different quantities: fragments retained per call, and distinct tool-call identities tracked per attempt.
- The assembler's **16-active-call** bound applies to *concurrent tool calls on one attempt*, which is a different quantity. A stream that legitimately interleaves more than 16 tool calls is refused even though no single call exceeded any canonical limit.
- The **16 MiB attempt-wide** budget is the sum across all in-flight calls. A single call is separately bounded by its own declared completeness requirement (default 1 MiB) or the shared assembly ceiling (default 64 KiB), whichever applies; the attempt-wide budget is a second, aggregate guard on top of that.

An operator who sees buffering-budget exhaustion under heavy parallel tool use has two levers, in this order:

1. Confirm the stream really is a legitimate many-concurrent-tool-calls workload rather than a runaway or malformed stream.
2. If it is legitimate, raise the constant in `internal/core/runtime/tool_call_assembler.go` and re-certify. These are deliberate behavioral limits, not configuration knobs, and raising them trades memory for concurrency.

## 4. Interaction with mandatory completeness requirements

These attempt-local bounds are **independent** of the mandatory completeness capability (`toolcall.BufferingRequirement`). A call that declares a completeness requirement can still be refused for exceeding the attempt-local aggregate budget, because the aggregate bound is a property of the attempt, not of any one declarer.

The two bound different things and neither weakens the other:

- The **declared bound** is per declarer, per call: "I must see this much of this call to decide."
- The **attempt-local budget** is per attempt: "this attempt may not buffer unboundedly many calls at once."

## 5. Related references

- Assembler and limits: `internal/core/runtime/tool_call_assembler.go`
- Exhaustion regressions: `internal/core/runtime/tool_call_buffer_budget_test.go`
- Per-declarer completeness contract: `pkg/aipsdk/toolcall/buffering.go`