# ADR 0002: Immutable baseline call and per-attempt derivation

## Status

Accepted (stage two).

## Context

Per-attempt capability negotiation, request-part hooks, and route merges must not mutate the logical client request used as the source for later B-legs. Sticky mutations across retries break cross-backend failover semantics.

## Decision

- After submit hooks, the executor captures an immutable **`baseline`** (`aipapi.CloneCall`).
- Each routing attempt uses **`attempt := aipapi.CloneCall(baseline)`**, then applies negotiation downgrades and request-part hooks **only** to `attempt`.
- Recv-phase replacement re-derives attempts from the same baseline.

## Consequences

- Hooks and negotiation see a fresh attempt copy every open; diagnostics correlate attempts via B-leg records.
- Tests must assert baseline fields (for example reasoning options) remain visible on later attempts when a prior attempt downgraded or failed pre-output.
