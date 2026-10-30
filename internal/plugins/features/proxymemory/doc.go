// Package proxymemory implements proxy-side Codex memory (codex-rs/memories
// version 1) for AIProxer: per-request eligibility, project identity from
// request content, transcript capture, background extraction and
// consolidation, first-turn summary injection, and the memory-tool surface.
//
// Isolation contract: every memory artifact and every query is keyed by Owner
// (tenant, principal, opaque project key). Project values are opaque labels;
// this package never reads, resolves or writes a client filesystem path.
// Content-free discipline: nothing here logs or labels metrics with project
// paths, principal IDs, A-leg IDs, prompts or memory text.
//
// Boundary: the package is a feature plugin. It may import pkg/aipapi,
// pkg/aipsdk contracts, the standard library, gopkg.in/yaml.v3 and Prometheus,
// but never internal/core, internal/infra or internal/standardplugins; the
// archtest closure rule fails the build otherwise.
package proxymemory
