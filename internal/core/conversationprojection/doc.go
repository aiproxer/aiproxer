// Package conversationprojection implements the pure kernel for semantic message
// identity, never_backend exclusion filtering, deterministic projection/reassertion,
// anchor/provenance primitives, and immutable projection DTOs at the A-leg/B-leg boundary.
//
// Projection is deterministic and content-preserving for unexcluded messages.
// This package contains zero stores, writers, locks, reflection, or network/storage I/O.
package conversationprojection
