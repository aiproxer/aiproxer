# Types, interfaces, and errors

Type names are nouns (`Decoder`, `UserID`, `ParseError`) and describe a domain concept or behavior. Interface names may use an `-er` suffix for a single clear operation (`Reader`, `Flusher`), but the suffix is not required and does not make a type satisfy an interface.

Sentinels use `Err` plus a stable condition (`ErrNotFound`). Custom error types name the failure and expose fields only when callers need structured classification. Use `errors.Is`/`errors.As`; never require callers to parse an error string.

Keep interfaces small and define them near their consumer. Use a compile-time assertion when a type must implement an external contract:

```go
var _ io.Reader = (*Decoder)(nil)
```

A method named `Read` with a different signature is simply a method named `Read`; method names alone do not establish substitutability.
