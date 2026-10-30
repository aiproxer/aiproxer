# Map behavior and representation

Go maps provide average constant-time lookup under ordinary workloads, but complexity and memory behavior are implementation details. Current Go releases use Swiss-table techniques (Go 1.24 and later); code must not depend on bucket layout, growth thresholds, iteration order, or hash details.

The zero value of a map is readable and iterable but cannot be assigned to. `delete` on a nil or absent key is safe. A map value can be copied, but copies refer to the same map data; clone when independent mutation is required.

Concurrent read-only access is valid when no goroutine mutates the map. Any mutation concurrent with reads or other writes requires a mutex, actor ownership, or a specialized synchronized structure. `sync.Map` has different typing and workload trade-offs; benchmark before choosing it.

For deterministic serialization, collect keys and sort them. For memory-sensitive code, measure retained keys/values and release a map by replacing it when bulk deletion leaves an unsuitable footprint; do not rely on undocumented compaction behavior.
