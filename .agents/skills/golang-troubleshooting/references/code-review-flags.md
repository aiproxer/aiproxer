# Review triage flags

These patterns are prompts to investigate, not automatic defects:

| Pattern | Investigate |
| --- | --- |
| Ignored error or unchecked `Close` | Is failure possible, and which layer owns it? |
| Goroutine without owner/shutdown | What cancels it, bounds it, and waits for it? |
| Lock held across I/O or callbacks | Could it deadlock or serialize unrelated work? |
| `time.After` in a hot loop | Allocation/timer churn under the real workload; reuse a timer if measured |
| Slice or map retained/returned | Could aliasing or a large backing array escape? |
| Lexical path-prefix authorization | `..`, separators, symlinks, case, and platform behavior |
| Broad `recover` | Is the process continuing with invalid state or hiding a bug? |

Confirm with callers, tests, the race detector, profiles, or a minimal reproduction before changing code.
