// Package processhost owns the project-selected supervised backend-plugin
// process host: lazy activation, process-model ownership, peer-gated local
// channels, generation invalidation, and composition BuildResult cleanup.
//
// Platform honesty: production Windows/Darwin launch and channel factories fail
// closed until an approved exact-identity profile is runtime-proven. Linux
// provides source/compile descriptor-launch and private UDS peercred paths.
//
// # Diagnostic output
//
// The host drains every connector's standard streams under one diagnostic
// budget, so a connector that floods the host is terminated instead of
// running unbounded. The drain costs no operating-system threads on Linux,
// where the runtime poller reads the pipes. On Windows the pipes are
// synchronous, so each live connector occupies one operating-system thread
// per drained stream: two threads per connector. A Windows deployment must
// therefore size its thread budget for two threads per live connector, and
// the connector count stays bounded by the caller's instance budget.
package processhost
