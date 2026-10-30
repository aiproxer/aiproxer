// Package economics defines provider-neutral public contracts for money,
// independent customer/operator rating, conservative exposure assumptions,
// immutable version snapshot references, and versioned snapshot sources.
//
// The route-carriage contracts (RouteCandidate, RouteTariffBinding,
// RouteTariffMode, RouteTariffCarrier, ReferenceFacts) are additive and
// opt-in: a quoter that declares RouteTariffCarrier receives the ordered
// planned-route occurrence list and returns one frozen binding per distinct
// canonical route key; a quoter that additionally implements ReferenceFacts
// supplies the frozen customer-pricing, charge-policy, and per-route
// operator-rate references. Quoters that declare neither keep the version-1
// behavior unchanged.
//
// Import DAG: authority → economics → metering (no cycles).
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - May import pkg/aipsdk/metering for EconomicPerspective and related enums.
//
// Compatibility (requirement 12.8): follows metering.CompatibilityPolicy —
// Validate/IsKnown reject or detect unknown enums for local enforcement;
// additive wire decode preserves unrecognized values and ignores unknown keys.
package economics
