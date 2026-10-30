// Package safecast provides bounded conversions for numeric values at protocol boundaries.
package safecast

import "math"

// IntFromInt64Clamp converts v to int, clamping when v does not fit the platform int size.
// Use for telemetry and usage counters where silent truncation is worse than saturation.
func IntFromInt64Clamp(v int64) int {
	if v > int64(math.MaxInt) {
		return math.MaxInt
	}
	if v < int64(math.MinInt) {
		return math.MinInt
	}
	return int(v)
}

// Int32FromIntClamp converts v to int32, clamping to [math.MinInt32, math.MaxInt32].
// Use at provider boundaries after validation (e.g. aipapi bounds max_output_tokens) so
// static analyzers see an explicit bound even when invariants already hold.
func Int32FromIntClamp(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}
