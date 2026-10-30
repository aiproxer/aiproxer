// Package metering defines provider-neutral public contracts for dual-plane
// metering facts, quantities, and journal append/query ports.
//
// These types are storage- and transport-agnostic. Implementations live in
// internal packages; enterprise and OSS adapters may depend on this package
// without importing internal/core.
//
// Boundary rules:
//   - Must not import internal/*, database/sql, net/http, or provider SDKs.
//   - May import pkg/aipsdk/scope for safe principal attribution on facts.
//
// Compatibility (requirement 12.8): see CompatibilityPolicy and UnknownEnum.
// Validate() rejects unknown enums for local strict construction; wire decode
// may preserve unknowns via IsKnown/UnknownEnum without mapping them to known
// constants. Optional fields and unknown JSON keys are additive.
package metering
