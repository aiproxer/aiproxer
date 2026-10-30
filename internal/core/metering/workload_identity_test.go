package metering_test

import (
	"errors"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	aipmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestProjectWorkloadIdentityDistinguishesPrimaryAndAuxiliaryFacts(t *testing.T) {
	t.Parallel()
	primary, err := coremetering.ProjectWorkloadIdentity(aipmetering.Fact{
		Lifecycle: aipmetering.LifecycleBackendAttempt,
		Scope:     scope.PrincipalScopeView{Origin: scope.OriginClient},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if primary.Class != usageevidence.WorkloadClassPrimary || primary.Role != "" {
		t.Fatalf("primary identity = %+v", primary)
	}
	auxiliary, err := coremetering.ProjectWorkloadIdentity(aipmetering.Fact{
		Lifecycle: aipmetering.LifecycleBackendAttempt,
		Scope:     scope.PrincipalScopeView{Origin: scope.OriginInternal},
	}, string(usageevidence.WorkloadRoleCompactionContinuityExtractor))
	if err != nil {
		t.Fatal(err)
	}
	if auxiliary.Class != usageevidence.WorkloadClassAuxiliary || auxiliary.Role != usageevidence.WorkloadRole(usageevidence.WorkloadRoleCompactionContinuityExtractor) {
		t.Fatalf("auxiliary identity = %+v", auxiliary)
	}
}

func TestProjectWorkloadIdentityRejectsUntrustedRoleOrPrimaryRole(t *testing.T) {
	t.Parallel()
	auxFact := aipmetering.Fact{Lifecycle: aipmetering.LifecycleAuxiliaryRequest}
	for _, role := range []string{"", "user prompt", "compaction_continuity_extractor:raw"} {
		if _, err := coremetering.ProjectWorkloadIdentity(auxFact, role); !errors.Is(err, usageevidence.ErrInvalidWorkloadIdentity) {
			t.Fatalf("role %q error = %v, want ErrInvalidWorkloadIdentity", role, err)
		}
	}
	primaryFact := aipmetering.Fact{Lifecycle: aipmetering.LifecycleBackendAttempt, Scope: scope.PrincipalScopeView{Origin: scope.OriginClient}}
	if _, err := coremetering.ProjectWorkloadIdentity(primaryFact, string(usageevidence.WorkloadRoleCompactionContinuityExtractor)); err == nil {
		t.Fatal("primary fact accepted auxiliary role")
	}
}
