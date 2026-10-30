# Handling and exposing errors

Separate three decisions:

1. **Classify:** preserve or add context so callers can use `errors.Is`/`errors.As`.
2. **Handle:** recover, retry, translate, or clean up at the layer that owns that policy.
3. **Report:** log detailed internal context once at the boundary that has the right audience.

Do not log and return the same error at every layer. A lower layer may log a local diagnostic and return only when that diagnostic is the complete handling policy; otherwise let the boundary log. Cleanup errors deserve an explicit policy—return, join, or record them without masking the primary cause.

Formatting is not an information-security boundary. `%v` omits wrapping, but it does not make text safe; `%w` preserves cause, but it does not authorize exposing cause details. Map internal errors explicitly:

```go
func publicStatus(err error) (int, string) {
    switch {
    case errors.Is(err, context.Canceled):
        return 499, "request canceled"
    case errors.Is(err, ErrNotFound):
        return http.StatusNotFound, "not found"
    default:
        return http.StatusInternalServerError, "internal error"
    }
}
```

Log the detailed error with a request correlation ID and redaction policy. Never send stack traces, SQL, tokens, or raw user input to an untrusted client.

Recover only at a deliberate boundary such as an HTTP server or worker goroutine. Convert the recovered value to an internal error, record stack/context, and keep the process or worker state consistent. Do not recover ordinary errors.
