---
name: golang-error-handling
description: "Create, wrap, classify, expose, log, and test Go errors using errors.Is/As, %w, errors.Join, custom types, sentinel values, panic boundaries, and structured logging. Use when implementing or reviewing error paths and public error contracts."
---

# Go error handling

An error is both control flow and a contract. Preserve the information callers need while keeping internal details out of public responses.

## Focused review

For a review, keep the work read-only unless fixes are requested. Establish the review base, supported versions, and relevant contract; inspect changed code plus the callers and tests needed to assess it. Stay within this skill’s lens.

- Trace each error from origin to caller/log/wire mapping, including deferred Close, joined errors, partial results, and cancellation. Check whether a shadowed or overwritten error becomes success.
- Compare errors.Is/As behavior before and after wrapping; %v loses causal identity and %w may expose an implementation cause as a compatibility commitment.
- Inspect typed-nil errors, errors.As pointer/value targets, custom Is/As/Unwrap methods, multiple causes, and retry classification. Error text alone is not a stable classifier.
- Check one layer owns reporting and safe public translation. Recovery must occur in the panicking goroutine and must respect the validity of state after the panic.

Report each actionable finding with severity, confidence, file/symbol, trigger, consequence, and smallest remedy. Separate introduced/worsened defects from pre-existing debt, and state executed checks versus inference. If none survives validation, say so and identify coverage gaps.

## Create and wrap

- Return `nil` only when the operation succeeded. Use `errors.New` for stable sentinel values and `fmt.Errorf("operation: %w", err)` to add context while preserving identity.
- Define a custom error type when callers need structured fields or a stable `errors.As` target. Keep messages useful to humans but do not make callers parse them.
- Use `errors.Is` for sentinels and `errors.As` for types. `errors.Join` is appropriate when multiple independent failures must be retained; document precedence when a caller needs one primary cause.
- Preserve cancellation and deadline classification through wrapping.

```go
var ErrNotFound = errors.New("not found")

func load(ctx context.Context, id string) (*Item, error) {
    item, err := store.Get(ctx, id)
    if err != nil {
        return nil, fmt.Errorf("load item %q: %w", id, err)
    }
    return item, nil
}
```

## Handle once, expose deliberately

At each layer choose the owner of handling: classify/translate, log, or return. Avoid logging the same error at every stack frame. A layer may add structured context and return; the boundary that has the right audience should log or map it.

`%v` versus `%w` is not a security boundary. Formatting changes text and `%v` loses wrapping, but neither guarantees that a message is safe to send to a client. Map internal errors explicitly to a public status/code/message, log the detailed cause with access controls, and keep secrets and user input out of logs unless redacted.

Use panic only for programmer invariants or initialization failures that cannot be represented as an error. Recover at a deliberate goroutine or server boundary, convert the value to an error, preserve the stack in internal diagnostics, and ensure the process does not continue with corrupt state. Do not use `recover` to hide ordinary errors.

## Verification

Test success, sentinel/type classification, joined errors, cancellation, and public mapping. Check every ignored error and every `defer` cleanup error for an intentional policy. See [creation](references/error-creation.md), [handling](references/error-handling.md), and [wrapping](references/error-wrapping.md).
