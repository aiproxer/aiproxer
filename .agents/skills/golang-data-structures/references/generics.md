# Generics

Use a type parameter when one algorithm should operate on several types and the constraint expresses the operations it needs:

```go
func Keys[K comparable, V any](m map[K]V) []K {
    keys := make([]K, 0, len(m))
    for key := range m { keys = append(keys, key) }
    return keys
}
```

This function does not promise ordering; callers that need stable output must sort with a suitable comparator. A `comparable` constraint permits map keys and `==`, but does not make values immutable or concurrency-safe.

Use an interface when the abstraction is behavior supplied at runtime (`io.Reader`, a service port) or when implementations are expected to vary independently. Do not replace every interface with `any`, and do not add a type parameter when it only saves one conversion or makes inference obscure.

Keep constraints minimal. A union of concrete types is appropriate for algorithms with representation-specific operations, not for modeling runtime polymorphism.
