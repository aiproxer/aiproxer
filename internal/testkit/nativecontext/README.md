# Native Context Evaluation

Run the deterministic four-mode harness from the repository root:

```text
go run ./internal/testkit/nativecontext/cmd/evaluate -json-only
```

The command emits one machine-readable JSON report on stdout. Without
`-json-only`, it also writes a concise summary to stderr. The report uses fixed
seed `104729`, the `native-context-fixed-v1` emulator fixture, paired
`baseline`, `reasoning_only`, `compaction_only`, and `full` results, numeric
baseline/full comparisons, and one-time compaction break-even turns.

The fixture is evaluation plumbing only. `quality_claim` is always
`observed_only`; it does not make provider or task-quality claims.
