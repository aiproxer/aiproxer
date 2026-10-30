// Package extensions publishes per-request extension runtime seams: the hook bus, plugin-facing
// service facades, and narrow views used by the executor without pulling concrete feature plugins
// into orchestration packages.
//
// # Grouped facade ([RequestRuntimeSnapshot])
//
// [RequestRuntimeSnapshot] is an intentional grouped facade (hexagonal spec task 5.1): one
// immutable binding per build for hook chains plus the service families extension stages consume
// together—state and auxiliary requests, traffic observation and raw capture, workspace resolution,
// session openers, tool catalog filters, request transforms, route hint providers, completion gates,
// and traffic redactors. Split a consumer onto a narrower interface (for example [CompletionGatesView]
// or [RequestRuntimeSnapshot.TrafficPortBundle]) only when it demonstrably depends on unrelated
// capabilities; otherwise keep the snapshot to avoid ceremony without coupling reduction.
//
// Composition roots construct snapshots via [NewRequestRuntimeSnapshot]; the executor attaches them
// with [WithRequestRuntimeSnapshot].
package extensions
