# Nil safety

## Collection zero values

Nil slices support `len`, `range`, and `append`; nil maps support lookup, `len`, `range`, and `delete`, but assignment panics. Decide whether nil means “absent,” “not initialized,” or “empty” before normalizing a collection. JSON and reflection may distinguish nil from allocated empty values.

## Interfaces

An interface containing a typed nil pointer is non-nil:

```go
var p *bytes.Buffer
var v any = p
fmt.Println(v == nil) // false
```

Avoid returning a typed nil behind an interface unless that behavior is deliberate. Validate dependencies at construction and use typed accessors or explicit nil checks at boundaries.

## Errors and cleanup

A nil error means success; do not manufacture a non-nil interface that contains a nil pointer as an error. Check errors before dereferencing results, and define whether a cleanup error replaces, joins, or is merely recorded alongside a primary error.

## Concurrency

Nil channels block forever on send and receive and disable a select case; use that only as an intentional state switch. A nil function call panics. A nil pointer receiver is valid only if every method path handles nil explicitly and the contract says so.
