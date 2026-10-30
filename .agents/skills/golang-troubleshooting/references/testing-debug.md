# Test-driven debugging

Turn the report into a failing test or minimal reproducer before changing production code. Use `go test -run '^TestName(?:/case)?$'`, `-count=20` for intermittent behavior, and `-race` for concurrent paths when supported. Keep inputs fixed and log only useful, redacted state.

For a flaky test, identify nondeterminism: time, goroutine scheduling, map iteration, shared fixtures, network, random seeds, or order dependence. Replace sleeps with synchronization or a controllable clock. `testing/synctest` can make timer/deadline behavior deterministic on toolchains that provide it.

After the fix, run the regression test repeatedly, the package tests, and the relevant broader gate. Keep the regression assertion at the public behavior that failed rather than at an implementation detail.
