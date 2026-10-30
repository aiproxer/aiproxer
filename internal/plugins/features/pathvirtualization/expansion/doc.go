// Package expansion implements the feature's INBOUND pass: the model-to-client path
// expansion finalizer of design.md "7. Path Expansion Finalizer".
//
// The two outbound passes in this feature virtualize real paths into the fixed V1
// alias before a backend sees them. This package is the other half. A model that
// reads such a path and emits a tool call may hand the alias straight back, and that
// alias must become the real project root again BEFORE the client - and therefore
// before any filesystem safety check, tool policy, or tool reactor - can act on it
// (requirements.md 4.1, 4.3, 5.1). An alias that reached client tool execution would
// be a path into a namespace that exists only in the proxy's spelling, which is the
// exact failure this feature exists to prevent.
//
// The pass is a generic toolcall.Finalizer, so it plugs into the SDK's existing
// finalizer plane. That means it never re-implements any of the machinery around it:
//
//   - the assembler buffers the COMPLETE argument document, and buffers it up to the
//     bound this pass declares, so the pass decides on one whole value rather than on
//     stream fragments (requirements.md 4.2, 4.5). The bound is a declaration, and
//     the assembler is what enforces it;
//   - the assembler also synthesizes the canonical rewritten lifecycle from the
//     document this pass publishes, so there is no second event-shaping rule here
//     (design.md section 7 step 10);
//   - tool policy and tool reactors run after finalization and therefore observe real
//     paths, not aliases (requirements.md 4.3, design.md section 7 step 11).
//
// Everything the pass actually decides is pure and derived per call:
//
//   - the workspace-bound mapping comes from meta.Workspace.ProjectRoot on every call,
//     which is the same authoritative view the outbound attempt transform reads. It is
//     never cached, so a changed root derives a different workspace tag immediately and
//     an alias a provider retained across the change fails closed instead of rebinding
//     (requirements.md 6.1, 6.5, design.md "Continuity and State");
//   - selectors come from the compiled exact-name profile layers and, optionally, from
//     the tool's own declared schema. Nothing else names a location, so an unknown tool
//     resolves to no selector and its arguments are never inspected
//     (requirements.md 3.5, 3.6, 4.9);
//   - the mutation is the SHARED byte splice both directions use, so requirement 4.8's
//     "preserve JSON validity and all non-selected argument fields" holds literally:
//     member order, duplicate keys, number spelling, escapes, whitespace, and
//     empty-versus-null presence all survive.
//
// The pass performs no filesystem I/O, holds no mutable state, and is safe to share
// across every request of a generation. The nil pointer and a nil resolver are both
// safe and both resolve to "nothing proven", which is the required answer rather than
// a failure.
package expansion
