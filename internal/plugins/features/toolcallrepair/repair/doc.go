// Package toolcallrepair holds the deterministic native tool-call repair engine
// contract (ADR 0007, issue #152).
//
// Phase 3 provides offline JSON Schema compilation/validation, catalog indexing,
// and name normalization. Engine.Repair retains append-only JSON completion and
// adds bounded safe terminal-tail recovery: exactly one terminal comma may be
// removed after a complete value, while an exact final root property may receive
// only a schema-selected const, single-valued enum, or default. Every candidate
// is shape-preflighted and applicable rewrites receive mandatory post-validation.
//
// OutcomePass leaves ArgsJSON nil; callers (Phase 5 finalizer adapters) must replay
// the exact original buffered argument bytes rather than treating nil as empty JSON.
package repair
