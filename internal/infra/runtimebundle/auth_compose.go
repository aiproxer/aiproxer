package runtimebundle

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	coreauth "github.com/aiproxer/aiproxer/internal/core/auth"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/osidentity"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	stdhttpauth "github.com/aiproxer/aiproxer/internal/stdhttp/auth"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/transport/httpauth"
)

func localAPIKeyRecordsForAuth(rec []config.AuthLocalAPIKeyRecord) []coreauth.LocalAPIKeyRecord {
	out := make([]coreauth.LocalAPIKeyRecord, 0, len(rec))
	for _, r := range rec {
		out = append(out, r.ToCore())
	}
	return out
}

func mergeAuthErrorRenderersByFrontend(reg *pluginreg.Registry, opts *BuildOptions) map[string]httpauth.AuthErrorRenderer {
	out := make(map[string]httpauth.AuthErrorRenderer)
	copyRenderers := func(m map[string]httpauth.AuthErrorRenderer) {
		for k, v := range m {
			if v != nil && strings.TrimSpace(k) != "" {
				out[strings.ToLower(strings.TrimSpace(k))] = v
			}
		}
	}
	if reg != nil {
		copyRenderers(reg.AuthErrorRenderers())
	}
	if opts != nil {
		copyRenderers(opts.Auth.AuthErrorRenderersByFrontend)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func needsInjectedRemoteDecider(h sdkauth.HandlerKind, rl sdkauth.RequiredLevel) bool {
	if rl == sdkauth.LevelAPIKeySSO {
		return true
	}
	return h == sdkauth.HandlerRemote
}

func resolveHTTPAuthProviders(cfg *config.Config, log *slog.Logger, opts *BuildOptions, authEvents *coreauth.EventDispatcher, sap coreauth.SessionAuditPolicy) ([]httpauth.Provider, error) {
	// Non-empty override with only nil entries must not bypass composed config auth (security).
	if opts != nil && httpAuthProvidersHasNonNil(opts.Auth.HTTPAuthProviders) {
		return slices.Clone(opts.Auth.HTTPAuthProviders), nil
	}
	return composeHTTPAuthProviders(cfg, log, opts, authEvents, sap)
}

// httpAuthProvidersHasNonNil reports whether providers contains at least one non-nil entry.
func httpAuthProvidersHasNonNil(providers []httpauth.Provider) bool {
	return slices.ContainsFunc(providers, func(p httpauth.Provider) bool { return p != nil })
}

func composeHTTPAuthProviders(cfg *config.Config, log *slog.Logger, opts *BuildOptions, authEvents *coreauth.EventDispatcher, sap coreauth.SessionAuditPolicy) ([]httpauth.Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtimebundle: nil config")
	}
	if opts == nil {
		return nil, fmt.Errorf("runtimebundle: nil BuildOptions")
	}
	if needsInjectedRemoteDecider(sap.HandlerKind, sap.RequiredLevel) && opts.Auth.RemoteDecider == nil {
		return nil, fmt.Errorf("%w", ErrRemoteDeciderRequired)
	}

	pa := coreauth.PolicyAuthenticator{
		Handler:  sap.HandlerKind,
		Required: sap.RequiredLevel,
		Remote:   opts.Auth.RemoteDecider,
	}
	if log != nil {
		pa.OnRemoteDecideError = func(ctx context.Context, err error) {
			log.DebugContext(ctx, "auth: remote decide failed", "error", err)
		}
	}

	switch sap.HandlerKind {
	case sdkauth.HandlerLocalNoop:
		osIdent := opts.Auth.OSIdentity
		if osIdent == nil {
			osIdent = &osidentity.Provider{}
		}
		noop := coreauth.LocalNoOpAuthenticator{OS: osIdent}
		if log != nil {
			noop.OnOSIdentityFallback = func(ctx context.Context, err error, hadProvider bool) {
				if !hadProvider {
					log.WarnContext(ctx, "auth: local_noop OS identity provider unset; using fallback principal",
						"fallback_principal", coreauth.LocalUnknownOSPrincipalID)
					return
				}
				if err != nil {
					log.WarnContext(ctx, "auth: local_noop OS identity lookup failed; using fallback principal",
						"error", err, "fallback_principal", coreauth.LocalUnknownOSPrincipalID)
				}
			}
		}
		pa.Noop = noop

	case sdkauth.HandlerLocalAPIKey:
		ak, err := coreauth.NewLocalAPIKeyAuthenticator(localAPIKeyRecordsForAuth(cfg.Auth.LocalAPIKeys))
		if err != nil {
			return nil, fmt.Errorf("runtimebundle: local api key authenticator: %w", err)
		}
		pa.APIKey = ak

	case sdkauth.HandlerRemote:
		if sap.RequiredLevel == sdkauth.LevelAPIKeySSO {
			ak, err := coreauth.NewLocalAPIKeyAuthenticator(localAPIKeyRecordsForAuth(cfg.Auth.LocalAPIKeys))
			if err != nil {
				return nil, fmt.Errorf("runtimebundle: local api key authenticator (api_key_sso): %w", err)
			}
			pa.APIKey = ak
		}

	default:
		return nil, fmt.Errorf("runtimebundle: unsupported auth handler %q", sap.HandlerKind)
	}

	// Keep this explicit copy rather than sharing one struct: session audit policy is core/executor
	// context, while PolicySnapshot is HTTP-adapter context for rendering and auth events.
	pol := stdhttpauth.PolicySnapshot{
		AccessMode:    sap.AccessMode,
		HandlerKind:   sap.HandlerKind,
		RequiredLevel: sap.RequiredLevel,
	}
	prov := stdhttpauth.NewPolicyProvider(&pa, authEvents, pol, opts.Auth.AuthErrorRenderer)
	prov.HTTPHeaders = cfg.HTTPHeaders.Effective()
	if byFe := mergeAuthErrorRenderersByFrontend(opts.PluginRegistry, opts); len(byFe) > 0 {
		prov.RendererByFrontend = byFe
	}
	return []httpauth.Provider{prov}, nil
}
