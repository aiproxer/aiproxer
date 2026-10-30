package openailegacy

import (
	"net/http"

	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// RouteClaims describes the route Mount registers for one frontend owner.
func RouteClaims(ownerID string) ([]httpcontract.RouteClaim, error) {
	return httpcontract.ClaimsForBasePath(ownerID, "/v1",
		httpcontract.RouteClaim{Method: http.MethodPost, Path: "/chat/completions", Kind: "openai_chat_completions"},
	)
}
