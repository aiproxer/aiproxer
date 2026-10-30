package localstub

import (
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var (
	_ leglifecycle.BLegAttempt = (*aipapi.FixedEventStream)(nil)
	_ leglifecycle.BLegAttempt = (*errorAfterPrefixStream)(nil)
)
