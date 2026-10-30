package state

import "errors"

// ErrNotConfigured means no state service is bound for this execution snapshot (task 4.1).
var ErrNotConfigured = errors.New("aipsdk/state: state store not configured")

// ErrMissingExecutionContext means the context has no execution views attached (or required ids are empty)
// for request/session/principal scoped operations (tasks 6–6.1).
var ErrMissingExecutionContext = errors.New("aipsdk/state: missing execution context for this scope")

// ErrMissingPrincipal means principal-scoped operations were called without a non-empty principal id (tasks 6–6.1).
var ErrMissingPrincipal = errors.New("aipsdk/state: principal scope requires authenticated principal id")
