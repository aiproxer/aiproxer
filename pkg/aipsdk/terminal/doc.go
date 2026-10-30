// Package terminal defines provider-neutral public contracts for single
// terminal ownership and durable terminal-work kinds/states.
//
// These types are storage- and transport-agnostic. Request/attempt owner
// state machines live in internal/core/terminal; work-item lifecycle lives in
// internal/core/terminalwork. Enterprise and OSS adapters may depend on this
// package without importing internal/core.
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - Enums use IsKnown/Validate for local strict construction; unknown wire
//     values may be preserved by callers without mapping them to known constants.
//
// Design rules D8, D9, D13: one terminal outcome per request/attempt; required
// effects complete or record durable intent; never retry after committed output.
package terminal
