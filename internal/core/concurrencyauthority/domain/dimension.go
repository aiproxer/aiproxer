package domain

import "github.com/aiproxer/aiproxer/internal/core/authorityattribution"

type (
	Dimensions        = authorityattribution.Dimensions
	DimensionKey      = authorityattribution.DimensionKey
	DimensionMatcher  = authorityattribution.DimensionMatcher
	DimensionsMatcher = authorityattribution.DimensionsMatcher
)

var (
	IsSafeLabelKey      = authorityattribution.IsSafeLabelKey
	DimensionsFromScope = authorityattribution.DimensionsFromScope
)
