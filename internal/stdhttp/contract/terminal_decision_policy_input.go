package contract

import (
	"context"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
)

// TerminalDecisionPolicyInput is the explicit composition projection for the
// provider-neutral client/operator policy routes.
type TerminalDecisionPolicyInput struct {
	Store                   *sessionpolicy.Store
	FeatureStatus           func(context.Context, string) (known, available bool, err error)
	ResolveClientScope      func(context.Context, *http.Request, string) (sessionpolicy.Key, sessionpolicy.Authority, error)
	AuthorizeOperatorTarget func(context.Context, *http.Request, string, string) (sessionpolicy.Key, sessionpolicy.Authority, error)
	GenerationDefault       func(string) bool
	MaxBodyBytes            int64
}
