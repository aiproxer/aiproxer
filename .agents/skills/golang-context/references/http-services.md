# Context in HTTP services

`http.Request.Context()` is canceled when the client disconnects, the request is canceled, or the handler returns. Pass it to downstream calls that should stop with the request. `http.NewRequestWithContext` attaches the signal to an outbound request; do not mutate a request’s context in place.

Set server-level timeouts (`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`) according to the service contract. A handler can derive a shorter operation deadline when needed and must call its cancel function.

```go
func handler(w http.ResponseWriter, r *http.Request) {
    ctx, cancel := context.WithTimeout(r.Context(), 800*time.Millisecond)
    defer cancel()

    result, err := service.Lookup(ctx, r.URL.Query().Get("id"))
    if err != nil {
        writePublicError(w, err)
        return
    }
    render(w, result)
}
```

Do not use a request context for work that must outlive the response. Submit a bounded job to an owned queue and copy only required metadata. On shutdown, stop accepting work, cancel workers, drain or reject queued jobs according to policy, and wait for completion.

Do not log raw context values or request headers indiscriminately; redact credentials and personal data. Context cancellation is not authorization—validate authentication and request state separately.
