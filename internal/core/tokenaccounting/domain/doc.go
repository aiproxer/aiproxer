// Package domain reconciles already-emitted token usage into billing-plane choices.
//
// It is pure domain logic: it does not count tokens, price usage, call providers,
// persist state, or collect runtime streams.
//
// Reconciliation is deterministic by usage plane. Non-policy_reserved candidates
// win over reservations for the same plane; reservations are additive only when
// no actual candidate exists for that plane. Strict provider billing requires an
// authoritative provider_billable entry sourced from provider_reported or
// provider_count_api.
package domain
