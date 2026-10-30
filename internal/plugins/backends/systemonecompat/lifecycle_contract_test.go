package systemonecompat

import (
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Successful fixed streams and failed-attempt evidence streams both expose the
// core-owned leglifecycle.BLegAttempt contract; neither owns a second lifecycle.
var (
	_ leglifecycle.BLegAttempt = (*aipapi.FixedEventStream)(nil)
	_ leglifecycle.BLegAttempt = (*failureUsageStream)(nil)
	_ leglifecycle.BLegAttempt = (*rotatedSuccessStream)(nil)
)
