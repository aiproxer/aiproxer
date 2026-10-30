package metering

import (
	"fmt"
	"strings"

	usageevidence "github.com/aiproxer/aiproxer/internal/core/usageevidence"
	aipmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ProjectWorkloadIdentity maps trusted auxiliary lineage onto the same bounded
// billing identity used by normal terminal/report records. The fact itself
// remains the existing metering contract: lifecycle and safe scope correlation
// carry classification, while the role is supplied from trusted lineage.
func ProjectWorkloadIdentity(f aipmetering.Fact, auxiliaryRole string) (usageevidence.WorkloadIdentity, error) {
	auxiliary := f.Lifecycle == aipmetering.LifecycleAuxiliaryRequest || f.Scope.Origin == scope.OriginInternal
	auxiliaryRole = strings.TrimSpace(auxiliaryRole)
	if !auxiliary {
		if auxiliaryRole != "" {
			return usageevidence.WorkloadIdentity{}, fmt.Errorf("metering: primary fact cannot carry auxiliary workload role")
		}
		return usageevidence.WorkloadIdentity{Class: usageevidence.WorkloadClassPrimary}, nil
	}
	return usageevidence.WorkloadIdentityFromAuxiliaryRole(auxiliaryRole)
}
