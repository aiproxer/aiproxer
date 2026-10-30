package terminaldecision

import "errors"

// Validation sentinels classify malformed provider contracts without exposing
// implementation details to callers.
var (
	ErrInvalidProvider = errors.New("terminaldecision: invalid provider")
	ErrInvalidInput    = errors.New("terminaldecision: invalid input")
	ErrInvalidDecision = errors.New("terminaldecision: invalid decision")
)
