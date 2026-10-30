package controlplane

import (
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// Compile-time proofs that concrete runtime services satisfy the narrow HTTP
// adapter contracts without forcing stdhttp to import core app packages. The
// accounting-authority proof moved with its adapter to runtimebundle (task
// 4.2): the concrete usage-authority service is adapted there instead.
var (
	_ ConcurrencyAuthorityQueries = (*concurrencyapp.Service)(nil)
	_ cp.Queries                  = (*controlplane.QueryService)(nil)
	_ cp.ReadinessReportReader    = (*controlplane.ReadinessReportService)(nil)
)
