# Containers and selection

Use a slice for a sequence, a map for keyed lookup, and an array when fixed length is part of the type. Use `container/heap` for a priority queue, `container/list` only when stable list-node insertion/removal is worth pointer overhead, and `container/ring` for a fixed circular structure. A slice plus an index is often clearer and faster than a linked list.

For a queue, define whether it is bounded, whether zero values are valid, and what full/empty operations do. For a set, use `map[T]struct{}` or `map[T]bool` according to whether membership state needs a value. Sort keys before deterministic output; map iteration order is unspecified.

Use `slices.Clone` when element storage must be independent or when a retained small result must stop keeping a large backing array alive. It copies elements shallowly: referenced mutable data still needs an ownership contract or a deliberate deeper copy. `slices.Clip` only sets capacity to length; it does not release the backing array or isolate writes to existing elements. Capacity hints should come from known sizes or measurements, not guessed constants.
