# Creating errors

Use a stable sentinel when callers need identity:

```go
var ErrNotFound = errors.New("not found")
```

Use a custom type when callers need fields or `errors.As`:

```go
type ParseError struct {
    Offset int
    Err    error
}

func (e *ParseError) Error() string { return fmt.Sprintf("parse at %d: %v", e.Offset, e.Err) }
func (e *ParseError) Unwrap() error { return e.Err }
```

Keep messages stable enough for operators but never make a caller parse them. Include identifiers only when they are safe and useful; redact secrets and untrusted data in logs. Prefer a small domain error over a generic string when the boundary must map failures to status codes.

Return the original error when no context is added. Add operation context with `%w`, not `%v`, when callers must classify the cause. Use `errors.Join` for independent cleanup or validation failures and test the resulting `errors.Is`/`errors.As` behavior.
