package stdhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// ComposeStandardHTTP is the canonical HandlerComposer: it builds the complete
// standard http.Handler for one generation from a focused StandardHTTPInput
// without binding a listener, without owning runtime.App, and without
// starting/stopping feature lifecycles (the candidate resource ledger owns
// those). This is the composer injected into runtimebundle.CompileGeneration /
// GenerationCompiler so runtimebundle never imports stdhttp (no import
// cycle; task 3.4–3.5). Route registration conflicts are returned as
// [ErrRouteConflict] rather than panicking. Management reload/status routes
// are intentionally absent: they remain process-owned outside this swappable
// request-plane graph (req 12.1).

// StandardHTTPInput and its groups are exact aliases of the cycle-neutral
// internal/stdhttp/contract definitions (task 3.4).
type (
	StandardHTTPInput           = httpcontract.StandardHTTPInput
	HTTPCoreInput               = httpcontract.HTTPCoreInput
	HTTPSecurityInput           = httpcontract.HTTPSecurityInput
	GeoIPSecurityInput          = httpcontract.GeoIPSecurityInput
	GeoIPResolverConfig         = httpcontract.GeoIPResolverConfig
	SelfDefenseSecurityInput    = httpcontract.SelfDefenseSecurityInput
	SelfDefenseObserver         = httpcontract.SelfDefenseObserver
	CredentialProbe             = httpcontract.CredentialProbe
	CredentialDisposition       = httpcontract.CredentialDisposition
	HTTPOperationsInput         = httpcontract.HTTPOperationsInput
	AdminMount                  = httpcontract.AdminMount
	TerminalDecisionPolicyInput = httpcontract.TerminalDecisionPolicyInput
	HTTPModelInput              = httpcontract.HTTPModelInput
	HTTPFrontendInput           = httpcontract.HTTPFrontendInput
)

const (
	// MayAuthenticate and DefinitelyNoCredential are the closed answers of the
	// conservative credential-presence probe, re-exported so the standard
	// composition and its tests share one vocabulary with the contract.
	MayAuthenticate        = httpcontract.MayAuthenticate
	DefinitelyNoCredential = httpcontract.DefinitelyNoCredential
)

func ComposeStandardHTTP(ctx context.Context, cfg *config.Config, log *slog.Logger, in StandardHTTPInput) (http.Handler, error) {
	if ctx == nil {
		return nil, errors.New("stdhttp: nil context")
	}
	if cfg == nil {
		return nil, errors.New("stdhttp: nil config")
	}
	if log == nil {
		return nil, errors.New("stdhttp: nil logger")
	}
	if in.Core.Executor == nil {
		return nil, errors.New("stdhttp: nil executor")
	}
	if in.Frontends.Registry == nil {
		return nil, errors.New("stdhttp: nil plugin registry")
	}
	if err := validateStartupSecurity(cfg); err != nil {
		return nil, err
	}
	handler, err := prepareStandardHandler(ctx, cfg, log, in)
	if err != nil {
		return nil, err
	}
	// Intentionally no runtime.App Start/Shutdown: feature lifecycles are owned
	// by the candidate resource ledger (singular Start/Stop).
	return handler, nil
}

// Ensure ComposeStandardHTTP remains assignable to the canonical HandlerComposer.
var _ runtimebundle.HandlerComposer = ComposeStandardHTTP
