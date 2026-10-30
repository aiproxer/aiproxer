// Package hooks defines stable plugin contracts for submit, part, and tool-reactor hooks.
// The core runtime imports these interfaces; hook implementations live in feature plugins.
//
// Hook interface shape (intentional):
//   - SubmitHook, RequestPartHook, and ResponsePartHook each expose four methods: ID, Order,
//     a policy selector (FailureMode), and the handler. That is a deliberate trade-off: the
//     hook bus can sort, log, and branch on identity and policy without auxiliary registries or
//     reflection. Splitting metadata into smaller interfaces would scatter the contract across
//     multiple types the core would still need to satisfy together, without simplifying callers.
//   - ToolReactor is narrower (three methods) because its error semantics are selected by the
//     hook bus via [ToolReactorErrorPolicy], not a per-hook FailureMode method.
//
// pkg/aipsdk stays free of internal/runtime types; only aipapi shapes appear in hook signatures.
//
// Tool reactions are expressed with [aipapi.ToolEvent] (canonical stream tool lifecycle); they
// must not depend on provider SDK event types (R9, R15).
package hooks
