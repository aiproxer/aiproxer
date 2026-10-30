// Package aipruntime is the public production composition facade for AIProxer.
//
// Closed enterprise modules construct a runtime through [Build] (requirements
// 12.1–12.5). Reload/status use [Runtime.Reload], [Runtime.ReloadStatus], and
// [ReloadControl] (16.1–16.2). [Runtime.ExecutorView] is generation-dispatching
// (16.12–16.13). Named accessors and [Runtime.Capabilities] report live wiring;
// executable generation identity is distinct from metadata-only publication.
package aipruntime
