package openresponsescompat

import (
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The Open seam returns [aipapi.NewFixedEventStream] streams for parsed
// non-streaming create responses and [*sseStream] streams for SSE create
// responses; both satisfy the leglifecycle.BLegAttempt contract that every
// official backend B-leg must expose.
var (
	_ leglifecycle.BLegAttempt = (*aipapi.FixedEventStream)(nil)
	_ leglifecycle.BLegAttempt = (*sseStream)(nil)
)
