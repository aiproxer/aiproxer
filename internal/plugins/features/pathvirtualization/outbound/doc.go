// Package outbound implements the feature's TWO outbound passes: the candidate attempt
// transform of design.md "Existing Architecture and Placement" step 2, and the
// idempotent request-part hook of step 4.
//
// The attempt transform virtualizes eligible path-bearing tool history before candidate
// sizing, context eligibility, and token-accounting preflight observe it
// (requirements.md 5.3). The request-part hook reapplies the same rewrite after the
// later request shaping that sits between the candidate attempt stage and the
// backend-bound request, so the final conversation-view reassertion and candidate
// adaptation cannot hand PTB or Backend.Open a real path-bearing tool history
// (requirements.md 5.2, 5.4).
//
// It exists as a subpackage of the lexical core for the same reason the rewriter and
// the schema-inference step do. The core in internal/plugins/features/pathvirtualization
// keeps no repository import at all, and its host-authority guard proves it; a
// contribution that reads pkg/aipapi and pkg/aipsdk must therefore live beside that
// core rather than inside it, or the guard has to be weakened.
//
// The package is deliberately thin. It owns exactly four things and delegates
// everything else:
//
//   - it derives the workspace-bound mapping from the authoritative
//     Workspace.ProjectRoot, using the core's existing pure DeriveMapping, and never
//     from anything else the stage carries;
//   - it hands that mapping to the ONE pure canonical outbound rewriter, in the rollout
//     mode the generation was compiled with, so both passes run identical detection
//     code;
//   - it publishes the rewriter's result onto the canonical call and records a bounded,
//     content-free report;
//   - it decides nothing else. Neither pass reads a route field, a candidate identity,
//     or a backend identity.
//
// The two passes differ in exactly one input and in one position, and both differences
// are forced rather than chosen:
//
//   - the attempt transform reads the workspace projection the runtime already pinned
//     onto its attempt metadata, while the hook reads that same PINNED view out of the
//     public SDK context projection the runtime publishes alongside session, scope, and
//     principal. The hook cannot read it from its own metadata - sdkhooks.PartMeta
//     carries only trace, A-leg, B-leg, attempt ordinal, and backend identity - and it
//     deliberately does not resolve the view a second time, which is what keeps
//     requirement 5.6's one-alias-per-turn property true by construction: two passes
//     reading one pin cannot disagree, whereas two passes reading two resolutions can;
//   - the hook sorts last inside the request-part chain, because design.md places step 4
//     after later request shaping and a pass that ran earlier could not observe a real
//     path an earlier participant had just restored.
//
// Four properties are load-bearing and each has a test:
//
//   - the passes mutate ONLY the call they were handed. They read no routing field, no
//     candidate identity, and no backend identity, so they cannot change route
//     identity, model or backend selection, retry/failover authority, or billing
//     authority (requirements.md 5.5, 5.6, 5.7);
//   - an unexpected transformation failure FAILS OPEN to the real path with a bounded
//     reason, because in the outbound direction no virtual alias is ever client-visible:
//     aliases go to the BACKEND, and the real path is the value the backend must be able
//     to reason about. The rewriter already returns the input call on failure, and
//     neither pass publishes any value at all on that path, so a partially rewritten
//     request cannot escape;
//   - reapplication is a no-op, because the rewriter's publication is a byte splice that
//     only fires on a segment-boundary real-root prefix (requirements.md 2.9). That is
//     what makes the second pass free on the common path, and what lets it be a genuine
//     late pass rather than a second opinion;
//   - the AUTHORITY IS THE PIN, and re-derivation is a pure function of it. Both passes
//     read one per-turn snapshot the runtime published - the early one out of its attempt
//     metadata, the late one out of the public SDK context projection - and neither
//     resolves a root of its own, so there is no second fact for them to disagree about
//     (requirements.md 5.6). It is worth being precise about which half of that is the
//     safety property, because the inverse framing is a real mistake: "re-derived on every
//     use, so nothing can go stale" has the reasoning backwards. A CACHE is the thing that
//     can disagree with a turn's root, because a stored alias outlives the root that
//     produced it. Re-derivation is safe here because it reads the pin rather than an
//     answer of its own, and requirement 6.2's restart-equivalence follows from that
//     purity rather than from the absence of stored state.
package outbound
