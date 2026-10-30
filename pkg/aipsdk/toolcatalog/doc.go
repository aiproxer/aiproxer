// Package toolcatalog defines the tool catalog filter stage (design §4, R9).
// Filters run after submit hooks and before request-wide transforms; they may remove or
// annotate tool definitions and must leave [aipapi.Call.ToolChoice] consistent with the
// remaining tool list (use [aipapi.ReconcileToolChoiceAfterToolListChange] when mutating Tools).
package toolcatalog
