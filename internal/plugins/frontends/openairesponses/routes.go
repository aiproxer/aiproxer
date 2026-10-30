package openairesponses

import (
	"net/http"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

const (
	RouteOperationCreate = "openai_responses_create"
	RouteOperationCancel = "openai_responses_cancel"
)

// RouteClaims describes the routes owned by this frontend instance.
func RouteClaims(ownerID string) ([]httpcontract.RouteClaim, error) {
	return httpcontract.ClaimsForBasePath(ownerID, "/v1",
		httpcontract.RouteClaim{Method: http.MethodPost, Path: "/responses", Kind: RouteOperationCreate},
		httpcontract.RouteClaim{Method: http.MethodPost, Path: "/responses/{id}/cancel", Kind: RouteOperationCancel},
	)
}
