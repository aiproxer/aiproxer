// Package execctx attaches stable plugin-facing view snapshots to request context (tasks 4+).
// The principal field is filled from [github.com/aiproxer/aiproxer/pkg/aipsdk/execview]
// context values set at the transport edge, then published here; it does not import transport packages.
// It does not import provider SDKs.
package execctx
