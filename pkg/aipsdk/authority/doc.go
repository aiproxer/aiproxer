// Package authority defines public request- and attempt-level authority
// provider contracts, decisions, safe evidence, and concurrency lease DTOs.
//
// Import DAG: authority → economics → metering; authority also uses
// policydecision and controlplane for EvidenceSink (no cycles).
// Implementations and stores live outside this package (Phase 6+ / 8).
//
// QuotaPolicy is an optional nonfinancial authority contract for persisted
// provider account-window gauges. It retains the provider account/pool/window
// reset binding, freshness and evidence status, but never represents money,
// provider request debits, payable postings or customer credit.
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - Must not reference Executor or runtimebundle types.
//
// Observers vs authorities (requirement 12.7): fail-open usage/traffic
// observers remain supported via ProviderKindObserver descriptors, but must
// not implement RequestProvider or AttemptProvider for strict admission.
// StrengthRequired is reserved for ProviderKindAuthority.
//
// Compatibility (requirement 12.8): follows metering.CompatibilityPolicy —
// Validate rejects unknown enums for local enforcement; additive wire decode
// preserves unrecognized values via IsKnown/UnknownEnum.
package authority
