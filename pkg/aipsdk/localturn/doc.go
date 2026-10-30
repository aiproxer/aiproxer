// Package localturn defines the trusted extension contract for generic
// proxy-local turn handling.
//
// Handlers are explicitly contributed via FeatureBundle and frozen into an
// immutable runtime snapshot. Match is a pure examination of the normalized
// canonical ingress call; it may only claim complete message indexes with
// bounded reason codes. Handle returns bounded assistant text (≤64KiB) from
// which core constructs the canonical assistant message and local stream. No
// arbitrary streams or provider calls are produced by handlers.
//
// Handler.FailureMode uses pkg/aipsdk/hooks.FailureMode (FailOpen/FailClosed)
// applied to Match errors before claim; after claim all failures are fail-closed
// by the runtime. Package localturn does not import internal packages.
package localturn
