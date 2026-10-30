// Package domain contains the pure concurrency-lease policy model.
//
// It stays free of I/O, SQL, HTTP, runtime orchestration, and provider SDKs.
// The package owns rules, safe dimensions, lease identity, TTL/generation
// state transitions, and client-safe denial evidence.
package domain
