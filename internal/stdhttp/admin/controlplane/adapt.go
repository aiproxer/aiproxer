package controlplane

import (
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// The accounting-authority projection moved to its composition owner
// (runtimebundle, task 4.2): stdhttp must not import the usage-authority
// service package, so the concrete service is adapted to
// [AccountingAuthorityQueries] where it is constructed.

// AdaptConcurrencyAuthorityQueries projects a concrete concurrency-authority service onto
// the narrow HTTP query contract. A nil or typed-nil pointer becomes a true nil interface.
func AdaptConcurrencyAuthorityQueries(svc *concurrencyapp.Service) ConcurrencyAuthorityQueries {
	if svc == nil {
		return nil
	}
	return svc
}

// AdaptControlPlaneQueries projects a concrete query service onto aipcp.Queries.
// A nil or typed-nil pointer becomes a true nil interface.
func AdaptControlPlaneQueries(q *controlplane.QueryService) cp.Queries {
	if q == nil {
		return nil
	}
	return q
}

// AdaptReadinessReport projects a concrete readiness service onto ReadinessReportReader.
// A nil or typed-nil pointer becomes a true nil interface.
func AdaptReadinessReport(r *controlplane.ReadinessReportService) cp.ReadinessReportReader {
	if r == nil {
		return nil
	}
	return r
}
