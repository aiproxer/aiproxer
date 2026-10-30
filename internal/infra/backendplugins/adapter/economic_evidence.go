package adapter

import (
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// connectorRejectionCategory maps one closed rejection token onto its policy
// category. The mapping is total over the closed set, so every rejection is
// classified and no token is orphaned.
func connectorRejectionCategory(reason execbackend.ConnectorEvidenceRejection) (string, bool) {
	return execbackend.ConnectorEvidenceRejectionCategoryOf(reason)
}

// hostBoundEvidenceNegotiated reports whether the peer opted into the
// host-bound economic evidence contract. Only such a peer's claim is bound
// into the host namespace.
func hostBoundEvidenceNegotiated(neg backendplugin.Negotiation) bool {
	return backendplugin.HostBoundEconomicEvidenceV2Negotiated(neg)
}
