// Package safety provides internal crash-isolation helpers for turning recovered panics
// into typed errors with bounded, server-side metadata. It does not import HTTP, metrics, or
// transport packages; call sites in adapters add client-safe response shaping and logging.
package safety
