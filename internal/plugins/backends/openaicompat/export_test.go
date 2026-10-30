package openaicompat

import (
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// HostedCaps exposes the standard hosted capabilities for test suites.
func HostedCaps() aipapi.BackendCaps {
	return openaicaps.HostedFull
}

// AttachWireProofForTest exposes attachWireProof for testing wire fast-path behavior.
func AttachWireProofForTest(be execbackend.Backend, spec BackendSpec, pool *credpool.Pool, inv modelinventory.Provider) execbackend.Backend {
	return attachWireProof(be, spec, pool, inv)
}
