# Slice and map safety

Slices are views over arrays. A subslice and its original may observe each other’s writes, and `append` may reuse the backing array. Clone data that crosses an ownership boundary or cap a view with `s[:len(s):len(s)]` when preventing growth overwrite is the goal.

```go
func Snapshot(in []byte) []byte { return bytes.Clone(in) }
```

Maps are reference-like: assigning a map variable does not clone its entries. Clone keys and values as needed for independent mutation. Map iteration order is unspecified; sort keys for stable output.

Concurrent read-only map access is safe when no goroutine mutates the map. Concurrent read/write and write/write access is not safe; use a mutex, ownership confinement, channels, or an appropriate synchronized map. Do not infer safety from a race-free test that never exercises concurrent mutation.

Check indexes and lengths before slicing. Be explicit about whether a function retains a slice/map after return, whether callers may mutate it, and whether nil versus empty is meaningful in a serialized contract.
