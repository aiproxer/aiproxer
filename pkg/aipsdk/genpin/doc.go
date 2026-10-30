// Package genpin defines the narrow request-context contract for retaining
// runtime configuration generation ownership beyond an HTTP handler lease.
//
// RuntimeGenerationID here is the request-plane generation identity
// (runtimehost.GenerationMeta.ID). It is distinct from executable-policy /
// snapshot generation IDs and from model registry/catalog generations.
//
// Implementations live in composition (runtimehost); core runtime and
// terminal-work code consume only this package so they never import
// internal/infra/runtimehost.
package genpin
