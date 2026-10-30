package gemini

import (
	"net/http"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// RouteDescriptor keeps the raw ServeMux mount pattern separate from the
// normalized route claim used by ownership and diagnostics.
type RouteDescriptor struct {
	MountPattern string
	Claim        httpcontract.RouteClaim
}

// RouteDescriptors describes the routes Mount registers for one frontend owner.
func RouteDescriptors(ownerID string) ([]RouteDescriptor, error) {
	descriptors := []RouteDescriptor{
		{MountPattern: "/v1beta/", Claim: httpcontract.RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: "/v1beta/", Kind: "gemini_generate"}},
		{MountPattern: "/v1beta1/", Claim: httpcontract.RouteClaim{OwnerID: ownerID, Method: http.MethodPost, Path: "/v1beta1/", Kind: "gemini_generate"}},
	}
	for i := range descriptors {
		normalized, err := descriptors[i].Claim.NormalizedClaim()
		if err != nil {
			return nil, err
		}
		descriptors[i].Claim = normalized
	}
	return descriptors, nil
}

// RouteClaims returns normalized ownership claims; MountPattern is intentionally
// not exposed through this compatibility projection.
func RouteClaims(ownerID string) ([]httpcontract.RouteClaim, error) {
	descriptors, err := RouteDescriptors(ownerID)
	if err != nil {
		return nil, err
	}
	claims := make([]httpcontract.RouteClaim, 0, len(descriptors))
	for _, descriptor := range descriptors {
		claims = append(claims, descriptor.Claim)
	}
	return claims, nil
}
