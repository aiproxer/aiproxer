// Package compactiondetect implements the proxy-derived coding-agent session
// compaction detector: a concrete, process-owned detector that inspects the
// effective canonical baseline after an upstream B-leg opens and every
// canonical event actually released by the retry stream, and derives typed
// started/completed lifecycle observations for fail-open SDK observers.
//
// The detector is pure with respect to external I/O: it returns derived
// [compaction.Event] values for the runtime to dispatch after the detector
// lock is released. It stores only bounded hashes/counts/timestamps and
// transaction metadata keyed by the authoritative A-leg, never prompt content,
// and requires no database state.
package compactiondetect
