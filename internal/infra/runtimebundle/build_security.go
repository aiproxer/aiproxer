package runtimebundle

import (
	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

// securityRuntime holds the auth-event dispatcher, session-audit policy, and
// HTTP auth providers produced by [buildSecurityRuntime]. None of these own
// closers; the control-plane store closer is already registered before this
// unit runs.
type securityRuntime struct {
	AuthEvents *coreauth.EventDispatcher
	SAP        coreauth.SessionAuditPolicy
	HTTPAuth   []httpauth.Provider
}

// buildSecurityRuntime sequences the four auth/security setup steps for a candidate
// compile: auth-event dispatcher, backend security-profile validation,
// session-audit policy, and HTTP auth provider resolution. Errors are returned
// unwrapped (helpers attach their own context) so the caller can apply
// withDisposedClosers unchanged.
func buildSecurityRuntime(bctx buildContext, cp *controlPlaneRuntime) (*securityRuntime, error) {
	cfg, log, opts := bctx.Cfg, bctx.Log, bctx.Opts
	reg := opts.PluginRegistry
	authEvents, err := buildAuthEventDispatcher(cfg, log, opts, cp.wrapAuthSink)
	if err != nil {
		return nil, err
	}
	if err := validateBackendSecurityProfiles(cfg, reg); err != nil {
		return nil, err
	}
	sap, err := buildSessionAuditPolicy(cfg)
	if err != nil {
		return nil, err
	}
	httpAuth, err := resolveHTTPAuthProviders(cfg, log, opts, authEvents, sap)
	if err != nil {
		return nil, err
	}
	return &securityRuntime{AuthEvents: authEvents, SAP: sap, HTTPAuth: httpAuth}, nil
}
