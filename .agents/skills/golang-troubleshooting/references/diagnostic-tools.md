# Diagnostic tools

- `go test -run`, `-count`, `-failfast`, and a saved fixture narrow behavior.
- `go test -race` finds data races in executed code on supported targets.
- `go vet` runs the standard analyzers. Shadow analysis is a separate analyzer; install and invoke it explicitly if the repository uses it.
- `go build -gcflags='all=-m=2'` provides escape/inlining diagnostics; treat them as compiler evidence, not a performance verdict.
- Delve (`dlv`) helps inspect a reproducible state. Avoid attaching to a process without an operational safety plan.
- `go tool pprof` and `go tool trace` explain CPU, memory, blocking, mutex, scheduler, and execution behavior under a controlled workload.
- Documented `GODEBUG` settings can expose runtime behavior; verify names and semantics against the active Go release, and never enable noisy diagnostics indefinitely in production.

Capture command, toolchain, revision, platform, workload, and profile duration with every artifact so comparisons remain meaningful.
