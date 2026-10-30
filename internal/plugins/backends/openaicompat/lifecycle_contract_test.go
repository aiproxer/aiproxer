package openaicompat

import "github.com/aiproxer/aiproxer/internal/core/leglifecycle"

var (
	_ leglifecycle.BLegAttempt = (*chatStream)(nil)
	_ leglifecycle.BLegAttempt = (*responsesStream)(nil)
)
