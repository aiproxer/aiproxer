// Package nonforwardable defines the trusted producer contract for tagging
// client-visible messages as never_backend.
//
// The Registrar is an explicitly constructed trusted service bound to an
// authoritative A-leg scope at composition time. It is never exposed through
// client frontends or a global service locator. All identifiers and reasons
// are bounded ascii identifiers; message identity is a replay-stable digest
// carried in MessageRef.
package nonforwardable
