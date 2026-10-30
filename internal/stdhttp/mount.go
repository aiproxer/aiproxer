// Package stdhttp registers bundled frontend HTTP handlers on a ServeMux (standard distribution wiring).
package stdhttp

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/config"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// geminiFrontendID is the factory ID for the Gemini frontend plugin. It is used
// to defer Gemini route registration (broad /v1beta/ prefixes) after narrower
// protocol handlers. Sync tests in mount_constants_test.go verify alignment
// with the canonical value in the gemini package.
const geminiFrontendID = "gemini"

// MountBundledFrontendsInput carries the generic frontend composition inputs.
type MountBundledFrontendsInput struct {
	Mux       *http.ServeMux
	Frontends HTTPFrontendInput
}

// MountBundledFrontends registers enabled frontend protocol handlers from config on mux.
// Gemini is mounted under /v1beta/ and /v1beta1/ only (after other prefixes when present).
// MaxRequestBodyBytes is forwarded to handlers; zero means each handler's default body cap.
// Mux, Frontends.Executor, and Frontends.Registry must be non-nil.
func MountBundledFrontends(in MountBundledFrontendsInput) error {
	fe := in.Frontends
	if in.Mux == nil {
		return fmt.Errorf("stdhttp: nil mux")
	}
	if fe.Executor == nil {
		return fmt.Errorf("stdhttp: nil exec")
	}
	if fe.Registry == nil {
		return fmt.Errorf("stdhttp: nil plugin registry")
	}
	// Owner-aware route claims are validated before any handler is mounted:
	// a canonical collision (for example base_path=/v1 taking over an existing
	// /v1 route) fails the candidate atomically with both-owner diagnostics.
	// The seam is generic — frontends with a registered claims provider opt in.
	if err := validateFrontendRouteClaims(fe); err != nil {
		return err
	}
	mountALegCancel(in.Mux, fe)
	specific := []config.PluginConfig{}
	geminiLast := []config.PluginConfig{}
	for _, p := range fe.Plugins {
		if !p.Enabled {
			continue
		}
		if p.FactoryID() == geminiFrontendID {
			geminiLast = append(geminiLast, p)
			continue
		}
		specific = append(specific, p)
	}
	ordered := append(specific, geminiLast...)
	return callMount(func() error {
		for _, p := range ordered {
			if err := fe.Registry.MountFrontend(
				p.FactoryID(),
				in.Mux,
				aipsdk.FrontendMountOptions{
					PluginCfg:           p.Config,
					Exec:                fe.Executor,
					DefaultRoute:        fe.DefaultRouteSelector,
					RoutePrefixes:       fe.RoutePrefixes,
					MaxRequestBodyBytes: fe.MaxRequestBodyBytes, DecodeAdmission: fe.DecodeAdmission,
					TrafficPorts:            fe.TrafficPorts,
					PreRequestKeepalive:     fe.PreRequestKeepalive,
					HTTPHeaders:             fe.HTTPHeaders,
					StreamKeepaliveInterval: fe.StreamKeepaliveInterval,
					GenerationContext:       fe.GenerationContext, ContinuationWiringFactory: fe.ContinuationWiringFactory,
					FrontendInstanceID: p.InstanceID(),
					LargePayload:       fe.LargePayload,
					Log:                fe.Log,
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// validateFrontendRouteClaims registers each enabled frontend's owner-aware
// route claims into one generation-scoped RouteRegistry and validates
// canonical path takeover before any ServeMux handler is mounted. A conflict
// returns a [httpcontract.RouteConflictError] naming both owners; no handler
// is registered, so the candidate fails atomically. Frontends without a
// claims provider in [httpcontract.HTTPFrontendInput.FrontendRouteClaims]
// participate in no ownership validation.
func validateFrontendRouteClaims(fe HTTPFrontendInput) error {
	if len(fe.FrontendRouteClaims) == 0 {
		return nil
	}
	reg := httpcontract.NewRouteRegistry()
	for _, p := range fe.Plugins {
		if !p.Enabled {
			continue
		}
		provider := fe.FrontendRouteClaims[p.FactoryID()]
		if provider == nil {
			continue
		}
		claims, err := provider(p.InstanceID(), p.Config)
		if err != nil {
			return fmt.Errorf("stdhttp: route claims for %q: %w", p.FactoryID(), err)
		}
		// Canonical takeover validation rejects base_path=/v1 collisions with
		// already-owned method/path pairs before registering the proposal.
		if err := reg.ValidateCanonicalPathTakeover(httpcontract.CanonicalLegacyBasePath, claims); err != nil {
			return wrapRouteConflict(err)
		}
		if err := reg.RegisterAll(claims); err != nil {
			return wrapRouteConflict(err)
		}
	}
	return nil
}

// wrapRouteConflict preserves both the sentinel ErrRouteConflict (so existing
// composition callers can errors.Is it) and the owner-aware
// RouteConflictError (so both owners are named) for a canonical route-claims
// collision detected before mounting.
func wrapRouteConflict(err error) error {
	if err == nil {
		return nil
	}
	if detail, ok := errors.AsType[httpcontract.RouteConflictError](err); ok {
		return fmt.Errorf("%w: %w", ErrRouteConflict, detail)
	}
	return err
}
