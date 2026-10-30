# Slice behavior

`len` is the visible element count and `cap` is the maximum length available before a new backing array may be allocated. `append` can mutate an array shared with another slice. Clip capacity when the contract only needs to prevent append from overwriting elements beyond the view. Clone when writes to existing elements must be isolated; a clone is shallow, so pointers, maps, or slices inside elements still refer to shared data.

```go
view := data[:n:n]       // later append cannot overwrite data beyond n
copyForCaller := slices.Clone(data[:n])
```

A nil slice and an allocated empty slice both have length zero but may differ in JSON, reflection, and API semantics. Preserve that distinction intentionally. Growth strategy and allocation sizes are runtime details; measure rather than relying on a fixed capacity multiplier.

For a focused review, trace both element writes and append through every alias. Capacity clipping retains the backing array, so it does not release a large retained buffer. Check whether filtering/deletion clears removed references and whether callers keep using the old slice length. Verify helper behavior against the supported Go version.
