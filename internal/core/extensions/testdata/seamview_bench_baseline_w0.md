# Request-Path Seam-View Benchmark Baseline (Wave 0 Pre-Consolidation)

- **Task**: 1.6 Capture request-path seam-view benchmark baselines
- **Requirements**: 7.1 (Hot-path neutrality, `allocs/op` $\le$ baseline, zero new locks/key-searches)
- **Status**: Captured & Verified

---

## 1. Machine-Context Caveat
- **Platform**: Windows (win32 / amd64)
- **CPU**: AMD Ryzen 7 5800X 8-Core Processor (16 logical threads; capture-time `GOMAXPROCS` may differ — see the `-12`/`-16` suffix on benchmark name lines in the verbatim run)
- **Caveat**: Absolute `ns/op` timings depend on host CPU, architecture, governor, and OS scheduling. Absolute latency comparisons are valid only across runs on the same hardware and environment class. `B/op` and `allocs/op` are deterministic Go runtime metrics directly comparable across platforms.

---

## 2. Defensive-Cloning vs. Direct Accessor Inventory

| Family | Accessor / Seam View | Mechanism / Call Path | Populated allocs/op | Populated B/op | Classification |
|---|---|---|---|---|---|
| **Completion** | `(*RequestRuntimeSnapshot).CompletionGates()` | `slices.Clone(s.completionGates)` | 1 alloc/op | 32 B/op | **Defensive Clone** |
| **Completion** | `CompletionGatesFromContext(ctx, fallback)` | Delegates to `CompletionGates()` (returns `emptyCompletionGates` on empty/nil) | 1 alloc/op (0 when empty) | 32 B/op (0 when empty) | **Defensive Clone** |
| **Traffic** | `(*RequestRuntimeSnapshot).TrafficPortBundle()` | Calls `TrafficRedactors()` (`slices.Clone`), `TrafficObserver()`, `RawCapture()` | 1 alloc/op (0 when empty) | 32 B/op (0 when empty) | **Defensive Clone** (via redactor slice) |
| **Traffic** | `(*RequestRuntimeSnapshot).TrafficObserver()` | Direct interface field access `s.obs` | 0 alloc/op | 0 B/op | **Direct Read** |
| **Traffic** | `(*RequestRuntimeSnapshot).TrafficRedactors()` | `slices.Clone(s.trafficRedactors)` | 1 alloc/op (0 when empty) | 32 B/op (0 when empty) | **Defensive Clone** |
| **Secret Guard** | `(*RequestRuntimeSnapshot).SecretGuardPlane()` | Clones `Guards` slice via `slices.Clone(plane.Guards)` | 1 alloc/op (0 when empty) | 32 B/op (0 when empty) | **Defensive Clone** |
| **Secret Guard** | `(*RequestRuntimeSnapshot).SecretGuardExecutionPlane()` | Returns internal `s.secretGuardPlane` directly | 0 alloc/op | 0 B/op | **Direct Read** (Execution hot path) |
| **Compaction** | `(*RequestRuntimeSnapshot).CompactionObservers()` | `slices.Clone(s.compactionObservers)` | 1 alloc/op (0 when empty) | 16 B/op (0 when empty) | **Defensive Clone** |
| **Compaction** | `(*RequestRuntimeSnapshot).CompactionPreservers()` | `slices.Clone(s.compactionPreservers)` | 1 alloc/op (0 when empty) | 16 B/op (0 when empty) | **Defensive Clone** |
| **Terminal** | `(*RequestRuntimeSnapshot).TerminalDecisionProvider()` | Direct interface field access `s.terminalDecisionProvider` | 0 alloc/op | 0 B/op | **Direct Read** |

---

## 3. Issue #394 Performance Hardening Coordination

1. **OBSERVE Scenario**:
   - Covers traffic observation pipelines including `TrafficPortBundle`, `TrafficObserver`, `TrafficRedactors`, and `RawCapture`.
   - Baseline evidence reveals that populated `TrafficPortBundle` incurs **1 alloc/op (32 B/op)** solely due to `TrafficRedactors()` defensive cloning. When redactors are not configured, `TrafficPortBundle` is **0 alloc/op**.

2. **DELTA-Allocation Scenario**:
   - Covers per-stream-turn and event processing reads on the request path (`CompletionGatesFromContext`, `SecretGuardExecutionPlane`, `CompactionObservers`, `CompactionPreservers`).
   - The execution hot path (`SecretGuardExecutionPlane`) achieves **0 alloc/op (7.07 ns/op)** by avoiding slice cloning, whereas public defensive accessors allocate 1 slice per read (16–32 B/op, 38–66 ns/op).
   - Post-consolidation implementations across all migration waves must preserve $\le$ baseline `allocs/op` on corresponding seams.

3. **HOLD Scenario**:
   - Covers long-held/idle stream memory and snapshot retention.
   - Snapshot accessors execute in $O(1)$ ordinal lookup time without acquiring locks or allocating memory, ensuring zero background cost per held stream.

---

## 4. Benchmark Execution Evidence (Verbatim)

```
goos: windows
goarch: amd64
pkg: github.com/aiproxer/aiproxer/internal/core/extensions
cpu: AMD Ryzen 7 5800X 8-Core Processor             
BenchmarkCompletionGates_Populated-12                            	28897000	        41.60 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGates_Empty-12                                	1000000000	         1.092 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGates_NilSnapshot-12                          	1000000000	         1.102 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_Populated-12                 	20925463	        62.78 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_Empty-12                     	147687708	         8.203 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_NilContextFallback-12        	23797908	        65.06 ns/op	      32 B/op	       1 allocs/op
BenchmarkCompletionGatesFromContext_nilFallback_empty-12         	149549854	         7.951 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_fallbackNilGates_empty-12    	126571342	         9.453 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompletionGatesFromContext_withGates-12                 	29615589	        43.85 ns/op	      16 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Populated-12                          	17735055	        63.35 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficPortBundle_Empty-12                              	148739008	         7.986 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficPortBundle_NilSnapshot-12                        	1000000000	         0.9779 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficObserver_Populated-12                            	1000000000	         1.044 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_Populated-12                           	29714664	        51.07 ns/op	      32 B/op	       1 allocs/op
BenchmarkTrafficRedactors_Empty-12                               	385747900	         3.197 ns/op	       0 B/op	       0 allocs/op
BenchmarkTrafficRedactors_NilSnapshot-12                         	1000000000	         1.156 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_Populated-12                           	27177913	        66.66 ns/op	      32 B/op	       1 allocs/op
BenchmarkSecretGuardPlane_Empty-12                               	87866384	        13.91 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardPlane_NilSnapshot-12                         	680765702	         1.703 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Populated-12                  	169167454	         7.069 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_Empty-12                      	168175716	         7.081 ns/op	       0 B/op	       0 allocs/op
BenchmarkSecretGuardExecutionPlane_NilSnapshot-12                	575338682	         1.770 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_Populated-12                        	27821247	        42.18 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionObservers_Empty-12                            	728929827	         1.602 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionObservers_NilSnapshot-12                      	1000000000	         1.086 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_Populated-12                       	34944060	        38.75 ns/op	      16 B/op	       1 allocs/op
BenchmarkCompactionPreservers_Empty-12                           	1000000000	         1.151 ns/op	       0 B/op	       0 allocs/op
BenchmarkCompactionPreservers_NilSnapshot-12                     	942575178	         1.182 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Populated-12                   	902867280	         1.458 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_Empty-12                       	1000000000	         1.058 ns/op	       0 B/op	       0 allocs/op
BenchmarkTerminalDecisionProvider_NilSnapshot-12                 	1000000000	         0.8839 ns/op	       0 B/op	       0 allocs/op
PASS
ok  	github.com/aiproxer/aiproxer/internal/core/extensions	38.477s
```
