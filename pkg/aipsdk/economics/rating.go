package economics

// RoundingPolicy identifies how fractional nano-units are resolved by exact
// money/token arithmetic. Monetary rating itself is owned by internal/core/billing.
type RoundingPolicy string

const (
	RoundingUnspecified      RoundingPolicy = ""
	RoundingHalfAwayFromZero RoundingPolicy = "half_away_from_zero"
	RoundingHalfEven         RoundingPolicy = "half_even"
	RoundingTowardZero       RoundingPolicy = "toward_zero"
	RoundingFloor            RoundingPolicy = "floor"
)
