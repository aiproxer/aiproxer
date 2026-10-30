# Research Log: proxy-memory-model-policy

## Summary

Discovery for the per-role model control, the operator inventory and locks, the compiled-actual-target enforcement and the settings surface. The findings that shaped the design:

- **The feature cannot resolve the final target itself.** The alias resolver, the default backend and the known-backend set live on the runtime executor, which is built after the feature host compiles, and the feature may not import core implementations. A narrow, feature-owned port bound by the composition root is the smallest reachable seam, and it has a source-backed precedent.
- **The capability facts are coarser than the policy.** The backend capability set carries a boolean reasoning capability and the model catalog a tri-state per-model reasoning fact; neither states which effort **values** a provider supports. An operator inventory is therefore a policy constraint, never provider-verified support.
- **The effort is stripped in two places today**, and the current sentinel fixture declares only streaming and tools — a fixture property, not evidence about production backends.

## Research Log

### Existing contracts (located, not invented)
| Fact | Source |
|---|---|
| The selector compile: trim → alias resolve → parse → model-only defaulting | the routing compile entry point |
| The alias resolver: the first matching pattern wins | the routing alias resolver |
| The route parameters are merged per attempt, after the candidate is chosen, and they **override the call's options, the reasoning effort included** | the runtime attempt path, with the route-parameter merge itself and its own tests |
| A non-empty reasoning effort implies the reasoning capability; the negotiation treats reasoning as a **soft, downgradable** capability and everything else as a hard reject | the canonical capability negotiation |
| The negotiated downgrades **strip** the effort; the two enforcement hooks are the pre-open candidate admission and the post-request transform | the canonical downgrade application and the two runtime hooks |
| The backend capability set is a **set of capabilities, not a value vocabulary**; each backend has static capabilities and a dynamic resolution | the per-backend capability contract |
| The model catalog carries a **tri-state** per-model reasoning fact (unknown / unsupported / supported), and the effective capabilities are the intersection of the catalog and the backend capabilities | the model catalog facts and the resolver |
| The attempt evidence records the backend, the model and the reasoning effort of the opened attempt | the attempt-evidence record |
| The closest precedent for an admin surface: a command-service port plus a **selector-validator port**, bound generation-bound in the composition root from the aliases, the default backend, the known backends, the execution resolver and the composition policy, and served behind the request-plane access-auth stack | the route-override admin store and its generation binding; the admin mount |
| The worker claims a session, then processes it; the phase-2 path claims a bank, then processes it; the collect path submits to the background client | the feature worker, phase 1 and phase 2 claim-and-process paths, and the collect path |
| The billing submission happens **inside** the core executor**, strictly after the auxiliary request is queued; the billing call identity is minted in core and the leg usage carries the backend and model | the runtime settlement and the leg usage record |

### Consequences for the design
- The effective settings must be frozen in the worker **immediately after the claim** — before the request is built and before it is submitted — because the billing submission happens deeper in the executor, after the queue. The worker queue and the auxiliary scheduler queue are different queues.
- The "final compiled actual target" must include the route parameters, because those can override the effort after the candidate is chosen. A check that ignores them validates a target that never serves the call.

### The capability gap (stated, not papered over)
The per-provider **effort values** exist in neither the backend capability set nor the model catalog. The verification of a chosen effort therefore needs an existing runtime surface to reuse or a narrow extension to add; this spec marks that as a **pending dependency** for the slice that enforces it, does not invent a vocabulary, and does not claim a closed global list of supported values.

### Budget facts (measured locally, with a drift warning)
- The Requirement 11.5 net shrinkage floor is `-791`; the measured convergence on this worktree is `-793` — two lines of headroom, not a free budget. The affected surfaces are the runtime bundle, the runtime host, the standard HTTP layer, the standard command and the public runtime facade.
- The "Proxy memory" path overlay is measured at 614 against a cap of 639, so 25 lines are available; the overlay table is extensible by path marker, and overlays must stay pairwise disjoint.
- The new composition-root file lands on an affected surface and is not currently credited, so it must be credited by **extending the existing marker** and the cap re-pinned with the measured value. An excess over the headroom goes to the owner as a separate budget approval; no marker is widened to hide a delta.
- *Drift warning: these numbers were measured locally on this worktree at the recorded baseline; they are not a statement about any remote ref, and the measurement must be repeated in the slice that changes those surfaces.*

### Sources
- Working tree at `a27ae0bab` (worktree `fix/proxy-memory-review-corrective`).
- The model-policy discovery report retained in the controller's external task evidence; its source-backed findings are summarized above.
- `.kiro/steering/{product,delivery,structure,testing}.md`, `VISION.md`, `AGENTS.md`, `.kiro/AGENTS.md`.

## Activation status (2026-10-21)

- The owner **approved** this scope and enabled the remaining tasks ("Proceed with all remaining tasks!"). `spec.json` carries the approval, the owner context and the date.
- The **role registration** and the **bank-claim fence** are implemented and reviewed in the archived spec `.kiro/specs/archive/proxy-memory-runtime-corrections` (role registration `4e2d588fe`, bank-claim fence `0b2d339ee`): the background phases now execute on the real path, so the cases in this spec have a subject. They are not to be redone.
- The **operator's inventory is policy, never provider-verified support**: the backend capability set carries a boolean reasoning capability and the model catalog a tri-state per-model fact, and neither states which effort values a provider supports. The allowed efforts are therefore declared per phase and per target, and the runtime capability surface for effort values must be confirmed or extended before the slice is complete.
- **Base:** implementation runs on this branch's own base `0b2d339ee` (inherited unshipped memory feature, archived runtime corrections). A rebase onto `main` is not a precondition, and the land is gated on the foundation reconciliation (#1287) and the owner's merge authorization.
- **Split state, checked locally at `b4587e66`:** only the workload-identity allowlist is carved to the usage-evidence package; the money files are still in the open-source billing package, so the money domain has **not** moved. Re-check the current split branch before writing code.
