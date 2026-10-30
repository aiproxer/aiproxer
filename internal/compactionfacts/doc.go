// Package compactionfacts provides protocol-neutral semantic facts extraction,
// deterministic item hashing, start-rule matching, and streaming fact builder
// primitives for compaction recognition without prompt retention.
//
// The package serves as the single internal source of truth for compaction facts
// shared between the canonical compaction detector (internal/infra/compactiondetect),
// execution runtime, and future streaming frontend proof compilation workers.
package compactionfacts
