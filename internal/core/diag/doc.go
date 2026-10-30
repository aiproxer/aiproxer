// Package diag provides request-scoped trace propagation, structured logging helpers,
// and minimal HTTP surfaces for health and B2BUA attempt diagnostics.
//
// Trace and A-leg identifiers are carried on context.Context for use by the executor
// and (in later tasks) frontend encoders and plugins via diag.TraceID / diag.ALegID.
//
// # Query-style read seams (requirement 7.6)
//
// Read-only admin, diagnostics, and reporting flows should use narrow query ports and stable read
// DTOs from [github.com/aiproxer/aiproxer/pkg/aipapi] instead of repository-shaped
// write abstractions when that is simpler. For example, [AttemptLoader] exposes only attempt row
// reads for the attempts HTTP handler; HTTP framing and JSON encoding stay in this package while
// orchestration types remain outside the handler contract.
package diag
