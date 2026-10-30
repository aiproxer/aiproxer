# Values and tracing metadata

Use an unexported key type so unrelated packages cannot collide:

```go
type requestKey struct{}

func withRequestID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, requestKey{}, id)
}

func requestID(ctx context.Context) (string, bool) {
    id, ok := ctx.Value(requestKey{}).(string)
    return id, ok
}
```

Prefer typed accessors over direct `Value` calls. Store small, immutable request metadata such as a correlation ID or authenticated principal when it is genuinely cross-cutting. Pass required inputs, loggers, clients, and configuration explicitly.

Do not put secrets, mutable shared state, large objects, or optional feature switches in context. A context value can outlive its request if copied into a queue; apply the same retention and privacy rules as any other stored data. Trace/span propagation should use the tracing library’s context APIs, and logging should redact identifiers at the sink or before emission.
