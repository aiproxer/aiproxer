// Package compatibleparity holds deterministic canonical parity and
// instance-isolation fixtures for the three built-in compatible backend modes.
//
// Fixtures reuse pkg/aipapi Call/Event types and drive in-process httptest
// servers only. They never contact real providers or require real credentials.
//
// Task 1.4 freezes these fixtures and RED tests. Production construction that
// attaches tokenizer and per-instance admission (Tasks 3.x/4.x) is intentionally
// absent; isolation tests fail until that behavior exists.
package compatibleparity
