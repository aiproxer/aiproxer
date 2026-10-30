package metering

// Compatibility documents additive versioning rules for public metering
// contracts (requirement 12.8). Economics and authority packages follow the
// same policy for their public enums and optional fields.
//
// Enum values:
//   - Local construction and Validate() reject unknown enum strings so strict
//     admission cannot silently reinterpret new semantics as a known value.
//   - Additive wire decode (JSON from a newer peer) MUST preserve unrecognized
//     non-empty enum strings for round-trip; consumers MUST NOT map them onto a
//     documented constant. Use IsKnown() to detect unknowns without failing the
//     entire decode when only observation/logging is required.
//
// Optional fields:
//   - Omitted optional JSON fields mean absent / zero value.
//   - Decoders MUST ignore unknown JSON object keys (forward compatible).
//   - New optional fields may be added without breaking older consumers.
//
// Contract identity:
//   - Packages version additively; removing or redefining a documented enum
//     value or required field is a breaking change.
const CompatibilityPolicy = "additive_v1"

// UnknownEnum reports whether raw is a non-empty value that is not among the
// documented constants for a public enum. Callers use this when preserving
// wire unknowns (requirement 12.8) instead of calling Validate().
func UnknownEnum(raw string, known bool) bool {
	return raw != "" && !known
}
