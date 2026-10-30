# Wrapping and classification

`%w` creates a wrapping edge that `errors.Is` and `errors.As` traverse:

```go
if err != nil {
    return fmt.Errorf("read config: %w", err)
}
```

Use `%v` only when deliberately rendering text and no caller should inspect the cause. Do not use it to hide details from an API consumer; public mapping belongs at the boundary.

`errors.Is(err, target)` checks the chain (and joined errors) using equality or an `Is` method. `errors.As` finds an assignable error type; pass it a pointer to the target variable. Preserve cancellation, deadline, permission, and domain sentinels through wrappers.

Use `errors.Join(err1, err2)` when both causes matter. It returns nil when all inputs are nil, and its text is a newline-joined rendering; callers should still classify with `Is`/`As` rather than parse the message. Do not join unrelated noise merely to avoid choosing an owner.

When translating an internal error to a public error, wrap only if the public consumer is trusted to receive the underlying classification. Otherwise create a safe public error and log the internal cause separately.
