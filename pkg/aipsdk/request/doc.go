// Package request defines the request-wide shaping stage contract (design §5, R12).
// Transforms run on the canonical [aipapi.Call] after submit hooks and tool catalog filtering,
// before per-attempt request-part hooks. Attempt-scoped identifiers are not populated in [RequestMeta]
// at this stage.
package request
