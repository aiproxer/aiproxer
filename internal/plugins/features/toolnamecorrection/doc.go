// Package toolnamecorrection owns the standard-distribution tool-name-correction
// feature (issue #997): a completed tool call naming a tool the model was never
// offered is withheld and the turn is continued once with a hidden correction
// message, so the client sees only corrected output.
//
// The feature is opt-in and fail-open: its tool-call finalizer is contributed
// only when the operator enables the plugins.features row, and every rail
// decline or failure delivers the original call unchanged.
package toolnamecorrection
