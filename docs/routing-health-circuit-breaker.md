# Routing health: circuit breaker semantics

Configuration: `routing.health.circuit_breaker` in the runtime YAML (`enabled`, `failure_threshold`, `open_for`). Wiring: [`internal/infra/routinghealth/config_health.go`](../internal/infra/routinghealth/config_health.go) (`CandidateHealthFromConfig`) is called from [`internal/infra/runtimebundle/build.go`](../internal/infra/runtimebundle/build.go) when constructing the executor; it returns a [`internal/core/policy.CircuitBreaker`](../internal/core/policy/circuitbreaker.go) (as `policy.CandidateHealth`) for the executor’s `CandidateHealth` field when the breaker is enabled.

## What counts as a failure

The executor reports routing outcomes via [`recordAttempt`](../internal/core/runtime/executor.go) into [`CircuitBreaker.OnRoutingAttemptOutcome`](../internal/core/policy/circuitbreaker.go):

| Attempt outcome | Counts toward opening? |
| --- | --- |
| `AttemptSurfacedFailure` | Yes |
| `AttemptSwallowedFailure` | Yes (includes recoverable **pre-output** failures such as failed stream `Open` / early recv errors) |
| `AttemptSuccess` | No (resets consecutive failures for that candidate key) |
| `AttemptCancelled` | No (client disconnect / deadline on the attempt does not penalize the candidate in the breaker) |
| Other / unknown | No |

So **swallowed** pre-output failures **do** advance the breaker, not only surfaced failures. This matches the integration test [`TestExecutor_circuitBreakerSkipsAfterFailures`](../internal/core/runtime/executor_circuitbreaker_test.go) (recoverable pre-output uses swallowed outcomes).

Policy unit tests: [`internal/core/policy/circuitbreaker_test.go`](../internal/core/policy/circuitbreaker_test.go).

### Short streams / EOF without terminal event

If the backend stream ends with `io.EOF` before an `EventResponseFinished` event, the executor records `AttemptSurfacedFailure` for that candidate (see [`attempt_stream.go`](../internal/core/runtime/attempt_stream.go)). That outcome **counts toward opening** the breaker like other surfaced failures. Operators should treat chronic partial streams as routing-health noise unless backends are fixed to emit the canonical terminal event.

## Recovery model (no half-open probing)

The implementation is intentionally minimal:

- After enough consecutive failures for a candidate key, the key is **blocked** until `open_for` elapses from the block time (`Now` is injectable for tests).
- When the cooldown period passes, the key is eligible again **without** a separate half-open probe or gradual ramp-up. That policy is **not** implemented; if we add it later, it should be a separate ADR.

## Observability

Candidate health state is reflected in routing (unhealthy keys are excluded from planning). There is **no** dedicated admin JSON payload for raw breaker counters in this stage; use structured **`aip.route`** logs (when a logger is configured) and existing diagnostics/trace routes. Candidate keys are **instance-shaped** (e.g. `openai-primary:gpt-4o-mini`), matching backend instance ids in routing.

The in-process breaker keeps a bounded map of candidate keys (see `MaxTrackedKeys` / default in [`circuitbreaker.go`](../internal/core/policy/circuitbreaker.go)); when the cap is exceeded, idle keys are evicted first, then lowest-pressure keys.

## Exhausted failover

When **every** failover arm is unhealthy or excluded and no candidate remains, routing returns [`ErrNoEligibleCandidate`](../internal/core/routing/planner.go); the executor surfaces that error to the caller when there is no capability rejection to wrap.
