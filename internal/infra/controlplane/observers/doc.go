// Package observers hosts the control-plane source adapters that fan existing
// runtime evidence seams (auth event sink, policy/usage observers, secure-
// session store, B2BUA store) into the core control-plane recorder without
// requiring those seams to understand the query capability (design "Source
// Adapters"; requirements 1.1–1.6, 3.1, 5.1–5.7, 8.1–8.6, 10.7).
//
// Adapter rules enforced here:
//   - Existing sink/observer/store behavior is preserved: adapters delegate to
//     the existing implementation and never change routing, policy, usage, or
//     session outcomes.
//   - Recording is best-effort by default. Required pre-work fail-closed
//     behavior applies only at safe pre-upstream points and only when the
//     existing source delivery policy is fail-closed (auth) or the lifecycle
//     category is configured required pre-work (secure-session create/audit).
//   - Post-output recording failures never request retry, failover, or
//     replacement (requirement 5.3, 10.7). Adapter-level only.
//   - Source event keys are deterministic and never hash raw payloads, headers,
//     tokens, or provider wire data.
//   - No provider SDK, HTTP, SQL/Bun, runtimebundle, or frontend wire imports.
package observers
