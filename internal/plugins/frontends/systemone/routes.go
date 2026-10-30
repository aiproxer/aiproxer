package systemone

import (
	"net/http"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// RouteClaims declares this frontend's sole non-streaming create route.
func RouteClaims(ownerID string) ([]httpcontract.RouteClaim, error) {
	return httpcontract.ClaimsForBasePath(ownerID, "/v1", httpcontract.RouteClaim{Method: http.MethodPost, Path: "/systemone", Kind: "systemone_decision"})
}
