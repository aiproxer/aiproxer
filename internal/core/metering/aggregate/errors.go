package aggregate

import "errors"

// Stable sentinel errors for Apply (requirements 6.5, 6.7).
var (
	// ErrMixedCurrency is returned when present money observations disagree on
	// normalized currency identity, or when a present money lacks currency.
	ErrMixedCurrency = errors.New("metering/aggregate: mixed or empty currency")
	// ErrOverflow is returned when quantity or money nano aggregation would
	// wrap int64 arithmetic.
	ErrOverflow = errors.New("metering/aggregate: int64 overflow")
)
