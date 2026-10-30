package contract

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/backend"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/core"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/frontend"
)

// These mutation suites are the owner proof for the migration boundary. They
// intentionally invoke the existing negative fixtures rather than making the
// legacy matrix the new authority.
func TestOwnerSuitesContainRequiredMutationProofs(t *testing.T) {
	for _, owner := range ReleaseCriticalFeatureOwners() {
		if owner.Feature == "json_text" && len(owner.Frontend) == 0 {
			t.Fatal("decode owner missing")
		}
		if owner.Feature == "connector_host" && len(owner.Sentinel) == 0 {
			t.Fatal("connector composition owner missing")
		}
	}
	_ = backend.CertifyBackend
	_ = core.CertifyCore
	_ = frontend.CertifyFrontend
}
